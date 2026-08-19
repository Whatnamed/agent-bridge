# Antigravity Compatibility Beta

This document describes the provider-neutral compatibility boundary between
OpenAI-compatible Requests/Chat Completions clients and the Direct OAuth
Antigravity provider. The `source` labels in fixtures are test/dashboard
metadata only; runtime translation never branches on ZCode or DSH.

## Compatibility matrix

| Capability | Responses | Chat Completions | ZCode fixture | DSH fixture | Strategy |
| --- | --- | --- | --- | --- | --- |
| Normal text | yes | yes | yes | yes | Translate to user/model text parts |
| System/developer instructions | yes | yes | yes | yes | Translate to `systemInstruction` text |
| Multi-turn history | yes | yes | yes | yes | Reconstruct canonical history from request data |
| Reasoning summaries | yes | yes | yes | yes | Replay-safe public summaries are ignored; supplied private encrypted content is forwarded explicitly |
| Streaming | yes | yes | yes | yes | Canonical Responses events, then Chat chunks |
| Non-streaming | yes | yes | yes | yes | Collect the same canonical stream |
| Single function call | yes | yes | yes | yes | Opaque call ID carries the signature when needed |
| Parallel function calls | yes | yes | offline fixture | offline fixture | One model Content and one ordered user Content; transport v2 carries position/group |
| Sequential tool steps | yes | yes | offline fixture | offline fixture | Each model/user step remains separate |
| Tool outputs | yes | yes | yes | yes | Order and name must match the originating call group |
| Cancellation | existing server contract | existing server contract | covered by shared server tests | covered by shared server tests | No provider-specific retry or replay |
| Usage/cache/reasoning tokens | yes | yes | telemetry fixture | telemetry fixture | Metadata only; no prompt/output/reasoning text |
| Inline image data | offline serializer | offline serializer | fixture-ready | fixture-ready | `data:` URL only; five image MIME types; one internal tiny smoke passed |
| Structured output | JSON object/schema | JSON object/schema | fixture-ready | fixture-ready | `generationConfig.responseMimeType/responseSchema`; one internal schema smoke passed |
| Unsupported content/tool types | 400 | 400 | fail closed | fail closed | Never silently discard semantic input |
| Error status/retry semantics | yes | yes | yes | yes | Typed provider mapping; no generation replay |

## Request policy

The adapter classifies request fields into three categories:

1. Correct translation: fields that affect model semantics and have an
   implemented CloudCode mapping, such as messages, tools, tool choice,
   reasoning presets, and inline image data.
2. Safe ignore: client metadata that does not change generation semantics and
   is not part of the CloudCode request. It may still be represented in
   privacy-safe telemetry.
3. Explicit rejection: semantically important fields that this beta cannot
   guarantee, including remote image URLs, files, unknown content parts,
   non-function tools, unsupported structured-output formats, and
   `parallel_tool_calls=false`.

## Tool continuation

The legacy `agytc_` envelope remains decodable. New grouped calls use the
URL-safe `agytc2_` envelope with version, original call ID, optional signature,
step ID, part index, and group size. The envelope is carried in the opaque
tool-call ID, so reconstructing a request after a Bridge restart does not
depend on an in-memory map. Invalid or incomplete metadata is rejected with a
4xx response.

## Verification status

The compatibility fixtures and offline tests are authoritative for protocol
shape. One tiny synthetic image probe and one tiny synthetic structured-output
probe were run against the internal endpoint using the existing credential;
neither used a user-provided image or exposed credentials. No OAuth flow was
performed and no general live generation was run.

Protocol references used during implementation:

- [Gemini 3 developer guide](https://ai.google.dev/gemini-api/docs/generate-content/gemini-3)
- [Thought signatures](https://ai.google.dev/gemini-api/docs/thought-signatures)
- [Image understanding](https://ai.google.dev/gemini-api/docs/generate-content/image-understanding)
- [Structured outputs](https://ai.google.dev/gemini-api/docs/structured-output)
- [Function calling](https://ai.google.dev/gemini-api/docs/generate-content/function-calling)
