package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	agyauth "github.com/whatnamed/agent-bridge/shared/antigravity/auth"
	"github.com/whatnamed/agent-bridge/shared/antigravity/cloudcode"
)

const (
	antigravityProviderID  = "antigravity"
	stableAntigravityModel = "gemini-3.7-flash-high"
)

type antigravityProvider struct {
	cfg          config
	tokenManager *agyauth.TokenManager
	client       *cloudcode.Client

	mu             sync.Mutex
	project        string
	projectExpires time.Time
	catalog        cloudcode.ModelsResponse
	catalogExpires time.Time
	tokenExpiry    time.Time
}

func newAntigravityProvider(cfg config) (*antigravityProvider, error) {
	profile := strings.TrimSpace(cfg.AntigravityOAuthProfile)
	if profile == "" {
		profile = string(agyauth.DefaultProfile)
	}
	oauthConfig, err := agyauth.ConfigForProfile(profile)
	if err != nil {
		return nil, fmt.Errorf("Antigravity OAuth configuration failed: %w", err)
	}
	httpClient := &http.Client{Timeout: cfg.Timeout}
	oauthConfig.HTTPClient = httpClient
	manager := &agyauth.TokenManager{
		Path:       expandHome(cfg.AntigravityCredentialPath),
		Config:     oauthConfig,
		HTTPClient: httpClient,
	}
	client := cloudcode.NewClient(cfg.AntigravityEndpoint, manager)
	client.HTTPClient = httpClient
	return &antigravityProvider{
		cfg:          cfg,
		tokenManager: manager,
		client:       client,
		catalog:      cloudcode.ModelsResponse{Models: map[string]cloudcode.AvailableModel{}},
	}, nil
}

func (p *antigravityProvider) ID() string { return antigravityProviderID }

func (p *antigravityProvider) listModels(ctx context.Context) ([]string, error) {
	if err := p.ensureControlPlane(ctx); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	ids := make([]string, 0, len(p.catalog.Models))
	for id := range p.catalog.Models {
		if isStableAntigravityModel(id) {
			ids = append(ids, id)
		}
	}
	return uniqueModelIDs(ids), nil
}

func (p *antigravityProvider) resolveModel(ctx context.Context, requested string) (cloudcode.ModelResolution, error) {
	if err := p.ensureControlPlane(ctx); err != nil {
		return cloudcode.ModelResolution{}, err
	}
	p.mu.Lock()
	catalog := p.catalog
	p.mu.Unlock()
	return cloudcode.ResolveModel(requested, catalog), nil
}

func (p *antigravityProvider) ensureControlPlane(ctx context.Context) error {
	if p == nil || p.client == nil || p.tokenManager == nil {
		return &backendError{503, "Antigravity provider is not initialized."}
	}
	now := time.Now()
	p.mu.Lock()
	projectFresh := p.project != "" && now.Before(p.projectExpires)
	catalogFresh := len(p.catalog.Models) > 0 && now.Before(p.catalogExpires)
	if projectFresh && catalogFresh {
		p.mu.Unlock()
		return nil
	}
	// Keep control-plane refreshes serialized. This is deliberately outside the
	// generation hot path once the TTLs are warm, while avoiding duplicate
	// loadCodeAssist/fetchAvailableModels requests during startup bursts.
	defer p.mu.Unlock()
	if !projectFresh {
		loaded, err := p.client.LoadCodeAssist(ctx)
		if err != nil {
			return &backendError{502, "Antigravity loadCodeAssist failed."}
		}
		project := strings.TrimSpace(p.cfg.AntigravityProject)
		if project == "" {
			project = strings.TrimSpace(loaded.CloudAICompanionProject)
		}
		if project == "" {
			return &backendError{502, "Antigravity loadCodeAssist did not return a verified project."}
		}
		p.project = project
		p.projectExpires = now.Add(p.projectTTL())
		projectFresh = true
	}
	if !catalogFresh {
		catalog, err := p.client.FetchAvailableModels(ctx)
		if err != nil {
			return &backendError{502, "Antigravity fetchAvailableModels failed."}
		}
		p.catalog = catalog
		p.catalogExpires = now.Add(p.catalogTTL())
	}
	status, err := p.tokenManager.CredentialStatus()
	if err == nil {
		p.tokenExpiry = status.Expiry
	}
	return nil
}

