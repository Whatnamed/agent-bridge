package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func addDashboardBridgeRecord(t *testing.T, store *telemetryStore, userAgent, model string) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader("{}"))
	request.Header.Set("User-Agent", userAgent)
	telemetry := store.begin(request, "/v1/responses")
	telemetry.observeRequest(map[string]any{"model": model}, "/v1/responses")
	capture := &responseCapture{ResponseWriter: httptest.NewRecorder()}
	capture.Write([]byte("ok"))
	store.finish(telemetry, request, capture, 2)
}

func TestDashboardSourceFilterMixesCodexAndBridgeRecords(t *testing.T) {
	cfg := defaultConfig()
	cfg.StateDir = t.TempDir()
	store := newTelemetryStore(cfg, nil)
	defer store.close()
	addDashboardBridgeRecord(t, store, "ZCode/desktop", "zcode-model")
	addDashboardBridgeRecord(t, store, "DSH/desktop", "dsh-model")

	codexDir := filepath.Join(cfg.StateDir, "telemetry", codexSummaryDirName)
	if err := os.MkdirAll(codexDir, 0700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	codexRecord := &requestRecord{
		InternalRequestID: "codex-dashboard-turn",
		StartedAt:         now,
		CompletedAt:       now.Add(120 * time.Millisecond),
		Outcome:           "success",
		Source:            "codex",
		RecordKind:        "turn",
		ClientType:        "Codex",
		Model:             "codex-model",
		CachedInputTokens: int64PointerForDashboardTest(0),
		OutputTokens:      int64PointerForDashboardTest(0),
	}
	data, err := json.Marshal(codexRecord)
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(codexDir, now.Local().Format(telemetryDateLayout)+".jsonl")
	if err := os.WriteFile(name, append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}

	server := &server{
		cfg:       cfg,
		responses: newResponseStore(cfg.MaxStored),
		chats:     newChatStore(cfg.MaxStored),
		telemetry: store,
	}
	all := httptest.NewRecorder()
	server.ServeHTTP(all, httptest.NewRequest(http.MethodGet, "/dashboard/api/requests?range=30d&source=all&limit=10", nil))
	var allPayload map[string]any
	if err := json.Unmarshal(all.Body.Bytes(), &allPayload); err != nil {
		t.Fatal(err)
	}
	if total, ok := numberAsInt(allPayload["total"]); !ok || total != 3 {
		t.Fatalf("all source total = %#v body=%s", allPayload["total"], all.Body.String())
	}

	codex := httptest.NewRecorder()
	server.ServeHTTP(codex, httptest.NewRequest(http.MethodGet, "/dashboard/api/requests?range=30d&source=Codex&limit=10", nil))
	var codexPayload map[string]any
	if err := json.Unmarshal(codex.Body.Bytes(), &codexPayload); err != nil {
		t.Fatal(err)
	}
	if total, ok := numberAsInt(codexPayload["total"]); !ok || total != 1 || !strings.Contains(codex.Body.String(), "codex-model") {
		t.Fatalf("Codex filter = %#v body=%s", codexPayload["total"], codex.Body.String())
	}

	overview := httptest.NewRecorder()
	server.ServeHTTP(overview, httptest.NewRequest(http.MethodGet, "/dashboard/api/overview?range=30d&source=ZCode", nil))
	var overviewPayload map[string]any
	if err := json.Unmarshal(overview.Body.Bytes(), &overviewPayload); err != nil {
		t.Fatal(err)
	}
	stats := mapAny(overviewPayload["stats"])
	if requests, ok := numberAsInt(stats["requests"]); !ok || requests != 1 {
		t.Fatalf("ZCode overview stats = %#v", stats)
	}
}

func TestDashboardSummaryPreservesUnavailableAndZero(t *testing.T) {
	zero := int64(0)
	stats := summarizeRecords([]*requestRecord{{
		CachedInputTokens: &zero,
		OutputTokens:      &zero,
	}})
	if stats["input_tokens"] != nil {
		t.Fatalf("unavailable input became zero: %#v", stats["input_tokens"])
	}
	if stats["cached_input_tokens"] != int64(0) || stats["output_tokens"] != int64(0) {
		t.Fatalf("zero usage changed: cached=%#v output=%#v", stats["cached_input_tokens"], stats["output_tokens"])
	}
	if stats["token_cache_ratio_percent"] != nil {
		t.Fatalf("unavailable ratio = %#v", stats["token_cache_ratio_percent"])
	}
}

func int64PointerForDashboardTest(value int64) *int64 {
	return &value
}

func TestDashboardAssetsExposeSourceFilterDrawerAndPrivacyContract(t *testing.T) {
	page, err := dashboardFiles.ReadFile("dashboard/index.html")
	if err != nil {
		t.Fatal(err)
	}
	app, err := dashboardFiles.ReadFile("dashboard/app.js")
	if err != nil {
		t.Fatal(err)
	}
	style, err := dashboardFiles.ReadFile("dashboard/style.css")
	if err != nil {
		t.Fatal(err)
	}
	pageText, appText, styleText := string(page), string(app), string(style)
	for _, marker := range []string{"source-filter", "detail-drawer", "Codex 本地采集", "record_kind"} {
		if !strings.Contains(pageText+appText, marker) {
			t.Fatalf("dashboard marker %q is missing", marker)
		}
	}
	for _, marker := range []string{"compactToken", "sampling", "Escape", "source:", "缓存诊断"} {
		if !strings.Contains(appText, marker) {
			t.Fatalf("dashboard script marker %q is missing", marker)
		}
	}
	if !strings.Contains(styleText, "position: sticky") {
		t.Fatal("request header is not sticky")
	}
	if strings.Contains(pageText, "data-tab=\"diagnostics\"") {
		t.Fatal("diagnostics tab should be removed")
	}
}
