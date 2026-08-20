# Streaming contract

**Status:** alignment table, corrected after independent source audits. No implementation yet.

**Pi baseline:** [`086c32e74530564922d011ade23ff582c9d63116`](https://github.com/earendil-works/pi/commit/086c32e74530564922d011ade23ff582c9d63116)
**Framework baseline:** eino `v0.9.14`

**Scope:** the observable event protocol of a streamed assistant reply, stated so each row can be
checked against source rather than argued about.

Evidence for every row is in `docs/research/streaming-contract-source-audit.md` and
`docs/research/streaming-alignment-verification.md`, which were produced independently and agree.

## 1. Event set

`packages/ai/src/types.ts:523-539`. Twelve variants, closed: `start`; `text_start` / `text_delta` /
`text_end`; `thinking_start` / `thinking_delta` / `thinking_end`; `toolcall_start` /
`toolcall_delta` / `toolcall_end`; `done`; `error`.

## 2. Ordering

1. **A content event is always preceded by `start`.** `start` is NOT guaranteed to be the first event
   of every stream: a stream may terminate with `error` having emitted nothing else, via the lazy
   wrapper (`api/lazy.ts:46-61`), any throw before the first push (`anthropic-messages.ts:574-583`),
   or an already-cancelled signal (`provider-retry.ts:117`, `faux.ts:347-352`).
2. Exactly one terminal event: `done` or `error`.
3. A **normally closed** block runs `*_start` → zero or more `*_delta` → `*_end`.
4. **An error may leave a block open.** `*_start` with no matching `*_end` is legal and occurs; no
   implementation synthesises closing events (`anthropic-messages.ts:610`, throw at `:413`/`:474`,
   catch at `:775-785`).
5. Blocks may interleave; `contentIndex` is what attributes an event to a block.

## 3. `contentIndex`

The position of the block this event concerns, within the accumulated message's content list.

## 4. `partial`

**Only the ten non-terminal events carry `partial`.** `done` carries `message`; `error` carries
`error` (`types.ts:523-539`).

`partial` is the accumulated message **as of this event**, so a consumer never has to maintain its own
accumulator.

### Deliberate divergence: snapshot, not alias

Pi passes the same mutating object every time (`partial: output`). pi-go delivers a deep copy per
event. **This is a value-semantics divergence, not an unchanged observable** — under Pi a consumer
mutating what it received affects later events and the terminal message; under pi-go it does not.

Chosen because Go delivers events across goroutines, where a shared mutable pointer is a data race,
and because this repository has twice fixed bugs where a reader could rewrite the record it was
handed.

Everything mutable must be copied, not just the top level. What pi-go carries, and therefore copies:
the block slice, each block, and the tool call's arguments.

**Declared gap, not silent omission.** Pi's message also carries `usage.cost` (mutated in place after
the fact, `models.ts:892-896`) and diagnostics with their details. pi-go does not carry those fields
at all, so there is nothing to copy and nothing to test. The deep-copy rule extends to them the moment
they are added, and the rule is written that way rather than enumerating today's fields — but the
fields themselves are missing, and a reader comparing against Pi should know that rather than infer
from a copy list that they are handled.

**Checkable, both directions:** a producer mutation after delivery must not appear in an already
delivered event; a consumer mutation of a delivered event must not appear in any later event or in
the terminal message.

**Not claimed:** that the message grows monotonically in structure. Blocks are edited in place and
scratch fields are removed, so later is not a superset of earlier.

## 5. Terminal states

| Terminal | `reason` | Carries |
| --- | --- | --- |
| `done` | `stop`, `length`, `toolUse`, `deferred` | final message |
| `error` | `error`, `aborted` | final message |

Cancellation is not a separate event: it terminates as `error` with reason `aborted`.

`errorMessage` is **optional** in Pi (`types.ts:427,474`) and optional on the wire. pi-go always
supplies non-empty text on a terminal error. **That is a stronger guarantee than Pi makes, not
alignment** — an error a user cannot read is an error they cannot act on.

