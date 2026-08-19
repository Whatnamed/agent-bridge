package cloudcode

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"time"
)

const (
	DefaultEndpoint         = "https://daily-cloudcode-pa.googleapis.com"
	DefaultRequestUserAgent = "antigravity"
	DefaultRequestType      = "agent"
	// Keep this in one place. It is the user's supplied AGY 1.1.14 baseline,
	// not a dependency on an installed AGY executable.
	DefaultHTTPUserAgentVersion = "1.1.14"
)

type TokenSource interface {
	AccessToken(context.Context) (string, error)
	Refresh(context.Context) error
}

type Client struct {
	Endpoint         string
	HTTPClient       *http.Client
	TokenSource      TokenSource
	RequestUserAgent string
	RequestType      string
	HTTPUserAgent    string
}

// HTTPError preserves only the upstream HTTP status. Response bodies are not
// retained because they may contain provider-specific or credential-adjacent
// details that must not cross the bridge boundary.
type HTTPError struct {
	Operation  string
	StatusCode int
}

func (e *HTTPError) Error() string {
	if e == nil {
		return "CloudCode HTTP request failed"
	}
	return fmt.Sprintf("CloudCode %s returned HTTP %d", e.Operation, e.StatusCode)
}

func NewClient(endpoint string, tokenSource TokenSource) *Client {
	if strings.TrimSpace(endpoint) == "" {
		endpoint = DefaultEndpoint
	}
	return &Client{
		Endpoint:         strings.TrimRight(endpoint, "/"),
		HTTPClient:       http.DefaultClient,
		TokenSource:      tokenSource,
		RequestUserAgent: DefaultRequestUserAgent,
		RequestType:      DefaultRequestType,
		HTTPUserAgent:    defaultHTTPUserAgent(),
	}
}

func defaultHTTPUserAgent() string {
	return fmt.Sprintf("antigravity/cli/%s (aidev_client; os_type=%s; arch=%s; cl=964361259; auth_method=consumer)", DefaultHTTPUserAgentVersion, runtime.GOOS, runtime.GOARCH)
}

func NewRequest(mode Mode, model, project, prompt string, includeTool bool) (GenerateRequest, error) {
	if mode != ModeCompat && mode != ModeMinimal {
		return GenerateRequest{}, fmt.Errorf("unsupported POC mode %q", mode)
	}
	if strings.TrimSpace(model) == "" {
		return GenerateRequest{}, errors.New("model is required")
	}
	if strings.TrimSpace(prompt) == "" {
		return GenerateRequest{}, errors.New("prompt is required")
	}
	requestID, err := NewRequestID()
	if err != nil {
		return GenerateRequest{}, err
	}
	sessionID, err := randomHex(16)
	if err != nil {
		return GenerateRequest{}, err
	}
	request := GenerateRequest{
		Model:       model,
		Project:     project,
		UserAgent:   DefaultRequestUserAgent,
		RequestType: DefaultRequestType,
		RequestID:   requestID,
		Request: InternalRequest{
			Contents:  []Content{{Role: "user", Parts: []ContentPart{{Text: prompt}}}},
			SessionID: sessionID,
		},
	}
	if mode == ModeCompat {
		includeThoughts := true
		thinkingBudget := 10001
		request.Request.SystemInstruction = &SystemInstruction{
			Role:  "user",
			Parts: []ContentPart{{Text: "This is an isolated Agent Bridge compatibility probe. Answer the user's request directly."}},
		}
		request.Request.GenerationConfig = &GenerationConfig{
			ThinkingConfig: &ThinkingConfig{
				IncludeThoughts: &includeThoughts,
				ThinkingBudget:  &thinkingBudget,
			},
		}
	}
	if includeTool {
		request.Request.Tools = []Tool{{FunctionDeclarations: []FunctionDeclaration{{
			Name:        "get_test_value",
			Description: "Return a fixed value for the requested test name. This is the only tool enabled by the POC.",
			Parameters: &ParameterSchema{
				Type:     "OBJECT",
				Required: []string{"name"},
				Properties: map[string]*ParameterSchema{
					"name": {Type: "STRING"},
				},
			},
		}}}}
	}
	return request, nil
}

