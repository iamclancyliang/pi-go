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

Everything mutable must be copied, not just the top level: the content slice, each block, tool-call
arguments (arbitrarily nested), `usage`, `usage.cost` (mutated in place after the fact,
`models.ts:892-896`), diagnostics and each diagnostic's details.

**Checkable, both directions:** a producer mutation after delivery must not appear in an already
delivered event; a consumer mutation of a delivered event must not appear in any later event or in
the terminal message.

**Not claimed:** that the message grows monotonically in structure. Blocks are edited in place and
scratch fields are removed, so later is not a superset of earlier.

## 5. Terminal states

| Terminal | `reason` | Carries |
| --- | --- | --- |
| `done` | `stop`, `length`, `toolUse`, `deferred` | final message |
| `error` | `error`, `aborted` | final message, `errorMessage` set |

Cancellation is not a separate event: it terminates as `error` with reason `aborted`.

## 6. Streaming scratch state must not survive

Scratch fields are removed before the terminal event on two different paths: **per block as it
closes** on the success path, and **a catch-all sweep** on the error path
(`anthropic-messages.ts:775-785`).

Fields observed across providers: `index`, `partialJson`, `partialArgs`, `customInput` (with a nested
`jsonBuffer`), `streamIndex`.

**Checkable:** nothing in the final message says how it was chunked.

**Known gap in Pi, not to be copied:** `openai-codex-responses.ts` and `mistral-conversations.ts` do
not strip `index`. pi-go strips uniformly; that is a deliberate improvement, recorded here so it is
not mistaken for divergence by omission.

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

`schema.AgenticMessage` **can**: `ContentBlock` is an ordered sum type over reasoning, text and tool
calls, and `ContentBlock.StreamingMeta.Index` spans all of them with sorted, type-checked merging
(`schema/agentic_message.go:102-124`, `:929-989`, `:1252-1267`).

**Rule:** the input seam must carry explicit ordered block identity. pi-go's own port type owns that
identity; where a framework type is involved it must be the agentic one. **If block identity is
absent, fail closed — do not infer boundaries from field transitions.**

Note: `MessageStreamingMeta` is `json:"-"` and does not survive serialization, so block identity
cannot be recovered from a serialized chunk.

## 10. Open question, stated rather than assumed

Whether any given chat-model adapter populates `StreamingMeta`, and with what index discipline, is
not decidable from Pi or eino core — those adapters live in `eino-ext`. Until an adapter is chosen and
read, pi-go must not assume indices arrive populated; the port type carries block identity itself.
