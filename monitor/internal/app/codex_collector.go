package app

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	codexSummaryDirName  = "codex"
	codexCheckpointName  = "checkpoint.json"
	codexMaxLineBytes    = 2 << 20
	codexPrefixHashBytes = 16 << 10
)

// codexSamplingSummary is deliberately limited to usage/timing metadata. It
// never contains the rollout event, prompt, response, tool payload, or raw
// identifiers.
type codexSamplingSummary struct {
	Index             int    `json:"index"`
	InputTokens       *int64 `json:"input_tokens"`
	CachedInputTokens *int64 `json:"cached_input_tokens"`
	CacheWriteTokens  *int64 `json:"cache_write_tokens"`
	OutputTokens      *int64 `json:"output_tokens"`
	ReasoningTokens   *int64 `json:"reasoning_tokens"`
	TotalTokens       *int64 `json:"total_tokens"`
}

type codexUsage struct {
	InputTokens       *int64 `json:"input_tokens"`
	CachedInputTokens *int64 `json:"cached_input_tokens"`
	CacheWriteTokens  *int64 `json:"cache_write_input_tokens"`
	OutputTokens      *int64 `json:"output_tokens"`
	ReasoningTokens   *int64 `json:"reasoning_output_tokens"`
	TotalTokens       *int64 `json:"total_tokens"`
}

type codexSessionMetadata struct {
	RolloutHash     string `json:"rollout_hash"`
	SessionIDHash   string `json:"session_id_hash,omitempty"`
	Source          string `json:"source,omitempty"`
	SecondarySource string `json:"secondary_source,omitempty"`
	ModelProvider   string `json:"model_provider,omitempty"`
	Subagent        *bool  `json:"subagent,omitempty"`
}

type codexTurnContext struct {
	Model           string `json:"model,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

type codexTurnState struct {
	TurnHash        string    `json:"turn_hash"`
	StartedAt       time.Time `json:"started_at"`
	CompletedAt     time.Time `json:"completed_at"`
	Model           string    `json:"model,omitempty"`
	ReasoningEffort string    `json:"reasoning_effort,omitempty"`
	ContextWindow   *int64    `json:"context_window,omitempty"`
	DurationMS      *int64    `json:"duration_ms,omitempty"`
	TTFTMS          *int64    `json:"ttft_ms,omitempty"`
	Completed       bool      `json:"completed"`
	Status          string    `json:"status,omitempty"`

	InputTokens           *int64 `json:"input_tokens,omitempty"`
	CachedInputTokens     *int64 `json:"cached_input_tokens,omitempty"`
	CacheWriteTokens      *int64 `json:"cache_write_tokens,omitempty"`
	OutputTokens          *int64 `json:"output_tokens,omitempty"`
	ReasoningTokens       *int64 `json:"reasoning_tokens,omitempty"`
	TotalTokens           *int64 `json:"total_tokens,omitempty"`
	InputSeen             bool   `json:"input_seen,omitempty"`
	CachedInputSeen       bool   `json:"cached_input_seen,omitempty"`
	CacheWriteSeen        bool   `json:"cache_write_seen,omitempty"`
	OutputSeen            bool   `json:"output_seen,omitempty"`
	ReasoningSeen         bool   `json:"reasoning_seen,omitempty"`
	TotalSeen             bool   `json:"total_seen,omitempty"`
	InputIncomplete       bool   `json:"input_incomplete,omitempty"`
	CachedInputIncomplete bool   `json:"cached_input_incomplete,omitempty"`
	CacheWriteIncomplete  bool   `json:"cache_write_incomplete,omitempty"`
	OutputIncomplete      bool   `json:"output_incomplete,omitempty"`
	ReasoningIncomplete   bool   `json:"reasoning_incomplete,omitempty"`
	TotalIncomplete       bool   `json:"total_incomplete,omitempty"`

	Sampling    []codexSamplingSummary `json:"sampling,omitempty"`
	SummaryHash string                 `json:"summary_hash,omitempty"`
}

type codexFileCheckpoint struct {
	RolloutHash string                      `json:"rollout_hash"`
	PathHash    string                      `json:"path_hash"`
	Offset      int64                       `json:"offset"`
	Size        int64                       `json:"size"`
	ModTime     time.Time                   `json:"mod_time"`
	PrefixHash  string                      `json:"prefix_hash,omitempty"`
	Metadata    codexSessionMetadata        `json:"metadata"`
	Contexts    map[string]codexTurnContext `json:"contexts,omitempty"`
	Current     *codexTurnState             `json:"current,omitempty"`
	Last        *codexTurnState             `json:"last,omitempty"`
	NextSample  int                         `json:"next_sample"`
	UpdatedAt   time.Time                   `json:"updated_at"`
}

type codexCheckpoint struct {
	Version int                             `json:"version"`
	Files   map[string]*codexFileCheckpoint `json:"files"`
}

type codexCollectorMetrics struct {
	trackedFiles       atomic.Int64
	importedTurns      atomic.Uint64
	parseErrors        atomic.Uint64
	unknownEvents      atomic.Uint64
	lastSyncUnixNano   atomic.Int64
	lastEventUnixNano  atomic.Int64
	lastScanDurationMS atomic.Int64
}

type codexCollector struct {
	store          *telemetryStore
	sessionsDir    string
	archivedDir    string
	summaryDir     string
	checkpointPath string
	importDays     int
	interval       time.Duration

	stop       chan struct{}
	wg         sync.WaitGroup
	scanMu     sync.Mutex
	stateMu    sync.Mutex
	checkpoint codexCheckpoint
	metrics    codexCollectorMetrics
}

type codexEnvelope struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"`
}

