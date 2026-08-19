package app

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	maxTelemetryRecords          = 200
	maxTelemetryLineBytes        = 2 << 20
	maxTelemetryFingerprintBytes = 16 << 10
	quotaRefreshInterval         = 60 * time.Second
	quotaRequestTimeout          = 10 * time.Second
	telemetryDateLayout          = "2006-01-02"
	telemetryDateFileName        = "2006-01-02.jsonl"
	quotaFileName                = "quota.jsonl"
)

type telemetryContextKey struct{}
type requestIdentityContextKey struct{}

type requestIdentity struct {
	SessionID       string
	SessionSource   string
	ThreadID        string
	ClientRequestID string
	ZCodeRequestID  string
	ZCodeQueryID    string
	PromptCacheKey  string
}

type requestRecord struct {
	InternalRequestID string                 `json:"internal_request_id"`
	StartedAt         time.Time              `json:"started_at"`
	CompletedAt       time.Time              `json:"completed_at"`
	Endpoint          string                 `json:"endpoint"`
	HTTPMethod        string                 `json:"http_method"`
	HTTPStatus        int                    `json:"http_status"`
	Outcome           string                 `json:"outcome"`
	RequestBytes      int64                  `json:"request_bytes"`
	ResponseBytes     int64                  `json:"response_bytes"`
	Stream            bool                   `json:"stream"`
	Source            string                 `json:"source"`
	RecordKind        string                 `json:"record_kind"`
	SecondarySource   string                 `json:"secondary_source,omitempty"`
	RolloutIDHash     string                 `json:"rollout_id_hash,omitempty"`
	SessionIDHash     string                 `json:"session_id_hash,omitempty"`
	TurnIDHash        string                 `json:"turn_id_hash,omitempty"`
	Subagent          *bool                  `json:"subagent,omitempty"`
	ContextWindow     *int64                 `json:"context_window,omitempty"`
	SamplingCount     int                    `json:"sampling_count,omitempty"`
	Sampling          []codexSamplingSummary `json:"sampling,omitempty"`

	Model                         string         `json:"model"`
	Provider                      string         `json:"provider,omitempty"`
	RequestedModel                string         `json:"requested_model,omitempty"`
	ActualUpstreamModel           string         `json:"actual_upstream_model,omitempty"`
	ControlPlaneProjectAvailable  bool           `json:"control_plane_project_available"`
	ModelCatalogSize              int            `json:"model_catalog_size,omitempty"`
	OAuthTokenExpiry              *time.Time     `json:"oauth_token_expiry,omitempty"`
	RequestedReasoningEffort      *string        `json:"requested_reasoning_effort"`
	RequestedReasoningSummary     *string        `json:"requested_reasoning_summary"`
	TextVerbosity                 *string        `json:"text_verbosity"`
	PreviousResponseIDPresent     bool           `json:"previous_response_id_present"`
	IncomingPromptCacheKeyPresent bool           `json:"incoming_prompt_cache_key_present"`
	IncomingPromptCacheKeyHash    string         `json:"incoming_prompt_cache_key_hash,omitempty"`
	UpstreamPromptCacheKeyPresent bool           `json:"upstream_prompt_cache_key_present"`
	UpstreamPromptCacheKeyHash    string         `json:"upstream_prompt_cache_key_hash,omitempty"`
	ToolCount                     int            `json:"tool_count"`
	ToolTypes                     map[string]int `json:"tool_types"`
	ParallelToolCalls             *bool          `json:"parallel_tool_calls"`
	ClientType                    string         `json:"client_type"`

	IncomingSessionSource       string `json:"incoming_session_source,omitempty"`
	IncomingSessionIDHash       string `json:"incoming_session_id_hash,omitempty"`
	IncomingThreadIDHash        string `json:"incoming_thread_id_hash,omitempty"`
	IncomingClientRequestIDHash string `json:"incoming_client_request_id_hash,omitempty"`
	IncomingZCodeRequestIDHash  string `json:"incoming_zcode_request_id_hash,omitempty"`
	IncomingZCodeQueryIDHash    string `json:"incoming_zcode_query_id_hash,omitempty"`

	UpstreamSessionIDHash       string `json:"upstream_session_id_hash,omitempty"`
	UpstreamLegacySessionIDHash string `json:"upstream_legacy_session_id_hash,omitempty"`
	UpstreamThreadIDHash        string `json:"upstream_thread_id_hash,omitempty"`
	UpstreamClientRequestIDHash string `json:"upstream_client_request_id_hash,omitempty"`

	InputPrefixHash          string `json:"input_prefix_hash,omitempty"`
	InstructionsHash         string `json:"instructions_hash,omitempty"`
	ToolsHash                string `json:"tools_hash,omitempty"`
	RequestShapeHash         string `json:"request_shape_hash,omitempty"`
	PreparedInputPrefixHash  string `json:"prepared_input_prefix_hash,omitempty"`
	PreparedInstructionsHash string `json:"prepared_instructions_hash,omitempty"`
	PreparedToolsHash        string `json:"prepared_tools_hash,omitempty"`
	PreparedRequestShapeHash string `json:"prepared_request_shape_hash,omitempty"`

	InputTokens       *int64 `json:"input_tokens"`
	CachedInputTokens *int64 `json:"cached_input_tokens"`
	CacheWriteTokens  *int64 `json:"cache_write_tokens"`
	OutputTokens      *int64 `json:"output_tokens"`
	ReasoningTokens   *int64 `json:"reasoning_tokens"`
	TotalTokens       *int64 `json:"total_tokens"`

	RequestDurationMS       int64  `json:"request_duration_ms"`
	DurationAvailable       bool   `json:"duration_available"`
	FirstUpstreamEventMS    *int64 `json:"first_upstream_event_ms"`
	FirstReasoningEventMS   *int64 `json:"first_reasoning_event_ms"`
	FirstOutputTextDeltaMS  *int64 `json:"first_output_text_delta_ms"`
	FirstToolCallMS         *int64 `json:"first_tool_call_ms"`
	TTFTMS                  *int64 `json:"ttft_ms"`
	LongestSSEGapMS         int64  `json:"longest_sse_gap_ms"`
	EventCount              int    `json:"event_count"`
	UpstreamEventCount      int    `json:"upstream_event_count"`
	DownstreamEventCount    int    `json:"downstream_event_count"`
	ReasoningItemCount      int    `json:"reasoning_item_count"`
	ReadableReasoningItems  int    `json:"readable_reasoning_summary_item_count"`
	ReasoningSummaryDeltas  int    `json:"reasoning_summary_delta_count"`
	RawReasoningTextItems   int    `json:"raw_reasoning_text_item_count"`
	EncryptedReasoningItems int    `json:"encrypted_reasoning_item_count"`
	FunctionCallCount       int    `json:"function_call_count"`
	ToolCallCount           int    `json:"tool_call_count"`
	ResponseCompletedCount  int    `json:"response_completed_count"`
	ResponseFailedCount     int    `json:"response_failed_count"`
	ResponseIncompleteCount int    `json:"response_incomplete_count"`

	UpstreamReasoningEffort          *string `json:"upstream_reasoning_effort"`
	UpstreamReasoningSummary         *string `json:"upstream_reasoning_summary"`
	UpstreamEncryptedReasoningEnable bool    `json:"upstream_encrypted_reasoning_enabled"`
	ClientDisconnected               bool    `json:"client_disconnected"`
	UpstreamStreamError              bool    `json:"upstream_stream_error"`
	UpstreamStatus                   *int    `json:"upstream_status,omitempty"`
	ErrorClass                       string  `json:"error_class,omitempty"`
	Retryable                        *bool   `json:"retryable,omitempty"`

	Timeline []telemetryEvent `json:"-"`
}

