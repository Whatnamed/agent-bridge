package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTelemetryCapturesMetadataWithoutPersistingContent(t *testing.T) {
	cfg := defaultConfig()
	cfg.StateDir = t.TempDir()
	cfg.TelemetryEventMemoryLimit = 3
	cfg.TelemetryQueueSize = 8
	store := newTelemetryStore(cfg, nil)

	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"input":"PRIVATE PROMPT"}`))
	request.Header.Set("User-Agent", "ZCode/1.0")
	telemetry := store.begin(request, "/v1/responses")
	telemetry.observeRequest(map[string]any{
		"model":                "gpt-5.6-luna",
		"stream":               true,
		"input":                "PRIVATE PROMPT",
		"reasoning":            map[string]any{"effort": "max"},
		"tools":                []any{map[string]any{"type": "function", "function": map[string]any{"name": "private_tool"}}},
		"previous_response_id": "resp_private",
	}, "/v1/responses")
	telemetry.observePrepared(map[string]any{
		"model":     "gpt-5.6-luna",
		"reasoning": map[string]any{"effort": "max"},
		"include":   []any{"reasoning.encrypted_content"},
	})
	telemetry.observeUpstreamEvent(map[string]any{"type": "response.output_item.added", "sequence_number": 1, "item": map[string]any{
		"id": "reasoning_private", "type": "reasoning", "encrypted_content": "PRIVATE ENCRYPTED", "summary": []any{map[string]any{"text": "PRIVATE SUMMARY"}},
	}})
	telemetry.observeUpstreamEvent(map[string]any{"type": "response.output_item.done", "sequence_number": 2, "item": map[string]any{
		"id": "reasoning_private", "type": "reasoning", "encrypted_content": "PRIVATE ENCRYPTED", "summary": []any{map[string]any{"text": "PRIVATE SUMMARY"}},
	}})
	telemetry.observeUpstreamEvent(map[string]any{"type": "response.output_text.delta", "sequence_number": 3, "delta": "PRIVATE OUTPUT"})
	telemetry.observeUpstreamEvent(map[string]any{"type": "response.completed", "sequence_number": 4, "response": map[string]any{
		"model": "gpt-5.6-luna",
		"usage": map[string]any{
			"input_tokens": 10, "input_tokens_details": map[string]any{"cached_tokens": 4},
			"output_tokens": 8, "output_tokens_details": map[string]any{"reasoning_tokens": 5}, "total_tokens": 18,
		},
	}})
	capture := &responseCapture{ResponseWriter: httptest.NewRecorder()}
	_, _ = capture.Write([]byte("PRIVATE OUTPUT"))
	store.finish(telemetry, request, capture, int64(len(`{"input":"PRIVATE PROMPT"}`)))
	store.close()

	records := store.recentRecords()
	if len(records) != 1 {
		t.Fatalf("records = %d", len(records))
	}
	record := records[0]
	if record.Outcome != "success" || record.Model != "gpt-5.6-luna" || record.ClientType != "ZCode" {
		t.Fatalf("record = %#v", record)
	}
	if record.RequestedReasoningEffort == nil || *record.RequestedReasoningEffort != "max" || record.UpstreamReasoningEffort == nil || *record.UpstreamReasoningEffort != "max" {
		t.Fatalf("reasoning fields = %#v", record)
	}
	if record.ReasoningItemCount != 1 || record.EncryptedReasoningItems != 1 || record.ReadableReasoningItems != 1 || record.TTFTMS == nil {
		t.Fatalf("reasoning counters = %#v", record)
	}
	if record.InputTokens == nil || *record.InputTokens != 10 || record.CachedInputTokens == nil || *record.CachedInputTokens != 4 || record.ReasoningTokens == nil || *record.ReasoningTokens != 5 {
		t.Fatalf("usage = %#v", record)
	}
	if len(record.Timeline) != 3 {
		t.Fatalf("timeline length = %d", len(record.Timeline))
	}
	data, err := os.ReadFile(filepath.Join(store.telemetryDir, time.Now().Local().Format(telemetryDateLayout)+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"PRIVATE PROMPT", "PRIVATE OUTPUT", "PRIVATE SUMMARY", "PRIVATE ENCRYPTED", "private_tool", "access_token"} {
		if bytes.Contains(data, []byte(secret)) {
			t.Fatalf("telemetry persisted %q: %s", secret, data)
		}
	}
	if !bytes.Contains(data, []byte(`"reasoning_item_count":1`)) {
		t.Fatalf("telemetry summary missing counters: %s", data)
	}
}

func TestTelemetryQueueDropIsNonBlocking(t *testing.T) {
	store := &telemetryStore{enabled: true, queue: make(chan telemetryWrite, 1)}
	store.enqueue(telemetryWrite{quota: map[string]any{"available": true}})
	store.enqueue(telemetryWrite{quota: map[string]any{"available": true}})
	if store.droppedCount() != 1 {
		t.Fatalf("dropped = %d", store.droppedCount())
	}
}

func TestTelemetryRetentionOnlyDeletesDateFilesInTelemetryDirectory(t *testing.T) {
	root := t.TempDir()
	telemetryDir := filepath.Join(root, "telemetry")
	if err := os.MkdirAll(telemetryDir, 0700); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(telemetryDir, "2000-01-01.jsonl")
	keep := filepath.Join(telemetryDir, "quota.jsonl")
	outside := filepath.Join(root, "do-not-delete.txt")
	for path := range map[string]bool{old: true, keep: true, outside: true} {
		if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	store := &telemetryStore{cfg: config{TelemetryRetentionDays: 1}, telemetryDir: telemetryDir}
	store.cleanupRetention()
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("old telemetry file was not removed: %v", err)
	}
	for _, path := range []string{keep, outside} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("unexpected deletion of %s: %v", path, err)
		}
	}
}

func TestQuotaSanitizationKeepsOnlyUsageMetadata(t *testing.T) {
	snapshot := sanitizeQuotaPayload(map[string]any{
		"plan_type":               "plus",
		"access_token":            "PRIVATE TOKEN",
		"rate_limit_reached_type": map[string]any{"type": "workspace_member_usage_limit_reached"},
		"rate_limit": map[string]any{
			"primary_window":   map[string]any{"used_percent": 21, "limit_window_seconds": 18000, "reset_at": 1234},
			"secondary_window": map[string]any{"used_percent": 4, "window_duration_mins": 10080, "resets_at": 5678},
		},
		"credits":       map[string]any{"balance": "12", "private_prompt": "DO NOT KEEP"},
		"spend_control": map[string]any{"individual_limit": map[string]any{"limit": "25000", "used": "8000", "remaining": "17000"}},
	})
	data, _ := json.Marshal(snapshot)
	for _, secret := range []string{"PRIVATE TOKEN", "DO NOT KEEP", "access_token"} {
		if bytes.Contains(data, []byte(secret)) {
			t.Fatalf("quota snapshot contains %q: %s", secret, data)
		}
	}
	primary := mapAny(snapshot["primary"])
	if primary["used_percent"] != float64(21) || primary["window_duration_mins"] != int64(300) {
		t.Fatalf("primary = %#v", primary)
	}
	if stringValue(snapshot["rate_limit_reached_type"]) != "workspace_member_usage_limit_reached" {
		t.Fatalf("rate limit reached type = %#v", snapshot["rate_limit_reached_type"])
	}
	individual := mapAny(snapshot["individual_limit"])
	if individual["limit"] != "25000" || individual["remaining"] != "17000" {
		t.Fatalf("individual limit = %#v", individual)
	}
}

func TestQuotaFetcherUsesOfficialUsageRouteAndAuthHeaders(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer usage-access-token" || r.Header.Get("ChatGPT-Account-ID") != "acct_usage" {
			t.Errorf("usage request = %s %s headers=%v", r.Method, r.URL.Path, r.Header)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"plan_type":"plus","rate_limit":{"primary_window":{"used_percent":12}}}`))
	}))
	defer server.Close()

	cfg := defaultConfig()
	cfg.BackendURL = server.URL + "/backend-api/codex"
	cfg.AuthJSON = writeTestAuth(t, authDocument("usage-access-token", map[string]any{"account_id": "acct_usage"}))
	snapshot, err := newBackend(cfg).fetchQuota(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] != "/backend-api/wham/usage" {
		t.Fatalf("usage paths = %#v", paths)
	}
	if stringValue(snapshot["plan_type"]) != "plus" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestQuotaURLUsesCodexAPIStyleWhenBackendURLIsNotBackendAPI(t *testing.T) {
	if got := quotaURL("https://example.test"); got != "https://example.test/api/codex/usage" {
		t.Fatalf("quotaURL = %s", got)
	}
	if got := quotaURL("https://example.test/backend-api/codex"); got != "https://example.test/backend-api/wham/usage" {
		t.Fatalf("ChatGPT quotaURL = %s", got)
	}
}

