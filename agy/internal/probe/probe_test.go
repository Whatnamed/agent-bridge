package probe

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/whatnamed/agent-bridge/agy/internal/auth"
	"github.com/whatnamed/agent-bridge/agy/internal/cloudcode"
)

func TestProbeRunsUTF8CompatSmokeAgainstFakeCloudCode(t *testing.T) {
	server, requests := fakeCloudCodeServer(t, false)
	defer server.Close()
	client := cloudcode.NewClient(server.URL, auth.StaticToken{Value: "fake-token"})
	client.HTTPClient = server.Client()
	result, err := Run(context.Background(), client, Config{
		Mode:           cloudcode.ModeCompat,
		RequestedModel: "gemini-3.7-flash-high",
		Prompt:         "只回复：OAUTH_OK",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "ok" || result.ResolvedModel != "gemini-3.7-flash-high" || result.ModelResolution != "catalog_exact" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.ResponseText != "你好，OAUTH_OK" || strings.ContainsRune(result.ResponseText, '\uFFFD') {
		t.Fatalf("unexpected response text: %q", result.ResponseText)
	}
	if result.InputTokens != 11 || result.OutputTokens != 7 || result.ThinkingTokens != 2 || result.CachedTokens != 3 || result.TotalTokens != 23 {
		t.Fatalf("unexpected usage: %+v", result)
	}
	if result.LoadCodeAssistMS == 0 || result.FetchModelsMS == 0 || result.RequestToHeadersMS == 0 || result.FirstSSEEventMS == 0 || result.FirstTextMS == 0 || result.TTFTMS == 0 || result.CompletionMS == 0 || result.GenerationTotalMS == 0 || result.WholeProbeTotalMS == 0 {
		t.Fatalf("missing stream metrics: %+v", result)
	}
	if result.GenerationTotalMS >= result.WholeProbeTotalMS {
		t.Fatalf("generation timing includes preflight: %+v", result)
	}
	if requests.GenerationCount() != 1 {
		t.Fatalf("generation count = %d", requests.GenerationCount())
	}
}

func TestProbeCompletesSafeToolRoundTrip(t *testing.T) {
	server, requests := fakeCloudCodeServer(t, true)
	defer server.Close()
	client := cloudcode.NewClient(server.URL, auth.StaticToken{Value: "fake-token"})
	client.HTTPClient = server.Client()
	result, err := Run(context.Background(), client, Config{
		Mode:           cloudcode.ModeMinimal,
		RequestedModel: "gemini-3.7-flash-high",
		ToolTest:       true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.ToolRoundTrip || result.ToolCallName != "get_test_value" || result.ResponseText != "工具回合成功" {
		t.Fatalf("unexpected tool result: %+v", result)
	}
	if prompt := requests.FirstPrompt(); prompt != DefaultToolTestPrompt {
		t.Fatalf("tool-test default prompt = %q, want %q", prompt, DefaultToolTestPrompt)
	}
	if requests.GenerationCount() != 2 || !requests.SawFunctionResponse() {
		t.Fatalf("tool continuation was not observed: %+v", requests)
	}
}

type fakeRequests struct {
	mu                  sync.Mutex
	generationBodies    [][]byte
	prompts             []string
	sawFunctionResponse bool
}

func (r *fakeRequests) GenerationCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.generationBodies)
}

func (r *fakeRequests) SawFunctionResponse() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sawFunctionResponse
}

func (r *fakeRequests) FirstPrompt() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.prompts) == 0 {
		return ""
	}
	return r.prompts[0]
}

func fakeCloudCodeServer(t *testing.T, tool bool) (*httptest.Server, *fakeRequests) {
	return fakeCloudCodeServerWithCatalog(t, tool, "gemini-3.7-flash-high")
}