type codexSessionMetaPayload struct {
	ID            string          `json:"id"`
	SessionID     string          `json:"session_id"`
	Source        json.RawMessage `json:"source"`
	ThreadSource  json.RawMessage `json:"thread_source"`
	ModelProvider string          `json:"model_provider"`
}

type codexTurnContextPayload struct {
	TurnID            string `json:"turn_id"`
	Model             string `json:"model"`
	CollaborationMode struct {
		Settings struct {
			Model           string `json:"model"`
			ReasoningEffort string `json:"reasoning_effort"`
		} `json:"settings"`
	} `json:"collaboration_mode"`
}

type codexEventMessagePayload struct {
	Type               string          `json:"type"`
	TurnID             string          `json:"turn_id"`
	ThreadID           string          `json:"thread_id"`
	Model              string          `json:"model"`
	ModelContextWindow *int64          `json:"model_context_window"`
	StartedAt          json.RawMessage `json:"started_at"`
	CompletedAt        json.RawMessage `json:"completed_at"`
	StartedAtMS        *int64          `json:"started_at_ms"`
	CompletedAtMS      *int64          `json:"completed_at_ms"`
	DurationMS         *int64          `json:"duration_ms"`
	TimeToFirstTokenMS *int64          `json:"time_to_first_token_ms"`
	Status             string          `json:"status"`
	LastAgentMessage   json.RawMessage `json:"last_agent_message"`
	Info               struct {
		LastTokenUsage  codexUsage `json:"last_token_usage"`
		TotalTokenUsage codexUsage `json:"total_token_usage"`
	} `json:"info"`
}

type codexLineProcessor struct {
	collector *codexCollector
	entry     *codexFileCheckpoint
	offset    int64
	dirty     map[string]*codexTurnState
	lastEvent time.Time
}

func newCodexCollector(store *telemetryStore) *codexCollector {
	if store == nil {
		return nil
	}
	interval := store.cfg.CodexScanInterval
	if interval <= 0 {
		interval = defaultCodexScanInterval
	}
	importDays := store.cfg.CodexImportDays
	if importDays < 1 {
		importDays = defaultCodexImportDays
	}
	summaryDir := filepath.Join(store.telemetryDir, codexSummaryDirName)
	collector := &codexCollector{
		store:          store,
		sessionsDir:    expandHome(store.cfg.CodexSessionsDir),
		archivedDir:    expandHome(store.cfg.CodexArchivedSessionsDir),
		summaryDir:     summaryDir,
		checkpointPath: filepath.Join(summaryDir, codexCheckpointName),
		importDays:     importDays,
		interval:       interval,
		stop:           make(chan struct{}),
		checkpoint:     codexCheckpoint{Version: 1, Files: map[string]*codexFileCheckpoint{}},
	}
	collector.loadCheckpoint()
	return collector
}

func (c *codexCollector) start() {
	if c == nil || (c.sessionsDir == "" && c.archivedDir == "") {
		return
	}
	c.wg.Add(1)
	go c.loop()
}

func (c *codexCollector) stopCollector() {
	if c == nil {
		return
	}
	select {
	case <-c.stop:
	default:
		close(c.stop)
	}
	c.wg.Wait()
}

