# Streaming contract

**Status:** alignment table, written before implementation

**Pi baseline:** [`086c32e74530564922d011ade23ff582c9d63116`](https://github.com/earendil-works/pi/commit/086c32e74530564922d011ade23ff582c9d63116)

**Scope:** the observable event protocol of a streamed assistant reply, stated so each row can be
checked against Pi's source and against a pi-go test rather than argued about.

Pi's real path is streaming. pi-go currently delivers a whole reply as one chunk and says so in
`internal/runtime/einomodel.go`; that satisfies the framework's interface and proves nothing about
incremental delivery. This table is what "aligned" has to mean before any of it is written.

## 1. Event set

Pi source: `packages/ai/src/types.ts:523-539`.

| Event | Carries | Emitted |
| --- | --- | --- |
| `start` | `partial` | once, before any other event |
| `text_start` | `contentIndex`, `partial` | when a text block opens |
| `text_delta` | `contentIndex`, `delta`, `partial` | per text increment |
| `text_end` | `contentIndex`, `content`, `partial` | when that block closes |
| `thinking_start` / `thinking_delta` / `thinking_end` | as text, `content` on end | thinking block |
| `toolcall_start` / `toolcall_delta` | `contentIndex`, `delta`, `partial` | tool-call block |
| `toolcall_end` | `contentIndex`, `toolCall`, `partial` | when the call is complete |
| `done` | `reason`, `message` | terminal, success |
| `error` | `reason`, `error` | terminal, failure |

**Checkable:** the set is closed. An implementation emitting anything outside it, or omitting
`start`, is not aligned.

## 2. Ordering

1. `start` precedes every other event.
2. Exactly one terminal event ends the stream: `done` or `error`, never both, never neither.
3. Within one content block: `*_start` → zero or more `*_delta` → `*_end`.
4. Blocks are **not** required to be sequential. `contentIndex` exists so a consumer can attribute an
   event to a block without assuming the previous one closed first.

**Checkable:** a recorded event sequence can be replayed and each rule asserted independently.

## 3. `contentIndex`

Pi source: `packages/ai/src/api/anthropic-messages.ts:610,651-656`.

The index of the block in the assistant message's content list — `output.content.length - 1` when the
block is appended, and the block's position when a delta arrives. It identifies **which block this
event is about**, and is the only way to do so when blocks interleave.

**Checkable:** for every `*_delta`, the block at `contentIndex` in `partial` is the one that grew.

## 4. `partial` — the accumulated message, not the increment

Pi source: same file; every event carries `partial: output`, and `output` is mutated in place
(`block.text += event.delta.text`).

A consumer must be able to read the whole assistant message **as of this event** without keeping its
own accumulator. This is the property a single-chunk implementation satisfies trivially and therefore
the one that has to be tested with more than one chunk.

**Divergence, deliberate.** Pi passes a live reference to the mutating object. pi-go will deliver a
**snapshot per event**, because:

- Go delivers events across goroutines, and a shared mutable pointer is a data race, not a style
  choice.
- A consumer that holds an event would otherwise see the message change underneath it — this
  repository has already fixed two bugs of exactly that shape, where a reader could rewrite session
  history through what it was handed.

The observable guarantee is unchanged and strictly stronger: the message as of that event. What is
not carried over is the aliasing.

**Checkable:** hold every event; after the terminal event, each held `partial` still reads as it did
when delivered, and their contents grow monotonically.

## 5. Terminal states

Pi source: `types.ts:393` (`StopReason`), `anthropic-messages.ts:773,781-783`.

| Terminal | `reason` values | Carries |
| --- | --- | --- |
| `done` | `stop`, `length`, `toolUse`, `deferred` | the final message |
| `error` | `error`, `aborted` | the final message, with `errorMessage` set |

Cancellation is **not** a separate event: an aborted stream terminates as `error` with reason
`aborted`. `pending` is a state of a message, never a terminal reason.

**Checkable:** cancel mid-stream and assert one terminal `error`/`aborted` carrying the partial work,
not a truncated success.

## 6. Streaming scratch state must not survive

Pi source: `anthropic-messages.ts:776-780` — `index` and `partialJson` are deleted from every block
before the terminal event, with the note that `partialJson` is "only a streaming scratch buffer;
never persist it".

**Checkable:** nothing in the final message identifies how it was chunked. The same reply delivered
in one chunk or twenty produces the same final message.

## 7. What this rules out

A one-chunk implementation satisfies §1 and §5 and fails §2 (no per-block progression), §3 (no index
to attribute), §4 (one `partial`, which is also the final message) and §6 (trivially, because there
is nothing to strip). Any test that a single chunk can pass is not testing streaming.

## 8. What the framework gives us, and what it does not

eino's streamed chunk is a partial `schema.Message`
(`schema/message.go:499-528`): text arrives in `Content`, thinking in
`ReasoningContent`, and tool calls in `ToolCalls`, where `ToolCall.Index` is documented as
identifying "the chunk of the tool call for merging".

**There is no equivalent of `contentIndex`.** eino separates content by FIELD; Pi has one ordered list
of heterogeneous blocks with a single index space. A tool-call index is not a block index: it counts
tool calls, not content blocks, so text and thinking do not appear in it at all.

Consequences, and they are the load-bearing ones for the implementation:

- pi-go cannot pass framework chunks through and obtain Pi's protocol. It must maintain the block
  list itself and assign `contentIndex` in its own space.
- Block boundaries have to be derived. A chunk carrying `Content` after a chunk carrying
  `ReasoningContent` is a new block; a chunk carrying more `Content` continues the current one.
- The mapping is therefore part of the contract under test, not an implementation detail: if it is
  wrong, `contentIndex` is wrong, and every consumer that attributes a delta to a block is wrong with
  it.

**Checkable:** a stream that interleaves text, thinking and tool-call chunks produces a block list
whose indices are stable, contiguous from zero, and unchanged by how the provider chunked them.
