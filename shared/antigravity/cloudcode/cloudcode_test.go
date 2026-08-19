package cloudcode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSSEDecoderPreservesUTF8AcrossReadBoundaries(t *testing.T) {
	payload := []byte("data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"你好，世界\"}]}}]}}\n\ndata: [DONE]\n\n")
	decoder := NewSSEDecoder(&oneByteReader{data: payload})
	first, err := decoder.Next()
	if err != nil {
		t.Fatal(err)
	}
	event, err := parseEvent(first.Data)
	if err != nil {
		t.Fatal(err)
	}
	if event.Text != "你好，世界" {
		t.Fatalf("text = %q", event.Text)
	}
	if strings.ContainsRune(event.Text, '\uFFFD') {
		t.Fatalf("replacement character found in UTF-8 text: %q", event.Text)
	}
	if decoder.HasReplacementCharacter() {
		t.Fatal("clean UTF-8 stream was marked as containing a replacement character")
	}
	done, err := decoder.Next()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimSpace(done.Data), []byte("[DONE]")) {
		t.Fatalf("unexpected done event: %q", done.Data)
	}
}

func TestSSEDecoderDetectsReplacementCharacterInRawData(t *testing.T) {
	payload := []byte(`data: {"response":{"candidates":[{"content":{"parts":[{"text":"bad `)
	payload = append(payload, 0xef, 0xbf, 0xbd)
	payload = append(payload, []byte(`"}]}}]}}

`)...)
	decoder := NewSSEDecoder(bytes.NewReader(payload))
	if _, err := decoder.Next(); err != nil {
		t.Fatal(err)
	}
	if !decoder.HasReplacementCharacter() {
		t.Fatal("raw SSE replacement character was not detected")
	}
}

