package app

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestCodexCollector(t *testing.T) (*telemetryStore, *codexCollector, string, string) {
	t.Helper()
	root := t.TempDir()
	sessions := filepath.Join(root, "sessions")
	archived := filepath.Join(root, "archived_sessions")
	telemetryDir := filepath.Join(root, "telemetry")
	for _, path := range []string{sessions, archived, telemetryDir} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	cfg := defaultConfig()
	cfg.StateDir = root
	cfg.CodexSessionsDir = sessions
	cfg.CodexArchivedSessionsDir = archived
	cfg.CodexImportDays = 30
	cfg.CodexScanInterval = time.Minute
	store := &telemetryStore{cfg: cfg, enabled: true, telemetryDir: telemetryDir}
	collector := newCodexCollector(store)
	return store, collector, sessions, archived
}

func writeCodexEnvelope(t *testing.T, file *os.File, kind, timestamp string, payload any, newline bool) {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"type":      kind,
		"timestamp": timestamp,
		"payload":   payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	if newline {
		data = append(data, '\n')
	}
	if _, err := file.Write(data); err != nil {
		t.Fatal(err)
	}
}

func TestCodexCollectorImportsOnlyRedactedTurnSummaries(t *testing.T) {
	store, collector, sessions, archived := newTestCodexCollector(t)
	base := time.Now().UTC().Add(-2 * time.Minute).Truncate(time.Millisecond)
	started := base.Add(time.Second)
	completed := started.Add(123 * time.Millisecond)
	fileName := "rollout-" + base.Local().Format("2006-01-02T15-04-05") + "-raw-session.jsonl"
	path := filepath.Join(sessions, fileName)
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writeCodexEnvelope(t, file, "session_meta", base.Format(time.RFC3339Nano), map[string]any{
		"id": "session-secret-raw", "session_id": "session-secret-raw", "source": "cli", "thread_source": "user", "model_provider": "openai",
	}, true)
	writeCodexEnvelope(t, file, "turn_context", started.Format(time.RFC3339Nano), map[string]any{
		"turn_id": "turn-secret-raw", "model": "gpt-test-model", "collaboration_mode": map[string]any{"settings": map[string]any{"reasoning_effort": "high"}},
	}, true)
	writeCodexEnvelope(t, file, "event_msg", started.Format(time.RFC3339Nano), map[string]any{
		"type": "task_started", "turn_id": "turn-secret-raw", "started_at": started.Format(time.RFC3339Nano), "model": "gpt-test-model", "model_context_window": 258400,
	}, true)
	writeCodexEnvelope(t, file, "event_msg", started.Add(10*time.Millisecond).Format(time.RFC3339Nano), map[string]any{
		"type": "token_count", "info": map[string]any{
			"last_token_usage": map[string]any{
				"input_tokens": 0, "cached_input_tokens": 0, "cache_write_input_tokens": 0,
				"output_tokens": 0, "reasoning_output_tokens": 0, "total_tokens": 145000,
			},
		},
	}, true)
	writeCodexEnvelope(t, file, "event_msg", started.Add(20*time.Millisecond).Format(time.RFC3339Nano), map[string]any{
		"type": "token_count", "info": map[string]any{
			"last_token_usage":  map[string]any{"input_tokens": 100, "cached_input_tokens": 20, "cache_write_input_tokens": 2, "output_tokens": 30, "reasoning_output_tokens": 10, "total_tokens": 160},
			"total_token_usage": map[string]any{"input_tokens": 1000, "cached_input_tokens": 900, "output_tokens": 900, "reasoning_output_tokens": 700, "total_tokens": 9999},
		},
	}, true)
	writeCodexEnvelope(t, file, "event_msg", started.Add(40*time.Millisecond).Format(time.RFC3339Nano), map[string]any{
		"type": "token_count", "info": map[string]any{
			"last_token_usage":  map[string]any{"input_tokens": 120, "cached_input_tokens": 40, "cache_write_input_tokens": 3, "output_tokens": 50, "reasoning_output_tokens": 20, "total_tokens": 230},
			"total_token_usage": map[string]any{"input_tokens": 2000, "cached_input_tokens": 1900, "output_tokens": 1900, "reasoning_output_tokens": 1700, "total_tokens": 19999},
		},
	}, true)
	// These payloads deliberately contain forbidden text. The collector must
	// consume their event type without copying any payload field to telemetry.
	writeCodexEnvelope(t, file, "event_msg", started.Add(60*time.Millisecond).Format(time.RFC3339Nano), map[string]any{
		"type": "agent_message", "text": "forbidden prompt output tool-secret",
	}, true)
	writeCodexEnvelope(t, file, "response_item", started.Add(70*time.Millisecond).Format(time.RFC3339Nano), map[string]any{
		"text": "forbidden response content",
	}, true)
	writeCodexEnvelope(t, file, "future_unknown_event", started.Add(80*time.Millisecond).Format(time.RFC3339Nano), map[string]any{
		"secret": "forbidden unknown payload",
	}, true)
	writeCodexEnvelope(t, file, "event_msg", completed.Format(time.RFC3339Nano), map[string]any{
		"type": "task_complete", "turn_id": "turn-secret-raw", "started_at": started.Format(time.RFC3339Nano), "completed_at": completed.Format(time.RFC3339Nano), "duration_ms": 123, "time_to_first_token_ms": 45, "status": "completed", "last_agent_message": "forbidden final text",
	}, true)
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	collector.scanOnce()
	records := store.readCodexRecords(time.Time{})
	if len(records) != 1 {
		t.Fatalf("records = %d, want one", len(records))
	}
	record := records[0]
	if record.Source != "codex" || record.RecordKind != "turn" || record.ClientType != "Codex" || record.Model != "gpt-test-model" {
		t.Fatalf("identity = %#v", record)
	}
	if record.InputTokens == nil || *record.InputTokens != 220 || record.CachedInputTokens == nil || *record.CachedInputTokens != 60 ||
		record.OutputTokens == nil || *record.OutputTokens != 80 || record.ReasoningTokens == nil || *record.ReasoningTokens != 30 ||
		record.TotalTokens == nil || *record.TotalTokens != 390 {
		t.Fatalf("last_token_usage aggregation = %#v", record)
	}
	if !record.DurationAvailable || record.RequestDurationMS != 123 || record.TTFTMS == nil || *record.TTFTMS != 45 || record.SamplingCount != 2 {
		t.Fatalf("timing/sampling = %#v", record)
	}
	if len(record.Sampling) != 2 || record.Sampling[0].TotalTokens == nil || *record.Sampling[0].TotalTokens != 160 {
		t.Fatalf("sampling = %#v", record.Sampling)
	}
	if collector.metrics.unknownEvents.Load() < 3 || collector.metrics.parseErrors.Load() != 0 {
		t.Fatalf("collector metrics unknown=%d parse=%d", collector.metrics.unknownEvents.Load(), collector.metrics.parseErrors.Load())
	}

	initialRollout, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, outputPath := range []string{
		filepath.Join(collector.summaryDir, timeToSummaryDate(record.CompletedAt)+".jsonl"), collector.checkpointPath,
	} {
		data, readErr := os.ReadFile(outputPath)
		if readErr != nil {
			t.Fatal(readErr)
		}
		for _, forbidden := range []string{"session-secret-raw", "turn-secret-raw", "forbidden prompt output tool-secret", "forbidden response content", "forbidden final text"} {
			if strings.Contains(string(data), forbidden) {
				t.Fatalf("privacy leak %q in %s", forbidden, outputPath)
			}
		}
	}

	entry := collector.checkpoint.Files[hashID("session-secret-raw")]
	if entry == nil || entry.Offset <= 0 {
		t.Fatalf("checkpoint entry = %#v", entry)
	}
	oldOffset := entry.Offset
	oldParseErrors := collector.metrics.parseErrors.Load()
	file, err = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	partial := fmt.Sprintf("{\"type\":\"future_partial\",\"timestamp\":%q,\"payload\":{\"secret\":\"partial-secret", base.Format(time.RFC3339Nano))
	if _, err := file.WriteString(partial); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	collector.scanOnce()
	entry = collector.checkpoint.Files[hashID("session-secret-raw")]
	if entry.Offset != oldOffset || collector.metrics.parseErrors.Load() != oldParseErrors || len(store.readCodexRecords(time.Time{})) != 1 {
		t.Fatalf("incomplete line handling offset=%d parse=%d records=%d", entry.Offset, collector.metrics.parseErrors.Load(), len(store.readCodexRecords(time.Time{})))
	}
	file, err = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("\"}}\n"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	collector.scanOnce()
	if len(store.readCodexRecords(time.Time{})) != 1 {
		t.Fatal("completed unknown line changed turn count")
	}

	// A new collector must reuse the checkpoint and the stable turn ID when
	// the rollout is discovered again after a restart and archive move.
	restarted := newCodexCollector(store)
	restarted.scanOnce()
	if len(store.readCodexRecords(time.Time{})) != 1 {
		t.Fatal("restart duplicated the turn")
	}
	archivedPath := filepath.Join(archived, fileName)
	if err := os.Rename(path, archivedPath); err != nil {
		t.Fatal(err)
	}
	restarted.scanOnce()
	if len(store.readCodexRecords(time.Time{})) != 1 {
		t.Fatal("archive move duplicated the turn")
	}

	malformedBefore := restarted.metrics.parseErrors.Load()
	file, err = os.OpenFile(archivedPath, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("{malformed-json}\n"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	restarted.scanOnce()
	if restarted.metrics.parseErrors.Load() <= malformedBefore || len(store.readCodexRecords(time.Time{})) != 1 {
		t.Fatalf("malformed line handling parse=%d records=%d", restarted.metrics.parseErrors.Load(), len(store.readCodexRecords(time.Time{})))
	}

	oldSummary := filepath.Join(collector.summaryDir, time.Now().AddDate(0, 0, -45).Format(telemetryDateLayout)+".jsonl")
	if err := os.WriteFile(oldSummary, []byte("{\"internal_request_id\":\"codex-old\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archivedPath, initialRollout, 0600); err != nil {
		t.Fatal(err)
	}
	// Truncation/rewrite is not allowed to create a second summary for the
	// same stable turn ID.
	restarted.scanOnce()
	if _, err := os.Stat(oldSummary); !os.IsNotExist(err) {
		t.Fatalf("old summary still exists, err=%v", err)
	}
	if len(store.readCodexRecords(time.Time{})) != 1 {
		t.Fatal("rewrite/truncate duplicated the turn")
	}
}

func TestCodexCollectorPrunesExpiredCheckpointsButKeepsRecentlyUpdatedRollouts(t *testing.T) {
	_, collector, sessions, _ := newTestCodexCollector(t)
	now := time.Now().Local().Truncate(time.Second)
	old := now.AddDate(0, 0, -45)
	oldName := "rollout-" + old.Format("2006-01-02T15-04-05") + "-old.jsonl"
	oldPath := filepath.Join(sessions, oldName)
	if err := os.WriteFile(oldPath, []byte{}, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(oldPath, old, old); err != nil {
		t.Fatal(err)
	}
	collector.checkpoint.Files["stale"] = &codexFileCheckpoint{
		RolloutHash: "stale",
		PathHash:    pathHash(oldPath),
		RolloutAt:   old,
		ModTime:     old,
	}
	collector.checkpoint.Files["missing"] = &codexFileCheckpoint{
		RolloutHash: "missing",
		PathHash:    pathHash(filepath.Join(sessions, "rollout-missing.jsonl")),
		RolloutAt:   old,
		ModTime:     old,
	}
	recentName := "rollout-" + old.Format("2006-01-02T15-04-05") + "-recent.jsonl"
	recentPath := filepath.Join(sessions, recentName)
	if err := os.WriteFile(recentPath, []byte{}, 0600); err != nil {
		t.Fatal(err)
	}

	collector.scanOnce()
	if _, ok := collector.checkpoint.Files["stale"]; ok {
		t.Fatal("expired checkpoint for old rollout was retained")
	}
	if _, ok := collector.checkpoint.Files["missing"]; ok {
		t.Fatal("expired checkpoint for missing rollout was retained")
	}
	var recent *codexFileCheckpoint
	for _, entry := range collector.checkpoint.Files {
		if entry != nil && entry.PathHash == pathHash(recentPath) {
			recent = entry
			break
		}
	}
	if recent == nil || !recent.RolloutAt.Before(now.AddDate(0, 0, -30)) || !recent.ModTime.After(old) {
		t.Fatalf("recently updated old rollout checkpoint = %#v", recent)
	}
}

func timeToSummaryDate(value time.Time) string {
	if value.IsZero() {
		return time.Now().Local().Format(telemetryDateLayout)
	}
	return value.Local().Format(telemetryDateLayout)
}

func TestCodexCollectorFailureDoesNotChangeBridgeTelemetryPath(t *testing.T) {
	cfg := defaultConfig()
	cfg.StateDir = t.TempDir()
	cfg.CodexSessionsDir = filepath.Join(cfg.StateDir, "missing-sessions")
	cfg.CodexArchivedSessionsDir = filepath.Join(cfg.StateDir, "missing-archive")
	store := newTelemetryStore(cfg, nil)
	defer store.close()
	if store.codex == nil {
		t.Fatal("collector was not configured")
	}
	store.codex.scanOnce()
	request := httptest.NewRequest("POST", "/v1/responses", strings.NewReader("{}"))
	telemetry := store.begin(request, "/v1/responses")
	telemetry.observeRequest(map[string]any{"model": "bridge-test"}, "/v1/responses")
	capture := &responseCapture{ResponseWriter: httptest.NewRecorder()}
	capture.Write([]byte("ok"))
	store.finish(telemetry, request, capture, 2)
	if len(store.recentRecords()) != 1 || store.recentRecords()[0].Source != "bridge" {
		t.Fatalf("bridge telemetry after collector failure = %#v", store.recentRecords())
	}
}

func TestCodexCollectorLocalRolloutImport(t *testing.T) {
	if os.Getenv("RUN_CODEX_LOCAL_IMPORT") != "1" {
		t.Skip("set RUN_CODEX_LOCAL_IMPORT=1 for a read-only local rollout import")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	sessions := filepath.Join(home, ".codex", "sessions")
	archived := filepath.Join(home, ".codex", "archived_sessions")
	if _, err := os.Stat(sessions); os.IsNotExist(err) {
		t.Skip("Codex sessions directory is not present")
	}
	root := t.TempDir()
	cfg := defaultConfig()
	cfg.StateDir = root
	cfg.CodexSessionsDir = sessions
	cfg.CodexArchivedSessionsDir = archived
	cfg.CodexImportDays = 30
	store := &telemetryStore{cfg: cfg, enabled: true, telemetryDir: filepath.Join(root, "telemetry")}
	collector := newCodexCollector(store)
	collector.scanOnce()
	records := store.readCodexRecords(time.Time{})
	t.Logf("local rollout import tracked_files=%d imported_turns=%d records=%d parse_errors=%d unknown_events_ignored=%d", collector.metrics.trackedFiles.Load(), collector.metrics.importedTurns.Load(), len(records), collector.metrics.parseErrors.Load(), collector.metrics.unknownEvents.Load())
	if collector.metrics.trackedFiles.Load() == 0 {
		t.Fatal("local rollout import found no files")
	}
}
