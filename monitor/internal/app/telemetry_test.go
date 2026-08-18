package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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
			"input_tokens": 10, "input_tokens_details": map[string]any{"cached_tokens": 4}, "cache_write_tokens": 2,
			"output_tokens": 8, "output_tokens_details": map[string]any{"reasoning_tokens": 5}, "total_tokens": 18,
		},
	}})
	telemetry.observeDownstreamEvent(map[string]any{"type": "response.output_text.delta", "delta": "PRIVATE OUTPUT"})
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
	if record.InputTokens == nil || *record.InputTokens != 10 || record.CachedInputTokens == nil || *record.CachedInputTokens != 4 || record.CacheWriteTokens == nil || *record.CacheWriteTokens != 2 || record.ReasoningTokens == nil || *record.ReasoningTokens != 5 {
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

func TestTelemetryRecordsAffinityAndShapeFingerprintsWithoutValues(t *testing.T) {
	cfg := defaultConfig()
	cfg.StateDir = t.TempDir()
	store := newTelemetryStore(cfg, nil)
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{}`))
	request.Header.Set("x-session-id", "zcode-session-secret")
	request.Header.Set("x-request-id", "zcode-request-secret")
	request.Header.Set("x-query-id", "zcode-query-secret")
	telemetry := store.begin(request, "/v1/responses")
	telemetry.observeRequest(map[string]any{
		"model":        "gpt-5.6-luna",
		"input":        "PRIVATE INPUT PREFIX",
		"instructions": "PRIVATE INSTRUCTIONS",
		"tools": []any{map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        "private-tool",
				"description": "private tool description",
			},
		}},
	}, "/v1/responses")
	telemetry.observePrepared(map[string]any{
		"model":            "gpt-5.6-luna",
		"input":            "PRIVATE PREPARED INPUT",
		"instructions":     "PRIVATE PREPARED INSTRUCTIONS",
		"prompt_cache_key": "PRIVATE CACHE KEY",
		"tools":            []any{map[string]any{"type": "function"}},
	})
	telemetry.observeUpstreamRequest(http.Header{
		"session-id":          []string{"zcode-session-secret"},
		"thread-id":           []string{"thread-secret"},
		"x-client-request-id": []string{"thread-secret"},
	})
	capture := &responseCapture{ResponseWriter: httptest.NewRecorder()}
	_, _ = capture.Write([]byte("ok"))
	store.finish(telemetry, request, capture, 2)
	store.close()

	record := store.recentRecords()[0]
	if record.IncomingSessionIDHash != safeIDHash("zcode-session-secret") || record.IncomingZCodeRequestIDHash != safeIDHash("zcode-request-secret") || record.IncomingZCodeQueryIDHash != safeIDHash("zcode-query-secret") {
		t.Fatalf("incoming identity fingerprints = %#v", record)
	}
	if record.UpstreamSessionIDHash != safeIDHash("zcode-session-secret") || record.UpstreamLegacySessionIDHash != "" || record.UpstreamThreadIDHash != safeIDHash("thread-secret") || record.UpstreamClientRequestIDHash != safeIDHash("thread-secret") {
		t.Fatalf("upstream identity fingerprints = %#v", record)
	}
	if record.IncomingPromptCacheKeyPresent || record.IncomingPromptCacheKeyHash != "" {
		t.Fatalf("incoming prompt cache key should be absent: %#v", record)
	}
	if !record.UpstreamPromptCacheKeyPresent || record.UpstreamPromptCacheKeyHash != safeIDHash("PRIVATE CACHE KEY") {
		t.Fatalf("upstream prompt cache key = %#v", record)
	}
	for name, value := range map[string]string{
		"upstream_prompt_cache_key": record.UpstreamPromptCacheKeyHash,
		"input_prefix":              record.InputPrefixHash,
		"instructions":              record.InstructionsHash,
		"tools":                     record.ToolsHash,
		"request_shape":             record.RequestShapeHash,
		"prepared_input_prefix":     record.PreparedInputPrefixHash,
		"prepared_instructions":     record.PreparedInstructionsHash,
		"prepared_tools":            record.PreparedToolsHash,
		"prepared_request_shape":    record.PreparedRequestShapeHash,
	} {
		if value == "" {
			t.Fatalf("missing %s fingerprint: %#v", name, record)
		}
	}
	data, err := os.ReadFile(filepath.Join(store.telemetryDir, time.Now().Local().Format(telemetryDateLayout)+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{
		"zcode-session-secret",
		"zcode-request-secret",
		"zcode-query-secret",
		"thread-secret",
		"PRIVATE INPUT PREFIX",
		"PRIVATE INSTRUCTIONS",
		"PRIVATE CACHE KEY",
		"private-tool",
	} {
		if bytes.Contains(data, []byte(secret)) {
			t.Fatalf("telemetry persisted %q: %s", secret, data)
		}
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

func TestTelemetryRetentionTrimsQuotaHistoryWithoutTouchingOutside(t *testing.T) {
	root := t.TempDir()
	telemetryDir := filepath.Join(root, "telemetry")
	if err := os.MkdirAll(telemetryDir, 0700); err != nil {
		t.Fatal(err)
	}
	oldFetchedAt := time.Now().UTC().AddDate(0, 0, -2)
	recentFetchedAt := time.Now().UTC().Add(-time.Hour)
	quota := strings.Join([]string{
		`{"fetched_at":"` + oldFetchedAt.Format(time.RFC3339Nano) + `","marker":"old"}`,
		`{"fetched_at":"` + recentFetchedAt.Format(time.RFC3339Nano) + `","marker":"recent"}`,
	}, "\n") + "\n"
	quotaPath := filepath.Join(telemetryDir, quotaFileName)
	outside := filepath.Join(root, "do-not-delete.txt")
	if err := os.WriteFile(quotaPath, []byte(quota), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}

	store := &telemetryStore{cfg: config{TelemetryRetentionDays: 1}, telemetryDir: telemetryDir}
	store.cleanupRetention()

	data, err := os.ReadFile(quotaPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"marker":"old"`) || !strings.Contains(string(data), `"marker":"recent"`) {
		t.Fatalf("quota retention result = %s", data)
	}
	if outsideData, err := os.ReadFile(outside); err != nil || string(outsideData) != "outside" {
		t.Fatalf("outside file changed: data=%q err=%v", outsideData, err)
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
	telemetry.observeRequest(map[string]any{
		"model":     "gpt-5.6-luna",
		"reasoning": map[string]any{"effort": "max"},
	}, "/v1/responses")
	capture := &responseCapture{ResponseWriter: httptest.NewRecorder()}
	_, _ = capture.Write([]byte("ok"))
	store.finish(telemetry, request, capture, 2)
	secondRequest := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"other-model"}`))
	secondTelemetry := store.begin(secondRequest, "/v1/chat/completions")
	secondTelemetry.observeRequest(map[string]any{
		"model": "other-model", "stream": false,
		"reasoning": map[string]any{"effort": "low"},
	}, "/v1/chat/completions")
	secondCapture := &responseCapture{ResponseWriter: httptest.NewRecorder()}
	secondCapture.WriteHeader(http.StatusBadRequest)
	store.finish(secondTelemetry, secondRequest, secondCapture, 3)
	server := &server{cfg: cfg, telemetry: store}

	page := httptest.NewRecorder()
	server.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/dashboard", nil))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Codex Bridge 监控") || !strings.Contains(page.Body.String(), `lang="zh-CN"`) {
		t.Fatalf("dashboard page = %d %s", page.Code, page.Body.String())
	}
	list := httptest.NewRecorder()
	server.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/dashboard/api/requests?limit=2", nil))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), "gpt-5.6-luna") {
		t.Fatalf("request list = %d %s", list.Code, list.Body.String())
	}
	var listPayload map[string]any
	if err := json.Unmarshal(list.Body.Bytes(), &listPayload); err != nil {
		t.Fatal(err)
	}
	if total, ok := numberAsInt(listPayload["total"]); !ok || total != 2 {
		t.Fatalf("request total = %#v", listPayload["total"])
	}
	items := sliceAny(listPayload["data"])
	id := stringValue(mapAny(items[0])["internal_request_id"])
	filtered := httptest.NewRecorder()
	server.ServeHTTP(filtered, httptest.NewRequest(http.MethodGet, "/dashboard/api/requests?range=24h&effort=max&endpoint=%2Fv1%2Fresponses&limit=10", nil))
	var filteredPayload map[string]any
	if err := json.Unmarshal(filtered.Body.Bytes(), &filteredPayload); err != nil {
		t.Fatal(err)
	}
	if total, ok := numberAsInt(filteredPayload["total"]); !ok || total != 1 {
		t.Fatalf("filtered total = %#v", filteredPayload["total"])
	}
	pageTwo := httptest.NewRecorder()
	server.ServeHTTP(pageTwo, httptest.NewRequest(http.MethodGet, "/dashboard/api/requests?range=24h&limit=1&offset=1&sort=newest", nil))
	var pageTwoPayload map[string]any
	if err := json.Unmarshal(pageTwo.Body.Bytes(), &pageTwoPayload); err != nil {
		t.Fatal(err)
	}
	if pageTwo.Code != http.StatusOK || len(sliceAny(pageTwoPayload["data"])) != 1 || boolValue(pageTwoPayload["has_more"]) {
		t.Fatalf("pagination response = %#v", pageTwoPayload)
	}
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

func TestDashboardSeparatesRequestCacheHitsFromTokenCacheRatio(t *testing.T) {
	cached := int64(40)
	zeroCached := int64(0)
	inputOne := int64(100)
	inputTwo := int64(100)
	inputThree := int64(100)
	stats := summarizeRecords([]*requestRecord{
		{CachedInputTokens: &cached, InputTokens: &inputOne},
		{CachedInputTokens: &zeroCached, InputTokens: &inputTwo},
		{InputTokens: &inputThree},
	})
	if got := stats["cache_hit_requests"]; got != 1 {
		t.Fatalf("cache hit requests = %#v", got)
	}
	if got := stats["request_cache_hit_percent"]; got != float64(100)/3 {
		t.Fatalf("request cache hit percent = %#v", got)
	}
	if got := stats["token_cache_ratio_percent"]; got != float64(40)/3 {
		t.Fatalf("token cache ratio = %#v", got)
	}
}

func TestDashboardAssetsExposeChineseRefreshAndDiagnosticsContract(t *testing.T) {
	page, err := dashboardFiles.ReadFile("dashboard/index.html")
	if err != nil {
		t.Fatal(err)
	}
	app, err := dashboardFiles.ReadFile("dashboard/app.js")
	if err != nil {
		t.Fatal(err)
	}
	pageText, appText := string(page), string(app)
	for _, marker := range []string{`lang="zh-CN"`, "首响应", "首字", "总耗时", "缓存诊断"} {
		if !strings.Contains(pageText+appText, marker) {
			t.Fatalf("dashboard marker %q is missing", marker)
		}
	}
	for _, marker := range []string{"request_cache_hit_percent", "token_cache_ratio_percent", "formatDuration", "visibilitychange", "5000"} {
		if !strings.Contains(appText, marker) {
			t.Fatalf("dashboard script marker %q is missing", marker)
		}
	}
}

func TestDashboardMarksOldQuotaSnapshotStale(t *testing.T) {
	cfg := defaultConfig()
	cfg.StateDir = t.TempDir()
	store := newTelemetryStore(cfg, nil)
	defer store.close()
	store.quota.mu.Lock()
	store.quota.snapshot = map[string]any{"primary": map[string]any{"used_percent": 12}}
	store.quota.fetchedAt = time.Now().Add(-2 * quotaRefreshInterval)
	store.quota.mu.Unlock()

	response := httptest.NewRecorder()
	(&server{cfg: cfg, telemetry: store}).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/dashboard/api/overview", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("overview status = %d: %s", response.Code, response.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if stringValue(payload["quota_status"]) != "stale" {
		t.Fatalf("quota status = %#v", payload["quota_status"])
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

func TestTelemetryTracksTerminalOutcomesAndMissingUsage(t *testing.T) {
	cases := []struct {
		name            string
		eventType       string
		httpStatus      int
		stream          bool
		streamError     bool
		wantOutcome     string
		wantFailed      int
		wantIncomplete  int
		wantInputTokens bool
	}{
		{name: "response failed", eventType: "response.failed", httpStatus: http.StatusOK, stream: true, wantOutcome: "failed", wantFailed: 1, wantInputTokens: true},
		{name: "response incomplete", eventType: "response.incomplete", httpStatus: http.StatusOK, stream: true, wantOutcome: "failed", wantIncomplete: 1, wantInputTokens: true},
		{name: "upstream stream error", httpStatus: http.StatusBadGateway, stream: true, streamError: true, wantOutcome: "failed"},
		{name: "non streamed response", eventType: "response.completed", httpStatus: http.StatusOK, stream: false, wantOutcome: "success", wantInputTokens: true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			cfg := defaultConfig()
			cfg.StateDir = t.TempDir()
			store := newTelemetryStore(cfg, nil)
			request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"test-model"}`))
			telemetry := store.begin(request, "/v1/responses")
			telemetry.observeRequest(map[string]any{"model": "test-model", "stream": test.stream, "reasoning": map[string]any{"effort": "max"}}, "/v1/responses")
			if test.eventType != "" {
				telemetry.observeUpstreamEvent(map[string]any{
					"type": test.eventType,
					"response": map[string]any{
						"model": "test-model",
						"usage": map[string]any{"input_tokens": 11, "output_tokens": 7, "total_tokens": 18},
					},
				})
				if !test.stream {
					telemetry.observeFinalResponse(map[string]any{"model": "test-model", "usage": map[string]any{"input_tokens": 11, "output_tokens": 7, "total_tokens": 18}})
				}
			} else if test.streamError {
				telemetry.observeStreamError()
			}
			capture := &responseCapture{ResponseWriter: httptest.NewRecorder()}
			capture.WriteHeader(test.httpStatus)
			store.finish(telemetry, request, capture, 29)
			store.close()

			records := store.recentRecords()
			if len(records) != 1 {
				t.Fatalf("records = %d", len(records))
			}
			record := records[0]
			if record.Outcome != test.wantOutcome || record.ResponseFailedCount != test.wantFailed || record.ResponseIncompleteCount != test.wantIncomplete {
				t.Fatalf("record outcome/counters = %#v", record)
			}
			if test.wantInputTokens != (record.InputTokens != nil) {
				t.Fatalf("input token presence = %v, want %v", record.InputTokens != nil, test.wantInputTokens)
			}
			if test.streamError != record.UpstreamStreamError {
				t.Fatalf("stream error = %v, want %v", record.UpstreamStreamError, test.streamError)
			}
		})
	}
}