func TestReasoningSummaryDefaultOnlyInjectsOnOptIn(t *testing.T) {
	for _, test := range []struct {
		name    string
		setting string
		want    string
	}{
		{name: "disabled", setting: "none"},
		{name: "auto", setting: "auto", want: "auto"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var received map[string]any
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/responses" {
					t.Errorf("upstream request = %s %s", r.Method, r.URL.Path)
				}
				if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
					t.Errorf("decode upstream body: %v", err)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"model\":\"test-model\"}}\n\n"))
			}))
			defer upstream.Close()

			cfg := defaultConfig()
			cfg.BackendURL = upstream.URL
			cfg.AuthJSON = writeTestAuth(t, authDocument("reasoning-access-token", map[string]any{"account_id": "acct_reasoning"}))
			cfg.ReasoningSummaryDefault = test.setting
			backend := newBackend(cfg)
			if err := backend.stream(context.Background(), map[string]any{
				"model":     "test-model",
				"reasoning": map[string]any{"effort": "high"},
			}, func(map[string]any) error { return nil }); err != nil {
				t.Fatal(err)
			}
			reasoning := mapAny(received["reasoning"])
			got := stringValue(reasoning["summary"])
			if got != test.want {
				t.Fatalf("reasoning summary = %q, want %q; payload=%#v", got, test.want, received)
			}
		})
	}
}

