# Antigravity Compatibility Beta Report

## Scope

This report covers the `agent/antigravity-compat-beta` branch work based on
`e5c69d9c016d0ef5eb61dcf221b9955e267c71ec`. PR #3 remains an open draft
(`agent/antigravity-protocol-fixes` -> `main`) at head
`0676260e41cc782849f76b6d8f135d9cf5518805`; it was not rewritten or merged.
The production `127.0.0.1:18080` daemon and deployed binary were not stopped,
replaced, or otherwise modified. No OAuth reauthorization was performed, and
no credential, token, prompt, request body, response body, or thought
signature was printed.

The branch remains independent of `main`; no merge was performed.

## Implemented compatibility areas

- Gemini 3 thought-signature transport now preserves single, parallel, and
  sequential tool-call grouping. The opaque `agytc2_` envelope carries the
  version, call ID, optional signature, step, part, and group metadata. Legacy
  `agytc_` IDs remain decodable. Missing or unrecoverable signatures fail
  closed.
- Responses and Chat tool-call translation preserve canonical event ordering,
  use a Chat-relative tool index, and keep modern Chat `thought_signature`
  outside `tool_calls[].function`. Legacy compatibility fields remain only on
  the legacy path.
- Upstream CloudCode failures are mapped to typed sanitized provider errors.
  Streaming failures do not emit a success terminator or persist a successful
  response, and no automatic replay is introduced.
- Antigravity readiness is serialized and reports `disabled`, `warming`,
  `ready`, or `degraded`. Listener startup can prewarm control-plane state
  without blocking the HTTP listener.
- The model catalog exposes only the exact verified presets:
  `gemini-3.7-flash-low`, `gemini-3.7-flash-medium`, and
  `gemini-3.7-flash-high`. Requested model IDs are not silently mapped to a
  default. Reasoning effort must match a suffixed preset when supplied.
- Inline image input accepts only strict base64 data URLs for PNG, JPEG, WEBP,
  HEIC, and HEIF. Remote URLs, malformed data URLs, unsupported MIME types,
  non-canonical base64, oversized images, and oversized serialized requests
  fail closed. Unknown content parts and unsupported tool forms are rejected
  rather than silently dropped.
- Structured output now maps `json_object` to
  `generationConfig.responseMimeType=application/json` and maps `json_schema`
  to the same MIME type plus a validated Gemini-supported JSON Schema subset.
  Explicit ordinary `text` format remains normal text; unknown formats,
  unsupported schema keywords, and malformed schemas fail closed. The mapping
  was verified by offline tests and one internal tiny schema probe.
- Function tool parameters likewise accept only the subset currently expressible
  by the CloudCode adapter (`type`, `description`, `properties`, `items`,
  `required`, and string `enum`); unsupported schema keywords are rejected
  before generation instead of being silently discarded.
- The dashboard now has an independent Provider filter for All, Codex,
  Antigravity, and Unknown, including combined source/provider filtering.
- The compatibility matrix and offline fixtures cover normal Responses, normal
  Chat, ZCode tool history, and DSH Chat tool history.
- Public reasoning summary items emitted by the Bridge are safe to replay: they
  are ignored as display metadata rather than being misclassified as Gemini
  private encrypted state. Provider-private encrypted reasoning remains an
  explicit, separate input path.

## Thought-signature transport

| Gemini case | CloudCode history reconstruction | Durable client transport |
| --- | --- | --- |
| Single function call | One model Content and one ordered function-response user Content; the exact part keeps its signature | Legacy-compatible `agytc_` or v2 envelope when grouping metadata is present |
| Parallel function calls | One model Content with ordered functionCall parts, followed by one user Content with ordered functionResponse parts | `agytc2_` carries step ID, part index, group size, and only the signature-bearing part's signature |
| Sequential function calls | Separate model/user Content pairs for each step; all signatures remain attached to their original parts | Each step receives its own v2 step ID; history alone is sufficient after a Bridge restart |

The envelope is deterministic, URL/JSON-safe, rejects malformed metadata, and is
idempotent across Responses -> Chat -> Responses conversion. Modern Chat keeps
the signature in `extra_content.google.thought_signature` and the opaque ID;
it is never added to `tool_calls[].function`.

## Upstream error mapping

| Upstream condition | Bridge result | Retry/replay behavior |
| --- | --- | --- |
| HTTP 400 / 422 | 400 `invalid_request_error`, `provider_invalid_request` | Not retryable; no generation replay |
| HTTP 401 | 401 provider authentication error | No provider replay; existing token-refresh path remains bounded to one retry |
| HTTP 403 | 403 provider permission error | Not retryable |
| HTTP 429 | 429 provider rate-limit error | Retryable metadata only; safe numeric `Retry-After` may be forwarded |
| HTTP 5xx | Sanitized 502/503/504 provider upstream error | Retryable metadata only; no automatic generation replay |
| Network timeout | 504 provider timeout | No automatic replay |
| Other transport failure | 502 provider transport error | No automatic replay |
| Mid-stream failure | SSE error event, immediate end, no `[DONE]`, no successful response-store entry | Never converted into a success |