func TestTelemetryTracksReasoningSummaryToolsAndDownstreamTTFT(t *testing.T) {
	cfg := defaultConfig()
	cfg.StateDir = t.TempDir()
	cfg.TelemetryEventMemoryLimit = 64
	store := newTelemetryStore(cfg, nil)
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.6-luna"}`))
	telemetry := store.begin(request, "/v1/responses")
	telemetry.observeRequest(map[string]any{"model": "gpt-5.6-luna", "stream": true, "reasoning": map[string]any{"effort": "max"}, "parallel_tool_calls": true}, "/v1/responses")
	telemetry.observePrepared(map[string]any{"model": "gpt-5.6-luna", "reasoning": map[string]any{"effort": "max"}, "include": []any{"reasoning.encrypted_content"}})
	telemetry.mu.Lock()
	telemetry.lastEvent = time.Now().Add(-1500 * time.Millisecond)
	telemetry.mu.Unlock()
	for _, id := range []string{"reasoning-one", "reasoning-two"} {
		telemetry.observeUpstreamEvent(map[string]any{"type": "response.output_item.added", "item": map[string]any{"id": id, "type": "reasoning", "encrypted_content": "cipher-" + id, "summary": []any{map[string]any{"type": "summary_text"}}}})
		telemetry.observeUpstreamEvent(map[string]any{"type": "response.output_item.done", "item": map[string]any{"id": id, "type": "reasoning", "encrypted_content": "cipher-" + id, "summary": []any{map[string]any{"type": "summary_text"}}}})
	}
	telemetry.observeUpstreamEvent(map[string]any{"type": "response.reasoning_summary_text.delta", "delta": "private summary"})
	telemetry.observeUpstreamEvent(map[string]any{"type": "response.reasoning_summary_text.delta", "delta": "private summary 2"})
	telemetry.observeUpstreamEvent(map[string]any{"type": "response.output_item.added", "item": map[string]any{"id": "call-one", "type": "function_call"}})
	telemetry.observeUpstreamEvent(map[string]any{"type": "response.output_item.done", "item": map[string]any{"id": "call-one", "type": "function_call"}})
	telemetry.observeUpstreamEvent(map[string]any{"type": "response.completed", "response": map[string]any{"model": "gpt-5.6-luna", "usage": map[string]any{"input_tokens": 100, "input_tokens_details": map[string]any{"cached_tokens": 40}, "output_tokens": 30, "output_tokens_details": map[string]any{"reasoning_tokens": 20}, "total_tokens": 130}}})
	telemetry.observeDownstreamEvent(map[string]any{"type": "response.output_text.delta", "delta": "visible text"})
	capture := &responseCapture{ResponseWriter: httptest.NewRecorder()}
	capture.Write([]byte("visible text"))
	store.finish(telemetry, request, capture, 42)
	store.close()

	record := store.recentRecords()[0]
	if record.ReasoningItemCount != 2 || record.ReadableReasoningItems != 2 || record.EncryptedReasoningItems != 2 || record.ReasoningSummaryDeltas != 2 {
		t.Fatalf("reasoning counters = %#v", record)
	}
	if record.ToolCallCount != 1 || record.FunctionCallCount != 1 || record.UpstreamEventCount != 9 || record.DownstreamEventCount != 1 {
		t.Fatalf("tool/event counters = %#v", record)
	}
	if record.TTFTMS == nil || record.FirstUpstreamEventMS == nil || record.FirstReasoningEventMS == nil || record.FirstToolCallMS == nil || record.LongestSSEGapMS < 1000 {
		t.Fatalf("timing fields = %#v", record)
	}
	if record.InputTokens == nil || *record.InputTokens != 100 || record.CachedInputTokens == nil || *record.CachedInputTokens != 40 || record.OutputTokens == nil || *record.OutputTokens != 30 || record.ReasoningTokens == nil || *record.ReasoningTokens != 20 || record.TotalTokens == nil || *record.TotalTokens != 130 {
		t.Fatalf("usage fields = %#v", record)
	}
	data, err := os.ReadFile(filepath.Join(store.telemetryDir, time.Now().Local().Format(telemetryDateLayout)+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"cipher-reasoning-one", "private summary", "visible text"} {
		if bytes.Contains(data, []byte(secret)) {
			t.Fatalf("telemetry persisted %q: %s", secret, data)
		}
	}
}

func TestTelemetryReasoningCountersObserveSummaryAfterItem(t *testing.T) {
	cfg := defaultConfig()
	cfg.StateDir = t.TempDir()
	store := newTelemetryStore(cfg, nil)
	defer store.close()
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"test"}`))
	telemetry := store.begin(request, "/v1/responses")
	telemetry.observeUpstreamEvent(map[string]any{
		"type": "response.output_item.added",
		"item": map[string]any{"id": "reasoning-late-summary", "type": "reasoning", "summary": []any{}},
	})
	telemetry.observeUpstreamEvent(map[string]any{
		"type": "response.output_item.done",
		"item": map[string]any{
			"id": "reasoning-late-summary", "type": "reasoning",
			"summary": []any{map[string]any{"type": "summary_text"}},
			"content": []any{map[string]any{"type": "reasoning_text"}},
		},
	})
	capture := &responseCapture{ResponseWriter: httptest.NewRecorder()}
	store.finish(telemetry, request, capture, 2)
	record := store.recentRecords()[0]
	if record.ReasoningItemCount != 1 || record.ReadableReasoningItems != 1 || record.RawReasoningTextItems != 1 {
		t.Fatalf("reasoning counters after later fields = %#v", record)
	}
}

func TestDashboardQueriesHistoricalJSONLBeyondRecentWindow(t *testing.T) {
	cfg := defaultConfig()
	cfg.StateDir = t.TempDir()
	telemetryDir := filepath.Join(cfg.StateDir, "telemetry")
	if err := os.MkdirAll(telemetryDir, 0700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	var data bytes.Buffer
	for i := 0; i < maxTelemetryRecords+5; i++ {
		record := &requestRecord{
			InternalRequestID: fmt.Sprintf("historical_%03d", i),
			StartedAt:         now.Add(-time.Duration(i) * time.Second),
			CompletedAt:       now.Add(-time.Duration(i) * time.Second),
			Endpoint:          "/v1/responses", Outcome: "success", Model: "history-model",
			RequestDurationMS: int64(i + 1),
		}
		encoded, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		data.Write(encoded)
		data.WriteByte('\n')
	}
	name := filepath.Join(telemetryDir, now.Local().Format(telemetryDateLayout)+".jsonl")
	if err := os.WriteFile(name, data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	store := newTelemetryStore(cfg, nil)
	defer store.close()
	server := &server{cfg: cfg, telemetry: store}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/dashboard/api/requests?range=30d&limit=1", nil))
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if total, ok := numberAsInt(payload["total"]); !ok || total != maxTelemetryRecords+5 {
		t.Fatalf("historical total = %#v", payload["total"])
	}
	detail := httptest.NewRecorder()
	server.ServeHTTP(detail, httptest.NewRequest(http.MethodGet, "/dashboard/api/requests/historical_000", nil))
	if detail.Code != http.StatusOK {
		t.Fatalf("historical detail status = %d: %s", detail.Code, detail.Body.String())
	}
}

func TestTelemetryTracksChatCompletionDownstreamTTFT(t *testing.T) {
	cfg := defaultConfig()
	cfg.StateDir = t.TempDir()
	store := newTelemetryStore(cfg, nil)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5.6-luna"}`))
	telemetry := store.begin(request, "/v1/chat/completions")
	telemetry.observeRequest(map[string]any{"model": "gpt-5.6-luna", "stream": true}, "/v1/chat/completions")
	telemetry.observeDownstreamEvent(map[string]any{
		"type":    "chat.completion.chunk",
		"choices": []any{map[string]any{"delta": map[string]any{"role": "assistant"}}},
	})
	if telemetry.record.FirstOutputTextDeltaMS != nil {
		t.Fatal("role-only chat chunk counted as output text")
	}
	telemetry.observeDownstreamEvent(map[string]any{
		"type":    "chat.completion.chunk",
		"choices": []any{map[string]any{"delta": map[string]any{"content": "visible text"}}},
	})
	capture := &responseCapture{ResponseWriter: httptest.NewRecorder()}
	_, _ = capture.Write([]byte("visible text"))
	store.finish(telemetry, request, capture, 42)
	store.close()
	if record := store.recentRecords()[0]; record.TTFTMS == nil {
		t.Fatalf("chat completion TTFT = %#v", record)
	}
}