func TestDashboardServesAPIAndRejectsUnknownRequest(t *testing.T) {
	cfg := defaultConfig()
	cfg.StateDir = t.TempDir()
	store := newTelemetryStore(cfg, nil)
	defer store.close()
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{}`))
	telemetry := store.begin(request, "/v1/responses")
	telemetry.observeRequest(map[string]any{"model": "gpt-5.6-luna"}, "/v1/responses")
	capture := &responseCapture{ResponseWriter: httptest.NewRecorder()}
	_, _ = capture.Write([]byte("ok"))
	store.finish(telemetry, request, capture, 2)
	server := &server{cfg: cfg, telemetry: store}

	page := httptest.NewRecorder()
	server.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/dashboard", nil))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Codex Bridge Monitor") {
		t.Fatalf("dashboard page = %d %s", page.Code, page.Body.String())
	}
	list := httptest.NewRecorder()
	server.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/dashboard/api/requests?limit=1", nil))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), "gpt-5.6-luna") {
		t.Fatalf("request list = %d %s", list.Code, list.Body.String())
	}
	var listPayload map[string]any
	if err := json.Unmarshal(list.Body.Bytes(), &listPayload); err != nil {
		t.Fatal(err)
	}
	items := sliceAny(listPayload["data"])
	id := stringValue(mapAny(items[0])["internal_request_id"])
	detail := httptest.NewRecorder()
	server.ServeHTTP(detail, httptest.NewRequest(http.MethodGet, "/dashboard/api/requests/"+id, nil))
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), "timeline") {
		t.Fatalf("request detail = %d %s", detail.Code, detail.Body.String())
	}
	unknown := httptest.NewRecorder()
	server.ServeHTTP(unknown, httptest.NewRequest(http.MethodGet, "/dashboard/api/requests/does-not-exist", nil))
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown detail status = %d", unknown.Code)
	}
}

func TestDashboardDisabledReturnsNotFound(t *testing.T) {
	cfg := defaultConfig()
	cfg.DashboardEnabled = false
	server := &server{cfg: cfg, telemetry: newTelemetryStore(cfg, nil)}
	defer server.telemetry.close()
	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/dashboard", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("dashboard disabled status = %d", response.Code)
	}
}

func TestTelemetryContextDoesNotChangeCancelledRequestSemantics(t *testing.T) {
	cfg := defaultConfig()
	cfg.StateDir = t.TempDir()
	store := newTelemetryStore(cfg, nil)
	defer store.close()
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{}`)).WithContext(ctx)
	telemetry := store.begin(request, "/v1/responses")
	cancel()
	capture := &responseCapture{ResponseWriter: httptest.NewRecorder()}
	capture.WriteHeader(http.StatusOK)
	store.finish(telemetry, request, capture, 2)
	if store.recentRecords()[0].Outcome != "cancelled" {
		t.Fatalf("cancelled outcome = %s", store.recentRecords()[0].Outcome)
	}
}