func (p *antigravityProvider) catalogTTL() time.Duration {
	if p.cfg.AntigravityCatalogTTL > 0 {
		return p.cfg.AntigravityCatalogTTL
	}
	return defaultAntigravityCatalogTTL
}

func (p *antigravityProvider) projectTTL() time.Duration {
	if p.cfg.AntigravityProjectTTL > 0 {
		return p.cfg.AntigravityProjectTTL
	}
	return defaultAntigravityProjectTTL
}

func (p *antigravityProvider) projectSnapshot() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.project
}

func (p *antigravityProvider) catalogSize() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.catalog.Models)
}

func (p *antigravityProvider) oauthExpiry() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.tokenExpiry
}

func isStableAntigravityModel(model string) bool {
	return strings.TrimSpace(model) == stableAntigravityModel
}

func (p *antigravityProvider) stream(ctx context.Context, payload map[string]any, fn func(map[string]any) error) error {
	if fn == nil {
		return errors.New("Antigravity stream callback is nil")
	}
	if err := p.ensureControlPlane(ctx); err != nil {
		return err
	}
	resolution, err := p.resolveModel(ctx, stringValue(payload["model"]))
	if err != nil {
		return err
	}
	if !resolution.Verified() || !isStableAntigravityModel(resolution.ActualUpstreamModel) {
		return &backendError{400, fmt.Sprintf("Model %q was not verified in the current Antigravity catalog; no fallback model was selected.", resolution.RequestedModel)}
	}
	p.mu.Lock()
	project := p.project
	p.mu.Unlock()
	request, err := buildAntigravityRequest(payload, resolution.ActualUpstreamModel, project)
	if err != nil {
		return &backendError{400, err.Error()}
	}
	if observer := telemetryFromContext(ctx); observer != nil {
		observer.observePrepared(payload)
	}
	stream, err := p.client.StreamGenerateContent(ctx, request)
	if err != nil {
		return &backendError{502, "Antigravity streamGenerateContent failed."}
	}
	defer stream.Close()
	return p.emitCanonicalStream(ctx, stream, resolution.ActualUpstreamModel, fn)
}