type telemetryEvent struct {
	Direction  string `json:"direction"`
	RelativeMS int64  `json:"relative_ms"`
	Type       string `json:"type"`
	Sequence   *int   `json:"sequence_number"`
	ItemType   string `json:"item_type"`
	ItemIDHash string `json:"item_id_hash"`
}

type requestTelemetry struct {
	store               *telemetryStore
	record              *requestRecord
	started             time.Time
	mu                  sync.Mutex
	finished            bool
	reasoningID         map[string]struct{}
	readableReasoningID map[string]struct{}
	rawReasoningID      map[string]struct{}
	encryptedID         map[string]struct{}
	toolID              map[string]struct{}
	lastEvent           time.Time
}

type telemetryWrite struct {
	record *requestRecord
	quota  map[string]any
}

type telemetryStore struct {
	cfg          config
	enabled      bool
	telemetryDir string
	queue        chan telemetryWrite
	stop         chan struct{}
	wg           sync.WaitGroup
	recordsMu    sync.RWMutex
	historyMu    sync.RWMutex
	records      []*requestRecord
	dropped      atomic.Uint64
	writerErrors atomic.Uint64
	active       atomic.Int64
	quota        *quotaState
	fetcher      quotaFetcher
	codex        *codexCollector
	closeOnce    sync.Once
}

type quotaFetcher interface {
	fetchQuota(context.Context) (map[string]any, error)
}

type quotaState struct {
	mu          sync.RWMutex
	snapshot    map[string]any
	fetchedAt   time.Time
	lastAttempt time.Time
	inFlight    bool
	lastError   bool
}

func withTelemetry(ctx context.Context, value *requestTelemetry) context.Context {
	if value == nil {
		return ctx
	}
	return context.WithValue(ctx, telemetryContextKey{}, value)
}

func withRequestIdentity(ctx context.Context, value requestIdentity) context.Context {
	return context.WithValue(ctx, requestIdentityContextKey{}, value)
}

func requestIdentityFromContext(ctx context.Context) requestIdentity {
	if ctx == nil {
		return requestIdentity{}
	}
	value, _ := ctx.Value(requestIdentityContextKey{}).(requestIdentity)
	return value
}

func requestIdentityFromHeaders(headers http.Header) requestIdentity {
	sessionID, sessionSource := firstHeader(headers,
		"session-id",
		"session_id",
		"x-session-id",
	)
	threadID, _ := firstHeader(headers, "thread-id")
	clientRequestID, _ := firstHeader(headers, "x-client-request-id")
	zcodeRequestID, _ := firstHeader(headers, "x-request-id")
	zcodeQueryID, _ := firstHeader(headers, "x-query-id")
	if threadID != "" {
		clientRequestID = threadID
	}
	return requestIdentity{
		SessionID:       sessionID,
		SessionSource:   sessionSource,
		ThreadID:        threadID,
		ClientRequestID: clientRequestID,
		ZCodeRequestID:  zcodeRequestID,
		ZCodeQueryID:    zcodeQueryID,
	}
}

func firstHeader(headers http.Header, names ...string) (string, string) {
	for _, name := range names {
		for key, values := range headers {
			if !strings.EqualFold(key, name) || len(values) == 0 {
				continue
			}
			if value := strings.TrimSpace(values[0]); value != "" {
				return value, name
			}
		}
	}
	return "", ""
}

func headerValue(headers http.Header, name string) string {
	value, _ := firstHeader(headers, name)
	return value
}

func safeJSONPrefixHash(value any) string {
	if value == nil {
		return ""
	}
	hasher := sha256.New()
	remaining := maxTelemetryFingerprintBytes
	truncated := false
	fingerprintValue(hasher, value, &remaining, &truncated, 0)
	if truncated {
		_, _ = hasher.Write([]byte{0})
	}
	return hex.EncodeToString(hasher.Sum(nil))[:16]
}

func fingerprintValue(hasher hash.Hash, value any, remaining *int, truncated *bool, depth int) {
	if *remaining <= 0 {
		*truncated = true
		return
	}
	if depth > 8 {
		writeFingerprint(hasher, []byte("depth"), remaining, truncated)
		return
	}
	switch typed := value.(type) {
	case nil:
		writeFingerprint(hasher, []byte("nil"), remaining, truncated)
	case string:
		writeFingerprint(hasher, []byte("string:"), remaining, truncated)
		writeFingerprint(hasher, []byte(strconv.Itoa(len(typed))), remaining, truncated)
		writeFingerprint(hasher, []byte{':'}, remaining, truncated)
		writeFingerprintString(hasher, typed, remaining, truncated)
	case bool:
		writeFingerprint(hasher, []byte("bool:"+strconv.FormatBool(typed)), remaining, truncated)
	case float64:
		writeFingerprint(hasher, []byte("number:"+strconv.FormatFloat(typed, 'g', -1, 64)), remaining, truncated)
	case json.Number:
		writeFingerprint(hasher, []byte("number:"+string(typed)), remaining, truncated)
	case []any:
		writeFingerprint(hasher, []byte("array:"+strconv.Itoa(len(typed)+0)+"["), remaining, truncated)
		for _, item := range typed {
			if *remaining <= 0 {
				*truncated = true
				break
			}
			fingerprintValue(hasher, item, remaining, truncated, depth+1)
		}
		writeFingerprint(hasher, []byte{']'}, remaining, truncated)
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		writeFingerprint(hasher, []byte("object:"+strconv.Itoa(len(keys))+"{"), remaining, truncated)
		for _, key := range keys {
			if *remaining <= 0 {
				*truncated = true
				break
			}
			writeFingerprint(hasher, []byte(key+":"), remaining, truncated)
			fingerprintValue(hasher, typed[key], remaining, truncated, depth+1)
		}
		writeFingerprint(hasher, []byte{'}'}, remaining, truncated)
	default:
		writeFingerprint(hasher, []byte(fmt.Sprintf("type:%T:%v", value, value)), remaining, truncated)
	}
}

func writeFingerprint(hasher hash.Hash, data []byte, remaining *int, truncated *bool) {
	if *remaining <= 0 {
		*truncated = true
		return
	}
	if len(data) > *remaining {
		_, _ = hasher.Write(data[:*remaining])
		*remaining = 0
		*truncated = true
		return
	}
	written, _ := hasher.Write(data)
	*remaining -= written
}