func TestParseEventReadsThoughtsUsageAndFunctionCall(t *testing.T) {
	data := []byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"内部思考","thought":true,"thoughtSignature":"sig-1"},{"functionCall":{"id":"call-1","name":"get_test_value","args":{"name":"smoke"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":7,"thoughtsTokenCount":5,"cachedContentTokenCount":3,"totalTokenCount":23}}}`)
	event, err := parseEvent(data)
	if err != nil {
		t.Fatal(err)
	}
	if event.Reasoning != "内部思考" || len(event.FunctionCalls) != 1 || event.FunctionCalls[0].ID != "call-1" {
		t.Fatalf("unexpected event: %+v", event)
	}
	if event.Usage.InputTokens != 11 || event.Usage.OutputTokens != 7 || event.Usage.ThinkingTokens != 5 || event.Usage.CachedTokens != 3 || event.Usage.TotalTokens != 23 {
		t.Fatalf("unexpected usage: %+v", event.Usage)
	}
	if event.FinishReason != "STOP" || len(event.ThoughtSignatures) != 1 {
		t.Fatalf("unexpected finish/signature: %+v", event)
	}
}

func TestParseEventBindsThoughtSignaturesToExactFunctionCallParts(t *testing.T) {
	data := []byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"bash-1","name":"Bash","args":{"command":"one"}},"thoughtSignature":"sig-1"},{"functionCall":{"id":"bash-2","name":"Bash","args":{"command":"two"}}},{"functionCall":{"id":"lookup-1","name":"lookup","args":{}}}]}}]}}`)
	event, err := parseEvent(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(event.FunctionCalls) != 3 || len(event.Candidates) != 1 || len(event.Candidates[0].Parts) != 3 {
		t.Fatalf("event = %#v", event)
	}
	for index, call := range event.FunctionCalls {
		if call.PartIndex != index {
			t.Fatalf("call %d part index = %d", index, call.PartIndex)
		}
	}
	if event.FunctionCalls[0].ThoughtSignature != "sig-1" || event.FunctionCalls[1].ThoughtSignature != "" || event.FunctionCalls[2].ThoughtSignature != "" {
		t.Fatalf("positional signatures = %#v", event.FunctionCalls)
	}
	if len(event.ThoughtSignatures) != 1 || event.ThoughtSignatures[0] != "sig-1" {
		t.Fatalf("flattened signature index = %#v", event.ThoughtSignatures)
	}
}

func TestNewRequestModesKeepRequiredOuterContract(t *testing.T) {
	compat, err := NewRequest(ModeCompat, "gemini-3.7-flash-high", "project", "OAUTH_OK", false)
	if err != nil {
		t.Fatal(err)
	}
	if compat.UserAgent != "antigravity" || compat.RequestType != "agent" || compat.RequestID == "" || compat.Request.SessionID == "" {
		t.Fatalf("compat request missing required fields: %+v", compat)
	}
	if compat.Request.SystemInstruction == nil || compat.Request.GenerationConfig == nil || compat.Request.GenerationConfig.ThinkingConfig == nil {
		t.Fatal("compat request should include compatibility-only fields")
	}
	minimal, err := NewRequest(ModeMinimal, "gemini-3.7-flash-high", "project", "OAUTH_OK", true)
	if err != nil {
		t.Fatal(err)
	}
	if minimal.UserAgent != "antigravity" || minimal.RequestType != "agent" || minimal.RequestID == "" || minimal.Request.SessionID == "" {
		t.Fatalf("minimal request missing required fields: %+v", minimal)
	}
	if minimal.Request.SystemInstruction != nil || minimal.Request.GenerationConfig != nil {
		t.Fatal("minimal request retained compatibility-only fields")
	}
	if len(minimal.Request.Tools) != 1 || len(minimal.Request.Tools[0].FunctionDeclarations) != 1 {
		t.Fatal("minimal request did not include the single safe POC tool")
	}
}

func TestResolveModelDoesNotInventFallback(t *testing.T) {
	catalog := ModelsResponse{Models: map[string]AvailableModel{"catalog-model": {DisplayName: "Catalog"}}, DefaultAgentModelID: "catalog-model"}
	exact := ResolveModel("catalog-model", catalog)
	if exact.ActualUpstreamModel != "catalog-model" || exact.Status != "catalog_exact" || !exact.Verified() {
		t.Fatalf("exact resolution = %+v", exact)
	}
	missing := ResolveModel("unknown-model", catalog)
	if missing.ActualUpstreamModel != "" || missing.Status != "requested_unverified" || missing.Verified() {
		t.Fatalf("missing model was not fail-closed: %+v", missing)
	}
	if got := ResolveModel("  catalog-model  ", catalog); got.ActualUpstreamModel != "catalog-model" || got.Status != "catalog_exact" {
		t.Fatalf("trimmed exact resolution = %+v", got)
	}
}

func TestStreamAllowsCleanEOFAfterTrustedTermination(t *testing.T) {
	stream := &Stream{decoder: NewSSEDecoder(strings.NewReader("data: {\"response\":{\"candidates\":[{\"finishReason\":\"STOP\"}]}}\n\n"))}
	if _, err := stream.Next(); err != nil {
		t.Fatal(err)
	}
	done, err := stream.Next()
	if err != nil {
		t.Fatal(err)
	}
	if !done.Done || !done.CleanEOF {
		t.Fatalf("clean EOF was not accepted: %+v", done)
	}
}

func TestStreamAllowsCleanEOFAfterFinalUsage(t *testing.T) {
	stream := &Stream{decoder: NewSSEDecoder(strings.NewReader("data: {\"response\":{\"usageMetadata\":{\"totalTokenCount\":3}}}\n\n"))}
	if _, err := stream.Next(); err != nil {
		t.Fatal(err)
	}
	done, err := stream.Next()
	if err != nil || !done.Done || !done.CleanEOF {
		t.Fatalf("final usage did not authorize clean EOF: done=%+v err=%v", done, err)
	}
}

func TestStreamRejectsCleanEOFWithoutTrustedTermination(t *testing.T) {
	stream := &Stream{decoder: NewSSEDecoder(strings.NewReader("data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"partial\"}]}}]}}\n\n"))}
	if _, err := stream.Next(); err != nil {
		t.Fatal(err)
	}
	_, err := stream.Next()
	if !errors.Is(err, ErrTruncatedStream) {
		t.Fatalf("EOF error = %v, want ErrTruncatedStream", err)
	}
}

func TestClientRefreshesOnceAfter401(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got == "" {
			t.Error("missing authorization")
		}
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"cloudaicompanionProject":"project"}`)
	}))
	defer server.Close()
	source := &refreshingTokenSource{}
	client := NewClient(server.URL, source)
	client.HTTPClient = server.Client()
	if _, err := client.LoadCodeAssist(context.Background()); err != nil {
		t.Fatal(err)
	}
	if source.refreshes != 1 || requests.Load() != 2 {
		t.Fatalf("refreshes=%d requests=%d", source.refreshes, requests.Load())
	}
}

