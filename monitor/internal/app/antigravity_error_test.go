package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAntigravityUpstream400MapsToProviderInvalidRequest(t *testing.T) {
	provider, fake, closeServer := newTestAntigravityProvider(t, nil)
	defer closeServer()
	fake.mu.Lock()
	fake.streamStatus = http.StatusBadRequest
	fake.mu.Unlock()
	err := provider.stream(context.Background(), map[string]any{
		"model": stableAntigravityModel, "input": []any{map[string]any{"role": "user", "content": "hello"}},
	}, func(map[string]any) error { return nil })
	var providerErr *providerBackendError
	if !errors.As(err, &providerErr) || providerErr.Status != http.StatusBadRequest || providerErr.UpstreamStatus != http.StatusBadRequest || providerErr.ErrorClass != "provider_invalid_request" || providerErr.Retryable {
		t.Fatalf("error = %#v", err)
	}
	recorder := httptest.NewRecorder()
	writeBackendError(recorder, err)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "provider_invalid_request") || strings.Contains(recorder.Body.String(), "upstream response body") {
		t.Fatalf("HTTP error = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestAntigravityUpstream429PreservesSafeRetryAfter(t *testing.T) {
	provider, fake, closeServer := newTestAntigravityProvider(t, nil)
	defer closeServer()
	fake.mu.Lock()
	fake.streamStatus = http.StatusTooManyRequests
	fake.retryAfter = "17"
	fake.mu.Unlock()
	err := provider.stream(context.Background(), map[string]any{
		"model": stableAntigravityModel, "input": []any{map[string]any{"role": "user", "content": "hello"}},
	}, func(map[string]any) error { return nil })
	var providerErr *providerBackendError
	if !errors.As(err, &providerErr) || providerErr.Status != http.StatusTooManyRequests || providerErr.RetryAfter != "17" || !providerErr.Retryable {
		t.Fatalf("error = %#v", err)
	}
	recorder := httptest.NewRecorder()
	writeBackendError(recorder, err)
	if recorder.Code != http.StatusTooManyRequests || recorder.Header().Get("Retry-After") != "17" {
		t.Fatalf("HTTP error = %d headers=%#v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}

func TestAntigravityTimeoutMapsTo504(t *testing.T) {
	err := antigravityProviderError(context.Background(), "streamGenerateContent", context.DeadlineExceeded)
	var providerErr *providerBackendError
	if !errors.As(err, &providerErr) || providerErr.Status != http.StatusGatewayTimeout || providerErr.ErrorClass != "provider_timeout" || !providerErr.Retryable {
		t.Fatalf("error = %#v", err)
	}
}

func TestAntigravityControlPlaneTransitionsDegradedThenReady(t *testing.T) {
	provider, fake, closeServer := newTestAntigravityProvider(t, nil)
	defer closeServer()
	fake.mu.Lock()
	fake.loadStatus = http.StatusBadGateway
	fake.mu.Unlock()
	provider.prewarm(context.Background())
	if got := provider.diagnostics()["status"]; got != antigravityStateDegraded {
		t.Fatalf("failed prewarm state = %#v", provider.diagnostics())
	}
	fake.mu.Lock()
	fake.loadStatus = 0
	fake.mu.Unlock()
	if err := provider.ensureControlPlane(context.Background()); err != nil {
		t.Fatal(err)
	}
	diagnostics := provider.diagnostics()
	if diagnostics["status"] != antigravityStateReady || diagnostics["catalog_size"] != 4 || diagnostics["project_available"] != true {
		t.Fatalf("ready diagnostics = %#v", diagnostics)
	}
	fake.mu.Lock()
	loadCalls, modelCalls := fake.loadCalls, fake.modelCalls
	fake.mu.Unlock()
	if loadCalls != 2 || modelCalls != 1 {
		t.Fatalf("control-plane calls = load:%d models:%d", loadCalls, modelCalls)
	}
}

func TestStreamResponseMidstreamErrorDoesNotSendDoneOrStoreSuccess(t *testing.T) {
	providerErr := &providerBackendError{Status: http.StatusBadGateway, Message: "Antigravity provider stream failed.", ErrorType: "api_error", Code: "provider_upstream_error", ErrorClass: "provider_upstream_error", Retryable: true}
	provider := scriptedModelProvider{
		events: []map[string]any{
			{"type": "response.created", "response": map[string]any{"id": "resp_midstream", "model": stableAntigravityModel}},
			{"type": "response.output_text.delta", "delta": "partial"},
		},
		err: providerErr,
	}
	responses := newResponseStore(5)
	s := &server{responses: responses}
	recorder := httptest.NewRecorder()
	s.streamResponse(recorder, httptest.NewRequest(http.MethodPost, "/v1/responses", nil), provider,
		map[string]any{"model": stableAntigravityModel, "input": []any{}}, map[string]any{"model": stableAntigravityModel}, "")
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"type":"error"`) || strings.Contains(recorder.Body.String(), "[DONE]") {
		t.Fatalf("midstream response = %d %s", recorder.Code, recorder.Body.String())
	}
	if responses.get("resp_midstream") != nil {
		t.Fatal("midstream error was stored as a successful response")
	}
}

func TestStreamChatMidstreamErrorDoesNotSendDone(t *testing.T) {
	provider := scriptedModelProvider{
		events: []map[string]any{
			{"type": "response.created", "response": map[string]any{"id": "resp_chat_midstream", "model": stableAntigravityModel}},
			{"type": "response.output_text.delta", "delta": "partial"},
		},
		err: &providerBackendError{Status: http.StatusBadGateway, Message: "Antigravity provider stream failed.", ErrorType: "api_error", Code: "provider_upstream_error", ErrorClass: "provider_upstream_error", Retryable: true},
	}
	recorder := httptest.NewRecorder()
	s := &server{}
	s.streamChat(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil), provider,
		map[string]any{"model": stableAntigravityModel}, map[string]any{"model": stableAntigravityModel, "stream": true}, false)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"error"`) || !strings.Contains(recorder.Body.String(), "Antigravity provider stream failed.") || strings.Contains(recorder.Body.String(), "[DONE]") {
		t.Fatalf("midstream chat response = %d %s", recorder.Code, recorder.Body.String())
	}
}