func writeFingerprintString(hasher hash.Hash, value string, remaining *int, truncated *bool) {
	const chunkSize = 4 << 10
	if *remaining <= 0 {
		*truncated = true
		return
	}
	limit := len(value)
	if limit > *remaining {
		limit = *remaining
	}
	for offset := 0; offset < limit; {
		end := offset + chunkSize
		if end > limit {
			end = limit
		}
		written, _ := hasher.Write([]byte(value[offset:end]))
		*remaining -= written
		offset += written
	}
	if limit < len(value) {
		*truncated = true
		*remaining = 0
	}
}

func requestShapeHash(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	shape := make(map[string]any, 11)
	for _, name := range []string{
		"model",
		"instructions",
		"tools",
		"tool_choice",
		"parallel_tool_calls",
		"reasoning",
		"store",
		"stream",
		"include",
		"service_tier",
		"prompt_cache_key",
		"text",
	} {
		if value, ok := payload[name]; ok {
			shape[name] = value
		}
	}
	return safeJSONPrefixHash(shape)
}

func upstreamRequestIdentity(ctx context.Context, payload map[string]any) requestIdentity {
	identity := requestIdentityFromContext(ctx)
	identity.PromptCacheKey = strings.TrimSpace(stringValue(payload["prompt_cache_key"]))
	if identity.PromptCacheKey == "" && identity.SessionID != "" {
		identity.PromptCacheKey = identity.SessionID
	}
	return identity
}

func telemetryFromContext(ctx context.Context) *requestTelemetry {
	if ctx == nil {
		return nil
	}
	value, _ := ctx.Value(telemetryContextKey{}).(*requestTelemetry)
	return value
}

func newTelemetryStore(cfg config, fetcher quotaFetcher) *telemetryStore {
	store := &telemetryStore{
		cfg:          cfg,
		enabled:      cfg.TelemetryEnabled,
		telemetryDir: filepath.Join(expandHome(cfg.StateDir), "telemetry"),
		stop:         make(chan struct{}),
		fetcher:      fetcher,
		quota:        &quotaState{},
	}
	if !store.enabled {
		return store
	}
	queueSize := cfg.TelemetryQueueSize
	if queueSize < 1 {
		queueSize = 256
	}
	store.queue = make(chan telemetryWrite, queueSize)
	if err := os.MkdirAll(store.telemetryDir, 0700); err != nil {
		store.writerErrors.Add(1)
		log.Printf("telemetry.init.error code=directory_unavailable")
	} else {
		store.loadRecentRecords()
		store.loadQuotaSnapshot()
		store.cleanupRetention()
	}
	store.wg.Add(1)
	go store.writerLoop()
	if cfg.CodexCollectorEnabled && (strings.TrimSpace(cfg.CodexSessionsDir) != "" || strings.TrimSpace(cfg.CodexArchivedSessionsDir) != "") {
		store.codex = newCodexCollector(store)
		store.codex.start()
	}
	return store
}

func (s *telemetryStore) close() {
	if s == nil || !s.enabled || s.queue == nil {
		return
	}
	s.closeOnce.Do(func() {
		if s.codex != nil {
			s.codex.stopCollector()
		}
		close(s.stop)
		s.wg.Wait()
	})
}

func (s *telemetryStore) readCodexRecords(since time.Time) []*requestRecord {
	if s == nil || !s.enabled {
		return nil
	}
	directory := filepath.Join(s.telemetryDir, codexSummaryDirName)
	if s.codex != nil {
		directory = s.codex.summaryDir
	}
	s.historyMu.RLock()
	defer s.historyMu.RUnlock()
	var records []*requestRecord
	for _, name := range telemetryHistoryFiles(directory, since) {
		file, err := os.Open(name)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64*1024), maxTelemetryLineBytes)
		for scanner.Scan() {
			var record requestRecord
			if json.Unmarshal(scanner.Bytes(), &record) != nil || record.InternalRequestID == "" {
				continue
			}
			normalizeSourceRecord(&record)
			if record.Source != "codex" {
				continue
			}
			if !since.IsZero() && record.StartedAt.Before(since) {
				continue
			}
			record.Timeline = nil
			copy := record
			records = append(records, &copy)
		}
		_ = file.Close()
	}
	return records
}

func (s *telemetryStore) codexSnapshot() map[string]any {
	if s == nil || s.codex == nil {
		return map[string]any{"enabled": false}
	}
	return s.codex.snapshot()
}

func (s *telemetryStore) begin(r *http.Request, endpoint string) *requestTelemetry {
	if s == nil || !s.enabled {
		return nil
	}
	now := time.Now().UTC()
	record := &requestRecord{
		InternalRequestID: newID("req"),
		StartedAt:         now,
		Endpoint:          endpoint,
		HTTPMethod:        r.Method,
		Outcome:           "failed",
		Source:            "bridge",
		RecordKind:        "request",
		ClientType:        detectClientType(r.Header),
		ToolTypes:         map[string]int{},
	}
	identity := requestIdentityFromHeaders(r.Header)
	record.IncomingSessionSource = identity.SessionSource
	record.IncomingSessionIDHash = safeIDHash(identity.SessionID)
	record.IncomingThreadIDHash = safeIDHash(identity.ThreadID)
	record.IncomingClientRequestIDHash = safeIDHash(identity.ClientRequestID)
	record.IncomingZCodeRequestIDHash = safeIDHash(identity.ZCodeRequestID)
	record.IncomingZCodeQueryIDHash = safeIDHash(identity.ZCodeQueryID)
	eventLimit := s.cfg.TelemetryEventMemoryLimit
	if eventLimit < 1 {
		eventLimit = 200
	}
	t := &requestTelemetry{
		store:               s,
		record:              record,
		started:             now,
		reasoningID:         map[string]struct{}{},
		readableReasoningID: map[string]struct{}{},
		rawReasoningID:      map[string]struct{}{},
		encryptedID:         map[string]struct{}{},
		toolID:              map[string]struct{}{},
	}
	record.Timeline = make([]telemetryEvent, 0, eventLimit)
	s.active.Add(1)
	return t
}

func (s *telemetryStore) finish(t *requestTelemetry, r *http.Request, capture *responseCapture, bodyBytes int64) {
	if t == nil {
		return
	}
	t.mu.Lock()
	if t.finished {
		t.mu.Unlock()
		return
	}
	t.finished = true
	record := t.record
	record.CompletedAt = time.Now().UTC()
	record.RequestBytes = bodyBytes
	record.ResponseBytes = capture.bytes
	record.HTTPStatus = capture.statusCode()
	record.RequestDurationMS = maxInt64(0, record.CompletedAt.Sub(record.StartedAt).Microseconds()/1000)
	record.DurationAvailable = true
	if record.FirstOutputTextDeltaMS != nil {
		record.TTFTMS = cloneInt64(record.FirstOutputTextDeltaMS)
	}
	if r.Context().Err() != nil {
		record.ClientDisconnected = true
	}
	if record.ClientDisconnected {
		record.Outcome = "cancelled"
	} else if record.HTTPStatus >= 200 && record.HTTPStatus < 400 && record.ResponseFailedCount == 0 && record.ResponseIncompleteCount == 0 && !record.UpstreamStreamError {
		record.Outcome = "success"
	} else {
		record.Outcome = "failed"
	}
	t.mu.Unlock()

	s.active.Add(-1)
	s.recordsMu.Lock()
	s.records = append(s.records, record)
	if len(s.records) > maxTelemetryRecords {
		s.records = s.records[len(s.records)-maxTelemetryRecords:]
	}
	s.recordsMu.Unlock()
	s.enqueue(telemetryWrite{record: record})
}

