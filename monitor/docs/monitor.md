# Codex Bridge Monitor

The monitor is intentionally local and small. It extends the Go runtime with
bounded request telemetry, a read-only quota snapshot, and an embedded
dashboard. It does not add a database, Node process, CDN dependency, or a new
authentication mechanism.

## What is recorded

Only `POST /v1/responses` and `POST /v1/chat/completions` create a request
record. A record contains timing, status/outcome, request/response byte counts,
stream mode, model, reasoning settings, text verbosity, presence-only flags for
`previous_response_id` and `prompt_cache_key`, tool counts/types, usage fields
when the upstream supplies them, and stream/event counters.

The in-memory event timeline is limited by `telemetry.event_memory_limit` and
defaults to 200 metadata-only events. Each event contains direction, relative
time, event type, sequence number, item type, and a short hash of an item ID.
TTFT is measured at the downstream boundary: it is the first non-empty text
delta visible to the client, not merely the first upstream event.
The monitor never stores prompt text, output text, reasoning text, encrypted
reasoning content, tool names, tool arguments/results, authorization headers,
tokens, or arbitrary request/response JSON.

The client type is only classified from an explicit `X-Client-Name`,
`X-Codex-Client`, or recognizable user-agent marker (`ZCode`/`DSH`); otherwise
it is `Unknown`.

## Persistence and fail-open behavior

When enabled, request records are appended asynchronously to:

```text
<state-dir>/telemetry/YYYY-MM-DD.jsonl
<state-dir>/telemetry/quota.jsonl
```

The directory and files are created with user-only permissions where the host
OS supports them. Date-named request files older than
`telemetry.retention_days` (default 30) are deleted only inside the telemetry
directory. The writer queue is bounded by `telemetry.queue_size` (default 256).
If it is full, the record is dropped and the drop counter is exposed in the
dashboard; the API request is never blocked for telemetry.

The server keeps only the most recent 200 records and their bounded event
timelines in memory. The Overview and Requests APIs read retained JSONL files
for the selected time range and overlay that recent in-memory window, so a
30-day query does not load 30 days of records into the browser DOM. Concurrent
dashboard reads are serialized with the JSONL append writer; malformed partial
lines from an interrupted older process are ignored. Set
`OPENAI_VIA_CODEX_TELEMETRY_ENABLED=false` or
`--telemetry-enabled=false` to disable collection and persistence.

## Read-only Codex rollout collector

When telemetry is enabled, the monitor can also import the official Codex local
rollout JSONL files without changing them. The default directories are:

```text
%USERPROFILE%\.codex\sessions
%USERPROFILE%\.codex\archived_sessions
```

The default import window is 30 days and the default discovery/tail interval is
60 seconds. The collector recognizes the observed top-level `session_meta`,
`turn_context`, and `event_msg` envelopes. Within `event_msg` it consumes only
`task_started`, `task_complete`, and `token_count`; other events are ignored and
counted. It tolerates malformed lines, oversized content, an incomplete final
line, file truncation/rewrite, archive moves, and files that are unavailable at
one scan. A byte offset checkpoint and stable hashed rollout/turn identity avoid
re-reading a closed file or creating a second summary after restart.

Codex turns are written separately under:

```text
<state-dir>/telemetry/codex/YYYY-MM-DD.jsonl
<state-dir>/telemetry/codex/checkpoint.json
```

Only model/source labels, hashed identifiers, context-window metadata, timing,
and per-sampling usage are retained. `last_token_usage` is treated as the
per-sampling usage; the cumulative `total_token_usage` snapshot is never used
as turn usage. Prompt, system/developer text, agent/reasoning text, tool
arguments/results, command/file content, OAuth data, and raw rollout/session/
turn IDs are not persisted. Codex records use `source=codex` and
`record_kind=turn`; bridge records use `source=bridge` and
`record_kind=request`, with the client classified as `ZCode`, `DSH`, or
`Unknown`. Collector errors are fail-open and do not affect bridge requests.

The `[codex]` settings are `collector_enabled`, `sessions_dir`,
`archived_sessions_dir`, `import_days`, and `scan_interval`; the equivalent
CLI flags and `OPENAI_VIA_CODEX_CODEX_*` environment variables are supported.

## Dashboard routes

The UI is served at:

```text
GET /dashboard
GET /dashboard/assets/app.js
GET /dashboard/assets/style.css
```

The JSON APIs are:

```text
GET /dashboard/api/overview
GET /dashboard/api/requests?range=today&sort=newest&limit=50&offset=0
GET /dashboard/api/requests/{internal_request_id}
```