func NewRequestID() (string, error) {
	conversationID, err := randomHex(16)
	if err != nil {
		return "", err
	}
	trajectoryID, err := randomHex(16)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("agent/%s/%d/%s/1", conversationID, time.Now().UnixMilli(), trajectoryID), nil
}

// NewSessionID returns a fresh CloudCode conversation identifier for a
// provider request. It is separate from the request id so callers can keep
// one session across a function-call continuation when the upstream contract
// requires it.
func NewSessionID() (string, error) {
	return randomHex(16)
}

func randomHex(size int) (string, error) {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate CloudCode request id: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func (c *Client) LoadCodeAssist(ctx context.Context) (LoadCodeAssistResponse, error) {
	body, err := json.Marshal(LoadCodeAssistRequest{Metadata: Metadata{IdeType: "ANTIGRAVITY"}})
	if err != nil {
		return LoadCodeAssistResponse{}, fmt.Errorf("encode loadCodeAssist request: %w", err)
	}
	responseBody, err := c.jsonRequest(ctx, http.MethodPost, "/v1internal:loadCodeAssist", body)
	if err != nil {
		return LoadCodeAssistResponse{}, err
	}
	var response LoadCodeAssistResponse
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return LoadCodeAssistResponse{}, fmt.Errorf("decode loadCodeAssist response: %w", err)
	}
	return response, nil
}

func (c *Client) FetchAvailableModels(ctx context.Context) (ModelsResponse, error) {
	responseBody, err := c.jsonRequest(ctx, http.MethodPost, "/v1internal:fetchAvailableModels", []byte("{}"))
	if err != nil {
		return ModelsResponse{}, err
	}
	var response ModelsResponse
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return ModelsResponse{}, fmt.Errorf("decode fetchAvailableModels response: %w", err)
	}
	if response.Models == nil {
		response.Models = map[string]AvailableModel{}
	}
	return response, nil
}

func (c *Client) jsonRequest(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	response, err := c.do(ctx, method, path, body, "application/json")
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64*1024))
		return nil, &HTTPError{Operation: path, StatusCode: response.StatusCode}
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 4*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read CloudCode %s response: %w", path, err)
	}
	return data, nil
}

func (c *Client) do(ctx context.Context, method, path string, body []byte, accept string) (*http.Response, error) {
	if c == nil {
		return nil, errors.New("CloudCode client is nil")
	}
	if c.HTTPClient == nil {
		c.HTTPClient = http.DefaultClient
	}
	if c.TokenSource == nil {
		return nil, errors.New("CloudCode token source is not configured")
	}
	token, err := c.TokenSource.AccessToken(ctx)
	if err != nil {
		return nil, err
	}
	response, err := c.doWithToken(ctx, method, path, body, accept, token)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusUnauthorized {
		return response, nil
	}
	_ = response.Body.Close()
	if err := c.TokenSource.Refresh(ctx); err != nil {
		return nil, fmt.Errorf("CloudCode returned HTTP 401 and token refresh failed: %w", err)
	}
	refreshedToken, err := c.TokenSource.AccessToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("reload refreshed CloudCode token: %w", err)
	}
	return c.doWithToken(ctx, method, path, body, accept, refreshedToken)
}

func (c *Client) doWithToken(ctx context.Context, method, path string, body []byte, accept, token string) (*http.Response, error) {
	endpoint := strings.TrimRight(c.Endpoint, "/") + path
	request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create CloudCode request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", accept)
	request.Header.Set("User-Agent", c.HTTPUserAgent)
	response, err := c.HTTPClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("CloudCode request failed: %w", err)
	}
	return response, nil
}