func (s *telemetryStore) enqueue(item telemetryWrite) {
	if s == nil || !s.enabled || s.queue == nil {
		return
	}
	select {
	case s.queue <- item:
	default:
		s.dropped.Add(1)
	}
}

func (s *telemetryStore) writerLoop() {
	defer s.wg.Done()
	for {
		select {
		case item := <-s.queue:
			s.writeItem(item)
		case <-s.stop:
			for {
				select {
				case item := <-s.queue:
					s.writeItem(item)
				default:
					return
				}
			}
		}
	}
}

func (s *telemetryStore) writeItem(item telemetryWrite) {
	if item.record != nil {
		when := item.record.CompletedAt
		if when.IsZero() {
			when = time.Now().UTC()
		}
		s.appendJSONL(when, item.record)
	}
	if item.quota != nil {
		s.appendQuota(item.quota)
	}
}

func (s *telemetryStore) appendJSONL(now time.Time, value any) {
	s.historyMu.Lock()
	defer s.historyMu.Unlock()
	name := filepath.Join(s.telemetryDir, now.Local().Format(telemetryDateLayout)+".jsonl")
	file, err := os.OpenFile(name, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		s.writerErrors.Add(1)
		log.Printf("telemetry.write.error code=request_file_unavailable")
		return
	}
	defer file.Close()
	data, err := json.Marshal(value)
	if err != nil {
		s.writerErrors.Add(1)
		log.Printf("telemetry.write.error code=request_encode_failed")
		return
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		s.writerErrors.Add(1)
		log.Printf("telemetry.write.error code=request_append_failed")
	}
}

func (s *telemetryStore) appendQuota(value map[string]any) {
	name := filepath.Join(s.telemetryDir, quotaFileName)
	file, err := os.OpenFile(name, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		s.writerErrors.Add(1)
		log.Printf("telemetry.write.error code=quota_file_unavailable")
		return
	}
	defer file.Close()
	data, err := json.Marshal(value)
	if err != nil {
		s.writerErrors.Add(1)
		return
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		s.writerErrors.Add(1)
	}
}

func (s *telemetryStore) loadRecentRecords() {
	files := telemetryHistoryFiles(s.telemetryDir, time.Time{})
	for _, name := range files {
		file, err := os.Open(name)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64*1024), maxTelemetryLineBytes)
		for scanner.Scan() {
			var record requestRecord
			if json.Unmarshal(scanner.Bytes(), &record) != nil || record.InternalRequestID == "" {
				continue
			}
			normalizeSourceRecord(&record)
			record.Timeline = nil
			s.records = append(s.records, &record)
			if len(s.records) > maxTelemetryRecords {
				s.records = s.records[len(s.records)-maxTelemetryRecords:]
			}
		}
		_ = file.Close()
	}
}

func telemetryHistoryFiles(telemetryDir string, since time.Time) []string {
	files, err := filepath.Glob(filepath.Join(telemetryDir, "*.jsonl"))
	if err != nil {
		return nil
	}
	sort.Strings(files)
	result := make([]string, 0, len(files))
	for _, name := range files {
		base := filepath.Base(name)
		if base == quotaFileName || len(base) != len(telemetryDateFileName) {
			continue
		}
		date, err := time.ParseInLocation(telemetryDateLayout, strings.TrimSuffix(base, ".jsonl"), time.Local)
		if err != nil {
			continue
		}
		if !since.IsZero() && date.AddDate(0, 0, 1).Before(since) {
			continue
		}
		result = append(result, name)
	}
	return result
}

// readHistoryRecords reads the append-only JSONL summaries without exposing
// request payloads. The writer serializes appendJSONL under historyMu, so a
// dashboard read cannot observe a partially written line from this process;
// malformed lines are still ignored defensively for files left by an abrupt
// process termination.
func (s *telemetryStore) readHistoryRecords(since time.Time) []*requestRecord {
	if s == nil || !s.enabled {
		return nil
	}
	s.historyMu.RLock()
	defer s.historyMu.RUnlock()
	var records []*requestRecord
	for _, name := range telemetryHistoryFiles(s.telemetryDir, since) {
		file, err := os.Open(name)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64*1024), maxTelemetryLineBytes)
		for scanner.Scan() {
			var record requestRecord
			if json.Unmarshal(scanner.Bytes(), &record) != nil || record.InternalRequestID == "" {
				continue
			}
			normalizeSourceRecord(&record)
			if !since.IsZero() && record.StartedAt.Before(since) {
				continue
			}
			record.Timeline = nil
			recordCopy := record
			records = append(records, &recordCopy)
		}
		_ = file.Close()
	}
	return records
}

func (s *telemetryStore) loadQuotaSnapshot() {
	name := filepath.Join(s.telemetryDir, quotaFileName)
	file, err := os.Open(name)
	if err != nil {
		return
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), maxTelemetryLineBytes)
	var last map[string]any
	for scanner.Scan() {
		var value map[string]any
		if json.Unmarshal(scanner.Bytes(), &value) == nil {
			last = value
		}
	}
	if last == nil {
		return
	}
	fetchedAt, _ := time.Parse(time.RFC3339Nano, stringValue(last["fetched_at"]))
	s.quota.mu.Lock()
	s.quota.snapshot = cloneMap(last)
	s.quota.fetchedAt = fetchedAt
	s.quota.mu.Unlock()
}

func (s *telemetryStore) cleanupRetention() {
	days := s.cfg.TelemetryRetentionDays
	if days < 1 {
		days = 30
	}
	cutoff := time.Now().Local().AddDate(0, 0, -days)
	files, _ := filepath.Glob(filepath.Join(s.telemetryDir, "*.jsonl"))
	root, err := filepath.Abs(s.telemetryDir)
	if err != nil {
		return
	}
	for _, name := range files {
		base := filepath.Base(name)
		if base == quotaFileName || len(base) != len(telemetryDateFileName) {
			continue
		}
		date, err := time.ParseInLocation(telemetryDateLayout, strings.TrimSuffix(base, ".jsonl"), time.Local)
		if err != nil || !date.Before(cutoff) {
			continue
		}
		absolute, err := filepath.Abs(name)
		if err != nil {
			continue
		}
		relative, err := filepath.Rel(root, absolute)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) || filepath.Dir(relative) != "." {
			continue
		}
		if err := os.Remove(absolute); err != nil {
			s.writerErrors.Add(1)
		}
	}
	s.cleanupQuotaHistory(cutoff, root)
}

