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
	streamStatus   int
	retryAfter     string
	loadStatus     int
	modelStatus    int
}

func newFakeAntigravityCloudCode(t *testing.T, events []string) (*fakeAntigravityCloudCode, *httptest.Server) {
	t.Helper()
	fake := &fakeAntigravityCloudCode{streamEvents: events}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1internal:loadCodeAssist":
			fake.mu.Lock()
			loadStatus := fake.loadStatus
			fake.loadCalls++
			fake.mu.Unlock()
			if loadStatus != 0 {
				w.WriteHeader(loadStatus)
				return
			}
			writeFakeJSON(w, map[string]any{"cloudaicompanionProject": "projects/test-project", "gcpManaged": false})
		case "/v1internal:fetchAvailableModels":
			fake.mu.Lock()
			modelStatus := fake.modelStatus
			fake.modelCalls++
			fake.mu.Unlock()
			if modelStatus != 0 {
				w.WriteHeader(modelStatus)
				return
			}
			writeFakeJSON(w, map[string]any{"models": map[string]any{
				gemini37LowModel:          map[string]any{"displayName": "Gemini Flash Low"},
				gemini37MediumModel:       map[string]any{"displayName": "Gemini Flash Medium"},
				stableAntigravityModel:    map[string]any{"displayName": "Gemini Flash High"},
				"gemini-internal-preview": map[string]any{"displayName": "internal"},
			}, "defaultAgentModelId": stableAntigravityModel})
		case "/v1internal:streamGenerateContent":
			fake.mu.Lock()
			streamStatus, retryAfter := fake.streamStatus, fake.retryAfter
			fake.mu.Unlock()
			if streamStatus != 0 {
				if retryAfter != "" {
					w.Header().Set("Retry-After", retryAfter)
				}
				w.WriteHeader(streamStatus)
				return
			}
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
	if len(first) != 3 || first[0] != stableAntigravityModel || first[1] != gemini37LowModel || first[2] != gemini37MediumModel || len(second) != 3 || second[0] != stableAntigravityModel || second[1] != gemini37LowModel || second[2] != gemini37MediumModel {
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

func TestAntigravityModelsExposeOnlyVerifiedGemini37Presets(t *testing.T) {
	provider, _, closeServer := newTestAntigravityProvider(t, nil)
	defer closeServer()
	ids, err := provider.listModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{stableAntigravityModel, gemini37LowModel, gemini37MediumModel} {
		found := false
		for _, got := range ids {
			if got == id {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("verified preset %q missing from %#v", id, ids)
		}
	}
	for _, id := range ids {
		if strings.Contains(id, "tiered") || strings.HasPrefix(id, "chat_") || strings.HasPrefix(id, "tab_") || id == "gemini-internal-preview" {
			t.Fatalf("unstable/internal model exposed: %q", id)
		}
	}
}

func TestAntigravityModelsUseProviderOwnership(t *testing.T) {
	provider, _, closeServer := newTestAntigravityProvider(t, nil)
	defer closeServer()
	server := &server{
		cfg:       config{},
		backend:   testCodexBackend{},
		providers: &providerRouter{codex: codexModelProvider{backend: testCodexBackend{}}, antigravity: provider},
	}
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("models status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var document map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, raw := range sliceAny(document["data"]) {
		model := mapAny(raw)
		if isStableAntigravityModel(stringValue(model["id"])) {
			if stringValue(model["owned_by"]) != antigravityProviderID {
				t.Fatalf("model ownership = %#v", model)
			}
			seen[stringValue(model["id"])] = true
		}
	}
	for _, id := range []string{gemini37LowModel, gemini37MediumModel, stableAntigravityModel} {
		if !seen[id] {
			t.Fatalf("model %q missing from /v1/models", id)
		}
	}
}

func TestAntigravityReasoningEffortMustMatchGemini37Preset(t *testing.T) {
	for _, tc := range []struct {
		name    string
		model   string
		effort  string
		wantErr bool
	}{
		{name: "low matches", model: gemini37LowModel, effort: "low"},
		{name: "medium matches", model: gemini37MediumModel, effort: "medium"},
		{name: "high matches", model: stableAntigravityModel, effort: "high"},
		{name: "no explicit effort", model: stableAntigravityModel},
		{name: "conflicting effort", model: stableAntigravityModel, effort: "low", wantErr: true},
		{name: "unknown effort", model: stableAntigravityModel, effort: "minimal", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := map[string]any{"model": tc.model, "input": "hello"}
			if tc.effort != "" {
				payload["reasoning"] = map[string]any{"effort": tc.effort}
			}
			err := validateAntigravityReasoningPreset(payload)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, wantErr=%t", err, tc.wantErr)
			}
			if err == nil && tc.effort != "" {
				request, buildErr := buildAntigravityRequest(payload, tc.model, "projects/test-project")
				if buildErr != nil {
					t.Fatal(buildErr)
				}
				if request.Request.GenerationConfig == nil || request.Request.GenerationConfig.ThinkingConfig == nil || request.Request.GenerationConfig.ThinkingConfig.ThinkingLevel != strings.ToUpper(tc.effort) {
					t.Fatalf("thinking config = %#v", request.Request.GenerationConfig)
				}
			}
		})
	}
}

func TestProviderModelListKeepsCodexModelsWhenAntigravityCatalogIsUnavailable(t *testing.T) {
	provider, _, closeServer := newTestAntigravityProvider(t, nil)
	closeServer()
	router := &providerRouter{codex: codexModelProvider{backend: testCodexBackend{}}, antigravity: provider}
	models, err := router.listModels(context.Background())
	if err != nil || len(models) != 1 || models[0] != "gpt-test" {
		t.Fatalf("models = %#v err=%v", models, err)
	}
}

func TestAntigravityModelResolutionFailsClosedWithoutGeneration(t *testing.T) {
	provider, fake, closeServer := newTestAntigravityProvider(t, []string{`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"must not run"}]},"finishReason":"STOP"}]}}`})
	defer closeServer()
	router := &providerRouter{codex: codexModelProvider{backend: testCodexBackend{}}, antigravity: provider}
	s := &server{cfg: config{}, backend: testCodexBackend{}, providers: router, responses: newResponseStore(5), chats: newChatStore(5)}
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gemini-3.7-flash-tiered","input":"hello"}`))
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
	events := []string{`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"你好"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":9,"candidatesTokenCount":2,"thoughtsTokenCount":3,"totalTokenCount":14}}}`}
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
	var responseDocument map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &responseDocument); err != nil {
		t.Fatal(err)
	}
	responseUsage := mapAny(responseDocument["usage"])
	if intValue(responseUsage["output_tokens"]) != 5 || intValue(mapAny(responseUsage["output_tokens_details"])["reasoning_tokens"]) != 3 {
		t.Fatalf("Responses usage = %#v", responseUsage)
	}
	chatRequest := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gemini-3.7-flash-high","messages":[{"role":"user","content":"hello"}]}`))
	chat := httptest.NewRecorder()
	s.ServeHTTP(chat, chatRequest)
	if chat.Code != http.StatusOK || !strings.Contains(chat.Body.String(), "你好") {
		t.Fatalf("Chat response = %d %s", chat.Code, chat.Body.String())
	}
	var chatDocument map[string]any
	if err := json.Unmarshal(chat.Body.Bytes(), &chatDocument); err != nil {
		t.Fatal(err)
	}
	chatUsage := mapAny(chatDocument["usage"])
	if intValue(chatUsage["completion_tokens"]) != 5 || intValue(mapAny(chatUsage["completion_tokens_details"])["reasoning_tokens"]) != 3 {
		t.Fatalf("Chat usage = %#v", chatUsage)
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
			map[string]any{"type": "function_call", "call_id": "call-2", "name": "get_test_value", "arguments": `{"name":"smoke"}`, "thought_signature": "signature-2"},
			map[string]any{"type": "function_call_output", "call_id": "call-2", "output": "plain tool value"},
		},
	}
	plainRequest, err := buildAntigravityRequest(plainPayload, stableAntigravityModel, "projects/test-project")
	if err != nil {
		t.Fatal(err)
	}
	plainResponse := plainRequest.Request.Contents[2].Parts[0].FunctionResponse.Response
	if plainResponse["output"] != "plain tool value" || plainResponse["_raw"] != nil {
		t.Fatalf("plain function response = %#v", plainResponse)
	}
}

