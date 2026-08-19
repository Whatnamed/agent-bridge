package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	agyauth "github.com/whatnamed/agent-bridge/shared/antigravity/auth"
	"github.com/whatnamed/agent-bridge/shared/antigravity/cloudcode"
)

const (
	antigravityProviderID  = "antigravity"
	stableAntigravityModel = "gemini-3.7-flash-high"
	gemini37LowModel       = "gemini-3.7-flash-low"
	gemini37MediumModel    = "gemini-3.7-flash-medium"
)

const (
	antigravityStateDisabled = "disabled"
	antigravityStateWarming  = "warming"
	antigravityStateReady    = "ready"
	antigravityStateDegraded = "degraded"
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
	state          string
	lastRefresh    time.Time
	lastErrorClass string
	lastErrorAt    time.Time
	prewarmOnce    sync.Once
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
		state:        antigravityStateWarming,
	}, nil
}

func (p *antigravityProvider) ID() string { return antigravityProviderID }

func (p *antigravityProvider) validatePayload(payload map[string]any) error {
	return validateAntigravityPayload(payload)
}

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
	sort.Strings(ids)
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
		p.state = antigravityStateReady
		p.mu.Unlock()
		return nil
	}
	p.state = antigravityStateWarming
	// Keep control-plane refreshes serialized. This is deliberately outside the
	// generation hot path once the TTLs are warm, while avoiding duplicate
	// loadCodeAssist/fetchAvailableModels requests during startup bursts.
	defer p.mu.Unlock()
	if !projectFresh {
		loaded, err := p.client.LoadCodeAssist(ctx)
		if err != nil {
			return p.controlPlaneFailureLocked(antigravityProviderError(ctx, "loadCodeAssist", err))
		}
		project := strings.TrimSpace(p.cfg.AntigravityProject)
		if project == "" {
			project = strings.TrimSpace(loaded.CloudAICompanionProject)
		}
		if project == "" {
			return p.controlPlaneFailureLocked(antigravityProviderStaticError(ctx, "loadCodeAssist", "Antigravity loadCodeAssist did not return a verified project.", "provider_control_plane", true))
		}
		p.project = project
		p.projectExpires = now.Add(p.projectTTL())
		projectFresh = true
	}
	if !catalogFresh {
		catalog, err := p.client.FetchAvailableModels(ctx)
		if err != nil {
			return p.controlPlaneFailureLocked(antigravityProviderError(ctx, "fetchAvailableModels", err))
		}
		p.catalog = catalog
		p.catalogExpires = now.Add(p.catalogTTL())
	}
	status, err := p.tokenManager.CredentialStatus()
	if err == nil {
		p.tokenExpiry = status.Expiry
	}
	p.state = antigravityStateReady
	p.lastRefresh = time.Now()
	p.lastErrorClass = ""
	p.lastErrorAt = time.Time{}
	return nil
}

func (p *antigravityProvider) controlPlaneFailure(err error) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.controlPlaneFailureLocked(err)
}

func (p *antigravityProvider) controlPlaneFailureLocked(err error) error {
	p.state = antigravityStateDegraded
	p.lastErrorAt = time.Now()
	var providerErr *providerBackendError
	if errors.As(err, &providerErr) && providerErr.ErrorClass != "" {
		p.lastErrorClass = providerErr.ErrorClass
	} else {
		p.lastErrorClass = "provider_control_plane"
	}
	return err
}

func (p *antigravityProvider) prewarm(parent context.Context) {
	if p == nil {
		return
	}
	p.prewarmOnce.Do(func() {
		timeout := p.cfg.Timeout
		if timeout <= 0 || timeout > 20*time.Second {
			timeout = 20 * time.Second
		}
		ctx, cancel := context.WithTimeout(parent, timeout)
		defer cancel()
		_ = p.ensureControlPlane(ctx)
	})
}

func (p *antigravityProvider) diagnostics() map[string]any {
	if p == nil {
		return map[string]any{"status": antigravityStateDisabled}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	result := map[string]any{
		"status":            valueOr(p.state, antigravityStateWarming),
		"catalog_size":      len(p.catalog.Models),
		"project_available": p.project != "",
	}
	if !p.lastRefresh.IsZero() {
		result["last_refresh"] = p.lastRefresh.UTC()
	}
	if !p.tokenExpiry.IsZero() {
		result["oauth_token_expiry"] = p.tokenExpiry.UTC()
	}
	if p.lastErrorClass != "" {
		result["last_error_class"] = p.lastErrorClass
	}
	if !p.lastErrorAt.IsZero() {
		result["last_error_at"] = p.lastErrorAt.UTC()
	}
	return result
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
	switch strings.TrimSpace(model) {
	case gemini37LowModel, gemini37MediumModel, stableAntigravityModel:
		return true
	default:
		return false
	}
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
		return antigravityProviderError(ctx, "streamGenerateContent", err)
	}
	defer stream.Close()
	return p.emitCanonicalStream(ctx, stream, resolution.ActualUpstreamModel, fn)
}