func TestHTTPErrorKeepsOnlySafeRetryAfter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "17")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	client := NewClient(server.URL, testStaticToken{value: "test-token"})
	client.HTTPClient = server.Client()
	_, err := client.LoadCodeAssist(context.Background())
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusTooManyRequests || httpErr.RetryAfter != "17" {
		t.Fatalf("error = %#v", err)
	}
	for _, value := range []string{"-1", "86401", "not-a-delay"} {
		if got := safeRetryAfter(value); got != "" {
			t.Fatalf("unsafe Retry-After %q became %q", value, got)
		}
	}
}

func TestStreamGenerateContentPreservesUpstreamHTTPStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"message":"provider detail must not be retained"}}`)
	}))
	defer server.Close()

	client := NewClient(server.URL, testStaticToken{value: "test-token"})
	client.HTTPClient = server.Client()
	_, err := client.StreamGenerateContent(context.Background(), GenerateRequest{Model: "model"})
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("error = %v, want HTTPError", err)
	}
	if httpErr.StatusCode != http.StatusServiceUnavailable || httpErr.Operation != "streamGenerateContent" {
		t.Fatalf("HTTP error = %+v", httpErr)
	}
	if strings.Contains(err.Error(), "provider detail") {
		t.Fatalf("upstream response body leaked: %v", err)
	}
}

func TestStreamCancellationClosesRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: {\"response\":{}}\n\n")
		flusher.Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	client := NewClient(server.URL, testStaticToken{value: "test-token"})
	client.HTTPClient = server.Client()
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := client.StreamGenerateContent(ctx, GenerateRequest{Model: "model", Request: InternalRequest{Contents: []Content{{Role: "user", Parts: []ContentPart{{Text: "x"}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Next(); err != nil {
		t.Fatal(err)
	}
	cancel()
	_, err = stream.Next()
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("stream error = %v, want context canceled", err)
	}
	_ = stream.Close()
}

type oneByteReader struct {
	data []byte
	pos  int
}

func (r *oneByteReader) Read(buffer []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	buffer[0] = r.data[r.pos]
	r.pos++
	return 1, nil
}

type refreshingTokenSource struct {
	refreshes int
}

func (s *refreshingTokenSource) AccessToken(context.Context) (string, error) {
	return "test-token", nil
}
func (s *refreshingTokenSource) Refresh(context.Context) error {
	s.refreshes++
	return nil
}

type testStaticToken struct {
	value string
}

func (s testStaticToken) AccessToken(context.Context) (string, error) { return s.value, nil }
func (s testStaticToken) Refresh(context.Context) error               { return nil }

func TestJSONRequestBodyIsValid(t *testing.T) {
	request, err := NewRequest(ModeCompat, "model", "project", "只回复：OAUTH_OK", false)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte("OAUTH_OK")) {
		t.Fatal("request body lost prompt")
	}
	if strings.Contains(string(body), "\uFFFD") {
		t.Fatal("request body contains replacement character")
	}
	if len(body) == 0 || fmt.Sprint(time.Now()) == "" {
		t.Fatal("unreachable guard")
	}
}
