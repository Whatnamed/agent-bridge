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

The server keeps only the most recent 200 records in memory for the dashboard.
On restart, recent JSONL records are loaded back into that bounded view. Set
`OPENAI_VIA_CODEX_TELEMETRY_ENABLED=false` or
`--telemetry-enabled=false` to disable collection and persistence.

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
`today`), `model`, `effort`, `status`, and `endpoint`. Supported sorts are
`newest`, `slowest`, `highest_input`, and `highest_reasoning`. Detail responses
include the bounded metadata timeline; persistence deliberately excludes that
timeline.

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