func fakeCloudCodeServerWithCatalog(t *testing.T, tool bool, catalogModel string) (*httptest.Server, *fakeRequests) {
	t.Helper()
	requests := &fakeRequests{}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1internal:loadCodeAssist", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"cloudaicompanionProject":"fake-project","gcpManaged":false}`)
	})
	mux.HandleFunc("/v1internal:fetchAvailableModels", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"models":{"`+catalogModel+`":{"displayName":"Catalog Model"}},"defaultAgentModelId":"`+catalogModel+`"}`)
	})
	mux.HandleFunc("/v1internal:streamGenerateContent", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read generation request: %v", err)
			return
		}
		var request cloudcode.GenerateRequest
		if err := json.Unmarshal(body, &request); err != nil {
			t.Errorf("decode generation request: %v", err)
			return
		}
		requests.mu.Lock()
		requests.generationBodies = append(requests.generationBodies, append([]byte(nil), body...))
		if len(request.Request.Contents) > 0 && len(request.Request.Contents[0].Parts) > 0 {
			requests.prompts = append(requests.prompts, request.Request.Contents[0].Parts[0].Text)
		}
		isContinuation := false
		for _, content := range request.Request.Contents {
			for _, part := range content.Parts {
				if part.FunctionResponse != nil {
					isContinuation = true
					requests.sawFunctionResponse = true
				}
			}
		}
		requests.mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		if tool && !isContinuation {
			_, _ = io.WriteString(w, "data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"functionCall\":{\"id\":\"call-1\",\"name\":\"get_test_value\",\"args\":{\"name\":\"smoke\"}},\"thoughtSignature\":\"sig-1\"}]}}]}}\n\n")
		} else if tool && isContinuation {
			_, _ = io.WriteString(w, "data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"工具回合成功\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":4,\"candidatesTokenCount\":3,\"thoughtsTokenCount\":1,\"totalTokenCount\":8}}}\n\n")
		} else {
			_, _ = io.WriteString(w, "data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"你\"}]}}]}}\n\n")
			_, _ = io.WriteString(w, "data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"好，OAUTH_OK\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":11,\"candidatesTokenCount\":7,\"thoughtsTokenCount\":2,\"cachedContentTokenCount\":3,\"totalTokenCount\":23}}}\n\n")
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	})
	return httptest.NewServer(mux), requests
}

func TestProbeStopsBeforeGenerationWhenRequestedModelIsMissing(t *testing.T) {
	server, requests := fakeCloudCodeServerWithCatalog(t, false, "catalog-only-model")
	defer server.Close()
	client := cloudcode.NewClient(server.URL, auth.StaticToken{Value: "fake-token"})
	client.HTTPClient = server.Client()
	result, err := Run(context.Background(), client, Config{RequestedModel: "gemini-3.7-flash-high"})
	if err == nil || !strings.Contains(err.Error(), "model-not-found") {
		t.Fatalf("missing model error = %v", err)
	}
	if result.Status != "model_not_found" || result.ModelResolution != "requested_unverified" || result.ActualUpstreamModel != "" {
		t.Fatalf("missing model result = %+v", result)
	}
	if requests.GenerationCount() != 0 {
		t.Fatalf("generation was sent for an unverified model: %d", requests.GenerationCount())
	}
}

func TestProbeTimingSeparatesPreflightFromGeneration(t *testing.T) {
	const delay = 25 * time.Millisecond
	server, requests := delayedFakeCloudCodeServer(t, delay)
	defer server.Close()
	client := cloudcode.NewClient(server.URL, auth.StaticToken{Value: "fake-token"})
	client.HTTPClient = server.Client()
	result, err := Run(context.Background(), client, Config{RequestedModel: "gemini-3.7-flash-high"})
	if err != nil {
		t.Fatal(err)
	}
	if requests.GenerationCount() != 1 {
		t.Fatalf("generation count = %d", requests.GenerationCount())
	}
	if result.LoadCodeAssistMS < 15 || result.FetchModelsMS < 15 || result.RequestToHeadersMS < 15 {
		t.Fatalf("delayed phases were not measured: %+v", result)
	}
	if result.GenerationTotalMS >= result.WholeProbeTotalMS-15 {
		t.Fatalf("generation total still looks like whole-probe time: %+v", result)
	}
}

func delayedFakeCloudCodeServer(t *testing.T, delay time.Duration) (*httptest.Server, *fakeRequests) {
	t.Helper()
	requests := &fakeRequests{}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1internal:loadCodeAssist", func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(delay)
		_, _ = io.WriteString(w, `{"cloudaicompanionProject":"fake-project","gcpManaged":false}`)
	})
	mux.HandleFunc("/v1internal:fetchAvailableModels", func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(delay)
		_, _ = io.WriteString(w, `{"models":{"gemini-3.7-flash-high":{"displayName":"Flash High"}}}`)
	})
	mux.HandleFunc("/v1internal:streamGenerateContent", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var request cloudcode.GenerateRequest
		if err := json.Unmarshal(body, &request); err != nil {
			t.Errorf("decode generation request: %v", err)
			return
		}
		requests.mu.Lock()
		requests.generationBodies = append(requests.generationBodies, append([]byte(nil), body...))
		requests.mu.Unlock()
		time.Sleep(delay)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}]}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	})
	return httptest.NewServer(mux), requests
}
