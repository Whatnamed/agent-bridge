package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	agyauth "github.com/whatnamed/agent-bridge/shared/antigravity/auth"
)

type fakeAntigravityCloudCode struct {
	mu             sync.Mutex
	loadCalls      int
	modelCalls     int
	streamCalls    int
	generateBodies []map[string]any
	streamEvents   []string
}

func newFakeAntigravityCloudCode(t *testing.T, events []string) (*fakeAntigravityCloudCode, *httptest.Server) {
	t.Helper()
	fake := &fakeAntigravityCloudCode{streamEvents: events}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1internal:loadCodeAssist":
			fake.mu.Lock()
			fake.loadCalls++
			fake.mu.Unlock()
			writeFakeJSON(w, map[string]any{"cloudaicompanionProject": "projects/test-project", "gcpManaged": false})
		case "/v1internal:fetchAvailableModels":
			fake.mu.Lock()
			fake.modelCalls++
			fake.mu.Unlock()
			writeFakeJSON(w, map[string]any{"models": map[string]any{
				stableAntigravityModel:    map[string]any{"displayName": "Gemini Flash High"},
				"gemini-internal-preview": map[string]any{"displayName": "internal"},
			}, "defaultAgentModelId": stableAntigravityModel})
		case "/v1internal:streamGenerateContent":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "read failed", http.StatusBadRequest)
				return
			}
			var request map[string]any
			if json.Unmarshal(body, &request) != nil {
				http.Error(w, "invalid json", http.StatusBadRequest)
				return
			}
			fake.mu.Lock()
			fake.streamCalls++
			fake.generateBodies = append(fake.generateBodies, request)
			events := append([]string(nil), fake.streamEvents...)
			fake.mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			for _, event := range events {
				fmt.Fprintf(w, "data: %s\n\n", event)
			}
			fmt.Fprint(w, "data: [DONE]\n\n")
		default:
			http.NotFound(w, r)
		}
	})
	return fake, httptest.NewServer(handler)
}

func writeFakeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func newTestAntigravityProvider(t *testing.T, events []string) (*antigravityProvider, *fakeAntigravityCloudCode, func()) {
	t.Helper()
	fake, server := newFakeAntigravityCloudCode(t, events)
	credentialPath := t.TempDir() + "\\oauth_creds.json"
	if err := agyauth.SaveCredentials(credentialPath, agyauth.Credentials{
		AccessToken: "offline-test-access-token", RefreshToken: "offline-test-refresh-token",
		ExpiryDate: time.Now().Add(time.Hour).UnixMilli(), ClientID: "offline-client",
	}); err != nil {
		server.Close()
		t.Fatal(err)
	}
	cfg := defaultConfig()
	cfg.Timeout = 2 * time.Second
	cfg.AntigravityEnabled = true
	cfg.AntigravityCredentialPath = credentialPath
	cfg.AntigravityEndpoint = server.URL
	cfg.AntigravityCatalogTTL = time.Hour
	cfg.AntigravityProjectTTL = time.Hour
	provider, err := newAntigravityProvider(cfg)
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	return provider, fake, server.Close
}

func TestAntigravityControlPlaneCachesAndListsOnlyStableModels(t *testing.T) {
	provider, fake, closeServer := newTestAntigravityProvider(t, []string{`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}}`})
	defer closeServer()
	ctx := context.Background()
	first, err := provider.listModels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second, err := provider.listModels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || first[0] != stableAntigravityModel || len(second) != 1 || second[0] != stableAntigravityModel {
		t.Fatalf("stable model list = %#v / %#v", first, second)
	}
	resolution, err := provider.resolveModel(ctx, stableAntigravityModel)
	if err != nil || !resolution.Verified() || resolution.ActualUpstreamModel != stableAntigravityModel {
		t.Fatalf("resolution = %#v err=%v", resolution, err)
	}
	fake.mu.Lock()
	loadCalls, modelCalls := fake.loadCalls, fake.modelCalls
	fake.mu.Unlock()
	if loadCalls != 1 || modelCalls != 1 {
		t.Fatalf("control-plane calls = load:%d models:%d, want one each", loadCalls, modelCalls)
	}
}

