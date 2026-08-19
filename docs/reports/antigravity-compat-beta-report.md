# Antigravity Compatibility Beta Report

## Scope

This report covers the `agent/antigravity-compat-beta` branch work based on
`e5c69d9c016d0ef5eb61dcf221b9955e267c71ec`. The production
`127.0.0.1:18080` daemon and deployed binary were not stopped, replaced, or
otherwise modified. No OAuth reauthorization was performed, and no
credential, token, prompt, request body, response body, or thought signature
was printed.

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
- Structured output remains deliberately fail-closed on Antigravity. Explicit
  ordinary `text` format is accepted; `json_schema`, `json_object`, and unknown
  formats return an unsupported request error. No structured-output generation
  probe was run.
- The dashboard now has an independent Provider filter for All, Codex,
  Antigravity, and Unknown, including combined source/provider filtering.
- The compatibility matrix and offline fixtures cover normal Responses, normal
  Chat, ZCode tool history, and DSH Chat tool history.

## Image probe boundary

Offline tests cover all five supported MIME types, ordering, serialization,
strict data-URL validation, size limits, and unsupported input rejection.
One allowed internal tiny 1x1 image probe was run using the existing formal
credential path and direct CloudCode endpoint. It completed successfully after
the required control-plane calls. No user image or credential content was used
or exposed. No general live generation test was run.

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
| monitor tox Python environment | CONDITIONAL: the existing Windows contract suite repeatedly timed out on the literal-percent proxy request; the isolated failing test passed on retry |

The Python timeout occurred in an existing local contract path after image/audio
proxy checks, not in the Antigravity protocol tests. It was not fixed by
changing production behavior or by weakening the assertion.

## Commits in this beta batch

- `e9dba43` `feat(antigravity): support inline image inputs`
- `043da93` `test(antigravity): add compatibility fixtures and protocol gates`
- `a20d439` `feat(monitor): add independent provider dashboard filter`
- `2a38305` `test(monitor): accept verified provider models`

Earlier beta commits `504e9da` and `e5c69d9` remain in the branch history.

## Deliberate limitations

This beta does not implement audio/video input, image generation, built-in
tools, MCP, remote image fetching, or structured output generation. These
capabilities remain explicit rejection paths until separately implemented and
verified.

## Verdict

**CONDITIONAL** — the implemented text, tool, error, readiness, model-preset,
inline-image, fixture, and dashboard compatibility gates are ready for offline
review. Structured output is intentionally not enabled or live-verified, and
the existing Windows tox contract timeout remains an environmental test
condition. No production deployment is implied by this branch.