func antigravityProviderStaticError(ctx context.Context, operation, message, class string, retryable bool) error {
	err := &providerBackendError{
		Status: 502, Message: message, ErrorType: "api_error", Code: "provider_error",
		Provider: antigravityProviderID, Operation: operation, ErrorClass: class, Retryable: retryable,
	}
	if observer := telemetryFromContext(ctx); observer != nil {
		observer.observeProviderError(err)
	}
	return err
}

func antigravityProviderError(ctx context.Context, operation string, cause error) error {
	if cause == nil {
		return antigravityProviderStaticError(ctx, operation, "Antigravity provider request failed.", "provider_error", true)
	}
	if errors.Is(cause, context.Canceled) {
		return cause
	}
	result := &providerBackendError{
		Status: 502, Message: "Antigravity provider transport failed.", ErrorType: "api_error", Code: "provider_transport_error",
		Provider: antigravityProviderID, Operation: operation, ErrorClass: "provider_transport_error", Retryable: true, Cause: cause,
	}
	var httpErr *cloudcode.HTTPError
	if errors.As(cause, &httpErr) {
		result.UpstreamStatus = httpErr.StatusCode
		result.RetryAfter = httpErr.RetryAfter
		switch httpErr.StatusCode {
		case http.StatusBadRequest, http.StatusUnprocessableEntity:
			result.Status = http.StatusBadRequest
			result.Message = fmt.Sprintf("Antigravity provider rejected the request (upstream HTTP %d).", httpErr.StatusCode)
			result.ErrorType = "invalid_request_error"
			result.Code = "provider_invalid_request"
			result.ErrorClass = "provider_invalid_request"
			result.Retryable = false
		case http.StatusUnauthorized:
			result.Status = http.StatusUnauthorized
			result.Message = "Antigravity provider authentication failed."
			result.ErrorType = "authentication_error"
			result.Code = "provider_authentication"
			result.ErrorClass = "provider_authentication"
			result.Retryable = false
		case http.StatusForbidden:
			result.Status = http.StatusForbidden
			result.Message = "Antigravity provider permission was denied."
			result.ErrorType = "permission_error"
			result.Code = "provider_permission"
			result.ErrorClass = "provider_permission"
			result.Retryable = false
		case http.StatusTooManyRequests:
			result.Status = http.StatusTooManyRequests
			result.Message = "Antigravity provider rate limited the request."
			result.ErrorType = "rate_limit_error"
			result.Code = "provider_rate_limit"
			result.ErrorClass = "provider_rate_limit"
			result.Retryable = true
		default:
			if httpErr.StatusCode >= 500 && httpErr.StatusCode <= 599 {
				result.Status = httpErr.StatusCode
				if result.Status != http.StatusBadGateway && result.Status != http.StatusServiceUnavailable && result.Status != http.StatusGatewayTimeout {
					result.Status = http.StatusBadGateway
				}
				result.Message = fmt.Sprintf("Antigravity provider upstream failed (upstream HTTP %d).", httpErr.StatusCode)
				result.Code = "provider_upstream_error"
				result.ErrorClass = "provider_upstream_error"
				result.Retryable = true
			} else {
				result.Message = fmt.Sprintf("Antigravity provider returned an unexpected HTTP status (upstream HTTP %d).", httpErr.StatusCode)
				result.Code = "provider_http_error"
				result.ErrorClass = "provider_http_error"
				result.Retryable = false
			}
		}
	} else {
		var networkErr net.Error
		if errors.Is(cause, context.DeadlineExceeded) || (errors.As(cause, &networkErr) && networkErr.Timeout()) {
			result.Status = http.StatusGatewayTimeout
			result.Message = "Antigravity provider request timed out."
			result.Code = "provider_timeout"
			result.ErrorClass = "provider_timeout"
			result.Retryable = true
		} else if errors.Is(cause, cloudcode.ErrTruncatedStream) {
			result.Message = "Antigravity provider stream ended before a trusted terminal event."
			result.Code = "provider_stream_truncated"
			result.ErrorClass = "provider_stream_truncated"
			result.Retryable = true
		}
	}
	if observer := telemetryFromContext(ctx); observer != nil {
		observer.observeProviderError(result)
	}
	return result
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
				return antigravityProviderStaticError(ctx, "streamGenerateContent", "Antigravity provider returned a failed response.", "provider_response_failed", true)
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
	functionGroupSequence := 0
	for {
		event, err := stream.Next()
		if err != nil {
			return antigravityProviderError(ctx, "streamGenerateContent", err)
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
				messageItem = map[string]any{"id": newID("msg"), "type": "message", "role": "assistant", "status": "in_progress", "phase": "final_answer", "content": []any{}}
				items = append(items, messageItem)
				outputIndex := len(items) - 1
				if err := emitAntigravityEvent(ctx, fn, map[string]any{"type": "response.output_item.added", "output_index": outputIndex, "item": cloneMap(messageItem)}); err != nil {
					return err
				}
				part := map[string]any{"type": "output_text", "text": "", "annotations": []any{}, "logprobs": []any{}}
				messageItem["content"] = []any{part}
				if err := emitAntigravityEvent(ctx, fn, map[string]any{
					"type": "response.content_part.added", "output_index": outputIndex, "item_id": messageItem["id"], "content_index": 0,
					"part": cloneMap(part),
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
		for _, calls := range functionCallGroups(event) {
			groupSize := len(calls)
			if groupSize == 0 {
				continue
			}
			stepID := ""
			if groupSize > 1 {
				stepID = fmt.Sprintf("%s:%d", responseID, functionGroupSequence)
			}
			functionGroupSequence++
			for partIndex, call := range calls {
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
					found := false
					for _, item := range items {
						existingCallID, _, decodeErr := decodeThoughtSignatureToolCallID(stringValue(item["call_id"]))
						if decodeErr != nil {
							return decodeErr
						}
						if existingCallID == callID && stringValue(item["name"]) == call.Name {
							found = true
							signature := strings.TrimSpace(call.ThoughtSignature)
							if signature != "" {
								existingSignature := firstMapString(item, "thought_signature", "thoughtSignature")
								if existingSignature == "" {
									return errors.New("Antigravity function call signature arrived after its transport id was emitted")
								}
								if existingSignature != signature {
									return errors.New("Antigravity function call thought signature changed for an existing call")
								}
							}
							functionArguments[stringValue(item["id"])] = arguments
							break
						}
					}
					if !found {
						return errors.New("Antigravity function call identity changed during streaming")
					}
					continue
				}
				functionKeys[key] = struct{}{}
				signature := strings.TrimSpace(call.ThoughtSignature)
				transportID := encodeFunctionCallTransportID(callID, signature, stepID, partIndex, groupSize)
				if groupSize > 1 && !strings.HasPrefix(transportID, functionCallTransportV2Prefix) {
					return errors.New("Antigravity parallel function call transport envelope could not be created")
				}
				item := map[string]any{
					"id": transportID, "type": "function_call", "status": "in_progress", "call_id": transportID,
					"name": call.Name, "arguments": "",
				}
				if signature != "" {
					item["thought_signature"] = signature
				}
				items = append(items, item)
				outputIndex := len(items) - 1
				functionArguments[transportID] = arguments
				if err := emitAntigravityEvent(ctx, fn, map[string]any{"type": "response.output_item.added", "output_index": outputIndex, "item": cloneMap(item)}); err != nil {
					return err
				}
				if err := emitAntigravityEvent(ctx, fn, map[string]any{"type": "response.function_call_arguments.delta", "output_index": outputIndex, "item_id": transportID, "delta": arguments}); err != nil {
					return err
				}
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
				"type": "response.function_call_arguments.done", "output_index": outputIndex, "item_id": item["id"], "name": item["name"], "arguments": arguments,
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

func functionThoughtSignature(_ cloudcode.Event, call cloudcode.FunctionCall) string {
	return strings.TrimSpace(call.ThoughtSignature)
}

func functionCallGroups(event cloudcode.Event) [][]cloudcode.FunctionCall {
	groups := make([][]cloudcode.FunctionCall, 0)
	for _, candidate := range event.Candidates {
		group := make([]cloudcode.FunctionCall, 0)
		for _, part := range candidate.Parts {
			if part.FunctionCall == nil {
				continue
			}
			call := *part.FunctionCall
			if call.ThoughtSignature == "" {
				call.ThoughtSignature = part.ThoughtSignature
			}
			group = append(group, call)
		}
		if len(group) > 0 {
			groups = append(groups, group)
		}
	}
	if len(groups) == 0 && len(event.FunctionCalls) > 0 {
		groups = append(groups, append([]cloudcode.FunctionCall(nil), event.FunctionCalls...))
	}
	return groups
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
	if err := validateAntigravityPayload(payload); err != nil {
		return cloudcode.GenerateRequest{}, err
	}
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
	contents, inlineSystemParts, err := antigravityContents(normalizeResponseInput(payload["input"]))
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
	tools, err := antigravityTools(payload["tools"])
	if err != nil {
		return cloudcode.GenerateRequest{}, err
	}
	request.Request.Tools = tools
	toolConfig, err := antigravityToolConfig(payload["tool_choice"])
	if err != nil {
		return cloudcode.GenerateRequest{}, err
	}
	request.Request.ToolConfig = toolConfig
	generation, err := antigravityGenerationConfig(payload)
	if err != nil {
		return cloudcode.GenerateRequest{}, err
	}
	if generation != nil {
		request.Request.GenerationConfig = generation
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return cloudcode.GenerateRequest{}, fmt.Errorf("Antigravity request could not be encoded: %w", err)
	}
	if len(encoded) > maxAntigravityRequestJSONBytes {
		return cloudcode.GenerateRequest{}, fmt.Errorf("Antigravity request exceeds the %d byte JSON limit", maxAntigravityRequestJSONBytes)
	}
	return request, nil
}

func validateAntigravityPayload(payload map[string]any) error {
	if err := validateAntigravityReasoningPreset(payload); err != nil {
		return err
	}
	if parallel, ok := payload["parallel_tool_calls"]; ok && parallel != nil {
		if enabled, ok := parallel.(bool); ok && !enabled {
			return errors.New("Antigravity does not support parallel_tool_calls=false")
		}
	}
	if _, _, err := antigravityStructuredOutputFormat(payload); err != nil {
		return err
	}
	if raw, exists := payload["response_format"]; exists && raw != nil {
		formatType := ""
		if format := mapAny(raw); format != nil {
			formatType = strings.TrimSpace(stringValue(format["type"]))
		}
		if formatType == "" {
			return errors.New("Antigravity does not support response_format")
		}
		return fmt.Errorf("Antigravity does not support response_format %q", formatType)
	}
	if _, _, err := antigravityContents(normalizeResponseInput(payload["input"])); err != nil {
		return err
	}
	if _, err := antigravityInstructionText(payload["instructions"]); err != nil {
		return err
	}
	if _, err := antigravityTools(payload["tools"]); err != nil {
		return err
	}
	return nil
}

func validateAntigravityReasoningPreset(payload map[string]any) error {
	reasoning := mapAny(payload["reasoning"])
	if reasoning == nil {
		return nil
	}
	effort := strings.ToLower(strings.TrimSpace(stringValue(reasoning["effort"])))
	if effort == "" {
		return nil
	}
	if effort != "low" && effort != "medium" && effort != "high" {
		return fmt.Errorf("Antigravity does not support reasoning effort %q", effort)
	}
	model := strings.TrimSpace(stringValue(payload["model"]))
	if strings.HasSuffix(model, "-low") || strings.HasSuffix(model, "-medium") || strings.HasSuffix(model, "-high") {
		suffix := model[strings.LastIndex(model, "-")+1:]
		if effort != suffix {
			return fmt.Errorf("Antigravity reasoning effort %q conflicts with model preset %q", effort, model)
		}
	}
	return nil
}

func antigravityInstructionText(value any) (string, error) {
	if value == nil {
		return "", nil
	}
	if text, ok := value.(string); ok {
		return text, nil
	}
	rawParts, ok := value.([]any)
	if !ok {
		return "", errors.New("Antigravity system/developer instructions support text parts only")
	}
	var result strings.Builder
	for _, raw := range rawParts {
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

func antigravityTools(value any) ([]cloudcode.Tool, error) {
	if value == nil {
		return nil, nil
	}
	tools, ok := value.([]any)
	if !ok {
		return nil, errors.New("Antigravity tools must be an array")
	}
	result := make([]cloudcode.Tool, 0, len(tools))
	for _, raw := range tools {
		tool := mapAny(raw)
		if tool == nil {
			return nil, errors.New("Antigravity tools must contain function definitions")
		}
		if toolType := strings.TrimSpace(stringValue(tool["type"])); toolType != "function" {
			return nil, fmt.Errorf("Antigravity does not support tool type %q", toolType)
		}
		fn := mapAny(tool["function"])
		if fn == nil {
			fn = tool
		}
		if fn == nil || strings.TrimSpace(stringValue(fn["name"])) == "" {
			return nil, errors.New("Antigravity function tools must include a name")
		}
		result = append(result, cloudcode.Tool{FunctionDeclarations: []cloudcode.FunctionDeclaration{{
			Name: stringValue(fn["name"]), Description: stringValue(fn["description"]), Parameters: antigravitySchema(fn["parameters"]),
		}}})
	}
	return result, nil
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

func antigravityGenerationConfig(payload map[string]any) (*cloudcode.GenerationConfig, error) {
	config := &cloudcode.GenerationConfig{}
	hasConfig := false
	if value, exists := payload["temperature"]; exists && value != nil {
		parsed, err := antigravityFloatParameter(value, "temperature", 0, 2)
		if err != nil {
			return nil, err
		}
		config.Temperature, hasConfig = parsed, true
	}
	if value, exists := payload["top_p"]; exists && value != nil {
		parsed, err := antigravityFloatParameter(value, "top_p", 0, 1)
		if err != nil {
			return nil, err
		}
		config.TopP, hasConfig = parsed, true
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
	formatType, schema, err := antigravityStructuredOutputFormat(payload)
	if err != nil {
		return nil, err
	}
	switch formatType {
	case "json_object", "json_schema":
		config.ResponseMimeType = "application/json"
		config.ResponseSchema = schema
		hasConfig = true
	}
	return func() (*cloudcode.GenerationConfig, error) {
		if !hasConfig {
			return nil, nil
		}
		return config, nil
	}()
}

func antigravityFloatParameter(value any, name string, minimum, maximum float64) (*float64, error) {
	var parsed float64
	switch value := value.(type) {
	case json.Number:
		var err error
		parsed, err = value.Float64()
		if err != nil {
			return nil, fmt.Errorf("Antigravity %s must be a JSON number", name)
		}
	case float64:
		parsed = value
	case float32:
		parsed = float64(value)
	case int:
		parsed = float64(value)
	case int8:
		parsed = float64(value)
	case int16:
		parsed = float64(value)
	case int32:
		parsed = float64(value)
	case int64:
		parsed = float64(value)
	case uint:
		parsed = float64(value)
	case uint8:
		parsed = float64(value)
	case uint16:
		parsed = float64(value)
	case uint32:
		parsed = float64(value)
	case uint64:
		parsed = float64(value)
	default:
		return nil, fmt.Errorf("Antigravity %s must be a JSON number", name)
	}
	if math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed < minimum || parsed > maximum {
		return nil, fmt.Errorf("Antigravity %s must be between %g and %g", name, minimum, maximum)
	}
	return &parsed, nil
}

func antigravityStructuredOutputFormat(payload map[string]any) (string, any, error) {
	text, exists := payload["text"]
	if !exists || text == nil {
		return "", nil, nil
	}
	textMap := mapAny(text)
	if textMap == nil {
		return "", nil, errors.New("Antigravity text configuration must be an object")
	}
	rawFormat, exists := textMap["format"]
	if !exists || rawFormat == nil {
		return "", nil, nil
	}
	format := mapAny(rawFormat)
	if format == nil {
		return "", nil, errors.New("Antigravity does not support text.format")
	}
	formatType := strings.TrimSpace(stringValue(format["type"]))
	switch formatType {
	case "text":
		return "text", nil, nil
	case "json_object":
		return "json_object", nil, nil
	case "json_schema":
		schema := format["schema"]
		if schema == nil {
			return "", nil, errors.New("Antigravity json_schema format requires schema")
		}
		switch value := schema.(type) {
		case map[string]any:
			return "json_schema", cloneMap(value), nil
		case []any:
			return "json_schema", cloneSlice(value), nil
		default:
			return "", nil, errors.New("Antigravity json_schema format requires an object or array schema")
		}
	case "":
		return "", nil, errors.New("Antigravity does not support text.format")
	default:
		return "", nil, fmt.Errorf("Antigravity does not support text.format %q", formatType)
	}
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