func (c *codexCollector) loop() {
	defer c.wg.Done()
	c.scanOnce()
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			c.scanOnce()
		case <-c.stop:
			return
		}
	}
}

func (c *codexCollector) scanOnce() {
	if c == nil {
		return
	}
	c.scanMu.Lock()
	defer c.scanMu.Unlock()
	started := time.Now()
	files := c.discoverFiles()
	c.metrics.trackedFiles.Store(int64(len(files)))
	if err := os.MkdirAll(c.summaryDir, 0700); err != nil {
		c.metrics.parseErrors.Add(1)
		c.logError("summary_directory_unavailable")
		return
	}
	for _, path := range files {
		if err := c.processFile(path); err != nil {
			c.metrics.parseErrors.Add(1)
			c.logError("rollout_scan_failed")
		}
	}
	c.cleanupRetention()
	c.saveCheckpoint()
	c.metrics.lastSyncUnixNano.Store(time.Now().UTC().UnixNano())
	c.metrics.lastScanDurationMS.Store(time.Since(started).Milliseconds())
}

func (c *codexCollector) logError(code string) {
	log.Printf("codex.collector.error code=%s", safeToken(code))
}

func (c *codexCollector) discoverFiles() []string {
	cutoff := time.Now().AddDate(0, 0, -c.importDays)
	seen := map[string]struct{}{}
	var result []string
	for _, root := range []string{c.sessionsDir, c.archivedDir} {
		if strings.TrimSpace(root) == "" {
			continue
		}
		_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry == nil {
				return nil
			}
			if entry.IsDir() {
				return nil
			}
			name := entry.Name()
			if !strings.HasPrefix(name, "rollout-") || !strings.HasSuffix(strings.ToLower(name), ".jsonl") {
				return nil
			}
			absolute, err := filepath.Abs(path)
			if err != nil {
				return nil
			}
			if _, ok := seen[absolute]; ok {
				return nil
			}
			known := c.knownPath(absolute)
			if !known {
				when, ok := rolloutTimeFromName(name)
				if !ok {
					if info, statErr := entry.Info(); statErr == nil {
						when = info.ModTime()
					}
				}
				if !when.IsZero() && when.Before(cutoff) {
					return nil
				}
			}
			seen[absolute] = struct{}{}
			result = append(result, absolute)
			return nil
		})
	}
	sort.Strings(result)
	return result
}

func (c *codexCollector) knownPath(path string) bool {
	hash := pathHash(path)
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	for _, entry := range c.checkpoint.Files {
		if entry != nil && entry.PathHash == hash {
			return true
		}
	}
	return false
}

func (c *codexCollector) processFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	prefixHash, err := filePrefixHash(path)
	if err != nil {
		return err
	}
	metadata, err := inspectRolloutMetadata(path, prefixHash)
	if err != nil {
		return err
	}
	key := metadata.RolloutHash
	if key == "" {
		key = pathHash(path)
		metadata.RolloutHash = key
	}
	c.stateMu.Lock()
	entry := c.checkpoint.Files[key]
	if entry == nil {
		for candidateKey, candidate := range c.checkpoint.Files {
			if candidate != nil && candidate.PathHash == pathHash(path) {
				entry = candidate
				key = candidateKey
				break
			}
		}
	}
	if entry == nil {
		entry = &codexFileCheckpoint{RolloutHash: key, Contexts: map[string]codexTurnContext{}}
		c.checkpoint.Files[key] = entry
	}
	if entry.Contexts == nil {
		entry.Contexts = map[string]codexTurnContext{}
	}
	previousSize := entry.Size
	previousModTime := entry.ModTime
	rewritten := entry.Offset > 0 && previousSize == info.Size() && !previousModTime.IsZero() && !previousModTime.Equal(info.ModTime())
	if entry.PrefixHash != "" && entry.PrefixHash != prefixHash || entry.Offset > info.Size() || rewritten {
		entry.Offset = 0
		entry.Current = nil
		entry.Last = nil
		entry.Contexts = map[string]codexTurnContext{}
		entry.NextSample = 0
	}
	entry.RolloutHash = key
	entry.PathHash = pathHash(path)
	entry.Size = info.Size()
	entry.ModTime = info.ModTime()
	entry.PrefixHash = prefixHash
	entry.Metadata = mergeCodexMetadata(entry.Metadata, metadata)
	startOffset := entry.Offset
	c.stateMu.Unlock()

	if startOffset == info.Size() {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Seek(startOffset, io.SeekStart); err != nil {
		return err
	}
	processor := &codexLineProcessor{collector: c, entry: entry, offset: startOffset, dirty: map[string]*codexTurnState{}}
	if err := processor.read(file); err != nil {
		return err
	}
	entry.Offset = processor.offset
	entry.Size = info.Size()
	entry.ModTime = info.ModTime()
	entry.UpdatedAt = time.Now().UTC()
	if !processor.lastEvent.IsZero() {
		c.metrics.lastEventUnixNano.Store(processor.lastEvent.UTC().UnixNano())
	}
	for _, turn := range processor.dirty {
		if err := c.writeTurn(entry.Metadata, turn); err != nil {
			c.metrics.parseErrors.Add(1)
			c.logError("summary_write_failed")
		}
	}
	c.stateMu.Lock()
	c.checkpoint.Files[key] = entry
	c.stateMu.Unlock()
	return nil
}