func TestAntigravityModelResolutionFailsClosedWithoutGeneration(t *testing.T) {
	provider, fake, closeServer := newTestAntigravityProvider(t, []string{`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"must not run"}]},"finishReason":"STOP"}]}}`})
	defer closeServer()
	router := &providerRouter{codex: codexModelProvider{backend: testCodexBackend{}}, antigravity: provider}
	s := &server{cfg: config{}, backend: testCodexBackend{}, providers: router, responses: newResponseStore(5), chats: newChatStore(5)}
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gemini-3.7-flash-low","input":"hello"}`))
	response := httptest.NewRecorder()
	s.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "no fallback") {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	fake.mu.Lock()
	streamCalls := fake.streamCalls
	fake.mu.Unlock()
	if streamCalls != 0 {
		t.Fatalf("generation calls = %d, want zero", streamCalls)
	}
}

func TestAntigravityResponsesAndChatUseTheSameProvider(t *testing.T) {
	events := []string{`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"你好"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":9,"candidatesTokenCount":2,"totalTokenCount":11}}}`}
	provider, fake, closeServer := newTestAntigravityProvider(t, events)
	defer closeServer()
	router := &providerRouter{codex: codexModelProvider{backend: testCodexBackend{}}, antigravity: provider}
	s := &server{cfg: config{}, backend: testCodexBackend{}, providers: router, responses: newResponseStore(5), chats: newChatStore(5)}
	responseRequest := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gemini-3.7-flash-high","input":"hello"}`))
	response := httptest.NewRecorder()
	s.ServeHTTP(response, responseRequest)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "你好") {
		t.Fatalf("Responses response = %d %s", response.Code, response.Body.String())
	}
	chatRequest := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gemini-3.7-flash-high","messages":[{"role":"user","content":"hello"}]}`))
	chat := httptest.NewRecorder()
	s.ServeHTTP(chat, chatRequest)
	if chat.Code != http.StatusOK || !strings.Contains(chat.Body.String(), "你好") {
		t.Fatalf("Chat response = %d %s", chat.Code, chat.Body.String())
	}
	fake.mu.Lock()
	streamCalls := fake.streamCalls
	fake.mu.Unlock()
	if streamCalls != 2 {
		t.Fatalf("generation calls = %d, want 2", streamCalls)
	}
}

func TestAntigravityToolReasoningAndSignatureTranslation(t *testing.T) {
	payload := map[string]any{
		"model":        stableAntigravityModel,
		"instructions": "be concise",
		"input": []any{
			map[string]any{"role": "user", "content": "call the tool"},
			map[string]any{"type": "function_call", "call_id": "call-1", "name": "get_test_value", "arguments": `{"name":"smoke"}`, "thought_signature": "signature-from-model"},
			map[string]any{"type": "function_call_output", "call_id": "call-1", "name": "get_test_value", "output": `{"value":"AGY_POC_OK"}`},
		},
		"tools": []any{map[string]any{"type": "function", "function": map[string]any{
			"name": "get_test_value", "description": "safe test", "parameters": map[string]any{
				"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}, "required": []any{"name"},
			},
		}}},
		"reasoning":         map[string]any{"effort": "high"},
		"max_output_tokens": float64(128),
	}
	request, err := buildAntigravityRequest(payload, stableAntigravityModel, "projects/test-project")
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Request.Contents) != 3 || request.Request.Contents[1].Role != "model" || request.Request.Contents[2].Role != "user" {
		t.Fatalf("contents = %#v", request.Request.Contents)
	}
	callPart := request.Request.Contents[1].Parts[0]
	if callPart.FunctionCall == nil || callPart.FunctionCall.Name != "get_test_value" || callPart.ThoughtSignature != "signature-from-model" {
		t.Fatalf("function call part = %#v", callPart)
	}
	if request.Request.Contents[2].Parts[0].FunctionResponse == nil || request.Request.Contents[2].Parts[0].FunctionResponse.Response["value"] != "AGY_POC_OK" {
		t.Fatalf("function response part = %#v", request.Request.Contents[2].Parts[0])
	}
	if len(request.Request.Tools) != 1 || len(request.Request.Tools[0].FunctionDeclarations) != 1 {
		t.Fatalf("tools = %#v", request.Request.Tools)
	}
	parameters := request.Request.Tools[0].FunctionDeclarations[0].Parameters
	if parameters == nil || parameters.Type != "OBJECT" || parameters.Properties["name"].Type != "STRING" {
		t.Fatalf("parameters = %#v", parameters)
	}
	if request.Request.GenerationConfig == nil || request.Request.GenerationConfig.ThinkingConfig == nil || request.Request.GenerationConfig.ThinkingConfig.ThinkingLevel != "HIGH" || request.Request.GenerationConfig.ThinkingConfig.ThinkingBudget != nil {
		t.Fatalf("generation config = %#v", request.Request.GenerationConfig)
	}
	plainPayload := map[string]any{
		"model": stableAntigravityModel,
		"input": []any{
			map[string]any{"role": "user", "content": "continue"},
			map[string]any{"type": "function_call_output", "call_id": "call-1", "output": "plain tool value"},
		},
	}
	plainRequest, err := buildAntigravityRequest(plainPayload, stableAntigravityModel, "projects/test-project")
	if err != nil {
		t.Fatal(err)
	}
	plainResponse := plainRequest.Request.Contents[1].Parts[0].FunctionResponse.Response
	if plainResponse["output"] != "plain tool value" || plainResponse["_raw"] != nil {
		t.Fatalf("plain function response = %#v", plainResponse)
	}
}