Supported request filters include `range` (`1h`, `6h`, `24h`, `7d`, `30d`, or
`today`), `source` (`ZCode`, `Codex`, `DSH`, `Unknown`, or `all`), `model`,
`effort`, `status`, and `endpoint`. Supported sorts are
`newest`, `slowest`, `highest_input`, and `highest_reasoning`; the UI exposes
bounded pagination with 50 mixed request/turn records per page. Detail responses include request
and completion timestamps, HTTP/stream/client metadata, supplied usage fields,
reasoning/tool/event counters, timing fields, and the bounded metadata
timeline. Codex details additionally expose per-sampling usage and collector
identity hashes. Persistence deliberately excludes the bridge timeline. The embedded UI is
Chinese (`lang=zh-CN`), uses `ms` below one second and seconds with two decimal
places at or above one second, and labels a missing first text delta as
`无文本`. On the first Requests page with newest sorting, the list refreshes
about every five seconds while the tab is visible; hidden tabs and later pages
are not auto-refreshed or moved. Cache diagnostics expose only presence flags
and hash prefixes. Request cache hit rate (requests with `cached_tokens > 0`)
is shown separately from token cache ratio (`cached_tokens / input_tokens`).
The table combines input with cached input and output with reasoning; compact
K/M values retain exact values in the detail drawer and title text. A null
usage field remains unavailable rather than being displayed as zero. The
dashboard also exposes Go Heap metrics, bounded store counts, and Codex
collector metrics; these are
runtime diagnostics, not a Windows Working Set measurement.

When `dashboard.enabled=false`, all dashboard paths return `404`. Dashboard
requests are not telemetry request records.

## Quota source and limits

Quota is fetched only when the dashboard overview is requested, no more than
once per 60 seconds, with a 10-second timeout. For the default ChatGPT
`backend-api` base, the endpoint is `GET /wham/usage`; a non-`backend-api` Codex
API base uses `GET /api/codex/usage`. The call reuses the existing Codex
authentication provider and never writes credentials to telemetry.

Only known fields such as plan type, primary/secondary windows, used percent,
reset time, credits, and individual spend-control fields are copied into the
sanitized snapshot. Unknown fields are discarded. A failed or unavailable
quota call leaves the API healthy and reports `unavailable`/`stale` status.
The monitor does not infer plan limits and does not claim exact per-request
quota attribution.

## Reasoning summary

The default is `reasoning.summary_default = "none"`, preserving the existing
protocol behavior. Set it explicitly to `auto` to add the backend's reasoning
summary request when the request already contains a reasoning object:

```toml
[reasoning]
summary_default = "auto"
```

This option is opt-in and does not persist the returned reasoning content.
Telemetry counts reasoning items, readable summaries, raw reasoning text, and
encrypted reasoning independently, so an item event that gains a summary in a
later event is counted correctly without counting the item twice.

## Deterministic tray runtime

The Windows tray controller passes the managed host, port, state directory, PID
file, log file, and (for start) auth path explicitly. Start also passes
`--telemetry-enabled=true`, `--dashboard-enabled=true`, and
`--reasoning-summary-default=none`; stop and status use only the flags their
CLI accepts. The package version remains a single controller configuration
point (`0.2.0`). External `config.toml`, environment variables, and bridge
defaults therefore cannot silently redirect the controller to another local
instance.

The response compatibility store retains one canonical `EffectiveInput` per
stored response and derives the previous-response context when read. This
preserves `previous_response_id`, input-items, GET, and cancel behavior without
retaining the same large input slice twice. The configured store bound is not
changed by the monitor.

## Non-billed validation

The deterministic test suite uses fake backends and synthetic auth fixtures.
For a local executable smoke test, use a separate port and state directory,
then request only health and dashboard assets:

```powershell
go build -trimpath -buildvcs=false -ldflags "-X main.version=0.2.0-monitor.1" -o .\bin\openai-api-server-via-codex.exe .\cmd\openai-api-server-via-codex
& .\bin\openai-api-server-via-codex.exe start --host 127.0.0.1 --port 18180 --state-dir $env:TEMP\codex-bridge-monitor-smoke --pid-file $env:TEMP\codex-bridge-monitor-smoke\server.pid --log-file $env:TEMP\codex-bridge-monitor-smoke\server.log --auth-json $env:USERPROFILE\.codex\auth.json --dashboard-enabled=true --telemetry-enabled=true
Invoke-WebRequest http://127.0.0.1:18180/healthz
Invoke-WebRequest http://127.0.0.1:18180/dashboard
& .\bin\openai-api-server-via-codex.exe stop --host 127.0.0.1 --port 18180 --state-dir $env:TEMP\codex-bridge-monitor-smoke --pid-file $env:TEMP\codex-bridge-monitor-smoke\server.pid --log-file $env:TEMP\codex-bridge-monitor-smoke\server.log
```

On Windows, the upstream daemon's graceful `stop` may return a non-zero
status when its detached console helper refuses a soft tree termination. The
tray controller treats that as the documented fallback trigger and only then
revalidates and terminates the verified bridge process tree. Do not send
`/v1/responses` or `/v1/chat/completions` during this smoke test.
The tray controller's normal `start`, `stop`, and `restart` paths continue to
pass explicit host, port, state, PID, log, and auth arguments so external
`config.toml` or environment changes cannot silently redirect the managed
instance.