func (p *antigravityProvider) collect(ctx context.Context, payload map[string]any) (map[string]any, error) {
	var completed map[string]any
	var output []any
	var text strings.Builder
	var responseID string
	err := p.stream(ctx, payload, func(event map[string]any) error {
		switch event["type"] {
		case "response.created":
			if response := mapAny(event["response"]); response != nil {
				responseID = stringValue(response["id"])
			}
		case "response.output_text.delta":
			text.WriteString(stringValue(event["delta"]))
		case "response.output_item.done":
			if item := mapAny(event["item"]); item != nil {
				output = append(output, item)
			}
		case "response.completed", "response.incomplete", "response.failed":
			completed = cloneMap(mapAny(event["response"]))
			if event["type"] == "response.failed" && completed == nil {
				return &backendError{502, "Antigravity response failed."}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if completed != nil {
		if len(sliceAny(completed["output"])) == 0 && len(output) > 0 {
			completed["output"] = output
		}
		return completed, nil
	}
	if responseID == "" {
		responseID = newID("resp")
	}
	return map[string]any{
		"id": responseID, "object": "response", "created_at": nowFloat(), "status": "completed",
		"model": payload["model"], "output": []any{outputMessage(text.String())},
		"parallel_tool_calls": true, "tool_choice": valueOr(payload["tool_choice"], "auto"), "tools": sliceAny(payload["tools"]),
	}, nil
}

func (p *antigravityProvider) emitCanonicalStream(ctx context.Context, stream *cloudcode.Stream, model string, fn func(map[string]any) error) error {
	responseID := newID("resp")
	created := map[string]any{
		"type": "response.created",
		"response": map[string]any{
			"id": responseID, "object": "response", "created_at": nowFloat(), "status": "in_progress", "model": model, "output": []any{},
		},
	}
	if err := emitAntigravityEvent(ctx, fn, created); err != nil {
		return err
	}
	if err := emitAntigravityEvent(ctx, fn, map[string]any{
		"type":     "response.in_progress",
		"response": cloneMap(mapAny(created["response"])),
	}); err != nil {
		return err
	}
	var text strings.Builder
	var reasoning strings.Builder
	var usage cloudcode.Usage
	finishReason := ""
	items := make([]map[string]any, 0, 3)
	functionKeys := make(map[string]struct{})
	functionArguments := make(map[string]string)
	var messageItem map[string]any
	var reasoningItem map[string]any
	for {
		event, err := stream.Next()
		if err != nil {
			return err
		}
		if event.Usage.HasData() {
			usage.Merge(event.Usage)
		}
		if event.FinishReason != "" {
			finishReason = event.FinishReason
		}
		if event.Reasoning != "" {
			reasoning.WriteString(event.Reasoning)
			if reasoningItem == nil {
				reasoningItem = map[string]any{"id": newID("rs"), "type": "reasoning", "status": "in_progress", "summary": []any{}}
				items = append(items, reasoningItem)
				outputIndex := len(items) - 1
				if err := emitAntigravityEvent(ctx, fn, map[string]any{"type": "response.output_item.added", "output_index": outputIndex, "item": cloneMap(reasoningItem)}); err != nil {
					return err
				}
				if err := emitAntigravityEvent(ctx, fn, map[string]any{
					"type": "response.reasoning_summary_part.added", "output_index": outputIndex,
					"item_id": reasoningItem["id"], "summary_index": 0,
					"part": map[string]any{"type": "summary_text", "text": ""},
				}); err != nil {
					return err
				}
			}
			if err := emitAntigravityEvent(ctx, fn, map[string]any{
				"type": "response.reasoning_summary_text.delta", "output_index": indexOfItem(items, reasoningItem),
				"item_id": reasoningItem["id"], "summary_index": 0, "delta": event.Reasoning,
			}); err != nil {
				return err
			}
		}
		if event.Text != "" {
			text.WriteString(event.Text)
			if messageItem == nil {
				messageItem = map[string]any{"id": newID("msg"), "type": "message", "role": "assistant", "status": "in_progress", "phase": "final_answer", "content": []any{map[string]any{"type": "output_text", "text": "", "annotations": []any{}, "logprobs": []any{}}}}
				items = append(items, messageItem)
				outputIndex := len(items) - 1
				if err := emitAntigravityEvent(ctx, fn, map[string]any{"type": "response.output_item.added", "output_index": outputIndex, "item": cloneMap(messageItem)}); err != nil {
					return err
				}
				if err := emitAntigravityEvent(ctx, fn, map[string]any{
					"type": "response.content_part.added", "output_index": outputIndex, "item_id": messageItem["id"], "content_index": 0,
					"part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}, "logprobs": []any{}},
				}); err != nil {
					return err
				}
			}
			if err := emitAntigravityEvent(ctx, fn, map[string]any{
				"type": "response.output_text.delta", "output_index": indexOfItem(items, messageItem), "item_id": messageItem["id"], "content_index": 0, "delta": event.Text,
			}); err != nil {
				return err
			}
		}
		for _, call := range event.FunctionCalls {
			callID := strings.TrimSpace(call.ID)
			if callID == "" {
				callID = newID("call")
			}
			call.Name = strings.TrimSpace(call.Name)
			if call.Name == "" {
				return errors.New("Antigravity function call did not include a name")
			}
			key := callID + "\x00" + call.Name
			arguments := "{}"
			if call.Args != nil {
				encoded, err := json.Marshal(call.Args)
				if err != nil {
					return fmt.Errorf("encode Antigravity function call arguments: %w", err)
				}
				arguments = string(encoded)
			}
			if _, seen := functionKeys[key]; seen {
				for _, item := range items {
					if stringValue(item["call_id"]) == callID && stringValue(item["name"]) == call.Name {
						functionArguments[stringValue(item["id"])] = arguments
						if stringValue(item["thought_signature"]) == "" {
							if signature := functionThoughtSignature(event, call); signature != "" {
								item["thought_signature"] = signature
							}
						}
						break
					}
				}
				continue
			}
			functionKeys[key] = struct{}{}
			item := map[string]any{
				"id": callID, "type": "function_call", "status": "in_progress", "call_id": callID,
				"name": call.Name, "arguments": "",
			}
			if signature := functionThoughtSignature(event, call); signature != "" {
				item["thought_signature"] = signature
			}
			items = append(items, item)
			outputIndex := len(items) - 1
			functionArguments[callID] = arguments
			if err := emitAntigravityEvent(ctx, fn, map[string]any{"type": "response.output_item.added", "output_index": outputIndex, "item": cloneMap(item)}); err != nil {
				return err
			}
			if err := emitAntigravityEvent(ctx, fn, map[string]any{"type": "response.function_call_arguments.delta", "output_index": outputIndex, "item_id": callID, "delta": arguments}); err != nil {
				return err
			}
		}
		if event.Done {
			break
		}
	}
	for outputIndex, item := range items {
		switch item["type"] {
		case "reasoning":
			item["status"] = "completed"
			item["summary"] = []any{map[string]any{"type": "summary_text", "text": reasoning.String()}}
			if err := emitAntigravityEvent(ctx, fn, map[string]any{
				"type": "response.reasoning_summary_text.done", "output_index": outputIndex, "item_id": item["id"], "summary_index": 0, "text": reasoning.String(),
			}); err != nil {
				return err
			}
			if err := emitAntigravityEvent(ctx, fn, map[string]any{
				"type": "response.reasoning_summary_part.done", "output_index": outputIndex, "item_id": item["id"], "summary_index": 0,
				"part": map[string]any{"type": "summary_text", "text": reasoning.String()},
			}); err != nil {
				return err
			}
		case "message":
			item["status"] = "completed"
			content := sliceAny(item["content"])
			part := mapAny(content[0])
			if part != nil {
				part["text"] = text.String()
			}
			if err := emitAntigravityEvent(ctx, fn, map[string]any{
				"type": "response.output_text.done", "output_index": outputIndex, "item_id": item["id"], "content_index": 0, "text": text.String(), "logprobs": []any{},
			}); err != nil {
				return err
			}
			if err := emitAntigravityEvent(ctx, fn, map[string]any{
				"type": "response.content_part.done", "output_index": outputIndex, "item_id": item["id"], "content_index": 0, "part": cloneMap(part),
			}); err != nil {
				return err
			}
		case "function_call":
			arguments := functionArguments[stringValue(item["id"])]
			if arguments == "" {
				arguments = "{}"
			}
			item["arguments"] = arguments
			item["status"] = "completed"
			if err := emitAntigravityEvent(ctx, fn, map[string]any{
				"type": "response.function_call_arguments.done", "output_index": outputIndex, "item_id": item["id"], "arguments": arguments,
			}); err != nil {
				return err
			}
		}
		if err := emitAntigravityEvent(ctx, fn, map[string]any{"type": "response.output_item.done", "output_index": outputIndex, "item": cloneMap(item)}); err != nil {
			return err
		}
	}
	status := "completed"
	if incompleteFinishReason(finishReason) {
		status = "incomplete"
	}
	response := map[string]any{
		"id": responseID, "object": "response", "created_at": nowFloat(), "status": status,
		"model": model, "output": mapsToAny(items), "parallel_tool_calls": true, "usage": antigravityUsage(usage),
	}
	terminalType := "response.completed"
	if status == "incomplete" {
		terminalType = "response.incomplete"
	}
	return emitAntigravityEvent(ctx, fn, map[string]any{"type": terminalType, "response": response})
}

func emitAntigravityEvent(ctx context.Context, fn func(map[string]any) error, event map[string]any) error {
	if observer := telemetryFromContext(ctx); observer != nil {
		observer.observeUpstreamEvent(event)
	}
	return fn(event)
}

func functionThoughtSignature(event cloudcode.Event, call cloudcode.FunctionCall) string {
	for _, candidate := range event.Candidates {
		for _, part := range candidate.Parts {
			if part.FunctionCall != nil && part.FunctionCall.Name == call.Name && (part.FunctionCall.ID == "" || part.FunctionCall.ID == call.ID) && part.ThoughtSignature != "" {
				return part.ThoughtSignature
			}
		}
	}
	if len(event.ThoughtSignatures) > 0 {
		return event.ThoughtSignatures[0]
	}
	return ""
}

func indexOfItem(items []map[string]any, wanted map[string]any) int {
	for index, item := range items {
		if item["id"] == wanted["id"] {
			return index
		}
	}
	return len(items)
}

func mapsToAny(items []map[string]any) []any {
	result := make([]any, len(items))
	for i, item := range items {
		result[i] = item
	}
	return result
}

func incompleteFinishReason(reason string) bool {
	reason = strings.ToLower(strings.TrimSpace(reason))
	return strings.Contains(reason, "max") || strings.Contains(reason, "length") || strings.Contains(reason, "safety")
}

func antigravityUsage(usage cloudcode.Usage) map[string]any {
	if !usage.HasData() {
		return nil
	}
	result := map[string]any{}
	if usage.InputTokens != 0 {
		result["input_tokens"] = usage.InputTokens
	}
	outputTokens := usage.OutputTokens + usage.ThinkingTokens
	if outputTokens != 0 {
		result["output_tokens"] = outputTokens
	}
	if usage.ThinkingTokens != 0 {
		result["output_tokens_details"] = map[string]any{"reasoning_tokens": usage.ThinkingTokens}
	}
	if usage.CachedTokens != 0 {
		result["input_tokens_details"] = map[string]any{"cached_tokens": usage.CachedTokens}
	}
	if usage.TotalTokens != 0 {
		result["total_tokens"] = usage.TotalTokens
	}
	return result
}

func buildAntigravityRequest(payload map[string]any, model, project string) (cloudcode.GenerateRequest, error) {
	requestID, err := cloudcode.NewRequestID()
	if err != nil {
		return cloudcode.GenerateRequest{}, err
	}
	sessionID, err := cloudcode.NewSessionID()
	if err != nil {
		return cloudcode.GenerateRequest{}, err
	}
	request := cloudcode.GenerateRequest{
		Model:        model,
		Project:      project,
		UserPromptID: stringValue(payload["user_prompt_id"]),
		UserAgent:    cloudcode.DefaultRequestUserAgent,
		RequestType:  cloudcode.DefaultRequestType,
		RequestID:    requestID,
		Request:      cloudcode.InternalRequest{SessionID: sessionID},
	}
	contents, inlineSystemParts, err := antigravityContents(sliceAny(payload["input"]))
	if err != nil {
		return cloudcode.GenerateRequest{}, err
	}
	request.Request.Contents = contents
	instructionParts := make([]cloudcode.ContentPart, 0, len(inlineSystemParts)+1)
	instructions, err := antigravityInstructionText(payload["instructions"])
	if err != nil {
		return cloudcode.GenerateRequest{}, err
	}
	if instructions = strings.TrimSpace(instructions); instructions != "" {
		instructionParts = append(instructionParts, cloudcode.ContentPart{Text: instructions})
	}
	instructionParts = append(instructionParts, inlineSystemParts...)
	if len(instructionParts) > 0 {
		request.Request.SystemInstruction = &cloudcode.SystemInstruction{Role: "user", Parts: instructionParts}
	}
	request.Request.Tools = antigravityTools(sliceAny(payload["tools"]))
	toolConfig, err := antigravityToolConfig(payload["tool_choice"])
	if err != nil {
		return cloudcode.GenerateRequest{}, err
	}
	request.Request.ToolConfig = toolConfig
	if generation := antigravityGenerationConfig(payload); generation != nil {
		request.Request.GenerationConfig = generation
	}
	return request, nil
}

func antigravityContents(input []any) ([]cloudcode.Content, []cloudcode.ContentPart, error) {
	contents := make([]cloudcode.Content, 0, len(input))
	systemParts := make([]cloudcode.ContentPart, 0)
	functionNames, err := responseFunctionCallNames(input)
	if err != nil {
		return nil, nil, err
	}
	for _, raw := range input {
		item := mapAny(raw)
		if item == nil {
			if text := strings.TrimSpace(stringValue(raw)); text != "" {
				contents = append(contents, cloudcode.Content{Role: "user", Parts: []cloudcode.ContentPart{{Text: text}}})
			}
			continue
		}
		typ := stringValue(item["type"])
		switch typ {
		case "function_call":
			callID := strings.TrimSpace(stringValue(valueOr(item["call_id"], item["id"])))
			name := strings.TrimSpace(stringValue(item["name"]))
			if callID == "" || name == "" {
				return nil, nil, errors.New("function_call must include call_id and name")
			}
			args := mapValueFromJSON(item["arguments"])
			part := cloudcode.ContentPart{FunctionCall: &cloudcode.FunctionCall{ID: callID, Name: name, Args: args}}
			if signature := firstMapString(item, "thought_signature", "thoughtSignature"); signature != "" {
				part.ThoughtSignature = signature
			}
			contents = append(contents, cloudcode.Content{Role: "model", Parts: []cloudcode.ContentPart{part}})
		case "function_call_output":
			name, err := resolveResponseFunctionOutputName(item, functionNames)
			if err != nil {
				return nil, nil, err
			}
			response := functionResponseValue(item["output"])
			contents = append(contents, cloudcode.Content{Role: "user", Parts: []cloudcode.ContentPart{{FunctionResponse: &cloudcode.FunctionResponse{ID: stringValue(item["call_id"]), Name: name, Response: response}}}})
		case "reasoning":
			if encrypted := firstMapString(item, "encrypted_content", "encryptedContent"); encrypted != "" {
				contents = append(contents, cloudcode.Content{Role: "model", Parts: []cloudcode.ContentPart{{EncryptedContent: encrypted}}})
			}
		default:
			role := stringValue(item["role"])
			if role == "system" || role == "developer" {
				text, err := antigravityInstructionText(item["content"])
				if err != nil {
					return nil, nil, err
				}
				if text = strings.TrimSpace(text); text != "" {
					systemParts = append(systemParts, cloudcode.ContentPart{Text: text})
				}
				continue
			}
			if role == "assistant" {
				role = "model"
			} else if role == "" || role == "user" {
				role = "user"
			} else {
				return nil, nil, fmt.Errorf("unsupported Responses input role %q", role)
			}
			text := responseInputText(item["content"])
			if text != "" {
				contents = append(contents, cloudcode.Content{Role: role, Parts: []cloudcode.ContentPart{{Text: text}}})
			}
		}
	}
	if len(contents) == 0 {
		return nil, nil, errors.New("Antigravity request input is empty")
	}
	return contents, systemParts, nil
}

func responseInputText(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	var result strings.Builder
	for _, raw := range sliceAny(value) {
		part := mapAny(raw)
		if part == nil {
			continue
		}
		if typ := stringValue(part["type"]); typ == "input_text" || typ == "text" || typ == "output_text" {
			result.WriteString(stringValue(part["text"]))
		}
	}
	return result.String()
}

func antigravityInstructionText(value any) (string, error) {
	if value == nil {
		return "", nil
	}
	if text, ok := value.(string); ok {
		return text, nil
	}
	var result strings.Builder
	for _, raw := range sliceAny(value) {
		part := mapAny(raw)
		if part == nil {
			return "", errors.New("Antigravity system/developer instructions support text parts only")
		}
		switch stringValue(part["type"]) {
		case "input_text", "text", "output_text":
			result.WriteString(stringValue(part["text"]))
		default:
			return "", errors.New("Antigravity system/developer instructions support text parts only")
		}
	}
	return result.String(), nil
}

func antigravityTools(tools []any) []cloudcode.Tool {
	result := make([]cloudcode.Tool, 0, len(tools))
	for _, raw := range tools {
		tool := mapAny(raw)
		if tool == nil {
			continue
		}
		fn := mapAny(tool["function"])
		if fn == nil && stringValue(tool["type"]) == "function" {
			fn = tool
		}
		if fn == nil || strings.TrimSpace(stringValue(fn["name"])) == "" {
			continue
		}
		result = append(result, cloudcode.Tool{FunctionDeclarations: []cloudcode.FunctionDeclaration{{
			Name: stringValue(fn["name"]), Description: stringValue(fn["description"]), Parameters: antigravitySchema(fn["parameters"]),
		}}})
	}
	return result
}

func antigravityToolConfig(value any) (*cloudcode.ToolConfig, error) {
	if value == nil {
		return nil, nil
	}
	if mode, ok := value.(string); ok {
		switch strings.ToLower(strings.TrimSpace(mode)) {
		case "", "auto":
			return nil, nil
		case "none":
			return &cloudcode.ToolConfig{FunctionCallingConfig: &cloudcode.FunctionCallingConfig{Mode: "NONE"}}, nil
		case "required":
			return &cloudcode.ToolConfig{FunctionCallingConfig: &cloudcode.FunctionCallingConfig{Mode: "ANY"}}, nil
		default:
			return nil, fmt.Errorf("unsupported tool_choice %q", mode)
		}
	}
	m := mapAny(value)
	if m == nil {
		return nil, errors.New("tool_choice must be auto, none, required, or a function selection")
	}
	if strings.ToLower(strings.TrimSpace(stringValue(m["type"]))) != "function" {
		return nil, errors.New("unsupported tool_choice object")
	}
	name := ""
	if fn := mapAny(m["function"]); fn != nil {
		name = strings.TrimSpace(stringValue(fn["name"]))
	}
	if name == "" {
		name = strings.TrimSpace(stringValue(m["name"]))
	}
	if name == "" {
		return nil, errors.New("tool_choice function name is required")
	}
	return &cloudcode.ToolConfig{FunctionCallingConfig: &cloudcode.FunctionCallingConfig{
		Mode:                 "ANY",
		AllowedFunctionNames: []string{name},
	}}, nil
}

func antigravitySchema(value any) *cloudcode.ParameterSchema {
	m := mapAny(value)
	if m == nil {
		return nil
	}
	result := &cloudcode.ParameterSchema{Type: strings.ToUpper(stringValue(m["type"])), Description: stringValue(m["description"])}
	for _, raw := range sliceAny(m["required"]) {
		if name := strings.TrimSpace(stringValue(raw)); name != "" {
			result.Required = append(result.Required, name)
		}
	}
	for _, raw := range sliceAny(m["enum"]) {
		result.Enum = append(result.Enum, stringValue(raw))
	}
	if properties := mapAny(m["properties"]); properties != nil {
		result.Properties = map[string]*cloudcode.ParameterSchema{}
		for name, raw := range properties {
			result.Properties[name] = antigravitySchema(raw)
		}
	}
	result.Items = antigravitySchema(m["items"])
	return result
}

func antigravityGenerationConfig(payload map[string]any) *cloudcode.GenerationConfig {
	config := &cloudcode.GenerationConfig{}
	hasConfig := false
	if value, ok := payload["temperature"].(float64); ok {
		config.Temperature, hasConfig = value, true
	}
	if value, ok := payload["top_p"].(float64); ok {
		config.TopP, hasConfig = value, true
	}
	if tokens := intValue(payload["max_output_tokens"]); tokens > 0 {
		config.MaxOutputTokens, hasConfig = tokens, true
	}
	if reasoning := mapAny(payload["reasoning"]); reasoning != nil {
		thinking := &cloudcode.ThinkingConfig{}
		includeThoughts := true
		thinking.IncludeThoughts = &includeThoughts
		thinking.ThinkingLevel = antigravityThinkingLevel(stringValue(reasoning["effort"]))
		config.ThinkingConfig = thinking
		hasConfig = true
	}
	return func() *cloudcode.GenerationConfig {
		if !hasConfig {
			return nil
		}
		return config
	}()
}

func antigravityThinkingLevel(effort string) string {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "low":
		return "LOW"
	case "medium":
		return "MEDIUM"
	case "high":
		return "HIGH"
	default:
		return ""
	}
}

func mapValueFromJSON(value any) map[string]any {
	if m := mapAny(value); m != nil {
		return cloneMap(m)
	}
	text := strings.TrimSpace(stringValue(value))
	if text == "" {
		return map[string]any{}
	}
	var result map[string]any
	if json.Unmarshal([]byte(text), &result) == nil && result != nil {
		return result
	}
	return map[string]any{"_raw": text}
}

func functionResponseValue(value any) map[string]any {
	if m := mapAny(value); m != nil {
		return cloneMap(m)
	}
	text := strings.TrimSpace(stringValue(value))
	if text != "" {
		var result map[string]any
		if json.Unmarshal([]byte(text), &result) == nil && result != nil {
			return result
		}
	}
	return map[string]any{"output": stringValue(value)}
}

func firstMapString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(stringValue(m[key])); value != "" {
			return value
		}
	}
	return ""
}

var _ modelProvider = (*antigravityProvider)(nil)