type Stream struct {
	response           *http.Response
	decoder            *SSEDecoder
	ResponseReceivedAt time.Time
	trustedTermination bool
}

func (c *Client) StreamGenerateContent(ctx context.Context, request GenerateRequest) (*Stream, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("encode CloudCode generation request: %w", err)
	}
	response, err := c.do(ctx, http.MethodPost, "/v1internal:streamGenerateContent?alt=sse", body, "text/event-stream")
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_ = response.Body.Close()
		return nil, &HTTPError{Operation: "streamGenerateContent", StatusCode: response.StatusCode}
	}
	return &Stream{
		response:           response,
		decoder:            NewSSEDecoder(response.Body),
		ResponseReceivedAt: time.Now(),
	}, nil
}

func (s *Stream) Next() (Event, error) {
	if s == nil || s.decoder == nil {
		return Event{}, errors.New("CloudCode stream is not initialized")
	}
	event, err := s.decoder.Next()
	if err != nil {
		if errors.Is(err, io.EOF) {
			if s.trustedTermination {
				return Event{Done: true, CleanEOF: true}, nil
			}
			return Event{}, fmt.Errorf("%w: no finishReason, terminal candidate, or final usage was observed", ErrTruncatedStream)
		}
		return Event{}, err
	}
	if bytes.Equal(bytes.TrimSpace(event.Data), []byte("[DONE]")) {
		return Event{Done: true}, nil
	}
	if len(bytes.TrimSpace(event.Data)) == 0 {
		return Event{}, nil
	}
	parsed, err := parseEvent(event.Data)
	if err != nil {
		return Event{}, err
	}
	if parsed.FinishReason != "" || parsed.Usage.HasData() {
		s.trustedTermination = true
	}
	for _, candidate := range parsed.Candidates {
		if candidate.FinishReason != "" {
			s.trustedTermination = true
			break
		}
	}
	return parsed, nil
}

func (s *Stream) Close() error {
	if s == nil || s.response == nil || s.response.Body == nil {
		return nil
	}
	return s.response.Body.Close()
}

func (s *Stream) HasReplacementCharacter() bool {
	return s != nil && s.decoder != nil && s.decoder.HasReplacementCharacter()
}

func parseEvent(data []byte) (Event, error) {
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return Event{}, fmt.Errorf("decode CloudCode SSE event: %w", err)
	}
	event := Event{}
	response := unwrapResponse(root)
	parseUsage(&event.Usage, root)
	parseUsage(&event.Usage, response)
	if reason := firstString(response, "finishReason", "finish_reason"); reason != "" {
		event.FinishReason = reason
	}
	candidates, _ := response["candidates"].([]any)
	for _, rawCandidate := range candidates {
		candidateMap, ok := rawCandidate.(map[string]any)
		if !ok {
			continue
		}
		candidate := Candidate{Role: stringValue(candidateMap["role"])}
		if reason := firstString(candidateMap, "finishReason", "finish_reason"); reason != "" {
			event.FinishReason = reason
			candidate.FinishReason = reason
		}
		content, _ := candidateMap["content"].(map[string]any)
		if candidate.Role == "" {
			candidate.Role = stringValue(content["role"])
		}
		parts, _ := content["parts"].([]any)
		for _, rawPart := range parts {
			partMap, ok := rawPart.(map[string]any)
			if !ok {
				continue
			}
			part := ContentPart{
				Text:             stringValue(partMap["text"]),
				Thought:          boolValue(partMap["thought"]),
				ThoughtSignature: firstString(partMap, "thoughtSignature", "thought_signature"),
			}
			if callMap, ok := firstMap(partMap, "functionCall", "function_call"); ok {
				part.FunctionCall = &FunctionCall{
					ID:   firstString(callMap, "id", "callId", "call_id"),
					Name: firstString(callMap, "name"),
					Args: mapValue(callMap, "args", "arguments"),
				}
				event.FunctionCalls = append(event.FunctionCalls, *part.FunctionCall)
			}
			candidate.Parts = append(candidate.Parts, part)
			if part.Thought {
				event.Reasoning += part.Text
			} else {
				event.Text += part.Text
			}
			if part.ThoughtSignature != "" {
				event.ThoughtSignatures = append(event.ThoughtSignatures, part.ThoughtSignature)
			}
		}
		event.Candidates = append(event.Candidates, candidate)
	}
	return event, nil
}