func (s *telemetryStore) cleanupQuotaHistory(cutoff time.Time, root string) {
	name := filepath.Join(s.telemetryDir, quotaFileName)
	absolute, err := filepath.Abs(name)
	if err != nil {
		return
	}
	relative, err := filepath.Rel(root, absolute)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) || filepath.Dir(relative) != "." || filepath.Base(relative) != quotaFileName {
		return
	}

	input, err := os.Open(absolute)
	if err != nil {
		if !os.IsNotExist(err) {
			s.writerErrors.Add(1)
		}
		return
	}
	inputClosed := false
	defer func() {
		if !inputClosed {
			_ = input.Close()
		}
	}()

	temporary, err := os.CreateTemp(root, ".quota-retention-*.jsonl")
	if err != nil {
		s.writerErrors.Add(1)
		return
	}
	temporaryName := temporary.Name()
	keepTemporary := false
	defer func() {
		_ = temporary.Close()
		if !keepTemporary {
			_ = os.Remove(temporaryName)
		}
	}()
	_ = temporary.Chmod(0600)

	writer := bufio.NewWriterSize(temporary, 16*1024)
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), maxTelemetryLineBytes)
	changed := false
	for scanner.Scan() {
		line := scanner.Bytes()
		keep := true
		var snapshot map[string]any
		if json.Unmarshal(line, &snapshot) == nil {
			if fetchedAt, parseErr := time.Parse(time.RFC3339Nano, stringValue(snapshot["fetched_at"])); parseErr == nil && fetchedAt.Before(cutoff) {
				keep = false
				changed = true
			}
		}
		if keep {
			if _, err := writer.Write(line); err != nil {
				s.writerErrors.Add(1)
				return
			}
			if err := writer.WriteByte('\n'); err != nil {
				s.writerErrors.Add(1)
				return
			}
		}
	}
	if err := scanner.Err(); err != nil {
		s.writerErrors.Add(1)
		return
	}
	if !changed {
		return
	}
	if err := writer.Flush(); err != nil {
		s.writerErrors.Add(1)
		return
	}
	if err := input.Close(); err != nil {
		s.writerErrors.Add(1)
		return
	}
	inputClosed = true
	if err := temporary.Close(); err != nil {
		s.writerErrors.Add(1)
		return
	}
	if err := os.Remove(absolute); err != nil {
		s.writerErrors.Add(1)
		return
	}
	if err := os.Rename(temporaryName, absolute); err != nil {
		s.writerErrors.Add(1)
		return
	}
	keepTemporary = true
}

func (s *telemetryStore) recentRecords() []*requestRecord {
	if s == nil {
		return nil
	}
	s.recordsMu.RLock()
	defer s.recordsMu.RUnlock()
	result := make([]*requestRecord, len(s.records))
	copy(result, s.records)
	return result
}

func (s *telemetryStore) findRecord(id string) *requestRecord {
	if s == nil {
		return nil
	}
	s.recordsMu.RLock()
	defer s.recordsMu.RUnlock()
	for i := len(s.records) - 1; i >= 0; i-- {
		if s.records[i].InternalRequestID == id {
			return s.records[i]
		}
	}
	history := s.readHistoryRecords(time.Time{})
	for i := len(history) - 1; i >= 0; i-- {
		record := history[i]
		if record.InternalRequestID == id {
			return record
		}
	}
	codex := s.readCodexRecords(time.Time{})
	for i := len(codex) - 1; i >= 0; i-- {
		record := codex[i]
		if record.InternalRequestID == id {
			return record
		}
	}
	return nil
}

func (s *telemetryStore) recentCount() int {
	if s == nil {
		return 0
	}
	s.recordsMu.RLock()
	defer s.recordsMu.RUnlock()
	return len(s.records)
}

func (s *telemetryStore) droppedCount() uint64 {
	if s == nil {
		return 0
	}
	return s.dropped.Load()
}

func (s *telemetryStore) writerErrorCount() uint64 {
	if s == nil {
		return 0
	}
	return s.writerErrors.Load()
}

func (s *telemetryStore) activeCount() int64 {
	if s == nil {
		return 0
	}
	return s.active.Load()
}

func (s *telemetryStore) quotaSnapshot() map[string]any {
	if s == nil || s.quota == nil {
		return nil
	}
	s.quota.mu.RLock()
	defer s.quota.mu.RUnlock()
	return cloneMap(s.quota.snapshot)
}

func (s *telemetryStore) quotaUpdateTime() time.Time {
	if s == nil || s.quota == nil {
		return time.Time{}
	}
	s.quota.mu.RLock()
	defer s.quota.mu.RUnlock()
	return s.quota.fetchedAt
}

func (s *telemetryStore) refreshQuota() {
	if s == nil || s.fetcher == nil || s.quota == nil {
		return
	}
	now := time.Now()
	s.quota.mu.Lock()
	if s.quota.inFlight || (!s.quota.lastAttempt.IsZero() && now.Sub(s.quota.lastAttempt) < quotaRefreshInterval) {
		s.quota.mu.Unlock()
		return
	}
	s.quota.lastAttempt = now
	s.quota.inFlight = true
	s.quota.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), quotaRequestTimeout)
		defer cancel()
		snapshot, err := s.fetcher.fetchQuota(ctx)
		s.quota.mu.Lock()
		s.quota.inFlight = false
		if err != nil || snapshot == nil {
			s.quota.lastError = true
			s.quota.mu.Unlock()
			log.Printf("quota.refresh.error code=unavailable")
			return
		}
		snapshot["fetched_at"] = time.Now().UTC().Format(time.RFC3339Nano)
		snapshot["source"] = "GET " + quotaRoute(s.cfg.BackendURL)
		s.quota.snapshot = cloneMap(snapshot)
		s.quota.fetchedAt = time.Now().UTC()
		s.quota.lastError = false
		s.quota.mu.Unlock()
		s.enqueue(telemetryWrite{quota: snapshot})
	}()
}

func (s *telemetryStore) quotaUnavailable() bool {
	if s == nil || s.quota == nil {
		return false
	}
	s.quota.mu.RLock()
	defer s.quota.mu.RUnlock()
	return s.quota.lastError
}

func recordForAPI(record *requestRecord, includeTimeline bool) map[string]any {
	if record == nil {
		return nil
	}
	data, _ := json.Marshal(record)
	var result map[string]any
	if json.Unmarshal(data, &result) != nil {
		return nil
	}
	if includeTimeline {
		timeline := make([]telemetryEvent, len(record.Timeline))
		copy(timeline, record.Timeline)
		result["timeline"] = timeline
	}
	return result
}

func normalizeSourceRecord(record *requestRecord) {
	if record == nil {
		return
	}
	if record.Source == "" {
		record.Source = "bridge"
	}
	if record.RecordKind == "" {
		record.RecordKind = "request"
	}
	record.Source = strings.ToLower(strings.TrimSpace(record.Source))
	if record.Source == "codex" {
		if record.ClientType == "" {
			record.ClientType = "Codex"
		}
		if record.RecordKind == "request" {
			record.RecordKind = "turn"
		}
	} else if record.ClientType == "" {
		record.ClientType = "Unknown"
	}
}