func TestAntigravityFunctionOutputWithoutHistoryFailsClosed(t *testing.T) {
	_, err := buildAntigravityRequest(map[string]any{
		"model": stableAntigravityModel,
		"input": []any{
			map[string]any{"role": "user", "content": "continue"},
			map[string]any{"type": "function_call_output", "call_id": "call-without-history", "output": "value"},
		},
	}, stableAntigravityModel, "projects/test-project")
	if err == nil || !strings.Contains(err.Error(), "function name") {
		t.Fatalf("missing function name was accepted: %v", err)
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
	transportID := stringValue(item["call_id"])
	callID, signature, err := decodeThoughtSignatureToolCallID(transportID)
	if err != nil || callID != "call-1" || signature != "sig-1" || !strings.HasPrefix(transportID, thoughtSignatureToolCallIDPrefix) {
		t.Fatalf("function output transport id = %q call_id=%q signature=%q err=%v", transportID, callID, signature, err)
	}
	if stringValue(item["id"]) != transportID {
		t.Fatalf("function output id/call_id mismatch = %#v", item)
	}
	types := make([]string, 0, len(received))
	for _, event := range received {
		types = append(types, stringValue(event["type"]))
	}
	wantTypes := []string{
		"response.created", "response.in_progress", "response.output_item.added",
		"response.function_call_arguments.delta", "response.function_call_arguments.done",
		"response.output_item.done", "response.completed",
	}
	if len(types) != len(wantTypes) {
		t.Fatalf("function event sequence = %#v, want %#v", types, wantTypes)
	}
	for i, expected := range wantTypes {
		if types[i] != expected {
			t.Fatalf("function event sequence = %#v, want %#v", types, wantTypes)
		}
	}
	done := received[4]
	if done["name"] != "get_test_value" || done["item_id"] != transportID || done["arguments"] != `{"name":"smoke"}` {
		t.Fatalf("function arguments done = %#v", done)
	}
	if received[3]["item_id"] != transportID {
		t.Fatalf("function arguments delta lost transport id = %#v", received[3])
	}
	if added := mapAny(received[2]["item"]); stringValue(added["id"]) != transportID || stringValue(added["call_id"]) != transportID {
		t.Fatalf("function output added payload = %#v", received[2])
	}

	request, err := buildAntigravityRequest(map[string]any{
		"model": stableAntigravityModel,
		"input": []any{
			map[string]any{"role": "user", "content": "use the tool result"},
			item,
			map[string]any{"type": "function_call_output", "call_id": transportID, "output": `{"value":"AGY_POC_OK"}`},
		},
	}, stableAntigravityModel, "projects/test-project")
	if err != nil {
		t.Fatalf("Responses tool continuation rejected transport id: %v", err)
	}
	if got := request.Request.Contents[1].Parts[0].FunctionCall; got == nil || got.ID != "call-1" || got.Name != "get_test_value" || got.Args["name"] != "smoke" || request.Request.Contents[1].Parts[0].ThoughtSignature != "sig-1" {
		t.Fatalf("decoded function call = %#v", request.Request.Contents[1].Parts[0])
	}
	if got := request.Request.Contents[2].Parts[0].FunctionResponse; got == nil || got.ID != "call-1" || got.Name != "get_test_value" || got.Response["value"] != "AGY_POC_OK" {
		t.Fatalf("decoded function response = %#v", request.Request.Contents[2].Parts[0])
	}
}

func TestAntigravityExplicitTextFormatRemainsOrdinaryText(t *testing.T) {
	request, err := buildAntigravityRequest(map[string]any{
		"model": stableAntigravityModel,
		"input": []any{map[string]any{"role": "user", "content": "hello"}},
		"text":  map[string]any{"format": map[string]any{"type": "text"}},
	}, stableAntigravityModel, "projects/test-project")
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Request.Contents) != 1 || request.Request.Contents[0].Parts[0].Text != "hello" {
		t.Fatalf("request contents = %#v", request.Request.Contents)
	}
}