func TestTelemetryWriteFailureDoesNotChangeModelOutcome(t *testing.T) {
	cfg := defaultConfig()
	cfg.StateDir = t.TempDir()
	store := newTelemetryStore(cfg, nil)
	badPath := filepath.Join(t.TempDir(), "telemetry-file")
	if err := os.WriteFile(badPath, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	store.telemetryDir = badPath
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{}`))
	telemetry := store.begin(request, "/v1/responses")
	telemetry.observeRequest(map[string]any{"model": "test-model"}, "/v1/responses")
	capture := &responseCapture{ResponseWriter: httptest.NewRecorder()}
	capture.Write([]byte("ok"))
	store.finish(telemetry, request, capture, 2)
	store.close()
	if store.recentRecords()[0].Outcome != "success" || store.writerErrorCount() == 0 {
		t.Fatalf("write failure changed outcome or was not recorded: %#v errors=%d", store.recentRecords()[0], store.writerErrorCount())
	}
}

func TestTelemetryQuotaRefreshIsBoundedAndFailOpen(t *testing.T) {
	cfg := defaultConfig()
	cfg.StateDir = t.TempDir()
	fetcher := &testQuotaFetcher{snapshot: map[string]any{"available": true}}
	store := newTelemetryStore(cfg, fetcher)
	defer store.close()
	store.refreshQuota()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && fetcher.calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	store.refreshQuota()
	store.refreshQuota()
	if calls := fetcher.calls.Load(); calls != 1 {
		t.Fatalf("quota calls = %d, want one call inside refresh window", calls)
	}
	if store.quotaSnapshot() == nil || store.quotaUpdateTime().IsZero() {
		t.Fatalf("quota snapshot = %#v fetched_at=%v", store.quotaSnapshot(), store.quotaUpdateTime())
	}

	failing := &testQuotaFetcher{err: context.DeadlineExceeded}
	failingStore := newTelemetryStore(cfg, failing)
	defer failingStore.close()
	failingStore.refreshQuota()
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) && !failingStore.quotaUnavailable() {
		time.Sleep(time.Millisecond)
	}
	if !failingStore.quotaUnavailable() {
		t.Fatal("quota failure was not recorded")
	}
}

func TestTelemetryRecordsSurviveRestartWithoutTimelinePersistence(t *testing.T) {
	cfg := defaultConfig()
	cfg.StateDir = t.TempDir()
	first := newTelemetryStore(cfg, nil)
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{}`))
	telemetry := first.begin(request, "/v1/responses")
	telemetry.observeRequest(map[string]any{"model": "restart-model"}, "/v1/responses")
	telemetry.observeUpstreamEvent(map[string]any{"type": "response.completed"})
	capture := &responseCapture{ResponseWriter: httptest.NewRecorder()}
	capture.Write([]byte("ok"))
	first.finish(telemetry, request, capture, 2)
	first.close()

	second := newTelemetryStore(cfg, nil)
	defer second.close()
	records := second.recentRecords()
	if len(records) != 1 || records[0].Model != "restart-model" || records[0].Timeline != nil {
		t.Fatalf("reloaded records = %#v", records)
	}
}