Provider error telemetry contains only provider, operation, upstream status,
error class, retryable metadata, and timing-safe diagnostics; no upstream body
or authorization material is retained.

## Readiness / cold-start state machine

| State | Meaning | Request behavior |
| --- | --- | --- |
| `disabled` | Antigravity is not configured/enabled | Codex remains available; Antigravity is not selected |
| `warming` | Background or first-request control-plane refresh is in progress | The first Antigravity request synchronously shares the serialized refresh |
| `ready` | Project and exact catalog are available | Generation may proceed after exact model verification |
| `degraded` | Last control-plane/provider refresh failed or expired | Antigravity request fails with typed provider diagnostics; Codex is unaffected |

`loadCodeAssist` and `fetchAvailableModels` share one serialized refresh path.
Prewarm is bounded and does not gate `/healthz` or create a second daemon.

## Compatibility matrix and sources

The authoritative detailed matrix is
[`docs/antigravity-compatibility.md`](../antigravity-compatibility.md). It
covers Responses, Chat Completions, ZCode and DSH fixtures across text,
instructions, history, reasoning, stream/non-stream, single/parallel/
sequential tools, tool outputs, cancellation, usage, images, structured
output, unsupported inputs, and error semantics. Source and Provider remain
independent dimensions; ZCode/DSH labels never alter runtime translation.

## Image probe boundary

Offline tests cover all five supported MIME types, ordering, serialization,
strict data-URL validation, size limits, and unsupported input rejection.
One allowed internal tiny 1x1 image probe and one allowed internal tiny
structured-output probe were run using the existing formal credential path and
direct CloudCode endpoint. Each probe made the two required control-plane calls
and one generation call; six application-level CloudCode calls were made in
total. Both completed successfully. No user image or credential content was
used or exposed, no OAuth flow was performed, and no general live generation
test was run.

## Verification

| Check | Result |
| --- | --- |
| `monitor: go test -count=1 ./...` | PASS |
| `monitor: go vet ./...` | PASS |
| `shared/antigravity: go test -count=1 ./...` | PASS |
| `agy: go test -count=1 ./...` | PASS |
| dashboard JavaScript syntax check | PASS |
| monitor tox lint | PASS |
| monitor tox type | PASS |
| monitor tox Go environment | PASS |
| monitor tox Python environment | PASS for the changed model contract; the existing Windows suite has a known intermittent literal-percent proxy timeout, and the isolated failing test passed on retry |

The Python timeout occurred in an existing local contract path after image/audio
proxy checks, not in the Antigravity protocol tests. It was not fixed by
changing production behavior or by weakening the assertion. It is treated as a
known Windows fake-server condition per the task scope.

## Commits in this beta batch

- `e9dba43` `feat(antigravity): support inline image inputs`
- `043da93` `test(antigravity): add compatibility fixtures and protocol gates`
- `a20d439` `feat(monitor): add independent provider dashboard filter`
- `2a38305` `test(monitor): accept verified provider models`
- `bcb0137` `docs(antigravity): publish compatibility beta report`
- `b70a798` `feat(antigravity): support verified structured output`
- `88f6f66` `docs(antigravity): record structured output verification`
- `6421979` `docs(antigravity): finalize compatibility beta report`

Earlier beta commits `504e9da` and `e5c69d9` remain in the branch history.

## Main files

- `monitor/internal/app/antigravity_provider.go`
- `monitor/internal/app/antigravity_tool_history.go`
- `monitor/internal/app/tool_protocol.go`
- `monitor/internal/app/compat.go`
- `monitor/internal/app/dashboard.go` and embedded dashboard assets
- `monitor/internal/app/testdata/compat/`
- `shared/antigravity/cloudcode/types.go`, `client.go`, and SSE parser
- `docs/antigravity-compatibility.md`
- `docs/reports/antigravity-compat-beta-report.md`

## Deliberate limitations

This beta does not implement audio/video input, image generation, built-in
tools, MCP, remote image fetching, Google Files API, or unsupported structured
output formats. Those capabilities remain explicit rejection paths.

## Verdict

**COMPATIBILITY BETA READY** — the P0 text/tool/error/readiness gates and the
P1 inline-image, exact-preset, structured-output, fixture, and dashboard gates
are implemented and verified within the approved offline/probe boundary. The
known Windows tox fake-server timeout remains documented and is not an
Antigravity regression. No production deployment is implied by this branch.