func (t *requestTelemetry) observeRequest(body map[string]any, endpoint string) {
	if t == nil || body == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.record.Endpoint = endpoint
	t.record.Model = stringValue(body["model"])
	t.record.RequestedModel = t.record.Model
	t.record.Stream = boolValue(body["stream"])
	t.record.PreviousResponseIDPresent = strings.TrimSpace(stringValue(body["previous_response_id"])) != ""
	incomingPromptCacheKey := strings.TrimSpace(stringValue(body["prompt_cache_key"]))
	t.record.IncomingPromptCacheKeyPresent = incomingPromptCacheKey != ""
	t.record.IncomingPromptCacheKeyHash = safeIDHash(incomingPromptCacheKey)
	t.record.InputPrefixHash = safeJSONPrefixHash(body["input"])
	t.record.InstructionsHash = safeJSONPrefixHash(body["instructions"])
	t.record.ToolsHash = safeJSONPrefixHash(body["tools"])
	t.record.RequestShapeHash = requestShapeHash(body)
	if value := stringValue(body["verbosity"]); value != "" {
		t.record.TextVerbosity = stringPointer(value)
	}
	if text := mapAny(body["text"]); text != nil && stringValue(text["verbosity"]) != "" {
		t.record.TextVerbosity = stringPointer(stringValue(text["verbosity"]))
	}
	if reasoning := mapAny(body["reasoning"]); reasoning != nil {
		if value := stringValue(reasoning["effort"]); value != "" {
			t.record.RequestedReasoningEffort = stringPointer(value)
		}
		if value := stringValue(reasoning["summary"]); value != "" {
			t.record.RequestedReasoningSummary = stringPointer(value)
		}
	} else if value := stringValue(body["reasoning_effort"]); value != "" {
		t.record.RequestedReasoningEffort = stringPointer(value)
	}
	if value, ok := body["parallel_tool_calls"].(bool); ok {
		t.record.ParallelToolCalls = boolPointer(value)
	}
	for _, raw := range sliceAny(body["tools"]) {
		tool := mapAny(raw)
		if tool == nil {
			continue
		}
		t.record.ToolCount++
		typeName := stringValue(tool["type"])
		if typeName == "function" {
			if fn := mapAny(tool["function"]); fn != nil && stringValue(fn["type"]) != "" {
				typeName = stringValue(fn["type"])
			}
		}
		if typeName == "" {
			typeName = "unknown"
		}
		t.record.ToolTypes[safeToken(typeName)]++
	}
}

func (t *requestTelemetry) observePrepared(payload map[string]any) {
	if t == nil || payload == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if value := stringValue(payload["model"]); value != "" {
		t.record.Model = value
	}
	t.record.PreparedInputPrefixHash = safeJSONPrefixHash(payload["input"])
	t.record.PreparedInstructionsHash = safeJSONPrefixHash(payload["instructions"])
	t.record.PreparedToolsHash = safeJSONPrefixHash(payload["tools"])
	t.record.PreparedRequestShapeHash = requestShapeHash(payload)
	upstreamPromptCacheKey := strings.TrimSpace(stringValue(payload["prompt_cache_key"]))
	t.record.UpstreamPromptCacheKeyPresent = upstreamPromptCacheKey != ""
	t.record.UpstreamPromptCacheKeyHash = safeIDHash(upstreamPromptCacheKey)
	if reasoning := mapAny(payload["reasoning"]); reasoning != nil {
		if value := stringValue(reasoning["effort"]); value != "" {
			t.record.UpstreamReasoningEffort = stringPointer(value)
		}
		if value := stringValue(reasoning["summary"]); value != "" {
			t.record.UpstreamReasoningSummary = stringPointer(value)
		}
	}
	for _, raw := range sliceAny(payload["include"]) {
		if stringValue(raw) == "reasoning.encrypted_content" {
			t.record.UpstreamEncryptedReasoningEnable = true
			break
		}
	}
}

func (t *requestTelemetry) observeProvider(route providerRoute) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.record.Provider = safeToken(route.Provider)
	if route.RequestedModel != "" {
		t.record.RequestedModel = route.RequestedModel
	}
	if route.ActualUpstreamModel != "" {
		t.record.ActualUpstreamModel = route.ActualUpstreamModel
		t.record.Model = route.ActualUpstreamModel
	}
	t.record.ControlPlaneProjectAvailable = route.ControlPlaneProjectAvailable
	if route.CatalogSize > 0 {
		t.record.ModelCatalogSize = route.CatalogSize
	}
	if !route.OAuthTokenExpiry.IsZero() {
		expiry := route.OAuthTokenExpiry.UTC()
		t.record.OAuthTokenExpiry = &expiry
	}
}

func (t *requestTelemetry) observeUpstreamRequest(headers http.Header) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.record.UpstreamSessionIDHash = safeIDHash(headerValue(headers, "session-id"))
	t.record.UpstreamLegacySessionIDHash = safeIDHash(headerValue(headers, "session_id"))
	t.record.UpstreamThreadIDHash = safeIDHash(headerValue(headers, "thread-id"))
	t.record.UpstreamClientRequestIDHash = safeIDHash(headerValue(headers, "x-client-request-id"))
}

func (t *requestTelemetry) observeUpstreamEvent(event map[string]any) {
	t.observeEvent("upstream", event)
}

func (t *requestTelemetry) observeDownstreamEvent(event map[string]any) {
	t.observeEvent("downstream", event)
}

