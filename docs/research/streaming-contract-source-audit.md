# Streaming contract source audit

## Conclusion

**NO-GO until the contract is corrected.** The event union, `contentIndex` meaning,
terminal reason sets, block interleaving, and the need to remove streaming-only
state are source-backed. Several stronger statements in
`docs/specs/streaming-contract.md` are not true of the pinned Pi baseline.

This audit is against:

- pi-go contract commits
  [`8e1d987dac99e1db809cec87260ea7831ed7e806`](https://github.com/iamclancyliang/pi-go/commit/8e1d987dac99e1db809cec87260ea7831ed7e806)
  and
  [`71def6075f8185fa109a86ccab15c4f79387fc36`](https://github.com/iamclancyliang/pi-go/commit/71def6075f8185fa109a86ccab15c4f79387fc36)
- Pi baseline
  [`086c32e74530564922d011ade23ff582c9d63116`](https://github.com/earendil-works/pi/commit/086c32e74530564922d011ade23ff582c9d63116)

## Corrections required before implementation

### 1. `start` is not guaranteed before an early terminal error

Pi's closed event union includes `start`, but an already-aborted faux stream emits
`error/aborted` without `start` ([`faux.ts`, lines 346-354](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/providers/faux.ts#L346-L354)).
Async setup failure does the same ([`lazy.ts`, lines 41-58](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/api/lazy.ts#L41-L58)).
Anthropic also performs authentication, request construction, the provider request,
and the response hook before it pushes `start`; any failure in that interval goes
straight to the catch-path `error` event
([`anthropic-messages.ts`, lines 528-583](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/api/anthropic-messages.ts#L528-L583),
[`775-784`](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/api/anthropic-messages.ts#L775-L784)).

Replace "`start` once, before any other event" with:

- `start` is emitted once before any content-block event on a stream that reaches
  the provider-streaming phase;
- an error or cancellation before that phase may terminate with `error` as the
  first event.

The check must therefore reject a content event before `start`, but accept a
pre-start terminal `error`.

### 2. Only nonterminal events carry `partial`

The complete `AssistantMessageEvent` union has twelve variants. `start` and the
nine block events carry `partial`; `done` carries `message`, and `error` carries
`error` ([`types.ts`, lines 515-539](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/types.ts#L515-L539)).
The terminal payload is the final accumulated message under a different field,
not another `partial`.

Replace "every event carries `partial`" with "every nonterminal event carries the
cumulative `partial`; the terminal event carries the final message as `message`
or `error`."

### 3. Per-block `*_end` ordering applies only to normally closed blocks

For a block that closes normally, Pi emits `*_start`, zero or more matching
`*_delta` events, then `*_end`. On mid-block cancellation or failure, `error` may
terminate the stream without that block's `*_end`. The faux provider's abort checks
return directly from inside thinking, text, and tool-call delta loops
([`faux.ts`, lines 366-420](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/providers/faux.ts#L366-L420));
Anthropic likewise catches a provider exception without synthesizing end events
([`anthropic-messages.ts`, lines 588-760](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/api/anthropic-messages.ts#L588-L760),
[`775-784`](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/api/anthropic-messages.ts#L775-L784)).

Keep the start/delta/end rule for normally completed blocks and explicitly allow
terminal `error` to cut an open block short.

### 4. Immutable snapshots are a deliberate value-semantic deviation and must be deep

Pi commonly creates one `output` object, passes the same reference as each event's
`partial`, and mutates that object and its nested blocks in place
([`anthropic-messages.ts`, lines 510-526](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/api/anthropic-messages.ts#L510-L526),
[`583-680`](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/api/anthropic-messages.ts#L583-L680),
[`722-758`](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/api/anthropic-messages.ts#L722-L758)).
`EventStream.push` queues or delivers the same value without cloning
([`event-stream.ts`, lines 21-35](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/utils/event-stream.ts#L21-L35)).
A retained early Pi event can therefore observe later text growth, argument
replacement, usage/stop-reason changes, and deletion of scratch fields.

No checked-in production consumer requires that identity. The agent loop replaces
the current message from each event ([`agent-loop.ts`, lines 314-358](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/agent/src/agent-loop.ts#L314-L358));
JSON/RPC removes `partial` ([`json-event.ts`, lines 23-45](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src/modes/json-event.ts#L23-L45));
and the proxy reconstructs an accumulator from deltas
([`proxy.ts`, lines 33-55](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/agent/src/proxy.ts#L33-L55),
[`237-307`](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/agent/src/proxy.ts#L237-L307)).
However, `AssistantMessageEvent` is public and is forwarded verbatim to extension
handlers ([`extensions/types.ts`, lines 748-753](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src/core/extensions/types.ts#L748-L753),
[`1233-1235`](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src/core/extensions/types.ts#L1233-L1235),
[`agent-session.ts`, lines 755-761](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src/core/agent-session.ts#L755-L761)).
An external Pi extension can observe reference identity and later mutation.

The contract should say that pi-go intentionally preserves event-time values but
does not preserve JavaScript reference identity or retroactive mutation. A shallow
copy is insufficient: even Pi's faux provider emits `{ ...partial }` while sharing
and later mutating the nested content array and block objects
([`faux.ts`, lines 346-354](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/providers/faux.ts#L346-L354),
[`367-420`](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/providers/faux.ts#L367-L420)).
Snapshots must sever the content slice, every content block, recursively nested
tool arguments, usage/cost, diagnostics/details, and deferred data. Those mutable
descendants are part of `AssistantMessage`
([`types.ts`, lines 338-407](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/types.ts#L338-L407),
[`415-427`](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/types.ts#L415-L427)).

The snapshot test should prove both directions of isolation: later producer
updates cannot change an older event, and consumer mutation of one received event
cannot change later events or the terminal message.

### 5. "Monotonically growing whole messages" is too strong

Text and thinking payloads append, and the raw tool JSON buffer appends in the
normal delta path. The whole `AssistantMessage` is not monotonic: parsed tool
arguments are repeatedly replaced with best-effort parses
([`anthropic-messages.ts`, lines 669-680](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/api/anthropic-messages.ts#L669-L680));
usage and stop metadata are overwritten; and OpenAI Responses can expose a
provisional `stop` before the terminal response changes it to `length`
([`openai-responses-terminal-event.test.ts`, lines 262-281](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/test/openai-responses-terminal-event.test.ts#L262-L281)).

Test append/prefix behavior for text, thinking, and raw tool-call deltas, plus the
event-time accuracy and isolation of each snapshot. Do not require a structural
monotonic ordering over the whole message.

The contract's assertion that the block at `contentIndex` visibly "grew" on
every delta is also too strong. The faux provider emits every `toolcall_delta`
without changing the partial tool-call arguments, then installs the final
arguments immediately before `toolcall_end`
([`faux.ts`, lines 407-420](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/providers/faux.ts#L407-L420)).
Real adapters that parse incomplete JSON can also produce the same best-effort
object for consecutive fragments. Assert that `contentIndex` selects the correct
block and that delta accumulation is correct; do not require a visible structural
change for every fragment.

### 6. Scratch cleanup citations must cover success and all scratch forms

The contract cites Anthropic's catch-path cleanup as though it covered every
terminal path. On success, Anthropic deletes `index` when each block stops and
deletes `partialJson` when a tool call stops
([`anthropic-messages.ts`, lines 690-719](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/api/anthropic-messages.ts#L690-L719)).
Lines 776-780 are the error-path fallback
([`anthropic-messages.ts`, lines 775-783](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/api/anthropic-messages.ts#L775-L783)).

Other Pi adapters use other scratch names. OpenAI Completions strips
`partialArgs`, `customInput`, and `streamIndex` on normal completion and in its
catch path ([`openai-completions.ts`, lines 305-348](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/api/openai-completions.ts#L305-L348),
[`591-609`](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/api/openai-completions.ts#L591-L609)).
OpenAI Responses removes `partialJson` or `customInput` when the provider marks an
output item done ([`openai-responses-shared.ts`, lines 680-738](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/api/openai-responses-shared.ts#L680-L738)).

State the invariant by behavior: no provider correlation index, raw partial JSON,
parser buffer, or other streaming-only field may survive in the terminal message.
The final-message equality check across different chunk boundaries is valid.

### 7. Cancellation does not universally preserve accumulated partial work

The reason mapping is correct: `done` accepts `stop`, `length`, `toolUse`, or
`deferred`; `error` accepts `error` or `aborted`; `pending` is not terminal
([`types.ts`, line 393](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/types.ts#L393),
[`523-539`](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/types.ts#L523-L539)).
Anthropic retains its current `output` when cancellation reaches the catch path
([`anthropic-messages.ts`, lines 762-783](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/api/anthropic-messages.ts#L762-L783)).

The type-level claim that every terminal error has a nonempty `errorMessage` is
not closed: `AssistantMessage.errorMessage` is optional
([`types.ts`, lines 415-435](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/types.ts#L415-L435)),
and the `pi-messages` wire event also makes it optional
([`pi-messages.ts`, lines 76-83](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/api/pi-messages.ts#L76-L83)).
Pi-generated transport/provider failures normally set it, but a mandatory
nonempty value in pi-go would be a stronger guarantee.

That is not universal Pi behavior. If the `pi-messages` transport fails or is
locally aborted after earlier events, its catch path builds a new error message
with empty content rather than terminalizing the converter's accumulated partial
([`pi-messages.ts`, lines 176-207](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/api/pi-messages.ts#L176-L207),
[`313-334`](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/api/pi-messages.ts#L313-L334),
[`404-415`](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/api/pi-messages.ts#L404-L415)).

If pi-go chooses to retain partial work on every mid-stream cancellation, document
that as a stronger deliberate guarantee, not as universal Pi source parity.

### 8. A one-source-chunk adapter is not ruled out by the event shape

The contract itself permits zero or more `*_delta` events, and a provider adapter
can synthesize `start`, block start/delta/end, and a terminal event after receiving
one complete upstream chunk. Such an adapter can satisfy the event sequence,
`contentIndex`, snapshot, and cleanup assertions without delivering anything
incrementally. Therefore the statements that one chunk "fails" sections 2, 3, 4,
and 6 are not source-backed.

The acceptance test should control the upstream source with at least two gated
chunks and prove that the consumer receives the first block update while later
chunks and the terminal event are still withheld. Then release the remaining
chunks and verify their separate deltas, cumulative event-time snapshots, and one
terminal event. This tests incremental delivery rather than merely a synthetic
event vocabulary.

### 9. Eino has indexed output parts, but not one unified Pi block space

The added framework section is right that `Message.Content`,
`Message.ReasoningContent`, and `Message.ToolCalls` are separate fields, and that
`ToolCall.Index` identifies tool-call fragments for merging rather than a Pi
content-block index
([`message.go`, lines 115-130](https://github.com/cloudwego/eino/blob/v0.9.14/schema/message.go#L115-L130),
[`498-531`](https://github.com/cloudwego/eino/blob/v0.9.14/schema/message.go#L498-L531)).
Eino's ordinary concatenation also accumulates text, reasoning, and tool calls in
three independent collections, which loses their cross-field order
([`message.go`, lines 1644-1722](https://github.com/cloudwego/eino/blob/v0.9.14/schema/message.go#L1644-L1722)).

However, "there is no equivalent of `contentIndex`" is too absolute. Eino v0.9.14
also has `AssistantGenMultiContent`; each `MessageOutputPart` may carry
`MessageStreamingMeta.Index`, documented as the part's position in the final
response for reassembling multiple reasoning/content parts
([`message.go`, lines 259-294](https://github.com/cloudwego/eino/blob/v0.9.14/schema/message.go#L259-L294),
[`512-513`](https://github.com/cloudwego/eino/blob/v0.9.14/schema/message.go#L512-L513)).
Its concatenator uses the index to decide whether text or reasoning fragments
belong to the same part
([`message.go`, lines 1387-1422](https://github.com/cloudwego/eino/blob/v0.9.14/schema/message.go#L1387-L1422)),
and Eino's own tests cover indexed reasoning/text blocks and multiple text blocks
with different indices
([`message_test.go`, lines 753-823](https://github.com/cloudwego/eino/blob/v0.9.14/schema/message_test.go#L753-L823)).

That metadata still does **not** provide one unified index space across output
parts and tool calls, so pi-go needs an explicit mapping to Pi's heterogeneous
block list. But the mapping must inspect and define precedence among all relevant
Eino representations, including `AssistantGenMultiContent`; it cannot be based
only on transitions among `Content`, `ReasoningContent`, and `ToolCalls`.

The proposed inference rule also cannot guarantee block indices "unchanged by how
the provider chunked them" when using only the three legacy fields. Two adjacent
text blocks have no boundary marker there, and a single chunk can populate more
than one field without expressing their relative order. To make that invariant
testable, the input seam must carry explicit ordered block identity (for example,
pi-go-owned typed chunks, or Eino output parts with streaming indices plus an
explicit tool-call mapping). If the input omits that information, the adapter must
fail closed or document a narrower representable contract rather than guess block
boundaries from field changes.

## Source-backed statements that can remain

- The event set is closed and consists of the twelve variants in
  [`types.ts`, lines 523-539](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/types.ts#L523-L539).
- `contentIndex` is the stable index into the accumulated assistant `content`
  list. Anthropic appends a block, emits `output.content.length - 1`, then maps
  provider indexes back to that list position for deltas
  ([`anthropic-messages.ts`, lines 602-680](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/api/anthropic-messages.ts#L602-L680)).
- Blocks may overlap. OpenAI Completions keeps text, thinking, and multiple tool
  blocks open while processing interleaved deltas, and emits their end events only
  after the upstream stream ends
  ([`openai-completions.ts`, lines 275-282](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/api/openai-completions.ts#L275-L282),
  [`351-438`](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/api/openai-completions.ts#L351-L438),
  [`473-570`](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/api/openai-completions.ts#L473-L570)).
- `AssistantMessageEventStream` treats `done` and `error` as terminal and extracts
  their `message` or `error` payload as the result
  ([`event-stream.ts`, lines 69-81](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/utils/event-stream.ts#L69-L81)).
- Cancellation is represented by terminal `error` with reason `aborted`, not by a
  separate event type
  ([`types.ts`, lines 523-539](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/types.ts#L523-L539)).

## Follow-up audit: `f910c9e`

**Verdict: NO-GO before implementation.** The prior seven corrections are substantially
closed, and the central decision that pi-go must own explicit heterogeneous block identity
is source-backed. Four remaining corrections are required.

### 1. `errorMessage` is not mandatory in the Pi contract

The terminal table still says every `error` has `errorMessage` set. The public
`AssistantMessage.errorMessage` field is optional
([`types.ts`, lines 415-435](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/types.ts#L415-L435)),
and the `pi-messages` wire error also makes it optional
([`pi-messages.ts`, lines 76-83](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/api/pi-messages.ts#L76-L83)).
If pi-go requires a nonempty message for every terminal error, record that as another
stronger guarantee rather than Pi parity.

### 2. The claimed Pi `index` cleanup gap is not present

The absence of `delete index` in `openai-codex-responses.ts` and
`mistral-conversations.ts` is not evidence that either adapter persists an `index` field.
OpenAI Responses keeps the provider output index in an `outputSlots` map; its block scratch
type contains only `partialJson` and `customInput`
([`openai-responses-shared.ts`, lines 402-440](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/api/openai-responses-shared.ts#L402-L440),
[`463-524`](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/api/openai-responses-shared.ts#L463-L524)).
Mistral uses `toolCall.index` only to form a local correlation key and stores the content-list
position in `toolBlocksByKey`; the block itself contains `partialArgs`, not `index`
([`mistral-conversations.ts`, lines 681-712](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/api/mistral-conversations.ts#L681-L712)).
Remove the “known gap” and “deliberate improvement” claim. Uniformly excluding scratch from
pi-go's public type remains a sound design, but it is not a correction of these two adapters.

### 3. The success-path cleanup citation is incomplete

The contract distinguishes per-block success cleanup from the error sweep but cites only the
Anthropic catch path. Cite the success path as well: `content_block_stop` removes `index` and,
for tool calls, `partialJson` before the end event
([`anthropic-messages.ts`, lines 690-719](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/ai/src/api/anthropic-messages.ts#L690-L719)).

### 4. Eino metadata has three lifecycle and enforcement limits

- Legacy `schema.Message` loses `MessageOutputPart.StreamingMeta` on JSON because that field is
  `json:"-"` (`schema/message.go:259-294`). Raw `schema.AgenticMessage` blocks differ:
  `ContentBlock.StreamingMeta` is serialized as `streaming_meta` (`schema/agentic_message.go:167-177`).
- Multi-chunk `ConcatAgenticMessages` consumes the agentic index to group and sort, then creates
  a new block without restoring `StreamingMeta` (`schema/agentic_message.go:971-989,1272-1295`).
  Therefore raw chunk metadata can survive JSON, but the merged result normally no longer
  carries it.
- The mixed-meta and same-index/type checks apply to actual multi-message concatenation. A
  single input returns immediately without validation (`schema/agentic_message.go:912-914`).
  Eino also does not allocate or guarantee unique heterogeneous indices: the constructor stores
  the caller's value, and an internal wrapper can stamp one supplied index onto every block in
  a message (`schema/agentic_message.go:654-658`, `adk/wrappers.go:597-606`).

So `schema.AgenticMessage` is a capable carrier and concatenator, not the owner of Pi block
identity. pi-go must allocate and validate that identity before conversion, and should not state
the hard-error or serialization properties without the limits above.