func TestAntigravityTextStreamUsesCanonicalLifecycle(t *testing.T) {
	provider, _, closeServer := newTestAntigravityProvider(t, []string{`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]},"finishReason":"STOP"}]}}`})
	defer closeServer()
	var received []map[string]any
	if err := provider.stream(context.Background(), map[string]any{
		"model": stableAntigravityModel, "input": []any{map[string]any{"role": "user", "content": "say hello"}},
	}, func(event map[string]any) error {
		received = append(received, event)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	types := make([]string, 0, len(received))
	for _, event := range received {
		types = append(types, stringValue(event["type"]))
	}
	wantTypes := []string{
		"response.created", "response.in_progress", "response.output_item.added", "response.content_part.added",
		"response.output_text.delta", "response.output_text.done", "response.content_part.done",
		"response.output_item.done", "response.completed",
	}
	if len(types) != len(wantTypes) {
		t.Fatalf("text event sequence = %#v, want %#v", types, wantTypes)
	}
	for i, expected := range wantTypes {
		if types[i] != expected {
			t.Fatalf("text event sequence = %#v, want %#v", types, wantTypes)
		}
	}
	addedItem := mapAny(received[2]["item"])
	if len(sliceAny(addedItem["content"])) != 0 || addedItem["status"] != "in_progress" {
		t.Fatalf("output item added payload = %#v", received[2])
	}
	partAdded := mapAny(received[3]["part"])
	if partAdded["type"] != "output_text" || partAdded["text"] != "" || received[3]["content_index"] != 0 {
		t.Fatalf("content part added payload = %#v", received[3])
	}
	if received[4]["delta"] != "hello" || received[5]["text"] != "hello" {
		t.Fatalf("text delta/done payloads = %#v / %#v", received[4], received[5])
	}
	partDone := mapAny(received[6]["part"])
	if partDone["type"] != "output_text" || partDone["text"] != "hello" {
		t.Fatalf("content part done payload = %#v", received[6])
	}
	doneItem := mapAny(received[7]["item"])
	if doneItem["status"] != "completed" || len(sliceAny(doneItem["content"])) != 1 || messageText(doneItem) != "hello" {
		t.Fatalf("output item done payload = %#v", received[7])
	}
}

func TestAntigravityUnsupportedCapabilitiesReturnBadRequestBeforeStreaming(t *testing.T) {
	provider, fake, closeServer := newTestAntigravityProvider(t, []string{`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"must not run"}]},"finishReason":"STOP"}]}}`})
	defer closeServer()
	router := &providerRouter{codex: codexModelProvider{backend: testCodexBackend{}}, antigravity: provider}
	s := &server{cfg: config{}, backend: testCodexBackend{}, providers: router, responses: newResponseStore(5), chats: newChatStore(5)}
	tests := []struct {
		name string
		path string
		body map[string]any
	}{
		{
			name: "responses json schema",
			path: "/v1/responses",
			body: map[string]any{
				"model": stableAntigravityModel, "stream": true, "input": "hello",
				"text": map[string]any{"format": map[string]any{"type": "json_schema"}},
			},
		},
		{
			name: "chat json object",
			path: "/v1/chat/completions",
			body: map[string]any{
				"model": stableAntigravityModel, "stream": true,
				"messages":        []any{map[string]any{"role": "user", "content": "hello"}},
				"response_format": map[string]any{"type": "json_object"},
			},
		},
		{
			name: "chat unknown response format",
			path: "/v1/chat/completions",
			body: map[string]any{
				"model": stableAntigravityModel, "stream": true,
				"messages":        []any{map[string]any{"role": "user", "content": "hello"}},
				"response_format": map[string]any{"type": "yaml"},
			},
		},
		{
			name: "parallel tool calls false",
			path: "/v1/responses",
			body: map[string]any{
				"model": stableAntigravityModel, "stream": true, "input": "hello", "parallel_tool_calls": false,
			},
		},
		{
			name: "unknown input item",
			path: "/v1/responses",
			body: map[string]any{
				"model": stableAntigravityModel, "stream": true,
				"input": []any{map[string]any{"type": "input_file", "file_id": "file_1"}},
			},
		},
		{
			name: "unknown content part",
			path: "/v1/responses",
			body: map[string]any{
				"model": stableAntigravityModel, "stream": true,
				"input": []any{map[string]any{"role": "user", "content": []any{
					map[string]any{"type": "input_file", "file_id": "file_1"},
				}}},
			},
		},
		{
			name: "chat unknown content part",
			path: "/v1/chat/completions",
			body: map[string]any{
				"model": stableAntigravityModel, "stream": true,
				"messages": []any{map[string]any{"role": "user", "content": []any{
					map[string]any{"type": "input_file", "file_id": "file_1"},
				}}},
			},
		},
		{
			name: "non function tool",
			path: "/v1/responses",
			body: map[string]any{
				"model": stableAntigravityModel, "stream": true, "input": "hello",
				"tools": []any{map[string]any{"type": "web_search"}},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(tc.body)
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(string(body)))
			response := httptest.NewRecorder()
			s.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "does not support") {
				t.Fatalf("response = %d %s", response.Code, response.Body.String())
			}
		})
	}
	fake.mu.Lock()
	streamCalls := fake.streamCalls
	fake.mu.Unlock()
	if streamCalls != 0 {
		t.Fatalf("unsupported requests reached generation = %d", streamCalls)
	}
}