## 6. Streaming scratch state must not survive

Scratch fields are removed before the terminal event on two different paths: **per block as it
closes** on the success path (`anthropic-messages.ts:690-719`, in `content_block_stop`), and **a
catch-all sweep** on the error path (`:775-785`).

Fields observed across providers: `index`, `partialJson`, `partialArgs`, `customInput` (with a nested
`jsonBuffer`), `streamIndex`.

**Checkable:** nothing in the final message says how it was chunked.

Which fields exist is per provider: a provider strips what it stored. `openai-codex-responses.ts`
stores only `partialJson` and `customInput` and strips both; `mistral-conversations.ts` stores only
`partialArgs` and strips it. Neither ever puts `index` on a block, so neither has anything to strip —
absence of a strip call is not a leak.

## 7. Cancellation and partial work — pi-go is STRONGER

Pi is not uniform. `anthropic-messages.ts:775-785` re-emits the accumulator, preserving partial work.
`pi-messages.ts:313-335` builds a **new** message with `content: []` and zero usage on every
client-side abort (`:413-415`), discarding what had accumulated; `lazy.ts` and `faux.ts` also emit
empty-content terminals.

pi-go preserves accumulated content on every abort path. **This is a stronger guarantee than Pi
gives, deliberately** — a partial answer the user watched arrive should not vanish because they
stopped it.

## 8. The discriminator: timing, not chunk count

A single upstream chunk **can** synthesise a whole correct-looking event sequence after the fact, so
"one chunk cannot pass this" is not a test.

**The property is delivery before the terminal.** The test needs at least two upstream chunks under
the test's control, and must observe the first block's update **while the second chunk and the
terminal are still withheld**. That, and only that, rules out buffering to completion and replaying.

## 9. Mapping from the framework

`schema.Message` **cannot** support this contract:

- All `Content` is concatenated into one string with no separator, so **two adjacent text blocks are
  unrecoverable** (`schema/message.go:1698-1701`, `:1772-1783`).
- Fields in one chunk carry **no relative order**; each accumulates independently, and `ToolCalls` is
  sorted by index rather than arrival (`:1350-1363`).
- `ToolCall.Index` is a tool-call space, not a block space.

`schema.AgenticMessage` **can carry** it: `ContentBlock` is an ordered sum type over reasoning, text
and tool calls, and `ContentBlock.StreamingMeta.Index` spans all of them with sorted, type-checked
merging (`schema/agentic_message.go:102-124`, `:929-989`, `:1252-1267`).

**Carrier, not owner.** The framework will transport identity; it does not establish it:

- eino neither allocates the index nor guarantees it is unique across heterogeneous blocks.
- Concatenating multiple chunks drops the streaming metadata, and a single-input concat bypasses the
  mixed-metadata and type checks entirely — so validation cannot be assumed to have happened.

**Rule:** pi-go allocates block identity on its own port and validates it there, then converts
outward to the framework. Identity is never inferred from field transitions, and never taken on trust
from a framework value.

Two related facts, easy to conflate: `ContentBlock.StreamingMeta` is `json:"streaming_meta,omitempty"`
and **does** serialize; the type that does not is `MessageStreamingMeta`
(`schema/message.go:294`, `json:"-"`), which belongs to the flat message.

## 10. Open question, stated rather than assumed

Whether any given chat-model adapter populates `StreamingMeta`, and with what index discipline, is
not decidable from Pi or eino core — those adapters live in `eino-ext`. Until an adapter is chosen and
read, pi-go must not assume indices arrive populated; the port type carries block identity itself.

## 11. What this costs to implement

Not a field swap on the existing adapter. `ai.Response` today carries no reasoning content, no block
order and no block identity, and both the runtime and the framework loop pass `*schema.Message`.

So the work is, in order: extend the port with a native streaming surface that owns block identity;
state which single ingress lane carries that identity; and only then convert outward to the framework
type. Anything that starts by reshaping the existing adapter will be inferring boundaries again.
