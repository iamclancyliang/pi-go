# eino-ext provider adapter audit

**Date:** 2026-08-21  
**pi-go baseline:** `94c3515f47e3763e1bae62fe18742a45f9e23961`  
**Eino baseline:** `github.com/cloudwego/eino v0.9.14`  
**Decision sought:** whether official `eino-ext` model adapters can replace the hand-written
DeepSeek port and become the provider expansion seam for pi-go.

## Executive decision

Use `eino-ext`, but do **not** install an `eino-ext` model directly into the runtime and do **not**
replace the current DeepSeek port yet.

The official repository has sixteen current model modules covering Ark, Anthropic, DeepSeek,
Gemini, Ollama, OpenAI/Azure, OpenRouter, Qianfan and Qwen. All current stable releases compile in a
scratch module when Go selects pi-go's Eino `v0.9.14`. That establishes dependency/API
compatibility only. It does not establish pi-go's behavioral contracts.

The adapters are useful for provider-specific request construction, SSE decoding, tool-schema
conversion and response conversion. They do not provide pi-go's credential confinement, shared
typed failure set, per-attempt usage ledger, stable block/event protocol, served-model truth,
retry ownership or overflow recovery. Those guarantees must remain in the pi-go-owned model module behind
`internal/ai.Port` and `internal/ai.StreamingPort`.

There is also a concrete replacement blocker. In an offline recorded-stream probe,
`agenticdeepseek@v0.1.0` converted a valid interleaved tool stream with provider indices
`0,1,0,1` into four Eino blocks/calls with indices `1,2,3,4`; continuation blocks had no tool ID or
name. Collecting the stream produced four calls rather than two. The shared OpenAI-compatible ACL
tracks only the last tool-call index, so the stable adapter cannot currently satisfy pi-go's
interleaved tool-fragment guarantee.

The recommended first pilot is therefore **OpenAI Responses through
`agenticopenai@v0.2.2`**, behind a private pi-go adapter. Its implementation exposes an injected HTTP client
and explicit retry limit, and its streaming converter assigns block identity from the provider's
output/content indices. Keep the hand-written DeepSeek implementation until an eino-ext candidate
passes the same conformance suite byte-for-byte and event-for-event.

## Scope and method

This audit used only first-party sources:

- pi-go source, architecture decisions and provider contract at the baseline above;
- pi-go's demand-side [provider-port obligations](https://github.com/iamclancyliang/pi-go/blob/94c3515f47e3763e1bae62fe18742a45f9e23961/docs/specs/provider-port-obligations.md),
  whose rows are tied to existing behavioral controls;
- Eino source at pi-go's pinned `v0.9.14`;
- official CloudWeGo `eino-ext` module tags and source;
- official SDK source where an adapter delegates retry behavior to that SDK.

For compatibility, a scratch module required Eino `v0.9.14` together with each adapter's latest
stable module release, then ran `go test -mod=mod -run '^$'` against the package. This compiled the
package and its dependencies but ran no adapter tests, opened no provider connection, used no
credential and made no API request. A passing row below means “builds with this selected Eino,” not
“certified against the provider.”