func (t *requestTelemetry) observeEvent(direction string, event map[string]any) {
	if t == nil {
		return
	}
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	if direction == "upstream" {
		t.record.UpstreamEventCount++
		t.record.EventCount = t.record.UpstreamEventCount
		if t.record.FirstUpstreamEventMS == nil {
			t.record.FirstUpstreamEventMS = durationPointer(now.Sub(t.started))
		}
		if !t.lastEvent.IsZero() {
			gap := now.Sub(t.lastEvent).Microseconds() / 1000
			if gap > t.record.LongestSSEGapMS {
				t.record.LongestSSEGapMS = gap
			}
		}
		t.lastEvent = now
	} else {
		t.record.DownstreamEventCount++
	}

	eventType := safeToken(stringValue(event["type"]))
	item := mapAny(event["item"])
	itemType := safeToken(stringValue(item["type"]))
	itemID := stringValue(item["id"])
	if itemID == "" {
		itemID = stringValue(item["call_id"])
	}
	sequence, hasSequence := numberAsInt(event["sequence_number"])
	var sequencePointer *int
	if hasSequence {
		sequencePointer = &sequence
	}
	timeline := telemetryEvent{
		Direction:  direction,
		RelativeMS: maxInt64(0, now.Sub(t.started).Microseconds()/1000),
		Type:       eventType,
		Sequence:   sequencePointer,
		ItemType:   itemType,
		ItemIDHash: safeIDHash(itemID),
	}
	limit := t.store.cfg.TelemetryEventMemoryLimit
	if limit < 1 {
		limit = 200
	}
	if len(t.record.Timeline) >= limit {
		copy(t.record.Timeline, t.record.Timeline[1:])
		t.record.Timeline[len(t.record.Timeline)-1] = timeline
	} else {
		t.record.Timeline = append(t.record.Timeline, timeline)
	}

	// TTFT is intentionally measured at the downstream boundary: this is the
	// first text delta that the client can actually observe. For Responses the
	// downstream event keeps the upstream event shape; for Chat Completions the
	// adapter emits chat.completion.chunk with content in choices[].delta.
	if direction == "downstream" && downstreamOutputTextEvent(eventType, event) && t.record.FirstOutputTextDeltaMS == nil {
		t.record.FirstOutputTextDeltaMS = durationPointer(now.Sub(t.started))
	}

	if direction != "upstream" {
		return
	}
	if reasoningEvent(eventType, item) {
		if t.record.FirstReasoningEventMS == nil {
			t.record.FirstReasoningEventMS = durationPointer(now.Sub(t.started))
		}
		id := safeIDHash(itemID)
		if id == "" {
			id = fmt.Sprintf("%s:%d", eventType, t.record.UpstreamEventCount)
		}
		if itemType == "reasoning" || strings.Contains(eventType, "reasoning.item") {
			if _, seen := t.reasoningID[id]; !seen {
				t.reasoningID[id] = struct{}{}
				t.record.ReasoningItemCount++
			}
		}
		if len(sliceAny(item["summary"])) > 0 {
			if _, seen := t.readableReasoningID[id]; !seen {
				t.readableReasoningID[id] = struct{}{}
				t.record.ReadableReasoningItems++
			}
		}
		if item["content"] != nil || item["text"] != nil {
			if _, seen := t.rawReasoningID[id]; !seen {
				t.rawReasoningID[id] = struct{}{}
				t.record.RawReasoningTextItems++
			}
		}
		if strings.Contains(eventType, "reasoning_summary") && strings.Contains(eventType, "delta") {
			t.record.ReasoningSummaryDeltas++
		}
		if encryptedContentPresent(event, item) {
			if _, seen := t.encryptedID[id]; !seen {
				t.encryptedID[id] = struct{}{}
				t.record.EncryptedReasoningItems++
			}
		}
	}
	if isToolEvent(eventType, itemType) {
		id := safeIDHash(itemID)
		if id == "" {
			id = fmt.Sprintf("%s:%d", eventType, t.record.UpstreamEventCount)
		}
		if _, seen := t.toolID[id]; !seen {
			t.toolID[id] = struct{}{}
			t.record.ToolCallCount++
			if itemType == "function_call" || strings.Contains(eventType, "function_call") {
				t.record.FunctionCallCount++
			}
			if t.record.FirstToolCallMS == nil {
				t.record.FirstToolCallMS = durationPointer(now.Sub(t.started))
			}
		}
	}
	if eventType == "response.completed" || eventType == "response.incomplete" || eventType == "response.failed" {
		if response := mapAny(event["response"]); response != nil {
			if value := stringValue(response["model"]); value != "" {
				t.record.Model = value
			}
			observeUsage(t.record, mapAny(response["usage"]))
		}
	}
	switch eventType {
	case "response.completed":
		t.record.ResponseCompletedCount++
	case "response.failed":
		t.record.ResponseFailedCount++
	case "response.incomplete":
		t.record.ResponseIncompleteCount++
	}
}

func (t *requestTelemetry) observeFinalResponse(response map[string]any) {
	if t == nil || response == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if value := stringValue(response["model"]); value != "" {
		t.record.Model = value
	}
	observeUsage(t.record, mapAny(response["usage"]))
}

func (t *requestTelemetry) observeStreamError() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.record.UpstreamStreamError = true
	t.mu.Unlock()
}

func (t *requestTelemetry) observeProviderError(err error) {
	if t == nil || err == nil {
		return
	}
	var providerErr *providerBackendError
	if !errors.As(err, &providerErr) {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if providerErr.UpstreamStatus != 0 {
		status := providerErr.UpstreamStatus
		t.record.UpstreamStatus = &status
	}
	if providerErr.ErrorClass != "" {
		t.record.ErrorClass = safeToken(providerErr.ErrorClass)
	}
	retryable := providerErr.Retryable
	t.record.Retryable = &retryable
}

func observeUsage(record *requestRecord, usage map[string]any) {
	if record == nil || usage == nil {
		return
	}
	record.InputTokens = firstNumberPointer(record.InputTokens, usage["input_tokens"])
	record.OutputTokens = firstNumberPointer(record.OutputTokens, usage["output_tokens"])
	record.TotalTokens = firstNumberPointer(record.TotalTokens, usage["total_tokens"])
	inputDetails := mapAny(usage["input_tokens_details"])
	outputDetails := mapAny(usage["output_tokens_details"])
	record.CachedInputTokens = firstNumberPointer(record.CachedInputTokens, firstNonNil(
		usage["cached_input_tokens"],
		usage["cache_read_tokens"],
		valueFromMap(inputDetails, "cached_tokens"),
	))
	record.CacheWriteTokens = firstNumberPointer(record.CacheWriteTokens, firstNonNil(
		usage["cache_write_tokens"],
		valueFromMap(inputDetails, "cache_write_tokens"),
	))
	record.ReasoningTokens = firstNumberPointer(record.ReasoningTokens, firstNonNil(
		usage["reasoning_tokens"],
		valueFromMap(outputDetails, "reasoning_tokens"),
	))
}

func detectClientType(headers http.Header) string {
	values := []string{headers.Get("X-Client-Name"), headers.Get("X-Codex-Client"), headers.Get("User-Agent")}
	for _, value := range values {
		lower := strings.ToLower(value)
		switch {
		case strings.Contains(lower, "zcode"):
			return "ZCode"
		case strings.Contains(lower, "dsh"):
			return "DSH"
		}
	}
	return "Unknown"
}

func reasoningEvent(eventType string, item map[string]any) bool {
	return stringValue(item["type"]) == "reasoning" || strings.Contains(eventType, "reasoning")
}

func firstOutputEvent(eventType string, event map[string]any) bool {
	if eventType == "response.output_text.delta" {
		return stringValue(event["delta"]) != ""
	}
	if eventType == "response.output_item.done" {
		item := mapAny(event["item"])
		return stringValue(item["type"]) == "message" && messageHasText(item)
	}
	return false
}

func downstreamOutputTextEvent(eventType string, event map[string]any) bool {
	if firstOutputEvent(eventType, event) {
		return true
	}
	if eventType != "chat.completion.chunk" {
		return false
	}
	for _, rawChoice := range sliceAny(event["choices"]) {
		choice := mapAny(rawChoice)
		if choice == nil {
			continue
		}
		delta := mapAny(choice["delta"])
		if stringValue(delta["content"]) != "" || stringValue(choice["text"]) != "" {
			return true
		}
	}
	return false
}

func messageHasText(item map[string]any) bool {
	for _, raw := range sliceAny(item["content"]) {
		part := mapAny(raw)
		if part != nil && part["type"] == "output_text" && stringValue(part["text"]) != "" {
			return true
		}
	}
	return false
}

func isToolEvent(eventType, itemType string) bool {
	if itemType == "function_call" || strings.Contains(itemType, "_tool_call") {
		return true
	}
	return strings.Contains(eventType, "function_call") || strings.Contains(eventType, "tool_call")
}

func encryptedContentPresent(event, item map[string]any) bool {
	return stringValue(event["encrypted_content"]) != "" || stringValue(item["encrypted_content"]) != ""
}

func safeToken(value string) string {
	value = redactSensitive(strings.TrimSpace(value))
	value = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, value)
	if len(value) > 128 {
		return value[:128]
	}
	return value
}