func unwrapResponse(root map[string]any) map[string]any {
	response, ok := root["response"].(map[string]any)
	if !ok {
		return root
	}
	if nested, ok := response["response"].(map[string]any); ok {
		return nested
	}
	return response
}

func parseUsage(usage *Usage, root map[string]any) {
	if usage == nil || root == nil {
		return
	}
	raw, ok := firstMap(root, "usageMetadata", "usage_metadata")
	if !ok {
		return
	}
	usage.InputTokens = firstInt(raw, "promptTokenCount", "prompt_token_count", "inputTokens", "input_tokens")
	usage.OutputTokens = firstInt(raw, "candidatesTokenCount", "candidates_token_count", "outputTokens", "output_tokens")
	usage.ThinkingTokens = firstInt(raw, "thoughtsTokenCount", "thoughts_token_count", "thinkingTokens", "thinking_tokens")
	usage.CachedTokens = firstInt(raw, "cachedContentTokenCount", "cached_content_token_count", "cachedTokens", "cached_tokens")
	usage.TotalTokens = firstInt(raw, "totalTokenCount", "total_token_count", "totalTokens", "total_tokens")
}

func firstMap(source map[string]any, keys ...string) (map[string]any, bool) {
	for _, key := range keys {
		if value, ok := source[key].(map[string]any); ok {
			return value, true
		}
	}
	return nil, false
}

func mapValue(source map[string]any, keys ...string) map[string]any {
	value, _ := firstMap(source, keys...)
	return value
}

func firstString(source map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := source[key].(string); ok {
			return value
		}
	}
	return ""
}

func stringValue(value any) string {
	valueString, _ := value.(string)
	return valueString
}

func boolValue(value any) bool {
	valueBool, _ := value.(bool)
	return valueBool
}

func firstInt(source map[string]any, keys ...string) int64 {
	for _, key := range keys {
		switch value := source[key].(type) {
		case float64:
			return int64(value)
		case int64:
			return value
		case json.Number:
			if parsed, err := value.Int64(); err == nil {
				return parsed
			}
		}
	}
	return 0
}

var ErrTruncatedStream = errors.New("truncated CloudCode SSE stream")

type ModelResolution struct {
	RequestedModel      string
	ActualUpstreamModel string
	Status              string
}

func (r ModelResolution) Verified() bool {
	return r.Status == "catalog_exact" || r.Status == "alias_verified"
}

// Keep this map empty until an alias has been verified against the current
// CloudCode catalog and request/response behavior. In particular, the catalog
// default is not a valid implicit fallback for a benchmark request.
var verifiedModelAliases = map[string]string{}

func ResolveModel(requested string, catalog ModelsResponse) ModelResolution {
	requested = strings.TrimSpace(requested)
	if _, ok := catalog.Models[requested]; ok {
		return ModelResolution{
			RequestedModel:      requested,
			ActualUpstreamModel: requested,
			Status:              "catalog_exact",
		}
	}
	if actual, ok := verifiedModelAliases[requested]; ok {
		if _, present := catalog.Models[actual]; present {
			return ModelResolution{
				RequestedModel:      requested,
				ActualUpstreamModel: actual,
				Status:              "alias_verified",
			}
		}
	}
	return ModelResolution{
		RequestedModel: requested,
		Status:         "requested_unverified",
	}
}