pi-go itself declares Go 1.25 and Eino `v0.9.14` in
[`go.mod`](https://github.com/iamclancyliang/pi-go/blob/94c3515f47e3763e1bae62fe18742a45f9e23961/go.mod).
All listed adapter modules declare a Go floor at or below 1.25 and an Eino requirement at or below
`v0.9.14`, so Go's module selection can retain pi-go's Eino pin.

## The two Eino model families

Eino `v0.9.14` exposes two related but non-interchangeable model surfaces.

The classic family implements `model.BaseModel` (`Generate` and `Stream`) and commonly
`model.ToolCallingChatModel` (`WithTools`) over `schema.Message`. The agentic family implements
`model.AgenticModel` over `schema.AgenticMessage`, with tools passed as call options. See Eino's
official [`components/model/interface.go`](https://github.com/cloudwego/eino/blob/v0.9.14/components/model/interface.go)
and [`components/model/option.go`](https://github.com/cloudwego/eino/blob/v0.9.14/components/model/option.go).

Both schemas carry useful streaming identity hints:

- classic tool calls have `ToolCall.Index` for stream merging;
- agentic content blocks have `StreamingMeta.Index`.

Neither hint transfers ownership of pi-go's block protocol to Eino. pi-go still has to reject
missing, jumping, changing or reopened block identities and emit its own start/delta/end events.
The relevant Eino schemas are
[`schema/message.go`](https://github.com/cloudwego/eino/blob/v0.9.14/schema/message.go) and
[`schema/agentic_message.go`](https://github.com/cloudwego/eino/blob/v0.9.14/schema/agentic_message.go).

The schemas also show two cross-provider limits:

- classic `ResponseMeta` and agentic `AgenticResponseMeta` carry finish/usage and extension data,
  but no common served-model field;
- token detail fields are ordinary integers. A nil usage object can mean “no usage reported,” but
  an individual detail cannot distinguish “unreported” from “reported zero” without a pi-go-owned
  mapping.

Sources: classic
[`ResponseMeta`](https://github.com/cloudwego/eino/blob/32759e61861d8f3773b9a21d2a7db2e60c58354e/schema/message.go#L448-L458)
and agentic
[`AgenticResponseMeta`](https://github.com/cloudwego/eino/blob/32759e61861d8f3773b9a21d2a7db2e60c58354e/schema/agentic_message.go#L85-L100).

## Official package inventory

Versions and source commits were resolved from the official module tags on 2026-08-21. “Retry”
describes controls visible at the adapter boundary; no visible knob is **not** proof that a
transitive SDK never retries.

### Classic `ToolCallingChatModel` packages

| Package | Release / source | Provider surface | Auth and configuration | Streaming, tools, usage, model, retry | pi-go assessment |
| --- | --- | --- | --- | --- | --- |
| `ark` | [`v0.1.69`](https://github.com/cloudwego/eino-ext/tree/90a15623ddb66465aea01fbe8c63ecc9d267acc1/components/model/ark) | Volcengine Ark Chat Completions, Responses and image generation | API key or AK/SK; endpoint/model configuration | Generate, Stream, tools; cached/reasoning usage; explicit `RetryTimes`, default 2. Chat Completions uniquely exposes served model through [`ark.GetModelName`](https://github.com/cloudwego/eino-ext/blob/90a15623ddb66465aea01fbe8c63ecc9d267acc1/components/model/ark/message_extra.go#L123-L133), not common `ResponseMeta`. | Broad adapter, but retry must be forced to zero and served-model extraction is provider-specific. |
| `arkbot` | [`v0.1.2`](https://github.com/cloudwego/eino-ext/tree/bbb3c85525d4f8ef536ab3c14429de9a931c7d70/components/model/arkbot) | Ark Bot chat | API key or AK/SK | Generate, Stream, tools; bot usage/request/reference extra; retry default 2; no served-model field. | Usable only behind the pi-go model module; bot-specific metadata needs an explicit owned mapping. |
| `claude` | [`v0.1.25`](https://github.com/cloudwego/eino-ext/tree/0ca750ffc2ffe5a87ef83ee7cf9e151022c6368a/components/model/claude) | Anthropic Messages, AWS Bedrock and Google Vertex | API key/auth token, AWS credentials/profile, or Vertex ADC/service-account JSON | Generate, Stream, tools, thinking, cache and server tools; cache/reasoning usage; no served model or adapter retry knob. The official Anthropic SDK used by this release defaults to [two retries](https://github.com/anthropics/anthropic-sdk-go/blob/b0e07bb34ffc0d018c89b5cec8f62a7b823927f3/option/requestoption.go#L214-L224). | Hidden SDK retry is incompatible with pi-go's attempt ledger unless disabled or fully instrumented. |
| `deepseek` | [`v0.1.7`](https://github.com/cloudwego/eino-ext/tree/9137edd89e72b72735ede69db1c5ae29178a6e41/components/model/deepseek) | DeepSeek Chat Completions | Raw API key, model, base URL/path and custom HTTP client | Generate, Stream, tools and reasoning; cached usage. Stream maps reasoning-token detail, while non-stream cannot because the pinned SDK omits it ([source](https://github.com/cloudwego/eino-ext/blob/9137edd89e72b72735ede69db1c5ae29178a6e41/components/model/deepseek/deepseek.go#L882-L920)); no served model or retry knob. | Better as a wire converter than as owner. It reinforces pi-go's “Stream is source; Generate collects Stream” rule, but does not replace the current port. |
| `gemini` | [`v0.1.33`](https://github.com/cloudwego/eino-ext/tree/0a651d382b8e4e3cc71ee2e40dbcb4f15902bbcd/components/model/gemini) | Google GenAI GenerateContent | Caller supplies an authenticated `*genai.Client` | Generate, Stream, multimodal and tools; cache/reasoning usage. Gemini omits tool-call IDs, so the adapter synthesizes UUIDs; no served model or retry knob. | Client injection helps confinement, but synthesized identity must be tested against replay/pairing guarantees. |
| `ollama` | [`v0.1.9`](https://github.com/cloudwego/eino-ext/tree/9b7587b89863115eb89f172858fdd3b4de30c3e7/components/model/ollama) | Ollama chat, local or remote | Base URL, custom HTTP client, model/options; no credential field | Generate, Stream, tools, multimodal and thinking; prompt/completion usage; no served model or retry knob. | Lowest secret risk; still needs pi-go's common policy for events, ledger and failures. |
| `openai` | [`v0.1.13`](https://github.com/cloudwego/eino-ext/tree/0ebab92e14f26088411dbc440a1ebdc904ccd8a1/components/model/openai) | OpenAI/Azure Chat Completions | API key, base URL/Azure mapping/version and custom client | Generate, Stream, tools, structured output, reasoning and audio; cache/reasoning usage; no served model or retry knob. Exported [`APIError`](https://github.com/cloudwego/eino-ext/blob/0ebab92e14f26088411dbc440a1ebdc904ccd8a1/components/model/openai/types.go#L35-L65) keeps status/code/message/param/type but not headers, request ID or retry-after. | Partial typed error is useful but insufficient for pi-go retry and diagnostics. |
| `openrouter` | [`v0.1.10`](https://github.com/cloudwego/eino-ext/tree/0ebab92e14f26088411dbc440a1ebdc904ccd8a1/components/model/openrouter) | OpenRouter OpenAI-compatible chat and provider-side model fallback | API key, base URL, one model or `Models` fallback list | Generate, Stream, tools and cache/reasoning extras; no served model or retry knob. A mid-stream provider failure may be carried as [`StreamTerminatedError` in message Extra](https://github.com/cloudwego/eino-ext/blob/0ebab92e14f26088411dbc440a1ebdc904ccd8a1/components/model/openrouter/message_extra.go#L58-L89). | Model fallback without served-model truth is a direct mismatch; Extra must be inspected or a failure can look complete. |
| `qianfan` | [`v0.1.4`](https://github.com/cloudwego/eino-ext/tree/a1da1e0520d8e4737d63f427712977786ca5d75e/components/model/qianfan) | Baidu Qianfan ChatCompletion V2 | Qianfan SDK singleton/global access-key and secret-key configuration | Generate, Stream, tools and multimodal; prompt/completion usage; explicit retry count/timeout/backoff; no served model. | Global credential configuration conflicts with per-provider injected ownership; defer unless wrapped by a strictly isolated client seam. |
| `qwen` | [`v0.1.9`](https://github.com/cloudwego/eino-ext/tree/0ebab92e14f26088411dbc440a1ebdc904ccd8a1/components/model/qwen) | DashScope OpenAI-compatible chat | API key, base URL and custom client | Generate, Stream, tools, thinking, multimodal/audio; cached/reasoning usage; no served model or retry knob. | Candidate after the OpenAI pilot; requires provider-specific error and stop mapping. |

### Agentic `AgenticModel` packages

| Package | Release / source | Provider surface | Auth and configuration | Streaming, tools, usage, model, retry | pi-go assessment |
| --- | --- | --- | --- | --- | --- |
| `agenticark` | [`v0.2.4`](https://github.com/cloudwego/eino-ext/tree/20b2a55477df4e2e078c51596deb019aa54ab133/components/model/agenticark) | Ark Responses API | API key or AK/SK | Generate, Stream, function/MCP/server tools, cache/reasoning usage; explicit `RetryTimes`; response status/error extensions but no served model. | Modern block shape, but retry and provider extensions stay inside the pi-go adapter. |
| `agenticclaude` | [`v0.1.3`](https://github.com/cloudwego/eino-ext/tree/0910e2add6ed466fbd9c1d92a50ec1958fe53d42/components/model/agenticclaude) | Anthropic Messages, Bedrock and Vertex | Same direct/cloud credential modes as classic Claude | Generate, Stream, function/deferred/client-search/server tools and cache; no served model or adapter retry knob; same SDK default-retry concern. | Promising feature breadth after retry control is proven. |
| `agenticdeepseek` | [`v0.1.0`](https://github.com/cloudwego/eino-ext/tree/95019b303cc63a4149352c48923ad2becee1885d/components/model/agenticdeepseek) | DeepSeek over the shared OpenAI-compatible ACL | Raw API key, base URL, model and sampling configuration | Generate, Stream, function tools and shared ACL usage; no served model or retry knob. | **Do not use as replacement now:** stable release fails pi-go's interleaved tool-fragment control. |
| `agenticgemini` | [`v0.2.2`](https://github.com/cloudwego/eino-ext/tree/0a651d382b8e4e3cc71ee2e40dbcb4f15902bbcd/components/model/agenticgemini) | Google GenAI GenerateContent | Caller supplies authenticated `*genai.Client` | Generate, Stream, multimodal/thinking/cache and hosted tools; cache/reasoning usage; no served model or retry knob. | Strong later candidate; identity and retry behavior still require conformance. |
| `agenticopenai` | [`v0.2.2`](https://github.com/cloudwego/eino-ext/tree/20b2a55477df4e2e078c51596deb019aa54ab133/components/model/agenticopenai) | Separate OpenAI/Azure Chat Completions and Responses implementations | API key/base URL/Azure options and injected HTTP client | Generate, Stream, function/MCP/server/tool-search tools; cache/reasoning usage; no served model. Responses exposes [`HTTPClient` and `MaxRetries`](https://github.com/cloudwego/eino-ext/blob/20b2a55477df4e2e078c51596deb019aa54ab133/components/model/agenticopenai/responses_model.go#L57-L63); nil retry uses the official SDK's [default of two](https://github.com/openai/openai-go/blob/43deb5df73c888ec90ad354cb192c69a5a636446/option/requestoption.go#L96-L106). | **Best first pilot:** both network and retry are controllable, and Responses carries strong provider indices. |
| `agenticqwen` | [`v0.1.0`](https://github.com/cloudwego/eino-ext/tree/95019b303cc63a4149352c48923ad2becee1885d/components/model/agenticqwen) | Qwen/DashScope over the shared OpenAI-compatible ACL | Raw API key, base URL and model options | Generate, Stream, tools, thinking and audio; shared ACL usage; no served model or retry knob. | Later candidate; shares ACL machinery implicated by the DeepSeek interleaving counterexample, so test before trusting. |

## Compatibility result

All sixteen rows above compiled with Eino `v0.9.14` selected. The current releases and their own Eino
requirements are:

| Module | Release | Declared Eino |
| --- | ---: | ---: |
| agenticark | v0.2.4 | v0.9.1 |
| agenticclaude | v0.1.3 | v0.9.1 |
| agenticdeepseek | v0.1.0 | v0.9.1 |
| agenticgemini | v0.2.2 | v0.9.1 |
| agenticopenai | v0.2.2 | v0.9.5 |
| agenticqwen | v0.1.0 | v0.9.1 |
| ark | v0.1.69 | v0.7.13 |
| arkbot | v0.1.2 | v0.7.11 |
| claude | v0.1.25 | v0.9.1 |
| deepseek | v0.1.7 | v0.7.13 |
| gemini | v0.1.33 | v0.7.13 |
| ollama | v0.1.9 | v0.7.13 |
| openai | v0.1.13 | v0.7.13 |
| openrouter | v0.1.10 | v0.7.13 |
| qianfan | v0.1.4 | v0.7.13 |
| qwen | v0.1.9 | v0.7.13 |

This is a green dependency gate, not a green product gate. In particular, a package can compile
while losing a provider's block identity, hiding SDK retry attempts or omitting served model.

## Cross-cutting capability findings

### Generate and Stream

Every current adapter exposes both forms. That does not justify two pi-go paths. The classic
DeepSeek source itself demonstrates a semantic difference: stream conversion can preserve
reasoning-token detail that its non-stream SDK response cannot. pi-go's existing contract therefore
remains correct: use the adapter's `Stream` as the only source and implement `Generate` by collecting
the pi-go stream.

### Tools and block identity

All current packages support function tools; agentic adapters additionally cover various hosted,
MCP, search or deferred tools. Feature breadth is not equivalence. pi-go requires stable block
identity, valid start/delta/end order, interleaved tool-fragment merging, exactly one terminal and
record-before-act tool governance.

The `agenticdeepseek@v0.1.0` counterexample is decisive for direct replacement. The offline probe
`/tmp/eino-ext-probe/deepseek_probe_test.go` used an injected `httptest.Server` and ran
`go test -run TestAgenticDeepSeekOfflineStreamShape -v`; it made no provider call. The valid SSE
held two tool calls with fragments ordered `0(id/name/prefix), 1(id/name/prefix), 0(suffix),
1(suffix)`. The adapter emitted block indices `1,2,3,4`, the suffix blocks lost ID/name, and
`schema.ConcatAgenticMessages` produced four calls rather than two. The shared ACL converter
[`advanceToolCall`](https://github.com/cloudwego/eino-ext/blob/846f52bd97c61b84abb576c6b8bbe585db7e20b9/libs/acl/openai/agentic_convert.go#L117-L131)
retains only the immediately previous provider index, then assigns the derived sequential index to
[`StreamingMeta`](https://github.com/cloudwego/eino-ext/blob/846f52bd97c61b84abb576c6b8bbe585db7e20b9/libs/acl/openai/agentic_convert.go#L175-L188).
pi-go must treat Eino indices as input hints, not as proof that the finished block graph is valid.

OpenAI Responses is the strongest pilot because its converter keys `StreamingMeta.Index` from the
provider's `OutputIndex` and `ContentIndex` across text, reasoning and tool events; see the official
[`responses_event_convertor.go`](https://github.com/cloudwego/eino-ext/blob/20b2a55477df4e2e078c51596deb019aa54ab133/components/model/agenticopenai/responses_event_convertor.go#L861-L981).
It still must pass pi-go's accumulator and conformance suite.

### Usage

Adapters generally map prompt/input, completion/output and total usage, with provider-dependent
cache and reasoning details. That is useful raw evidence, not an owned ledger:

- Eino represents one response's usage, not every hidden retry attempt;
- integer detail fields collapse absent and reported zero;
- failed attempts may have no response message at all;
- no adapter derives a pi-go logical-call total from immutable attempt snapshots.

The pi-go adapter must convert to `ai.Usage`, preserve absence, attach failed-attempt usage to the typed
failure and record each attempt once. Currency cost remains unknown unless pi-go separately owns a
pinned price source.

### Served model and substitution

There is no common served-model field in Eino's response metadata. Classic Ark Chat Completions is
the only audited adapter with an accessor for model name, and even that is provider Extra. OpenRouter
can ask the provider to choose among several models but does not report the selected model through
the standard response.

Therefore an eino-ext adapter cannot establish pi-go's `Response.Model` contract by default. The pi-go adapter
must either extract a documented provider-specific field or report the model as unknown. It must not
copy the requested model into “served model” and call that observation. pi-go also retains the
existing handler-order rule: model/provider selection remains last and innermost because an Eino
substituting handler can prevent later handlers from running.

### Errors and retries

There is no shared eino-ext error taxonomy. Most packages wrap or return their SDK's errors;
classic OpenAI retains a subset of typed fields, OpenRouter may put a stream failure in message
Extra, and Responses adapters retain provider-specific status/error extensions.

Retry behavior is heterogeneous:

- Ark/Ark Bot have a default retry count of two;
- Qianfan exposes retry controls;
- OpenAI Responses exposes `MaxRetries`, but nil means the SDK default of two;
- Claude exposes no adapter knob while its official SDK defaults to two;
- absence of an adapter knob elsewhere does not prove absence of SDK retries.

pi-go must set every reachable retry budget to zero, inject a counting transport and classify
quota/billing before any retry decision. If a package cannot disable or expose internal attempts,
it cannot enter the first tranche because it would make the per-attempt ledger and request bound
unprovable.

### Authentication and configuration

Most credentialed adapters accept raw API-key or cloud-credential fields in exported config.
Gemini takes an already constructed client; Ollama has no credential field; Qianfan uses SDK-global
configuration. None implements pi-go's credential store, serialized modify/delete, injected
environment precedence, non-secret enumeration, no-disk rule or redaction surfaces.

The pi-go model module must resolve credentials before construction and keep the eino-ext instance private and
short-lived. No eino-ext config, SDK client or error may be formatted into logs, events or session
truth without a pi-go-owned redaction pass.

### Model catalogs and limits

Adapter configs generally accept arbitrary model strings. Their packages are not a trustworthy,
exhaustive model catalog and do not establish per-model context windows or output caps. pi-go must
continue requiring explicit model and output limit, and must own any configured context window used
for numeric overflow detection.

## Supply matched against pi-go's obligations

The demand-side source is
[`docs/specs/provider-port-obligations.md`](https://github.com/iamclancyliang/pi-go/blob/94c3515f47e3763e1bae62fe18742a45f9e23961/docs/specs/provider-port-obligations.md).
“Adapter carries” below means the official package exposes enough first-party data/mechanism to do
the job; it does not mean pi-go may skip its own validation. “Cannot prove” is intentionally
different from “does not carry”: it is a gate for a recorded control, not permission to assume.

| Obligation group | Adapter carries | pi-go module must supply | Cannot currently prove from the common Eino result |
| --- | --- | --- | --- |
| Credentials | Provider-specific key/client fields and, for many packages, injected HTTP client | store semantics, precedence, injected environment, redaction, no-disk policy, construction lifetime | that a transitive SDK never formats or persists a secret without inspecting that SDK/path |
| Failure classification | Raw SDK error or provider-specific response/Extra in some packages | one closed typed taxonomy, quota-before-retry, cancellation/deadline preservation, 200-level terminal failures, unknown-terminal fail-closed | uniform headers/status/retry-after after adapters that flatten or move errors into Extra |
| Retry and request count | Explicit retry knob in Ark, Ark Bot, Qianfan and OpenAI Responses | force every layer to zero, counting transport, bounded pi-go-owned retries/backoff | zero hidden retries in packages with no adapter knob until an injected transport proves it |
| Usage | Response-level prompt/output/cache/reasoning values, varying by provider | absent-vs-zero mapping, failed-attempt usage, immutable attempt ledger and derived totals | individual field absence after conversion to Eino integer fields |
| Streaming and tools | Generate/Stream, tool schemas, classic tool indices or agentic block indices | identity validation, interleaving, block lifecycle, snapshots, exactly one terminal, partial failure | correct interleaved merging for shared ACL; stable agenticdeepseek proves the opposite |
| Conversation/tool governance | Reasoning/tool content sufficient for projection on many packages | durable reasoning, visible-answer separation, policy, source order, record-before-act and result pairing | any governance guarantee from an adapter alone; these live above the provider wire |
| Served model | Ark-specific Extra only in the audited set | provider-specific extraction or explicit unknown, plus model-change truth/order | served model from classic/agentic common response metadata, including OpenAI Responses |
| Overflow | Finish reason and provider usage | numeric/text detector, `ai.ErrContextOverflow`, failed usage, compaction and once-only retry | a shared cross-provider overflow classification in eino-ext |

For OpenAI Responses specifically, common `AgenticResponseMeta` omits served model and flattens
usage detail into integers. The pilot must capture the relevant provider terminal payload **before**
that conversion—through a provider-specific terminal codec or a controlled streaming transport—and
map it immediately into pi-go-owned values. If the adapter offers no supported interception point,
the pilot cannot satisfy served-model truth or absent-vs-zero usage merely by reading the final Eino
message; that is a blocker to production registration, not a reason to invent values.

## Guarantees that cannot be delegated

The following remain pi-go responsibilities even when eino-ext performs the wire conversion.

| Guarantee | Why eino-ext cannot own it | Required pi-go control |
| --- | --- | --- |
| Credential store and confinement | Adapter configs accept raw secrets/clients or global SDK auth; they do not implement pi-go's store or redaction contract. | Resolve through the injected credential context; construct privately; test all formatting/log/event/session surfaces; no disk in v1. |
| Typed failures | Provider SDK errors and package-specific Extra values do not form the repository's quota/auth/refusal/throttle/timeout/transport/interrupted taxonomy or `ai.ErrContextOverflow`. | Classify at the provider boundary while status, headers and stop data still exist; preserve causes through runtime wrapping. |
| Attempt ledger | Internal SDK retries can be invisible and Eino carries only response-level usage. | Disable hidden retries; count transport requests; store immutable usage for every successful or failed attempt. |
| Usage semantics | Optional usage object exists, but detail integers collapse absent and zero; provider fields vary. | Convert into `ai.Usage` with owned optional fields; derive totals from attempts; do not invent cost. |
| Block identity and public events | Eino supplies indices but does not enforce pi-go's contiguous identity, block lifecycle, snapshots, terminal or renderer contract; stable agenticdeepseek already fails interleaving. | Route every chunk through pi-go's accumulator; fail closed on missing/changed/reopened identity; emit one terminal carrying partial content. |
| Tool governance | Adapter tool support only encodes/decodes calls. | Preserve source order, policy denial, durable intent and record-before-act/result pairing in runtime/session. |
| Reasoning ownership | Providers expose reasoning differently and some need prior reasoning replayed. | Keep reasoning separate from visible text, persist it in session truth and project it back on later requests. |
| Served model and substitution | Common Eino metadata has no served model; fallback providers can hide the selected model. | Extract provider-specific evidence or mark unknown; preserve model-change truth and innermost selection order. |
| Retry ownership | Defaults and controls differ; hidden retries defeat request counts and quota-first classification. | Force zero at adapter and SDK layers, count requests, and apply pi-go retry only outside the adapter. |
| Overflow recovery | Adapters map finish reasons/usage but do not implement pi-go's numeric detection, sentinel, compaction and once-only retry. | Detect in the pi-go adapter, return `ai.ErrContextOverflow` with failed usage, then let runtime own shortening and durable terminal accounting. |

These responsibilities follow pi-go's accepted architecture: the model port is owned by `ai`, Eino
and provider types stay inside its implementation, and runtime reaches them only through the
pi-go-owned port. See [ADR-0001](../adr/0001-module-boundary.md),
[ADR-0002](../adr/0002-eino-ownership-boundary.md) and the architecture's Eino edge E1 in
[`docs/architecture/architecture.md`](../architecture/architecture.md).

## Recommended seam

Deepen the existing model module behind the existing `internal/ai` interface. Do not publish a new
provider interface merely to mirror eino-ext config: `ai.Port` / `ai.StreamingPort` is already the
real seam, with the hand-written DeepSeek adapter and the proposed OpenAI adapter as two concrete
implementations. The external provider is a true external dependency, so production receives an
injected HTTP adapter and tests receive a recorded/counting HTTP adapter at a private internal seam.

The module earns its depth by hiding provider SDK selection, Eino schema conversion, credential
placement, retry suppression, failure classification, usage normalization and stream validation
behind the same small pi-go interface callers already know. Deleting it would spread those rules
across every provider and runtime caller. Do not add a third Eino edge and do not pass an eino-ext
model directly to `internal/runtime`.

```text
composition root
    |
    | provider id + model + pi-go-resolved credential + injected HTTP transport
    v
internal/provider/openai (private ai.StreamingPort adapter)
    |-- constructs one official adapter for one logical attempt
    |-- eino-ext owns request/SSE/provider schema conversion only
    |-- captures terminal metadata before Eino flattens it
    |-- converts Eino chunks/errors/usage immediately to pi-go values
    v
ai.StreamingPort / ai.Port
    |
    | pi-go accumulator, typed errors, attempt ledger, overflow, session/tool governance
    v
internal/runtime (existing Eino TurnLoop bridge)
```

The module's implementation should:

1. start as the concrete `internal/provider/openai` adapter and expose only pi-go types; do not add
   a generic eino-ext registry/factory until a second eino-ext-backed adapter proves the private
   variation and common policy can replace duplication rather than add a pass-through layer;
2. receive a credential value and injected `*http.Client`/`RoundTripper`, never read ambient process
   environment itself;
3. construct the adapter per logical attempt where practical, with every SDK retry explicitly zero;
4. drive only the adapter's streaming method and derive `Generate` by collecting pi-go events;
5. translate each classic message/tool index or agentic content block into `ai.Chunk`, then let
   `ai.Accumulator` validate identity and emit snapshots;
6. inspect provider-specific terminal/Extra data before declaring success;
7. capture and map served model, usage presence and errors before Eino/SDK details are flattened;
8. return served model only from provider evidence, never from the requested-model input;
9. leave credential storage, retries, overflow recovery, session truth and tool execution outside
   the adapter wrapper.

This preserves E1 exactly: `ai implementation -> Eino ChatModel/provider adapter`, hidden behind
the model port. It avoids a new `runtime -> eino-ext provider` edge and keeps ADR-0002 reversible.

## Bounded first implementation tranche

The first tranche should prove one concrete adapter at the existing model seam, not announce
support for every package or introduce a provider framework.

### Include

1. Pin only `github.com/cloudwego/eino-ext/components/model/agenticopenai@v0.2.2` and the minimal
   transitive modules selected with Eino `v0.9.14`.
2. Implement OpenAI **Responses** only, behind a private `ai.StreamingPort`; do not expose
   `schema.AgenticMessage` or adapter config outside the provider implementation.
3. Require explicit provider, model, output cap, pi-go-resolved credential and injected counting
   HTTP transport.
4. Set `MaxRetries` to zero and prove exactly one request for a non-overflow logical call.
5. Support text, reasoning, function-tool calls, provider finish/status, reported usage and partial
   failure. Treat hosted/MCP/server tools as unsupported at the pi-go boundary for this tranche.
6. Add a provider-specific terminal capture path before conversion to common Eino metadata; prove
   it retains served model and field presence. If the official adapter has no supported seam for
   this, stop the pilot rather than forking Eino or fabricating the requested model/zero values.
7. Preserve provider output/content identity through pi-go's accumulator; do not renumber before
   validation merely to make a malformed stream contiguous.
8. Map OpenAI/Azure errors into the existing pi-go typed outcomes before any retry decision.
9. Keep Generate derived from Stream and keep every attempt in the session ledger.
10. Add offline recorded-event controls for:
   - Generate/Stream final equivalence;
   - two interleaved tool calls and fragmented arguments;
   - text/reasoning/tool block ordering and exactly one terminal;
   - reported zero versus unreported usage details;
   - failed-attempt usage and request count;
   - quota-before-retry and retry headers;
   - unknown/incomplete terminal failure;
   - served-model absent versus provider-reported;
   - cancellation with partial content;
   - `Agent.Run` through provider -> runtime -> session/tool/render surface.
11. Run the existing DeepSeek port and the new adapter side-by-side only in offline conformance tests;
    do not add automatic provider fallback or dual live calls.

### Explicitly defer

- replacing `internal/provider/deepseek`;
- `agenticdeepseek` and `agenticqwen` until the shared ACL interleaving defect is fixed and pinned;
- classic adapter expansion;
- Ark, Claude, Gemini, Ollama, OpenRouter, Qianfan and Qwen production registration;
- MCP, hosted/server tools, images, audio and computer-use blocks;
- OAuth and credential persistence;
- provider model catalogs, automatic model fallback and currency cost;
- any real provider call. Live smoke remains separately authorized and bounded by the existing
  provider contract.

## Expansion gate after the pilot

An adapter becomes production-eligible only when the same provider-independent suite proves all of
the following at its exact module tag:

1. no Eino or SDK type escapes the pi-go adapter;
2. hidden retries are disabled and counting transport observes the expected request count;
3. typed failures and failed usage survive into runtime/session;
4. interleaved tool fragments retain identity and source order;
5. stream and collected results agree;
6. unreported usage remains distinct from zero;
7. served-model truth is reported from evidence or explicitly absent;
8. cancellation/error terminal retains partial content exactly once;
9. reasoning is durable but not visible answer text;
10. numeric/text overflow cases reach the existing recovery contract without widening other
    refusal matches.

Until a row passes that gate, “officially supported by eino-ext” means only that CloudWeGo ships an
adapter for the provider. It does not mean the adapter satisfies pi-go's contract or can replace an
existing port.