type testQuotaFetcher struct {
	calls    atomic.Int32
	snapshot map[string]any
	err      error
}

func (f *testQuotaFetcher) fetchQuota(context.Context) (map[string]any, error) {
	f.calls.Add(1)
	if f.err != nil {
		return nil, f.err
	}
	return cloneMap(f.snapshot), nil
}

func BenchmarkTelemetrySyntheticEvents(b *testing.B) {
	for _, enabled := range []bool{false, true} {
		name := "disabled"
		if enabled {
			name = "enabled"
		}
		b.Run(name, func(b *testing.B) {
			cfg := defaultConfig()
			cfg.TelemetryEventMemoryLimit = 200
			store := &telemetryStore{cfg: cfg, enabled: enabled}
			event := map[string]any{"type": "response.output_text.delta", "sequence_number": 1, "delta": "x"}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{}`))
				telemetry := store.begin(request, "/v1/responses")
				for j := 0; j < 1000; j++ {
					if telemetry != nil {
						telemetry.observeUpstreamEvent(event)
					}
				}
			}
			b.StopTimer()
		})
	}
}

func BenchmarkTelemetryLargeInputFingerprints(b *testing.B) {
	largeInput := strings.Repeat("large private input ", 20000)
	payload := map[string]any{
		"model":        "gpt-5.6-luna",
		"input":        largeInput,
		"instructions": "stable instructions",
		"tools":        []any{map[string]any{"type": "function", "function": map[string]any{"name": "tool"}}},
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = safeJSONPrefixHash(payload["input"])
		_ = requestShapeHash(payload)
	}
}