func (p *codexLineProcessor) read(reader io.Reader) error {
	buffered := bufio.NewReaderSize(reader, 64*1024)
	var line []byte
	lineBytes := int64(0)
	tooLarge := false
	for {
		part, err := buffered.ReadSlice('\n')
		lineBytes += int64(len(part))
		if !tooLarge {
			if len(line)+len(part) > codexMaxLineBytes {
				tooLarge = true
				line = nil
			} else {
				line = append(line, part...)
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err == io.EOF {
			// Codex appends JSONL records atomically but the last write can be
			// observed without its newline. Keep the offset at the beginning
			// of that line so the next scan can finish it.
			return nil
		}
		if err != nil {
			return err
		}
		p.offset += lineBytes
		if tooLarge {
			p.collector.metrics.parseErrors.Add(1)
		} else {
			p.handleLine(bytesTrimLine(line))
		}
		line = nil
		lineBytes = 0
		tooLarge = false
	}
}

func bytesTrimLine(value []byte) []byte {
	value = bytes.TrimSuffix(value, []byte{'\n'})
	value = bytes.TrimSuffix(value, []byte{'\r'})
	return value
}

func (p *codexLineProcessor) handleLine(line []byte) {
	if len(strings.TrimSpace(string(line))) == 0 {
		return
	}
	var envelope codexEnvelope
	if err := json.Unmarshal(line, &envelope); err != nil {
		p.collector.metrics.parseErrors.Add(1)
		return
	}
	if envelope.Payload == nil {
		p.collector.metrics.unknownEvents.Add(1)
		return
	}
	switch envelope.Type {
	case "session_meta":
		var payload codexSessionMetaPayload
		if json.Unmarshal(envelope.Payload, &payload) == nil {
			metadata := sessionMetadataFromPayload(payload)
			p.entry.Metadata = mergeCodexMetadata(p.entry.Metadata, metadata)
		}
	case "turn_context":
		var payload codexTurnContextPayload
		if json.Unmarshal(envelope.Payload, &payload) == nil {
			turnHash := hashID(payload.TurnID)
			if turnHash != "" {
				if p.entry.Contexts == nil {
					p.entry.Contexts = map[string]codexTurnContext{}
				}
				context := codexTurnContext{Model: payload.Model, ReasoningEffort: payload.CollaborationMode.Settings.ReasoningEffort}
				if context.Model == "" {
					context.Model = payload.CollaborationMode.Settings.Model
				}
				p.entry.Contexts[turnHash] = context
			}
		}
	case "event_msg":
		var payload codexEventMessagePayload
		if json.Unmarshal(envelope.Payload, &payload) != nil {
			p.collector.metrics.parseErrors.Add(1)
			return
		}
		p.lastEvent = envelopeTime(envelope.Timestamp)
		switch payload.Type {
		case "task_started":
			p.taskStarted(payload, envelope.Timestamp)
		case "task_complete":
			p.taskComplete(payload, envelope.Timestamp)
		case "token_count":
			p.tokenCount(payload)
		default:
			p.collector.metrics.unknownEvents.Add(1)
		}
	default:
		p.collector.metrics.unknownEvents.Add(1)
	}
}

func (p *codexLineProcessor) taskStarted(payload codexEventMessagePayload, envelopeTimestamp string) {
	turnHash := hashID(payload.TurnID)
	if turnHash == "" {
		p.collector.metrics.unknownEvents.Add(1)
		return
	}
	if p.entry.Current != nil && p.entry.Current.Completed {
		p.entry.Last = p.entry.Current
	}
	context := p.entry.Contexts[turnHash]
	started := parseCodexTime(payload.StartedAt, payload.StartedAtMS, envelopeTimestamp)
	p.entry.Current = &codexTurnState{
		TurnHash:        turnHash,
		StartedAt:       started,
		Model:           firstNonEmpty(payload.Model, context.Model),
		ReasoningEffort: context.ReasoningEffort,
		ContextWindow:   cloneInt64(payload.ModelContextWindow),
		Status:          "running",
	}
}

func (p *codexLineProcessor) taskComplete(payload codexEventMessagePayload, envelopeTimestamp string) {
	turnHash := hashID(payload.TurnID)
	if turnHash == "" {
		p.collector.metrics.unknownEvents.Add(1)
		return
	}
	turn := p.entry.Current
	if turn == nil || turn.TurnHash != turnHash {
		context := p.entry.Contexts[turnHash]
		turn = &codexTurnState{TurnHash: turnHash, Model: context.Model, ReasoningEffort: context.ReasoningEffort}
		p.entry.Current = turn
	}
	if turn.StartedAt.IsZero() {
		turn.StartedAt = parseCodexTime(payload.StartedAt, payload.StartedAtMS, envelopeTimestamp)
	}
	turn.CompletedAt = parseCodexTime(payload.CompletedAt, payload.CompletedAtMS, envelopeTimestamp)
	if turn.CompletedAt.IsZero() {
		turn.CompletedAt = time.Now().UTC()
	}
	turn.DurationMS = cloneInt64(payload.DurationMS)
	turn.TTFTMS = cloneInt64(payload.TimeToFirstTokenMS)
	turn.Completed = true
	turn.Status = firstNonEmpty(payload.Status, "success")
	p.dirty[turn.TurnHash] = turn
}

func (p *codexLineProcessor) tokenCount(payload codexEventMessagePayload) {
	turn := p.entry.Current
	if turn == nil {
		turn = p.entry.Last
	}
	if turn == nil {
		p.collector.metrics.unknownEvents.Add(1)
		return
	}
	usage := normalizeCodexUsage(payload.Info.LastTokenUsage)
	if usage == nil {
		return
	}
	p.entry.NextSample++
	turn.Sampling = append(turn.Sampling, codexSamplingSummary{
		Index:             p.entry.NextSample,
		InputTokens:       cloneInt64(usage.InputTokens),
		CachedInputTokens: cloneInt64(usage.CachedInputTokens),
		CacheWriteTokens:  cloneInt64(usage.CacheWriteTokens),
		OutputTokens:      cloneInt64(usage.OutputTokens),
		ReasoningTokens:   cloneInt64(usage.ReasoningTokens),
		TotalTokens:       cloneInt64(usage.TotalTokens),
	})
	mergeCodexTurnUsage(turn, usage)
	if turn.Completed {
		p.dirty[turn.TurnHash] = turn
	}
}

func normalizeCodexUsage(usage codexUsage) *codexUsage {
	if usage.InputTokens == nil && usage.CachedInputTokens == nil && usage.CacheWriteTokens == nil && usage.OutputTokens == nil && usage.ReasoningTokens == nil && usage.TotalTokens == nil {
		return nil
	}
	// The first token_count snapshot observed in the current schema can carry
	// only total_tokens while serializing zeroes for the component fields. Do
	// not turn those placeholder zeroes into real usage.
	if usage.TotalTokens != nil && *usage.TotalTokens > 0 && numberValue(usage.InputTokens) == 0 && numberValue(usage.CachedInputTokens) == 0 && numberValue(usage.CacheWriteTokens) == 0 && numberValue(usage.OutputTokens) == 0 && numberValue(usage.ReasoningTokens) == 0 {
		usage.InputTokens = nil
		usage.CachedInputTokens = nil
		usage.CacheWriteTokens = nil
		usage.OutputTokens = nil
		usage.ReasoningTokens = nil
	}
	return &usage
}

func mergeCodexTurnUsage(turn *codexTurnState, usage *codexUsage) {
	if turn == nil || usage == nil {
		return
	}
	turn.InputTokens, turn.InputSeen, turn.InputIncomplete = mergeNullable(turn.InputTokens, turn.InputSeen, turn.InputIncomplete, usage.InputTokens)
	turn.CachedInputTokens, turn.CachedInputSeen, turn.CachedInputIncomplete = mergeNullable(turn.CachedInputTokens, turn.CachedInputSeen, turn.CachedInputIncomplete, usage.CachedInputTokens)
	turn.CacheWriteTokens, turn.CacheWriteSeen, turn.CacheWriteIncomplete = mergeNullable(turn.CacheWriteTokens, turn.CacheWriteSeen, turn.CacheWriteIncomplete, usage.CacheWriteTokens)
	turn.OutputTokens, turn.OutputSeen, turn.OutputIncomplete = mergeNullable(turn.OutputTokens, turn.OutputSeen, turn.OutputIncomplete, usage.OutputTokens)
	turn.ReasoningTokens, turn.ReasoningSeen, turn.ReasoningIncomplete = mergeNullable(turn.ReasoningTokens, turn.ReasoningSeen, turn.ReasoningIncomplete, usage.ReasoningTokens)
	turn.TotalTokens, turn.TotalSeen, turn.TotalIncomplete = mergeNullable(turn.TotalTokens, turn.TotalSeen, turn.TotalIncomplete, usage.TotalTokens)
}

func mergeNullable(current *int64, seen, incomplete bool, next *int64) (*int64, bool, bool) {
	seen = true
	if next == nil {
		return nil, seen, true
	}
	if incomplete {
		return nil, seen, true
	}
	value := numberValue(current) + *next
	return &value, seen, false
}

func (c *codexCollector) writeTurn(metadata codexSessionMetadata, turn *codexTurnState) error {
	if turn == nil || !turn.Completed {
		return nil
	}
	record := codexRecordFromTurn(metadata, turn)
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	fingerprintBytes := sha256.Sum256(data)
	fingerprint := hex.EncodeToString(fingerprintBytes[:])[:16]
	if turn.SummaryHash == fingerprint {
		return nil
	}
	when := record.CompletedAt
	if when.IsZero() {
		when = record.StartedAt
	}
	if when.IsZero() {
		when = time.Now().UTC()
	}
	name := filepath.Join(c.summaryDir, when.Local().Format(telemetryDateLayout)+".jsonl")
	c.store.historyMu.Lock()
	defer c.store.historyMu.Unlock()
	if codexSummaryExists(name, record.InternalRequestID) {
		turn.SummaryHash = fingerprint
		return nil
	}
	file, err := os.OpenFile(name, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(append(data, '\n'))
	_ = file.Close()
	if writeErr != nil {
		return writeErr
	}
	turn.SummaryHash = fingerprint
	c.metrics.importedTurns.Add(1)
	return nil
}

func codexSummaryExists(name, id string) bool {
	if strings.TrimSpace(id) == "" {
		return false
	}
	file, err := os.Open(name)
	if err != nil {
		return false
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), maxTelemetryLineBytes)
	for scanner.Scan() {
		var record requestRecord
		if json.Unmarshal(scanner.Bytes(), &record) == nil && record.InternalRequestID == id {
			return true
		}
	}
	return false
}

func codexRecordFromTurn(metadata codexSessionMetadata, turn *codexTurnState) *requestRecord {
	record := &requestRecord{
		InternalRequestID:        "codex-" + turn.TurnHash,
		StartedAt:                turn.StartedAt,
		CompletedAt:              turn.CompletedAt,
		Outcome:                  "success",
		Source:                   "codex",
		RecordKind:               "turn",
		ClientType:               "Codex",
		SecondarySource:          metadata.SecondarySource,
		Model:                    turn.Model,
		RequestedReasoningEffort: stringPointerOrNil(turn.ReasoningEffort),
		ContextWindow:            cloneInt64(turn.ContextWindow),
		InputTokens:              nullableAggregate(turn.InputTokens, turn.InputSeen, turn.InputIncomplete),
		CachedInputTokens:        nullableAggregate(turn.CachedInputTokens, turn.CachedInputSeen, turn.CachedInputIncomplete),
		CacheWriteTokens:         nullableAggregate(turn.CacheWriteTokens, turn.CacheWriteSeen, turn.CacheWriteIncomplete),
		OutputTokens:             nullableAggregate(turn.OutputTokens, turn.OutputSeen, turn.OutputIncomplete),
		ReasoningTokens:          nullableAggregate(turn.ReasoningTokens, turn.ReasoningSeen, turn.ReasoningIncomplete),
		TotalTokens:              nullableAggregate(turn.TotalTokens, turn.TotalSeen, turn.TotalIncomplete),
		RequestDurationMS:        numberValue(turn.DurationMS),
		DurationAvailable:        turn.DurationMS != nil,
		TTFTMS:                   cloneInt64(turn.TTFTMS),
		RolloutIDHash:            metadata.RolloutHash,
		SessionIDHash:            metadata.SessionIDHash,
		TurnIDHash:               turn.TurnHash,
		Subagent:                 cloneBool(metadata.Subagent),
		SamplingCount:            len(turn.Sampling),
		Sampling:                 append([]codexSamplingSummary(nil), turn.Sampling...),
	}
	if record.CompletedAt.IsZero() {
		record.CompletedAt = record.StartedAt
	}
	return record
}

func nullableAggregate(value *int64, seen, incomplete bool) *int64 {
	if !seen || incomplete || value == nil {
		return nil
	}
	return cloneInt64(value)
}

func (c *codexCollector) loadCheckpoint() {
	data, err := os.ReadFile(c.checkpointPath)
	if os.IsNotExist(err) {
		return
	}
	if err != nil || json.Unmarshal(data, &c.checkpoint) != nil || c.checkpoint.Version != 1 || c.checkpoint.Files == nil {
		c.metrics.parseErrors.Add(1)
		c.checkpoint = codexCheckpoint{Version: 1, Files: map[string]*codexFileCheckpoint{}}
		c.logError("checkpoint_unavailable")
	}
}

func (c *codexCollector) saveCheckpoint() {
	c.stateMu.Lock()
	data, err := json.Marshal(c.checkpoint)
	c.stateMu.Unlock()
	if err != nil {
		c.metrics.parseErrors.Add(1)
		c.logError("checkpoint_encode_failed")
		return
	}
	if err := os.MkdirAll(c.summaryDir, 0700); err != nil {
		c.metrics.parseErrors.Add(1)
		c.logError("checkpoint_directory_unavailable")
		return
	}
	temporary := c.checkpointPath + ".tmp"
	if err := os.WriteFile(temporary, data, 0600); err != nil {
		c.metrics.parseErrors.Add(1)
		c.logError("checkpoint_write_failed")
		return
	}
	if err := os.Remove(c.checkpointPath); err != nil && !os.IsNotExist(err) {
		_ = os.Remove(temporary)
		c.metrics.parseErrors.Add(1)
		c.logError("checkpoint_replace_failed")
		return
	}
	if err := os.Rename(temporary, c.checkpointPath); err != nil {
		_ = os.Remove(temporary)
		c.metrics.parseErrors.Add(1)
		c.logError("checkpoint_rename_failed")
	}
}

func (c *codexCollector) cleanupRetention() {
	c.store.historyMu.Lock()
	defer c.store.historyMu.Unlock()
	cutoff := time.Now().Local().AddDate(0, 0, -c.importDays)
	files, _ := filepath.Glob(filepath.Join(c.summaryDir, "*.jsonl"))
	for _, name := range files {
		if filepath.Base(name) == codexCheckpointName {
			continue
		}
		date, err := time.ParseInLocation(telemetryDateLayout, strings.TrimSuffix(filepath.Base(name), ".jsonl"), time.Local)
		if err == nil && date.AddDate(0, 0, 1).Before(cutoff) {
			_ = os.Remove(name)
		}
	}
}

func (c *codexCollector) snapshot() map[string]any {
	if c == nil {
		return map[string]any{"enabled": false}
	}
	result := map[string]any{
		"enabled":                true,
		"tracked_files":          c.metrics.trackedFiles.Load(),
		"imported_turns":         c.metrics.importedTurns.Load(),
		"parse_errors":           c.metrics.parseErrors.Load(),
		"unknown_events_ignored": c.metrics.unknownEvents.Load(),
		"last_scan_duration_ms":  c.metrics.lastScanDurationMS.Load(),
	}
	if value := c.metrics.lastSyncUnixNano.Load(); value > 0 {
		result["last_sync"] = time.Unix(0, value).UTC()
	}
	if value := c.metrics.lastEventUnixNano.Load(); value > 0 {
		lag := time.Since(time.Unix(0, value))
		if lag < 0 {
			lag = 0
		}
		result["collector_lag_ms"] = lag.Milliseconds()
	}
	return result
}

func inspectRolloutMetadata(path, prefixHash string) (codexSessionMetadata, error) {
	metadata := codexSessionMetadata{RolloutHash: prefixHash}
	file, err := os.Open(path)
	if err != nil {
		return metadata, err
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 64*1024)
	for lineNumber := 0; lineNumber < 128; lineNumber++ {
		line, err := reader.ReadString('\n')
		if len(line) > codexMaxLineBytes {
			return metadata, nil
		}
		if len(line) > 0 {
			var envelope codexEnvelope
			if json.Unmarshal([]byte(strings.TrimSpace(line)), &envelope) == nil && envelope.Type == "session_meta" {
				var payload codexSessionMetaPayload
				if json.Unmarshal(envelope.Payload, &payload) == nil {
					metadata = mergeCodexMetadata(metadata, sessionMetadataFromPayload(payload))
				}
				return metadata, nil
			}
		}
		if err != nil {
			if err == io.EOF {
				return metadata, nil
			}
			return metadata, err
		}
	}
	return metadata, nil
}

func sessionMetadataFromPayload(payload codexSessionMetaPayload) codexSessionMetadata {
	identifier := firstNonEmpty(payload.SessionID, payload.ID)
	source := sourceLabel(payload.Source)
	threadSource := sourceLabel(payload.ThreadSource)
	secondary := source
	if secondary == "" || secondary == "unknown" {
		secondary = threadSource
	}
	subagent := source == "subagent" || threadSource == "subagent"
	var subagentPointer *bool
	if source != "" || threadSource != "" {
		subagentPointer = &subagent
	}
	return codexSessionMetadata{
		RolloutHash:     hashID(identifier),
		SessionIDHash:   hashID(identifier),
		Source:          safeToken(source),
		SecondarySource: safeToken(secondary),
		ModelProvider:   safeToken(payload.ModelProvider),
		Subagent:        subagentPointer,
	}
}

func mergeCodexMetadata(existing, incoming codexSessionMetadata) codexSessionMetadata {
	if incoming.RolloutHash != "" {
		existing.RolloutHash = incoming.RolloutHash
	}
	if incoming.SessionIDHash != "" {
		existing.SessionIDHash = incoming.SessionIDHash
	}
	if incoming.Source != "" {
		existing.Source = incoming.Source
	}
	if incoming.SecondarySource != "" {
		existing.SecondarySource = incoming.SecondarySource
	}
	if incoming.ModelProvider != "" {
		existing.ModelProvider = incoming.ModelProvider
	}
	if incoming.Subagent != nil {
		existing.Subagent = cloneBool(incoming.Subagent)
	}
	return existing
}

func sourceLabel(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return strings.TrimSpace(value)
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) == nil {
		if _, ok := object["subagent"]; ok {
			return "subagent"
		}
	}
	return "unknown"
}

func rolloutTimeFromName(name string) (time.Time, bool) {
	name = strings.TrimPrefix(name, "rollout-")
	if len(name) < len("2006-01-02T15-04-05") {
		return time.Time{}, false
	}
	value, err := time.ParseInLocation("2006-01-02T15-04-05", name[:len("2006-01-02T15-04-05")], time.Local)
	return value, err == nil
}

func parseCodexTime(raw json.RawMessage, milliseconds *int64, fallback string) time.Time {
	if len(raw) > 0 && string(raw) != "null" {
		var value string
		if json.Unmarshal(raw, &value) == nil {
			if parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value)); err == nil {
				return parsed.UTC()
			}
		}
		var number float64
		if json.Unmarshal(raw, &number) == nil && number > 0 {
			if number >= 1e11 {
				return time.UnixMilli(int64(number)).UTC()
			}
			seconds := int64(number)
			nanos := int64((number - float64(seconds)) * float64(time.Second))
			return time.Unix(seconds, nanos).UTC()
		}
	}
	if milliseconds != nil && *milliseconds > 0 {
		return time.UnixMilli(*milliseconds).UTC()
	}
	if strings.TrimSpace(fallback) != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(fallback)); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}

func envelopeTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}

func filePrefixHash(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.CopyN(hasher, file, codexPrefixHashBytes); err != nil && err != io.EOF {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil))[:16], nil
}

func pathHash(path string) string {
	clean, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		clean = filepath.Clean(path)
	}
	return hashID(strings.ToLower(filepath.ToSlash(clean)))
}

func hashID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	hash := sha256.Sum256([]byte(value))
	return hex.EncodeToString(hash[:])[:16]
}

func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func stringPointerOrNil(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return stringPointer(value)
}

func numberValue(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