func safeIDHash(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:16]
}

func stringPointer(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func boolPointer(value bool) *bool { return &value }

func durationPointer(value time.Duration) *int64 {
	result := value.Microseconds() / 1000
	if result < 0 {
		result = 0
	}
	return &result
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func numberAsInt(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case json.Number:
		result, err := strconv.Atoi(string(typed))
		return result, err == nil
	case string:
		result, err := strconv.Atoi(typed)
		return result, err == nil
	default:
		return 0, false
	}
}

func numberAsInt64(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return 0, false
		}
		return int64(typed), true
	case json.Number:
		result, err := strconv.ParseInt(string(typed), 10, 64)
		return result, err == nil
	case string:
		result, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return result, err == nil
	default:
		return 0, false
	}
}

func firstNumberPointer(current *int64, values ...any) *int64 {
	if current != nil {
		return current
	}
	for _, value := range values {
		if number, ok := numberAsInt64(value); ok {
			return &number
		}
	}
	return nil
}

func firstNonNil(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func valueFromMap(value map[string]any, key string) any {
	if value == nil {
		return nil
	}
	return value[key]
}

func (b *backend) fetchQuota(ctx context.Context) (map[string]any, error) {
	resp, err := b.doAuthenticated(func(cred credentials) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, quotaURL(b.cfg.BackendURL), nil)
		if err != nil {
			return nil, err
		}
		req.Header = b.headers(cred, false, requestIdentity{})
		return req, nil
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, decodeBackendError(resp)
	}
	var payload map[string]any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return nil, err
	}
	return sanitizeQuotaPayload(payload), nil
}

func quotaURL(baseURL string) string {
	baseURL = strings.TrimRight(baseURL, "/")
	if marker := strings.Index(baseURL, "/backend-api"); marker >= 0 {
		return baseURL[:marker] + "/backend-api/wham/usage"
	}
	return baseURL + "/api/codex/usage"
}

func quotaRoute(baseURL string) string {
	if strings.Contains(baseURL, "/backend-api") {
		return "/wham/usage"
	}
	return "/api/codex/usage"
}

func sanitizeQuotaPayload(payload map[string]any) map[string]any {
	result := map[string]any{"available": true}
	if plan := stringValue(payload["plan_type"]); plan != "" {
		result["plan_type"] = safeToken(plan)
	}
	if reached := quotaReachedType(payload["rate_limit_reached_type"]); reached != "" {
		result["rate_limit_reached_type"] = safeToken(reached)
	}
	if window := quotaWindow(payload, "primary"); window != nil {
		result["primary"] = sanitizeQuotaWindow(window)
	}
	if window := quotaWindow(payload, "secondary"); window != nil {
		result["secondary"] = sanitizeQuotaWindow(window)
	}
	if credits := mapAny(payload["credits"]); credits != nil {
		result["credits"] = sanitizeCredits(credits)
	}
	if limit := quotaIndividualLimit(payload); limit != nil {
		result["individual_limit"] = sanitizeIndividualLimit(limit)
	}
	if rate := mapAny(payload["rate_limit"]); rate != nil {
		if value := stringValue(rate["limit_id"]); value != "" {
			result["limit_id"] = safeToken(value)
		}
	}
	return result
}

func firstNonEmptyString(values ...any) string {
	for _, value := range values {
		if result := stringValue(value); result != "" {
			return result
		}
	}
	return ""
}

func quotaReachedType(value any) string {
	if result, ok := value.(string); ok && result != "" {
		return result
	}
	if object := mapAny(value); object != nil {
		return firstNonEmptyString(object["type"], object["kind"])
	}
	return ""
}

func quotaIndividualLimit(payload map[string]any) map[string]any {
	if limit := firstMap(payload, "individual_limit", "spend_control_limit"); limit != nil {
		return limit
	}
	if spendControl := mapAny(payload["spend_control"]); spendControl != nil {
		if limit := mapAny(spendControl["individual_limit"]); limit != nil {
			return limit
		}
		return spendControl
	}
	return nil
}

func quotaWindow(payload map[string]any, name string) map[string]any {
	roots := []map[string]any{payload, mapAny(payload["rate_limit"]), mapAny(payload["rate_limits"])}
	names := []string{name, name + "_window"}
	for _, root := range roots {
		for _, candidate := range names {
			if value := mapAny(root[candidate]); value != nil {
				return value
			}
		}
	}
	if name == "primary" {
		for _, root := range roots {
			if value := mapAny(root["primary_window"]); value != nil {
				return value
			}
		}
	}
	if name == "secondary" {
		for _, root := range roots {
			if value := mapAny(root["secondary_window"]); value != nil {
				return value
			}
		}
	}
	return nil
}

func sanitizeQuotaWindow(window map[string]any) map[string]any {
	result := map[string]any{}
	if value := firstNonNil(window["used_percent"], window["usedPercent"]); value != nil {
		if number, ok := numberAsFloat(value); ok {
			result["used_percent"] = number
			result["remaining_percent"] = maxFloat(0, 100-number)
		}
	}
	if value := firstNonNil(window["window_duration_mins"], window["window_minutes"]); value != nil {
		if number, ok := numberAsInt64(value); ok {
			result["window_duration_mins"] = number
		}
	} else if value := window["limit_window_seconds"]; value != nil {
		if number, ok := numberAsInt64(value); ok {
			result["window_duration_mins"] = number / 60
		}
	}
	if value := firstNonNil(window["resets_at"], window["reset_at"]); value != nil {
		result["resets_at"] = safeQuotaValue(value)
	}
	return result
}

func sanitizeCredits(value map[string]any) map[string]any {
	result := map[string]any{}
	for _, key := range []string{"has_credits", "unlimited"} {
		if raw, ok := value[key].(bool); ok {
			result[key] = raw
		}
	}
	for _, key := range []string{"balance", "remaining", "used", "limit"} {
		if raw := value[key]; raw != nil {
			result[key] = safeQuotaValue(raw)
		}
	}
	return result
}

func sanitizeIndividualLimit(value map[string]any) map[string]any {
	result := map[string]any{}
	for _, key := range []string{"limit", "used", "remaining", "remaining_percent", "percent_remaining"} {
		if raw := value[key]; raw != nil {
			result[key] = safeQuotaValue(raw)
		}
	}
	if raw := firstNonNil(value["resets_at"], value["reset_at"]); raw != nil {
		result["resets_at"] = safeQuotaValue(raw)
	}
	return result
}

func firstMap(value map[string]any, keys ...string) map[string]any {
	for _, key := range keys {
		if result := mapAny(value[key]); result != nil {
			return result
		}
	}
	return nil
}

func safeQuotaValue(value any) any {
	switch typed := value.(type) {
	case string:
		return safeToken(typed)
	case float64, float32, int, int64, json.Number, bool:
		return typed
	default:
		return nil
	}
}

func numberAsFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, !math.IsNaN(typed) && !math.IsInf(typed, 0)
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		result, err := strconv.ParseFloat(string(typed), 64)
		return result, err == nil
	case string:
		result, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return result, err == nil
	default:
		return 0, false
	}
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

var _ quotaFetcher = (*backend)(nil)
