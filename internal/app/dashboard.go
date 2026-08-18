package app

import (
	"embed"
	"encoding/json"
	"net/http"
	"net/url"
	"runtime"
	"sort"
	"strings"
	"time"
)

// The dashboard is deliberately embedded so the server remains a single
// binary with no Node, CDN, or separate process.
//
//go:embed dashboard/*
var dashboardFiles embed.FS

func (s *server) dashboard(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.DashboardEnabled {
		http.NotFound(w, r)
		return
	}
	switch {
	case r.URL.Path == "/dashboard" || r.URL.Path == "/dashboard/":
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		data, err := dashboardFiles.ReadFile("dashboard/index.html")
		if err != nil {
			http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(data)
	case r.URL.Path == "/dashboard/assets/app.js":
		s.dashboardAsset(w, r, "dashboard/app.js", "text/javascript; charset=utf-8")
	case r.URL.Path == "/dashboard/assets/style.css":
		s.dashboardAsset(w, r, "dashboard/style.css", "text/css; charset=utf-8")
	case r.URL.Path == "/dashboard/api/overview":
		s.dashboardOverview(w, r)
	case r.URL.Path == "/dashboard/api/requests":
		s.dashboardRequests(w, r)
	case strings.HasPrefix(r.URL.Path, "/dashboard/api/requests/"):
		s.dashboardRequestDetail(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *server) dashboardAsset(w http.ResponseWriter, r *http.Request, name, contentType string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	data, err := dashboardFiles.ReadFile(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

func (s *server) dashboardOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	s.telemetry.refreshQuota()
	records := s.dashboardRecords(r.URL.Query())
	quota := s.telemetry.quotaSnapshot()
	telemetryEnabled := s.telemetry != nil && s.telemetry.enabled
	var lastQuota any
	if value := s.telemetry.quotaUpdateTime(); !value.IsZero() {
		lastQuota = value
	}
	quotaStatus := "unavailable"
	if quota != nil {
		quotaStatus = "stale"
		if !s.telemetry.quotaUpdateTime().IsZero() && time.Since(s.telemetry.quotaUpdateTime()) < quotaRefreshInterval {
			quotaStatus = "fresh"
		}
	}
	if quota == nil && !s.telemetry.quotaUnavailable() {
		quotaStatus = "pending"
	}
	writeDashboardJSON(w, 200, map[string]any{
		"bridge_status":           "running",
		"telemetry_enabled":       telemetryEnabled,
		"dashboard_enabled":       s.cfg.DashboardEnabled,
		"active_requests":         s.telemetry.activeCount(),
		"dropped_telemetry_count": s.telemetry.droppedCount(),
		"telemetry_writer_errors": s.telemetry.writerErrorCount(),
		"last_quota_update":       lastQuota,
		"quota_status":            quotaStatus,
		"quota":                   quota,
		"range":                   dashboardRangeName(r.URL.Query()),
		"stats":                   summarizeRecords(records),
		"memory":                  s.dashboardMemorySnapshot(),
	})
}

func (s *server) dashboardRequests(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	query := r.URL.Query()
	records := s.dashboardRecords(query)
	sortTelemetryRecords(records, query.Get("sort"))
	total := len(records)
	limit := positiveInt(query.Get("limit"))
	if limit == 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	offset := positiveInt(query.Get("offset"))
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	items := make([]map[string]any, 0, end-offset)
	for _, record := range records[offset:end] {
		items = append(items, recordForAPI(record, false))
	}
	writeDashboardJSON(w, 200, map[string]any{
		"data": items, "total": total, "offset": offset, "limit": limit, "has_more": end < total,
	})
}

func (s *server) dashboardRequestDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	rawID := strings.TrimPrefix(r.URL.Path, "/dashboard/api/requests/")
	id, err := url.PathUnescape(rawID)
	if err != nil || id == "" {
		writeDashboardJSON(w, 404, map[string]any{"error": "request_not_found"})
		return
	}
	record := s.telemetry.findRecord(id)
	if record == nil {
		writeDashboardJSON(w, 404, map[string]any{"error": "request_not_found"})
		return
	}
	writeDashboardJSON(w, 200, recordForAPI(record, true))
}

func (s *server) dashboardRecords(query url.Values) []*requestRecord {
	if s == nil || s.telemetry == nil {
		return nil
	}
	since := telemetrySince(query)
	// JSONL is the long-term source of truth. Overlay the bounded in-memory
	// window so the latest records retain their event timeline metadata even
	// before/after the asynchronous writer flushes them to disk.
	byID := make(map[string]*requestRecord)
	for _, record := range s.telemetry.readHistoryRecords(since) {
		if record != nil && record.InternalRequestID != "" {
			byID[record.InternalRequestID] = record
		}
	}
	for _, record := range s.telemetry.recentRecords() {
		if record != nil && record.InternalRequestID != "" {
			byID[record.InternalRequestID] = record
		}
	}
	records := make([]*requestRecord, 0, len(byID))
	for _, record := range byID {
		records = append(records, record)
	}
	return filterTelemetryRecords(records, query)
}

func (s *server) dashboardMemorySnapshot() map[string]any {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return map[string]any{
		"heap_alloc_bytes":     stats.HeapAlloc,
		"heap_inuse_bytes":     stats.HeapInuse,
		"heap_sys_bytes":       stats.HeapSys,
		"num_gc":               stats.NumGC,
		"response_store_items": s.responses.count(),
		"chat_store_items":     s.chats.count(),
		"telemetry_records":    s.telemetry.recentCount(),
		"active_requests":      s.telemetry.activeCount(),
	}
}

func writeDashboardJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func dashboardRangeName(query url.Values) string {
	if value := strings.TrimSpace(query.Get("range")); value != "" {
		return value
	}
	return "today"
}

func telemetrySince(query url.Values) time.Time {
	if raw := strings.TrimSpace(query.Get("from")); raw != "" {
		if value, err := time.Parse(time.RFC3339, raw); err == nil {
			return value
		}
	}
	now := time.Now()
	switch strings.ToLower(strings.TrimSpace(query.Get("range"))) {
	case "1h":
		return now.Add(-time.Hour)
	case "6h":
		return now.Add(-6 * time.Hour)
	case "24h":
		return now.Add(-24 * time.Hour)
	case "7d":
		return now.Add(-7 * 24 * time.Hour)
	case "30d":
		return now.Add(-30 * 24 * time.Hour)
	case "":
		return localDayStart(now)
	default:
		return localDayStart(now)
	}
}

func localDayStart(value time.Time) time.Time {
	year, month, day := value.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, value.Location())
}

func filterTelemetryRecords(records []*requestRecord, query url.Values) []*requestRecord {
	since := telemetrySince(query)
	model := strings.TrimSpace(query.Get("model"))
	effort := strings.TrimSpace(query.Get("effort"))
	outcome := strings.TrimSpace(query.Get("status"))
	if outcome == "" {
		outcome = strings.TrimSpace(query.Get("outcome"))
	}
	endpoint := strings.TrimSpace(query.Get("endpoint"))
	result := make([]*requestRecord, 0, len(records))
	for _, record := range records {
		if !since.IsZero() && record.StartedAt.Before(since) {
			continue
		}
		if model != "" && record.Model != model {
			continue
		}
		if effort != "" && stringPointerValue(record.RequestedReasoningEffort) != effort {
			continue
		}
		if outcome != "" && record.Outcome != outcome {
			continue
		}
		if endpoint != "" && record.Endpoint != endpoint {
			continue
		}
		result = append(result, record)
	}
	return result
}

func sortTelemetryRecords(records []*requestRecord, order string) {
	switch strings.ToLower(order) {
	case "slowest":
		sort.SliceStable(records, func(i, j int) bool { return records[i].RequestDurationMS > records[j].RequestDurationMS })
	case "highest_input":
		sort.SliceStable(records, func(i, j int) bool {
			return recordNumber(records[i].InputTokens) > recordNumber(records[j].InputTokens)
		})
	case "highest_reasoning":
		sort.SliceStable(records, func(i, j int) bool {
			return recordNumber(records[i].ReasoningTokens) > recordNumber(records[j].ReasoningTokens)
		})
	default:
		sort.SliceStable(records, func(i, j int) bool { return records[i].StartedAt.After(records[j].StartedAt) })
	}
}

func summarizeRecords(records []*requestRecord) map[string]any {
	var success, input, cached, output, reasoning int64
	var cacheHitRequests int
	var requests int
	var ttfts, durations []int64
	for _, record := range records {
		requests++
		if record.Outcome == "success" {
			success++
		}
		input += recordNumber(record.InputTokens)
		cached += recordNumber(record.CachedInputTokens)
		if record.CachedInputTokens != nil && *record.CachedInputTokens > 0 {
			cacheHitRequests++
		}
		output += recordNumber(record.OutputTokens)
		reasoning += recordNumber(record.ReasoningTokens)
		if record.TTFTMS != nil {
			ttfts = append(ttfts, *record.TTFTMS)
		}
		durations = append(durations, record.RequestDurationMS)
	}
	var successRate, requestCacheHit, tokenCacheRatio float64
	if requests > 0 {
		successRate = float64(success) * 100 / float64(requests)
		requestCacheHit = float64(cacheHitRequests) * 100 / float64(requests)
	}
	if input > 0 {
		tokenCacheRatio = float64(cached) * 100 / float64(input)
	}
	return map[string]any{
		"requests": requests, "success_rate": successRate,
		"input_tokens": input, "cached_input_tokens": cached,
		"cache_hit_requests":        cacheHitRequests,
		"request_cache_hit_percent": requestCacheHit,
		"token_cache_ratio_percent": tokenCacheRatio,
		// Keep the old field as a compatibility alias for existing local pages.
		"cache_hit_percent": tokenCacheRatio,
		"output_tokens":     output, "reasoning_tokens": reasoning,
		"median_ttft_ms": medianInt64(ttfts), "p95_ttft_ms": percentileInt64(ttfts, 0.95),
		"median_duration_ms": medianInt64(durations), "p95_duration_ms": percentileInt64(durations, 0.95),
	}
}

func recordNumber(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func stringPointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func medianInt64(values []int64) any {
	if len(values) == 0 {
		return nil
	}
	copyValues := append([]int64(nil), values...)
	sort.Slice(copyValues, func(i, j int) bool { return copyValues[i] < copyValues[j] })
	middle := len(copyValues) / 2
	if len(copyValues)%2 == 1 {
		return copyValues[middle]
	}
	return (copyValues[middle-1] + copyValues[middle]) / 2
}

func percentileInt64(values []int64, percentile float64) any {
	if len(values) == 0 {
		return nil
	}
	copyValues := append([]int64(nil), values...)
	sort.Slice(copyValues, func(i, j int) bool { return copyValues[i] < copyValues[j] })
	index := int(float64(len(copyValues)-1) * percentile)
	if index < 0 {
		index = 0
	}
	if index >= len(copyValues) {
		index = len(copyValues) - 1
	}
	return copyValues[index]
}