func TestAntigravitySystemDeveloperAndToolChoiceAreExplicitlyTranslated(t *testing.T) {
	request, err := buildAntigravityRequest(map[string]any{
		"model":        stableAntigravityModel,
		"instructions": "top-level instruction",
		"input": []any{
			map[string]any{"role": "developer", "content": "developer instruction"},
			map[string]any{"role": "user", "content": "call the selected tool"},
		},
		"tools":       []any{map[string]any{"type": "function", "function": map[string]any{"name": "lookup"}}},
		"tool_choice": map[string]any{"type": "function", "name": "lookup"},
	}, stableAntigravityModel, "projects/test-project")
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Request.Contents) != 1 || request.Request.Contents[0].Role != "user" {
		t.Fatalf("contents = %#v", request.Request.Contents)
	}
	if request.Request.SystemInstruction == nil || len(request.Request.SystemInstruction.Parts) != 2 || request.Request.SystemInstruction.Parts[0].Text != "top-level instruction" || request.Request.SystemInstruction.Parts[1].Text != "developer instruction" {
		t.Fatalf("system instruction = %#v", request.Request.SystemInstruction)
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	if wire["session_id"] != nil || mapAny(wire["request"])["sessionId"] == nil {
		t.Fatalf("session id placement = %#v", wire)
	}
	config := request.Request.ToolConfig
	if config == nil || config.FunctionCallingConfig == nil || config.FunctionCallingConfig.Mode != "ANY" || len(config.FunctionCallingConfig.AllowedFunctionNames) != 1 || config.FunctionCallingConfig.AllowedFunctionNames[0] != "lookup" {
		t.Fatalf("tool config = %#v", config)
	}
	noneRequest, err := buildAntigravityRequest(map[string]any{
		"model":       stableAntigravityModel,
		"input":       []any{map[string]any{"role": "user", "content": "plain"}},
		"tool_choice": "none",
	}, stableAntigravityModel, "projects/test-project")
	if err != nil || noneRequest.Request.ToolConfig == nil || noneRequest.Request.ToolConfig.FunctionCallingConfig.Mode != "NONE" {
		t.Fatalf("none tool config = %#v err=%v", noneRequest.Request.ToolConfig, err)
	}
	if _, err := buildAntigravityRequest(map[string]any{
		"model":       stableAntigravityModel,
		"input":       []any{map[string]any{"role": "user", "content": "plain"}},
		"tool_choice": "unsupported",
	}, stableAntigravityModel, "projects/test-project"); err == nil {
		t.Fatal("unsupported tool_choice was silently ignored")
	}
}

func TestProviderTelemetryStoresMetadataOnly(t *testing.T) {
	tokenExpiry := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	record := &requestRecord{}
	telemetry := &requestTelemetry{record: record}
	telemetry.observeProvider(providerRoute{
		Provider:                     antigravityProviderID,
		RequestedModel:               "gemini-requested",
		ActualUpstreamModel:          stableAntigravityModel,
		ControlPlaneProjectAvailable: true,
		CatalogSize:                  4,
		OAuthTokenExpiry:             tokenExpiry,
	})
	encoded := string(mustJSON(record))
	for _, expected := range []string{"antigravity", stableAntigravityModel, `"control_plane_project_available":true`} {
		if !strings.Contains(encoded, expected) {
			t.Fatalf("telemetry omitted %q: %s", expected, encoded)
		}
	}
	if strings.Contains(encoded, "projects/test-project") {
		t.Fatalf("telemetry persisted the control-plane project: %s", encoded)
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