func TestAntigravityToolStreamProducesFunctionCallResponse(t *testing.T) {
	events := []string{`{"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"call-1","name":"get_test_value","args":{"name":"smoke"}},"thoughtSignature":"sig-1"}]},"finishReason":"STOP"}]}}`}
	provider, _, closeServer := newTestAntigravityProvider(t, events)
	defer closeServer()
	var received []map[string]any
	err := provider.stream(context.Background(), map[string]any{
		"model": stableAntigravityModel, "input": []any{map[string]any{"role": "user", "content": "use tool"}},
	}, func(event map[string]any) error {
		received = append(received, event)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var completed map[string]any
	for _, event := range received {
		if event["type"] == "response.completed" {
			completed = mapAny(event["response"])
		}
	}
	if completed == nil || len(sliceAny(completed["output"])) != 1 {
		t.Fatalf("completed = %#v", completed)
	}
	item := mapAny(sliceAny(completed["output"])[0])
	if item["type"] != "function_call" || item["name"] != "get_test_value" || item["thought_signature"] != "sig-1" {
		t.Fatalf("function output = %#v", item)
	}
}

func TestProviderTelemetryStoresMetadataOnly(t *testing.T) {
	tokenExpiry := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	record := &requestRecord{}
	telemetry := &requestTelemetry{record: record}
	telemetry.observeProvider(providerRoute{
		Provider:            antigravityProviderID,
		RequestedModel:      "gemini-requested",
		ActualUpstreamModel: stableAntigravityModel,
		ControlPlaneProject: "projects/test-project",
		CatalogSize:         4,
		OAuthTokenExpiry:    tokenExpiry,
	})
	encoded := string(mustJSON(record))
	for _, expected := range []string{"antigravity", stableAntigravityModel, "projects/test-project"} {
		if !strings.Contains(encoded, expected) {
			t.Fatalf("telemetry omitted %q: %s", expected, encoded)
		}
	}
	for _, forbidden := range []string{"access-token", "refresh-token", "hello", "AGY_POC_OK", "signature-from-model"} {
		if strings.Contains(strings.ToLower(encoded), forbidden) {
			t.Fatalf("telemetry contains forbidden material %q: %s", forbidden, encoded)
		}
	}
}

func mustJSON(value any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}

type testCodexBackend struct{}

func (testCodexBackend) stream(context.Context, map[string]any, func(map[string]any) error) error {
	return errors.New("test Codex backend should not be used")
}
func (testCodexBackend) collect(context.Context, map[string]any) (map[string]any, error) {
	return nil, errors.New("test Codex backend should not be used")
}
func (testCodexBackend) listModels(context.Context) []string { return []string{"gpt-test"} }
func (testCodexBackend) proxy(context.Context, string, string, string, http.Header, io.Reader) (*http.Response, error) {
	return nil, errors.New("test Codex backend should not be used")
}
func (testCodexBackend) transcribe(context.Context, http.Header, io.Reader) (*http.Response, error) {
	return nil, errors.New("test Codex backend should not be used")
}
