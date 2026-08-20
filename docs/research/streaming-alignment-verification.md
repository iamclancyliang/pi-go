# Streaming Alignment Verification

**Status:** Research note. Primary sources only.

**Where this lives and why:** the repo keeps normative specs in `docs/specs/`
(e.g. `docs/specs/streaming-contract.md`). This document is *not* a spec — it is a
source-verification record that settles seven disputed claims by quoting code at fixed
versions. `docs/research/` is the sibling home for that kind of evidence-gathering
artefact, so a spec can cite it without the spec itself carrying pages of quoted
TypeScript and Go. (`docs/research/` already exists and holds comparable notes.)

## Sources

| # | Source | Version pin | How it was read |
|---|--------|-------------|-----------------|
| 1 | Pi | commit `086c32e74530564922d011ade23ff582c9d63116` | `git archive 086c32e7… packages/ai/src` extracted to a scratch dir; the working tree at `/Users/ugreen/Project/github/pi` was never read. HEAD at time of writing is a different commit (`086c32e74 fix(ai): retry Copilot GET /models once on 429 during login` *is* the pin). |
| 2 | eino | `v0.9.14` | read directly from the read-only module cache at `/Users/ugreen/go/pkg/mod/github.com/cloudwego/eino@v0.9.14` |

All `path:line` references below are relative to `packages/ai/src/…` (Pi, at the pin) or
to the eino module root. No blogs, changelogs or secondary summaries were consulted.

---

## Claim 1 — Is `start` the guaranteed first event of every `AssistantMessageEventStream`?

### Verdict: **REFUTED.**

`start` is *not* guaranteed. A terminal `error` can be — and routinely is — the **first and
only** event on the stream. Three distinct classes of path produce this.

### Evidence

The doc comment states an intent, not a guarantee, and it says "should":

```
packages/ai/src/types.ts:515-522
/**
 * Event protocol for AssistantMessageEventStream.
 *
 * Streams should emit `start` before partial updates, then terminate with either:
 * - `done` carrying the final successful AssistantMessage, or
 * - `error` carrying the final AssistantMessage with stopReason "error" or "aborted"
 *   and errorMessage.
 */
```

**(a) The shared wrapper: `lazyStream`.** Every built-in API implementation is reached
through a `*.lazy.ts` module that wraps it in `lazyApi` → `lazyStream`. Setup failure
(auth resolution, dynamic `import()` failure, unsupported API) pushes `error` with no
prior `start`:

```
packages/ai/src/api/lazy.ts:41-61
/**
 * Returns a stream synchronously while running async setup (auth resolution,
 * lazy module loading) behind it. Setup failures terminate the stream with an
 * error event.
 */
export function lazyStream(
	model: Model<Api>,
	setup: () => Promise<AsyncIterable<AssistantMessageEvent>>,
): AssistantMessageEventStream {
	const outer = new AssistantMessageEventStream();

	setup()
		.then((inner) => forwardStream(outer, inner))
		.catch((error) => {
			const message = createSetupErrorMessage(model, error);
			outer.push({ type: "error", reason: "error", error: message });
			outer.end(message);
		});

	return outer;
}
```

```
packages/ai/src/api/anthropic-messages.lazy.ts:1-4
import type { ProviderStreams } from "../types.ts";
import { lazyApi } from "./lazy.ts";

export const anthropicMessagesApi = (): ProviderStreams => lazyApi(() => import("./anthropic-messages.ts"));
```

`lazyStream` is also the entry point for the `Models` façade and for provider dispatch:

```
packages/ai/src/models.ts:667-680
	stream<TApi extends Api>(
		model: Model<TApi>,
		context: Context,
		options?: ModelsApiStreamOptions<TApi>,
	): AssistantMessageEventStream {
		return lazyStream(model, async () => {
			const provider = this.requireProvider(model);
			const { requestModel, requestOptions } = await this.applyAuth(
				model,
				options as ModelsApiStreamOptions<Api> | undefined,
			);
			return provider.stream(requestModel as Model<TApi>, context, requestOptions as ApiStreamOptions<TApi>);
		});
	}
```

```
packages/ai/src/models.ts:781-792
	const dispatch = (
		model: Model<Api>,
		run: (streams: ProviderStreams) => AssistantMessageEventStream,
	): AssistantMessageEventStream => {
		const streams = apiFor(model);
		if (!streams) {
			return lazyStream(model, async () => {
				throw new ModelsError("stream", `Provider ${input.id} has no API implementation for "${model.api}"`);
			});
		}
		return run(streams);
	};
```

**(b) Provider/setup failure before the first push.** In every API implementation the
`start` push sits *after* client construction, param building, the `onPayload` hook and the
awaited HTTP request. Anything in that prefix that throws lands in the `catch` and emits a
bare `error`:

```
packages/ai/src/api/anthropic-messages.ts:574-583
			const response = await retryProviderRequest(
				() => client.messages.create({ ...params, stream: true }, requestOptions).asResponse(),
				{
					maxRetries: options?.maxRetries,
					maxRetryDelayMs: options?.maxRetryDelayMs,
					signal: options?.signal,
				},
			);
			await options?.onResponse?.({ status: response.status, headers: headersToRecord(response.headers) }, model);
			stream.push({ type: "start", partial: output });
```

The throwing prefix includes `assertRequestAuth(model.provider, apiKey, options?.headers);`
(`packages/ai/src/api/anthropic-messages.ts:537`).

**(c) Pre-cancelled signal.** A signal already aborted when `stream()` is called never
reaches the `start` push. `retryProviderRequest` converts any request rejection into an
abort error when the signal is set:

```
packages/ai/src/utils/provider-retry.ts:112-124
	for (;;) {
		try {
			// Each retry is a fresh SDK request, so X-Stainless-Retry-Count remains zero.
			return await request();
		} catch (error) {
			if (options.signal?.aborted) throw createAbortError();
			if (retriesRemaining <= 0 || !isProviderError(error) || !isRetryableProviderError(error)) throw error;
			…
```

and the local/faux transport checks the signal explicitly *before* pushing `start`, making
the pre-cancelled case unambiguous:

```
packages/ai/src/providers/faux.ts:346-354
	const partial: AssistantMessage = { ...message, content: [], stopReason: "pending" };
	if (signal?.aborted) {
		const aborted = createAbortedMessage(partial);
		stream.push({ type: "error", reason: "aborted", error: aborted });
		stream.end(aborted);
		return;
	}

	stream.push({ type: "start", partial: { ...partial } });
```

**Additional finding (not asked, but load-bearing): `start` is not guaranteed to be emitted
*at all* on a successful-looking stream, and Bedrock gates it on a provider event.**

```
packages/ai/src/api/bedrock-converse-stream.ts:264-269
			for await (const item of response.stream!) {
				if (item.messageStart) {
					if (item.messageStart.role !== ConversationRole.ASSISTANT) {
						throw new Error("Unexpected assistant message start but got user message start instead");
					}
					stream.push({ type: "start", partial: output });
```

Bedrock emits `start` once per `messageStart` frame from the provider; a stream that errors
before `messageStart` produces `error` with no `start`, and nothing in the code caps `start`
at one occurrence.

Similarly, `pi-messages` forwards a `start` only if the backend sends one:

```
packages/ai/src/api/pi-messages.ts:404-412
			for await (const piEvent of readPiMessagesEvents(response.body)) {
				const event = convertEvent(piEvent);
				eventStream.push(event);
				if (event.type === "done" || event.type === "error") {
					return;
				}
			}

			throw new Error(`${model.provider} stream ended without a terminal event`);
```

### Call sites of `{ type: "start" }` (complete, at the pin)

| Path | Position relative to network I/O |
|---|---|
| `api/anthropic-messages.ts:583` | after awaited request + `onResponse` |
| `api/openai-responses.ts:159` | after awaited request + `onResponse` |
| `api/azure-openai-responses.ts:127` | after awaited request + `onResponse` |
| `api/openai-completions.ts:256` | after awaited request + `onResponse` |
| `api/openai-codex-responses.ts:311`, `:465` | guarded by `startEmitted`; WS path emits on connect callback, SSE path after response ok |
| `api/google-generative-ai.ts:94` | after awaited request |
| `api/google-vertex.ts:112` | after awaited request |
| `api/mistral-conversations.ts:146` | after awaited request |
| `api/bedrock-converse-stream.ts:269` | only on a provider `messageStart` frame |
| `providers/faux.ts:354` | after an explicit `signal?.aborted` early return |
| `api/pi-messages.ts` | none — `start` is only relayed from the backend (`:53`, `:208-209`) |

---

## Claim 2 — Which events carry `partial`? Can a `*_start` be orphaned?

### Verdict: **CONFIRMED** (both halves).

### Evidence — the 10 non-terminal events carry `partial`; terminals do not

```
packages/ai/src/types.ts:523-539
export type AssistantMessageEvent =
	| { type: "start"; partial: AssistantMessage }
	| { type: "text_start"; contentIndex: number; partial: AssistantMessage }
	| { type: "text_delta"; contentIndex: number; delta: string; partial: AssistantMessage }
	| { type: "text_end"; contentIndex: number; content: string; partial: AssistantMessage }
	| { type: "thinking_start"; contentIndex: number; partial: AssistantMessage }
	| { type: "thinking_delta"; contentIndex: number; delta: string; partial: AssistantMessage }
	| { type: "thinking_end"; contentIndex: number; content: string; partial: AssistantMessage }
	| { type: "toolcall_start"; contentIndex: number; partial: AssistantMessage }
	| { type: "toolcall_delta"; contentIndex: number; delta: string; partial: AssistantMessage }
	| { type: "toolcall_end"; contentIndex: number; toolCall: ToolCall; partial: AssistantMessage }
	| {
			type: "done";
			reason: Extract<StopReason, "stop" | "length" | "toolUse" | "deferred">;
			message: AssistantMessage;
	  }
	| { type: "error"; reason: Extract<StopReason, "aborted" | "error">; error: AssistantMessage };
```

Exactly 10 non-terminal variants, each with `partial: AssistantMessage`. `done` carries
`message` (no `partial`); `error` carries `error` (no `partial`). The terminal payload field
is what the stream's result extractor reads:

```
packages/ai/src/utils/event-stream.ts:69-83
export class AssistantMessageEventStream extends EventStream<AssistantMessageEvent, AssistantMessage> {
	constructor() {
		super(
			(event) => event.type === "done" || event.type === "error",
			(event) => {
				if (event.type === "done") {
					return event.message;
				} else if (event.type === "error") {
					return event.error;
				}
				throw new Error("Unexpected event type for final result");
			},
		);
	}
}
```

### Evidence — orphaned `*_start` with no matching `*_end`

There is nothing anywhere in the error paths that synthesises missing `*_end` events. The
`catch` blocks strip scratch fields and push `error` directly. Concrete path in
`anthropic-messages.ts`:

1. `content_block_start` of type `text` pushes `text_start` and nothing has closed it yet:

```
packages/ai/src/api/anthropic-messages.ts:603-610
					if (event.content_block.type === "text") {
						const block: Block = {
							type: "text",
							text: event.content_block.text ?? "",
							index: event.index,
						};
						output.content.push(block);
						stream.push({ type: "text_start", contentIndex: output.content.length - 1, partial: output });
```

2. The SSE iterator throws mid-block — e.g. the connection drops, or an `error` SSE frame
   arrives, or the signal aborts:

```
packages/ai/src/api/anthropic-messages.ts:411-415
		while (true) {
			if (signal?.aborted) {
				throw new Error("Request was aborted");
			}
```

```
packages/ai/src/api/anthropic-messages.ts:472-475
	for await (const sse of iterateSseMessages(response.body, signal)) {
		if (sse.event === "error") {
			throw new Error(sse.data);
		}
```

```
packages/ai/src/api/anthropic-messages.ts:497-499
	if (sawMessageStart && !sawMessageEnd) {
		throw new Error("Anthropic stream ended before message_stop");
	}
```

3. The `catch` emits `error` immediately. No `text_end` is ever pushed:

```
packages/ai/src/api/anthropic-messages.ts:775-785
		} catch (error) {
			for (const block of output.content) {
				delete (block as { index?: number }).index;
				// partialJson is only a streaming scratch buffer; never persist it.
				delete (block as { partialJson?: string }).partialJson;
			}
			output.stopReason = options?.signal?.aborted ? "aborted" : "error";
			output.errorMessage = error instanceof Error ? error.message : JSON.stringify(error);
			stream.push({ type: "error", reason: output.stopReason, error: output });
			stream.end();
		}
```

The same shape holds in every other implementation (`openai-responses.ts:180-191`,
`azure-openai-responses.ts:144-155`, `openai-completions.ts:591-611`,
`openai-codex-responses.ts:476-486`, `bedrock-converse-stream.ts:311-324`,
`google-generative-ai.ts:279-290`, `google-vertex.ts:296-307`,
`mistral-conversations.ts:162-171`). None of them close open blocks.

---

## Claim 3 — Is `partial` the SAME mutable object across events?

### Verdict: **CONFIRMED for all real providers.** **PARTIAL for the faux/local transport**,
which shallow-copies the top level but still aliases the individual content blocks.

### Evidence — full aliasing (the normal case)

A single `output` object is created once and passed by reference into every event:

```
packages/ai/src/api/anthropic-messages.ts:510-526
		const output: AssistantMessage = {
			role: "assistant",
			content: [],
			api: model.api as Api,
			provider: model.provider,
			model: model.id,
			usage: {
				input: 0,
				output: 0,
				cacheRead: 0,
				cacheWrite: 0,
				totalTokens: 0,
				cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 },
			},
			stopReason: "pending",
			timestamp: Date.now(),
		};
```

```
packages/ai/src/api/anthropic-messages.ts:585-586
			type Block = (ThinkingContent | TextContent | (ToolCall & { partialJson: string })) & { index: number };
			const blocks = output.content as Block[];
```

`blocks` *is* `output.content` (a cast, not a copy). Every push uses `partial: output`, and
the terminal uses `message: output` / `error: output` — so the object handed to the consumer
in `done`/`error` is the very same object previously handed out as `partial`:

```
packages/ai/src/api/anthropic-messages.ts:773
			stream.push({ type: "done", reason: output.stopReason, message: output });
```

In `pi-messages` the converter closes over one `partial` and spreads only the *event*:

```
packages/ai/src/api/pi-messages.ts:176-189
function createEventConverter(model: Model<"pi-messages">) {
	const partial: AssistantMessage = {
		role: "assistant",
		content: [],
		…
	};
	const toolJson = new Map<number, string>();

	return (event: PiMessagesEvent): AssistantMessageEvent => {
```

```
packages/ai/src/api/pi-messages.ts:262
		return { ...event, partial } as AssistantMessageEvent;
```

### Evidence — the faux/local exception

`faux.ts` spreads the top level on every push, but mutates blocks in place, so blocks stay
aliased across snapshots:

```
packages/ai/src/providers/faux.ts:389-403
		if (block.type === "text") {
			partial.content = [...partial.content, { type: "text", text: "" }];
			stream.push({ type: "text_start", contentIndex: index, partial: { ...partial } });
			for (const chunk of splitStringByTokenSize(block.text, minTokenSize, maxTokenSize)) {
				await scheduleChunk(chunk, tokensPerSecond);
				…
				(partial.content[index] as TextContent).text += chunk;
				stream.push({ type: "text_delta", contentIndex: index, delta: chunk, partial: { ...partial } });
			}
			stream.push({ type: "text_end", contentIndex: index, content: block.text, partial: { ...partial } });
			continue;
		}
```

`partial.content = [...partial.content, …]` makes a *new array*, so older snapshots keep an
older array — but `(partial.content[index] as TextContent).text += chunk` mutates the block
object that those older arrays still point at. Aliasing at block granularity survives.

### Mutable descendants a Go port must deep-copy

Enumerated from the type definitions plus every observed mutation site:

| Descendant | Type | Mutated where (examples) |
|---|---|---|
| `content` (the array itself) | `(TextContent \| ThinkingContent \| ToolCall)[]` | `output.content.push(block)` — `anthropic-messages.ts:609,618,628,641`; index-assignment `partial.content[event.contentIndex] = {…}` — `pi-messages.ts:211,223,236` |
| individual `TextContent` | `{ type, text, textSignature? }` (`types.ts:338-342`) | `block.text += event.delta.text` — `anthropic-messages.ts:649`; whole-value overwrite `slot.block.text = …` — `openai-responses-shared.ts:699`; `textSignature` set at `:700` |
| individual `ThinkingContent` | `{ type, thinking, thinkingSignature?, redacted? }` (`types.ts:344-352`) | `block.thinking += …` — `anthropic-messages.ts:661`; `block.thinkingSignature += event.delta.signature` — `:687` |
| individual `ToolCall` | `{ type, id, name, arguments, thoughtSignature?, namespace? }` (`types.ts:360-368`) | `block.arguments = parseStreamingJson(...)` — `anthropic-messages.ts:674,710`; `slot.block.namespace = item.namespace` — `openai-responses-shared.ts:714,730` |
| `ToolCall.arguments` | `Record<string, any>` — **arbitrarily deep JSON** | replaced wholesale each delta by `parseStreamingJson`, and re-derived from the scratch buffer at block close (`anthropic-messages.ts:710`); for grammar tools it is rebuilt as `{ [property]: string }` — `openai-completions.ts:302` |
| `usage` | `Usage` (`types.ts:370-391`) | field-by-field in place — `anthropic-messages.ts:593-600` and `:734-757` |
| `usage.cost` | nested object `{input,output,cacheRead,cacheWrite,total}` | mutated in place, not replaced: `models.ts:892-896` `usage.cost.input = …; … usage.cost.total = …` |
| `diagnostics` | `AssistantMessageDiagnostic[]` (`utils/diagnostics.ts:8-13`) | `message.diagnostics = [...(message.diagnostics ?? []), diagnostic];` — `utils/diagnostics.ts:44` (array replaced, but the appended `diagnostic` objects — including `diagnostic.details: Record<string, unknown>` and `diagnostic.error` — are shared) |
| streaming scratch fields | `index`, `partialJson`, `partialArgs`, `customInput`, `streamIndex` | see Claim 4 — these live *on the same block objects* until stripped |
| scalars that still change after being observed | `stopReason`, `errorMessage`, `rawStopReason`, `responseId`, `responseModel`, `endTurn` | e.g. `anthropic-messages.ts:590,724-728,781-782` |

`customInput.jsonBuffer` is itself a mutable struct (`{ input: string; started: boolean;
closed: boolean }` — `openai-responses-shared.ts:404-407`,
`openai-completions.ts:260-263`) mutated by `appendGrammarToolInputJsonDelta`.

Immutable-by-construction and safe to share: `role`, `api`, `provider`, `model`, `timestamp`.

---

## Claim 4 — Scratch-field stripping before terminal events

### Verdict: **CONFIRMED**, with an important asymmetry: the success path strips
**per block, at block close**, while the error path strips **as a catch-all loop over
`output.content`** — and the two sets of fields are *not identical in every file*.

### The complete field inventory

Derived from every `delete` statement in `packages/ai/src/api/` (the exhaustive list; the
only non-block `delete`s in that directory are `anthropic-messages.ts:282` on a headers
record, `mistral-conversations.ts:421` on a mapping record,
`openai-codex-responses.ts:1048` on WS headers, and
`transform-messages.ts:133` on a normalised replay tool call — none are streaming scratch).

| Field | Meaning | Files that carry it |
|---|---|---|
| `index` | provider block index used to correlate deltas to blocks | `anthropic-messages.ts` (`:585`, set at `:607,616,626,639`), `bedrock-converse-stream.ts` (`:103`) |
| `partialJson` | raw accumulating tool-argument JSON | `anthropic-messages.ts` (`:585,638`), `bedrock-converse-stream.ts` (`:103,524`), `openai-responses-shared.ts` (`:403,492`) — the latter shared by `openai-responses.ts`, `azure-openai-responses.ts`, `openai-codex-responses.ts` |
| `partialArgs` | same idea, chat-completions naming | `openai-completions.ts` (`:259,399`), `mistral-conversations.ts` (`:693,708`) |
| `customInput` | `{ property, jsonBuffer }` state for OpenAI grammar/custom tools | `openai-completions.ts` (`:260-263`), `openai-responses-shared.ts` (`:404-407`) |
| `streamIndex` | OpenAI chat-completions `tool_calls[].index` used to match delta chunks | `openai-completions.ts` (`:264`) |

No other provider-specific streaming scratch field exists on content blocks at this pin.
`thoughtSignature`, `textSignature`, `thinkingSignature`, `namespace` and `redacted` are
**persisted** parts of the public content types (`types.ts:341,347,351,365,367`), not scratch.

### Success path — per-block clearing at block close

Anthropic clears `index` for *every* block type, and `partialJson` only for tool calls:

```
packages/ai/src/api/anthropic-messages.ts:690-720
				} else if (event.type === "content_block_stop") {
					const index = blocks.findIndex((b) => b.index === event.index);
					const block = blocks[index];
					if (block) {
						delete (block as any).index;
						if (block.type === "text") {
							…
						} else if (block.type === "toolCall") {
							block.arguments = parseStreamingJson(block.partialJson);
							// Finalize in-place and strip the scratch buffer so replay only
							// carries parsed arguments.
							delete (block as { partialJson?: string }).partialJson;
							stream.push({
								type: "toolcall_end",
								contentIndex: index,
								toolCall: block,
								partial: output,
							});
						}
					}
```

Bedrock, same shape:

```
packages/ai/src/api/bedrock-converse-stream.ts:611-629
	const index = blocks.findIndex((b) => b.index === event.contentBlockIndex);
	const block = blocks[index];
	if (!block) return;
	delete (block as Block).index;

	switch (block.type) {
		…
		case "toolCall":
			block.arguments = parseStreamingJson(block.partialJson);
			// Finalize in-place and strip the scratch buffer so replay only
			// carries parsed arguments.
			delete (block as Block).partialJson;
			stream.push({ type: "toolcall_end", contentIndex: index, toolCall: block, partial: output });
			break;
	}
```

OpenAI chat-completions strips three fields at once, and only for tool calls:

```
packages/ai/src/api/openai-completions.ts:324-348
				} else if (block.type === "toolCall") {
					if (block.customInput) {
						const delta = appendCustomToolCallInput(block, getCustomToolCallInput(block), true);
						…
					} else {
						block.arguments = parseStreamingJson(block.partialArgs);
					}
					// Finalize in-place and strip the scratch buffers so replay only
					// carries parsed arguments.
					delete block.partialArgs;
					delete block.customInput;
					delete block.streamIndex;
					stream.push({
						type: "toolcall_end",
						contentIndex,
						toolCall: block,
						partial: output,
					});
				}
```

OpenAI Responses (shared by `openai-responses`, `azure-openai-responses`,
`openai-codex-responses`) strips `partialJson` on function calls and `customInput` on custom
tool calls, on two *different* branches:

```
packages/ai/src/api/openai-responses-shared.ts:708-731
			} else if (
				item.type === "function_call" &&
				slot?.type === "toolCall" &&
				slot.block.partialJson !== undefined
			) {
				slot.block.arguments = parseStreamingJson(item.arguments || slot.block.partialJson || "{}");
				if (item.namespace !== undefined) slot.block.namespace = item.namespace;
				// Finalize in-place and strip the scratch buffer so replay only
				// carries parsed arguments.
				delete slot.block.partialJson;
				…
			} else if (item.type === "custom_tool_call" && slot?.type === "toolCall" && slot.block.customInput) {
				…
				delete slot.block.customInput;
```

Mistral:

```
packages/ai/src/api/mistral-conversations.ts:734-738
	const toolBlock = block as ToolCall & { partialArgs?: string };
	toolBlock.arguments = parseStreamingJson<Record<string, unknown>>(toolBlock.partialArgs);
	…
	delete toolBlock.partialArgs;
```

### Error path — catch-all loop over every block

| File:line | Fields cleared in `catch` |
|---|---|
| `api/anthropic-messages.ts:776-780` | `index`, `partialJson` |
| `api/bedrock-converse-stream.ts:312-316` | `index`, `partialJson` |
| `api/openai-responses.ts:181-186` | `index`, `partialJson`, `customInput` |
| `api/azure-openai-responses.ts:145-150` | `index`, `partialJson`, `customInput` |
| `api/openai-codex-responses.ts:477-481` | `partialJson`, `customInput` (**no `index`**) |
| `api/openai-completions.ts:592-598` | `index`, `partialArgs`, `customInput`, `streamIndex` |
| `api/mistral-conversations.ts:163-166` | `partialArgs` only (**no `index`**) |
| `api/google-generative-ai.ts:280-285` | `index` only |
| `api/google-vertex.ts:297-302` | `index` only |
| `api/pi-messages.ts` | **none** — the error path discards the accumulated message entirely (Claim 5) |

```
packages/ai/src/api/openai-completions.ts:591-598
		} catch (error) {
			for (const block of output.content) {
				delete (block as { index?: number }).index;
				// Streaming scratch buffers are only used during parsing; never persist them.
				delete (block as { partialArgs?: string }).partialArgs;
				delete (block as { customInput?: unknown }).customInput;
				delete (block as { streamIndex?: number }).streamIndex;
			}
```

```
packages/ai/src/api/google-vertex.ts:296-302
		} catch (error) {
			// Remove internal index property used during streaming
			for (const block of output.content) {
				if ("index" in block) {
					delete (block as { index?: number }).index;
				}
			}
```

Note two of these are **defensive no-ops** at this pin: `index` is never assigned to a block
in the Google implementations (the only occurrences of `index` on blocks in
`google-generative-ai.ts` are the `delete` at `:280-285`; block ordering is tracked by the
local `blockIndex = () => blocks.length - 1` at `:97`), and likewise `index` is never
assigned in `openai-responses-shared.ts`, which correlates blocks via an `outputSlots`
`Map<number, ResponsesOutputSlot>` instead. The `delete … .index` in `openai-responses.ts:182`
and `azure-openai-responses.ts:146` therefore has nothing to delete.

**Timing:** on the success path the strip happens *before* the `toolcall_end` event that
exposes the block as `toolCall`, and therefore before `done`. On the error path it happens
inside `catch`, immediately before `stream.push({ type: "error", … })`. In both cases,
consumers that captured an earlier `partial` alias will observe the fields **disappear
retroactively** from objects they already hold — a direct consequence of Claim 3.

---

## Claim 5 — On abort, does Pi ALWAYS preserve already-produced partial content?

### Verdict: **REFUTED.** Anthropic (and every other in-process provider) preserves;
`pi-messages` **discards** and emits a brand-new assistant message with **empty content**.
`lazyStream` likewise emits an empty-content message for setup failures.

### Evidence — preserving path (`anthropic-messages.ts`)

`output` is the accumulator; the abort path mutates it and re-emits *the same object*, so all
text/thinking/tool-call blocks produced up to the abort survive, along with the usage
captured at `message_start`:

```
packages/ai/src/api/anthropic-messages.ts:775-785
		} catch (error) {
			for (const block of output.content) {
				delete (block as { index?: number }).index;
				// partialJson is only a streaming scratch buffer; never persist it.
				delete (block as { partialJson?: string }).partialJson;
			}
			output.stopReason = options?.signal?.aborted ? "aborted" : "error";
			output.errorMessage = error instanceof Error ? error.message : JSON.stringify(error);
			stream.push({ type: "error", reason: output.stopReason, error: output });
			stream.end();
		}
```

The intent is explicit in a comment on the usage capture:

```
packages/ai/src/api/anthropic-messages.ts:591-593
					// Capture initial token usage from message_start event
					// This ensures we have input token counts even if the stream is aborted early
					output.usage.input = event.message.usage.input_tokens || 0;
```

The same preserve-the-accumulator shape holds for `openai-responses.ts:180-191`,
`azure-openai-responses.ts:144-155`, `openai-completions.ts:591-611`,
`openai-codex-responses.ts:476-486`, `bedrock-converse-stream.ts:311-324`,
`google-generative-ai.ts:279-290`, `google-vertex.ts:296-307`,
`mistral-conversations.ts:162-171`.

### Evidence — discarding path (`pi-messages.ts`)

A **new** `AssistantMessage` is built with `content: []`, ignoring the `partial` the event
converter has been accumulating in its closure:

```
packages/ai/src/api/pi-messages.ts:313-335
function createErrorEvent(model: Model<"pi-messages">, error: unknown, aborted: boolean): AssistantMessageEvent {
	const reason = aborted ? "aborted" : "error";
	const assistantMessage: AssistantMessage = {
		role: "assistant",
		content: [],
		api: model.api,
		provider: model.provider,
		model: model.id,
		usage: createEmptyUsage(),
		stopReason: reason,
		errorMessage: error instanceof Error ? error.message : String(error),
		timestamp: Date.now(),
	};

	if (!aborted && error instanceof PiMessagesResponseError) {
		appendAssistantMessageDiagnostic(
			assistantMessage,
			createAssistantMessageDiagnostic("pi_messages_response_failure", error, error.diagnosticDetails),
		);
	}

	return { type: "error", reason, error: assistantMessage };
}
```

and it is the **only** thing the `catch` can emit — including on abort, where the SSE
reader's `read()` rejects mid-stream:

```
packages/ai/src/api/pi-messages.ts:404-415
			for await (const piEvent of readPiMessagesEvents(response.body)) {
				const event = convertEvent(piEvent);
				eventStream.push(event);
				if (event.type === "done" || event.type === "error") {
					return;
				}
			}

			throw new Error(`${model.provider} stream ended without a terminal event`);
		} catch (error) {
			eventStream.push(createErrorEvent(model, error, options?.signal?.aborted ?? false));
		}
```

The accumulator that *does* hold the produced content is unreachable from `createErrorEvent`
— it lives in a closure inside `createEventConverter` and is only surfaced when the *backend*
sends a terminal `done`/`error` frame:

```
packages/ai/src/api/pi-messages.ts:199-207
			case "error":
				Object.assign(partial, {
					stopReason: event.reason,
					usage: event.usage,
					errorMessage: event.errorMessage,
					responseId: event.responseId,
				});
				appendRewriteDiagnostic(partial, event.rewrite);
				return { type: "error", reason: event.reason, error: partial };
```

So: a client-side abort of a `pi-messages` stream loses everything (content **and** usage);
a server-side terminal `error` frame preserves everything. Two different behaviours behind
one event type.

### Evidence — the local/faux transport preserves on abort but not on throw

`faux.ts` abort path *does* preserve, by spreading the accumulator:

```
packages/ai/src/providers/faux.ts:321-328
function createAbortedMessage(partial: AssistantMessage): AssistantMessage {
	return {
		...partial,
		stopReason: "aborted",
		errorMessage: "Request was aborted",
		timestamp: Date.now(),
	};
}
```

but its generic `catch` builds an empty one:

```
packages/ai/src/providers/faux.ts:307-319
function createErrorMessage(error: unknown, api: string, provider: string, modelId: string): AssistantMessage {
	return {
		role: "assistant",
		content: [],
		api,
		provider,
		model: modelId,
		usage: DEFAULT_USAGE,
		stopReason: "error",
		errorMessage: error instanceof Error ? error.message : String(error),
		timestamp: Date.now(),
	};
}
```

### Evidence — the shared wrapper always emits empty content

```
packages/ai/src/api/lazy.ts:4-23
function createSetupErrorMessage(model: Model<Api>, error: unknown): AssistantMessage {
	return {
		role: "assistant",
		content: [],
		api: model.api,
		provider: model.provider,
		model: model.id,
		usage: {
			input: 0,
			output: 0,
			cacheRead: 0,
			cacheWrite: 0,
			totalTokens: 0,
			cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 },
		},
		stopReason: "error",
		errorMessage: error instanceof Error ? error.message : String(error),
		timestamp: Date.now(),
	};
}
```

This is correct there (nothing has been produced yet), but it means "terminal `error` carries
an empty-content message" is a legitimate, reachable state that a consumer must handle.

---

## Claim 6 — eino `AssistantGenMultiContent[].StreamingMeta.Index`

### Verdict: **CONFIRMED** that the field exists and is documented as a streaming position /
merge identity for text *and* reasoning parts. **REFUTED** for the "unified heterogeneous
index space spanning text + thinking + tool calls" — *in `schema.Message`*. **CONFIRMED**
that such a unified space does exist, but in a **different type**: `schema.AgenticMessage`'s
`ContentBlock.StreamingMeta.Index`.

### Evidence — the field exists, on `MessageOutputPart`

```
schema/message.go:512-513
	// AssistantGenMultiContent is for receiving multimodal output from the model.
	AssistantGenMultiContent []MessageOutputPart `json:"assistant_output_multi_content,omitempty"`
```

```
schema/message.go:259-295
// MessageStreamingMeta contains metadata for streaming responses.
// It is used to track position of part when the model outputs multiple parts in a single response.
type MessageStreamingMeta struct {
	// Index specifies the index position of this part in the final response.
	// This is useful for reassembling multiple reasoning/content parts in correct order.
	Index int `json:"index,omitempty"`
}

// MessageOutputPart represents a part of an assistant-generated message.
// It can contain text, or multimedia content like images, audio, or video.
type MessageOutputPart struct {
	// Type is the type of the part, e.g. "text", "image_url", "audio_url", "video_url".
	Type ChatMessagePartType `json:"type"`

	// Text is the text of the part, it's used when Type is "text".
	Text string `json:"text,omitempty"`
	…
	// Reasoning contains the reasoning content generated by the model.
	// Used when Type is ChatMessagePartTypeReasoning.
	Reasoning *MessageOutputReasoning `json:"reasoning,omitempty"`

	// Extra is used to store extra information.
	Extra map[string]any `json:"extra,omitempty"`

	// StreamingMeta contains metadata for streaming responses.
	// This field is typically used at runtime and not serialized.
	StreamingMeta *MessageStreamingMeta `json:"-"`
}
```

Note `json:"-"` — it is runtime-only and does not survive serialization.

### Evidence — it is genuinely the merge identity, and it covers text *and* reasoning

```
schema/message.go:1408-1433
func canMergeOutputParts(current, next MessageOutputPart) bool {
	if current.Type != next.Type {
		return false
	}

	if !isMergeableOutputPartType(current) {
		return false
	}

	if current.StreamingMeta != nil && next.StreamingMeta != nil {
		return current.StreamingMeta.Index == next.StreamingMeta.Index
	}

	return current.StreamingMeta == nil && next.StreamingMeta == nil
}

func isMergeableOutputPartType(part MessageOutputPart) bool {
	switch part.Type {
	case ChatMessagePartTypeText, ChatMessagePartTypeReasoning:
		return true
	case ChatMessagePartTypeAudioURL:
		return isBase64MessageOutputAudioPart(part)
	default:
		return false
	}
}
```

Both text and reasoning parts are merged by `Index`, and the merged part inherits the first
chunk's meta:

```
schema/message.go:1476-1481
	return MessageOutputPart{
		Type:          ChatMessagePartTypeText,
		Text:          sb.String(),
		Extra:         mergedExtra,
		StreamingMeta: group[0].StreamingMeta,
	}, nil
```

```
schema/message.go:1507-1515
	return MessageOutputPart{
		Type: ChatMessagePartTypeReasoning,
		Reasoning: &MessageOutputReasoning{
			Text:      textBuilder.String(),
			Signature: signature,
		},
		Extra:         mergedExtra,
		StreamingMeta: group[0].StreamingMeta,
	}, nil
```

**Important limitation — grouping is adjacency-based, not index-based:**

```
schema/message.go:1387-1406
func groupOutputParts(parts []MessageOutputPart) [][]MessageOutputPart {
	if len(parts) == 0 {
		return nil
	}

	groups := make([][]MessageOutputPart, 0)
	currentGroup := []MessageOutputPart{parts[0]}

	for i := 1; i < len(parts); i++ {
		if canMergeOutputParts(currentGroup[0], parts[i]) {
			currentGroup = append(currentGroup, parts[i])
		} else {
			groups = append(groups, currentGroup)
			currentGroup = []MessageOutputPart{parts[i]}
		}
	}
	groups = append(groups, currentGroup)

	return groups
}
```

`Index` only *separates* adjacent runs; there is **no sort by `Index`** and no regrouping of
non-adjacent parts that share an `Index`. Interleaved chunks for index 0 and index 1 would
produce four groups, not two. Contrast this with tool calls, which *are* regrouped and sorted
(below), and with `AgenticMessage`, which *is* regrouped and sorted.

### Evidence — relation to `schema.ToolCall.Index`

`ToolCall.Index` is a **separate, independent** index space on a **different field** of
`Message`:

```
schema/message.go:133-145
type ToolCall struct {
	// Index is used when there are multiple tool calls in a message.
	// In stream mode, it's used to identify the chunk of the tool call for merging.
	Index *int `json:"index,omitempty"`
	// ID is the id of the tool call, it can be used to identify the specific tool call.
	ID string `json:"id"`
	// Type is the type of the tool call, default is "function".
	Type string `json:"type"`
	// Function is the function call to be made.
	Function FunctionCall `json:"function"`
	// Extra is used to store extra information for the tool call.
	Extra map[string]any `json:"extra,omitempty"`
}
```

```
schema/message.go:517-518
	// only for AssistantMessage
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
```

Differences that matter for a port:

- `ToolCall.Index` is `*int` (nilable; nil means "not chunked, append as-is" —
  `schema/message.go:1288-1293`); `MessageStreamingMeta.Index` is a non-pointer `int`
  reached through a nilable `*MessageStreamingMeta`, so "absent" is expressed one level up.
- `ToolCall.Index` groups **globally** via a map and then **sorts**:
  `m := make(map[int][]int)` (`:1286`) … `sort.SliceStable(merged, …)` (`:1351-1362`).
  `MessageStreamingMeta.Index` groups only by **adjacency** and never sorts.
- They index different collections. Nothing relates a `ToolCall.Index` of 0 to an
  `AssistantGenMultiContent` `StreamingMeta.Index` of 0.

### Evidence — no unified heterogeneous space in `schema.Message`

`ConcatMessages` handles each field in its own silo and never records cross-field position:

```
schema/message.go:1698-1725
		if msg.Content != "" {
			contents = append(contents, msg.Content)
			contentLen += len(msg.Content)
		}
		if msg.ReasoningContent != "" {
			reasoningContents = append(reasoningContents, msg.ReasoningContent)
			reasoningContentLen += len(msg.ReasoningContent)
		}

		if len(msg.ToolCalls) > 0 {
			toolCalls = append(toolCalls, msg.ToolCalls...)
		}
		…
		if len(msg.AssistantGenMultiContent) > 0 {
			assistantGenMultiContentParts = append(assistantGenMultiContentParts, msg.AssistantGenMultiContent...)
		}
```

`Content` and `ReasoningContent` are plain strings on `Message` with no index at all:

```
schema/message.go:501-502
	// Content is for user text input and model text output.
	Content string `json:"content"`
```

```
schema/message.go:527-528
	// ReasoningContent is the thinking process of the model, which will be included when the model returns reasoning content.
	ReasoningContent string `json:"reasoning_content,omitempty"`
```

### Evidence — a unified space DOES exist, in `schema.AgenticMessage`

`ContentBlock` is a single sum type covering reasoning, assistant text, function tool calls,
server tool calls, MCP calls and results — and it carries one `StreamingMeta`:

```
schema/agentic_message.go:102-178
type ContentBlock struct {
	Type ContentBlockType `json:"type"`

	// Reasoning contains the reasoning content generated by the model.
	Reasoning *Reasoning `json:"reasoning,omitempty"`
	…
	// AssistantGenText contains the text content generated by the model.
	AssistantGenText *AssistantGenText `json:"assistant_gen_text,omitempty"`
	…
	// FunctionToolCall contains the invocation details for a user-defined tool.
	FunctionToolCall *FunctionToolCall `json:"function_tool_call,omitempty"`
	…
	// StreamingMeta contains metadata for streaming responses.
	// Only set for streaming responses.
	StreamingMeta *StreamingMeta `json:"streaming_meta,omitempty"`

	// Extra contains additional information for the content block.
	Extra map[string]any `json:"extra,omitempty"`
}

type StreamingMeta struct {
	// Index specifies the index position of this block in the final response.
	Index int `json:"index"`
}
```

`ConcatAgenticMessages` groups by that index globally, sorts, and forbids mixing indexed and
non-indexed blocks:

```
schema/agentic_message.go:929-989
		for _, block := range msg.ContentBlocks {
			if block == nil {
				continue
			}
			if block.StreamingMeta == nil {
				// Non-streaming block
				if len(blockIndices) > 0 {
					// Cannot mix streaming and non-streaming blocks
					return nil, fmt.Errorf("found non-streaming block after streaming blocks")
				}
				// Collect non-streaming block
				blocks = append(blocks, block)
			} else {
				// Streaming block
				if len(blocks) > 0 {
					// Cannot mix non-streaming and streaming blocks
					return nil, fmt.Errorf("found streaming block after non-streaming blocks")
				}
				// Collect streaming block by index
				if blocks_, ok := indexToBlocks[block.StreamingMeta.Index]; ok {
					indexToBlocks[block.StreamingMeta.Index] = append(blocks_, block)
				} else {
					blockIndices = append(blockIndices, block.StreamingMeta.Index)
					indexToBlocks[block.StreamingMeta.Index] = []*ContentBlock{block}
				}
			}
		}
		…
	if len(blockIndices) > 0 {
		// All blocks are streaming, concat each group by index
		indexToBlock := map[int]*ContentBlock{}
		for idx, bs := range indexToBlocks {
			var b *ContentBlock
			b, err = concatChunksOfSameContentBlock(bs)
			…
		}
		blocks = make([]*ContentBlock, 0, len(blockIndices))
		sort.Slice(blockIndices, func(i, j int) bool {
			return blockIndices[i] < blockIndices[j]
		})
		for _, idx := range blockIndices {
			blocks = append(blocks, indexToBlock[idx])
		}
	}
```

and each index group must be type-homogeneous:

```
schema/agentic_message.go:1252-1267
func concatContentBlockHelper[T contentBlockVariant](
	blocks []*ContentBlock,
	expectedType ContentBlockType,
	getter func(*ContentBlock) *T,
	concatFunc func([]*T) (*T, error),
) (*ContentBlock, error) {
	items, err := genericGetTFromContentBlocks(blocks, func(block *ContentBlock) (*T, error) {
		if block.Type != expectedType {
			return nil, fmt.Errorf("content block type mismatch: expected '%s', but got '%s'", expectedType, block.Type)
		}
		item := getter(block)
		if item == nil {
			return nil, fmt.Errorf("'%s' content is nil", expectedType)
		}
		return item, nil
	})
```

The public constructor for producing such chunks:

```
schema/agentic_message.go:654-659
// NewContentBlockChunk creates a new ContentBlock with the given content and streaming metadata.
func NewContentBlockChunk[T contentBlockVariant](content *T, meta *StreamingMeta) *ContentBlock {
	block := NewContentBlock(content)
	block.StreamingMeta = meta
	return block
}
```

Producers inside eino do assign this index across heterogeneous blocks, e.g.:

```
compose/agentic_tools_node.go:142
					StreamingMeta:      &schema.StreamingMeta{Index: i},
```

```
adk/wrappers.go:597-607
func markAgenticMessageStreamingMeta(msg *schema.AgenticMessage, index int) {
	if msg == nil {
		return
	}
	for _, block := range msg.ContentBlocks {
		if block == nil {
			continue
		}
		block.StreamingMeta = &schema.StreamingMeta{Index: index}
	}
}
```

**Summary of the two index spaces in eino v0.9.14:**

| | `schema.Message` | `schema.AgenticMessage` |
|---|---|---|
| text position | none (`Content string`, concatenated) | `ContentBlock.StreamingMeta.Index` |
| reasoning position | none (`ReasoningContent string`) *or* `MessageOutputPart.StreamingMeta.Index` if the model uses `AssistantGenMultiContent` | `ContentBlock.StreamingMeta.Index` |
| tool-call position | `ToolCall.Index *int`, separate space | `ContentBlock.StreamingMeta.Index`, same space |
| unified heterogeneous space | **no** | **yes** |
| grouping | multi-content: adjacency only; tool calls: map + sort | map + sort, homogeneity enforced |

---

## Claim 7 — Can content-block boundaries be derived from field transitions alone?

### Verdict: **REFUTED for `schema.Message`.** Neither sub-question can be answered
affirmatively from the chunk shape. **CONFIRMED possible for `schema.AgenticMessage`**,
where the block identity is carried explicitly rather than derived.

### (a) Two ADJACENT text blocks — cannot be told apart

`Message.Content` is a single `string`. Every non-empty chunk `Content` is appended to one
flat list and joined into one string with no separator and no boundary marker:

```
schema/message.go:1698-1701
		if msg.Content != "" {
			contents = append(contents, msg.Content)
			contentLen += len(msg.Content)
		}
```

```
schema/message.go:1772-1783
	if len(contents) > 0 {
		var sb strings.Builder
		sb.Grow(contentLen)
		for _, content := range contents {
			_, err := sb.WriteString(content)
			if err != nil {
				return nil, err
			}
		}

		ret.Content = sb.String()
	}
```

There is no state, no sentinel and no metadata that distinguishes "chunk 5 continues block A"
from "chunk 5 starts block B". A gap in `Content` (an empty-`Content` chunk) is
indistinguishable from a keepalive or a chunk that only carried usage: the `!= ""` guard at
`:1698` drops it silently. Therefore **two adjacent text blocks are unrecoverable** from
`Content` transitions.

The one place a boundary *can* exist is `AssistantGenMultiContent`, and only if the model
implementation populates `StreamingMeta` with distinct indices — and even then only for
adjacent runs (`groupOutputParts`, `schema/message.go:1387-1406`, quoted under Claim 6).
Whether a given chat-model implementation does so **is not determined by this module**;
`schema` only defines the merge rule.

### (b) Multiple fields set in ONE chunk — no defined relative order

Nothing in `schema` defines an order between `Content`, `ReasoningContent` and `ToolCalls`
within a chunk, or between a chunk's `Content` and a *previous* chunk's `ReasoningContent`.
`ConcatMessages` routes each field into its own accumulator and emits each into its own
output field:

```
schema/message.go:1702-1709
		if msg.ReasoningContent != "" {
			reasoningContents = append(reasoningContents, msg.ReasoningContent)
			reasoningContentLen += len(msg.ReasoningContent)
		}

		if len(msg.ToolCalls) > 0 {
			toolCalls = append(toolCalls, msg.ToolCalls...)
		}
```

```
schema/message.go:1784-1804
	if len(reasoningContents) > 0 {
		var sb strings.Builder
		sb.Grow(reasoningContentLen)
		for _, rc := range reasoningContents {
			_, err := sb.WriteString(rc)
			if err != nil {
				return nil, err
			}
		}

		ret.ReasoningContent = sb.String()
	}

	if len(toolCalls) > 0 {
		merged, err := concatToolCalls(toolCalls)
		if err != nil {
			return nil, err
		}

		ret.ToolCalls = merged
	}
```

The merged result has three parallel, order-free components. The only ordering that survives
the merge is:

- **within** `Content` — chunk arrival order;
- **within** `ReasoningContent` — chunk arrival order;
- **within** `ToolCalls` — by `*Index` ascending, with nil-index calls sorted first
  (`schema/message.go:1350-1363`), which is *not* arrival order.

```
schema/message.go:1350-1363
	if len(merged) > 1 {
		sort.SliceStable(merged, func(i, j int) bool {
			iVal, jVal := merged[i].Index, merged[j].Index
			if iVal == nil && jVal == nil {
				return false
			} else if iVal == nil && jVal != nil {
				return true
			} else if iVal != nil && jVal == nil {
				return false
			}

			return *iVal < *jVal
		})
	}
```

Even the rendering helper fixes an arbitrary display order rather than reflecting a semantic
one — content first, then multi-content, then reasoning, then tool calls:

```
schema/message.go:856-893
func (m *Message) String() string {
	sb := &strings.Builder{}
	sb.WriteString(fmt.Sprintf("%s: %s", m.Role, m.Content))
	…
	if len(m.AssistantGenMultiContent) > 0 {
		sb.WriteString("\nassistant_gen_multi_content:")
		…
	}
	…
	if len(m.ReasoningContent) > 0 {
		sb.WriteString("\nreasoning content:\n")
		sb.WriteString(m.ReasoningContent)
	}
	if len(m.ToolCalls) > 0 {
		sb.WriteString("\ntool_calls:\n")
		…
	}
```

That is presentation code, not a contract; it merely confirms no relative ordering is stored.

### Where boundaries *are* reliable

`AgenticMessage` carries block identity explicitly, so boundaries need not be derived at all:
`ContentBlock.Type` names the block kind, `StreamingMeta.Index` names the block instance, and
`ConcatAgenticMessages` regroups by index and sorts (`schema/agentic_message.go:929-989`,
quoted under Claim 6). Two adjacent assistant-text blocks are distinguishable there because
they carry different `Index` values. This holds only when the producer sets `StreamingMeta` —
`ConcatAgenticMessages` rejects mixing set and unset within one stream
(`schema/agentic_message.go:933-946`).

### What cannot be settled from these two trees

**CANNOT BE SETTLED FROM SOURCE:** whether any *particular* chat-model implementation
(e.g. an `eino-ext` ARK/OpenAI/Claude adapter) populates `MessageOutputPart.StreamingMeta`
or `ContentBlock.StreamingMeta`, and with what index discipline. Those adapters live outside
`github.com/cloudwego/eino@v0.9.14`; this module defines only the container types and the
merge rules. **Evidence that would settle it:** the source of the specific
`eino-ext/components/model/*` adapter at a pinned version, showing whether it constructs
`AssistantGenMultiContent` / `ContentBlocks` with `StreamingMeta` populated, and how it
allocates indices across text, reasoning and tool-call chunks.

---

## Consequences for a Go port

Each item follows directly from the evidence above; nothing is inferred beyond it.

1. **The consumer must accept a terminal `error` as the first event.** A Go state machine
   that asserts `start` arrives first will panic or deadlock on setup failure
   (`api/lazy.ts:46-61`), on a pre-cancelled context, and on any provider error raised before
   the first push (`api/anthropic-messages.ts:574-583`). *(Claim 1)*

2. **The consumer must accept an `error` whose message has empty content and zero usage.**
   `lazy.ts:4-23`, `pi-messages.ts:313-335` and `faux.ts:307-319` all construct one.
   "Terminal error implies partial content preserved" is not a safe invariant. *(Claims 1, 5)*

3. **The consumer must tolerate unmatched `*_start` events.** No error path synthesises
   closing events. Any Go structure that pairs starts with ends (a stack, a
   `map[int]openBlock` drained on `*_end`) needs an explicit "close everything still open"
   step on terminal `error`. *(Claim 2)*

4. **`partial` must be deep-copied at the moment of capture, or not captured at all.** In Go
   the equivalent aliasing bug is passing `*AssistantMessage` and retaining it. The deep-copy
   must cover, at minimum: the `content` slice, each block struct, `ToolCall.Arguments`
   (arbitrarily nested `map[string]any`), `Usage`, `Usage.Cost`, `Diagnostics` and each
   diagnostic's `Details` map. Copying only the top-level struct reproduces the faux bug —
   block mutations still leak through. *(Claim 3)*

5. **Scratch fields must not be modelled as optional fields on the public content types.**
   Pi's `delete` is retroactive across every alias already handed out. A Go port that puts
   `PartialJSON`/`Index`/`StreamIndex`/`CustomInput` on the exported block struct inherits
   both the leak risk (they will marshal into persisted sessions unless tagged `json:"-"`)
   and the retroactive-mutation surprise. Keeping streaming state in a separate,
   non-exported accumulator struct and projecting a clean block on close removes both.
   The full field set to keep out of the public type is `index`, `partialJson`,
   `partialArgs`, `customInput` (with its nested `jsonBuffer`), `streamIndex`. *(Claim 4)*

6. **Abort semantics differ per transport in Pi; the port must choose one and state it.**
   Anthropic-shaped providers preserve content and usage on abort; `pi-messages` returns an
   empty message. If the Go port wants "abort always preserves", `pi-messages` needs the
   accumulator threaded into its error construction — which is a behaviour change relative to
   the pin, and should be recorded as such. *(Claim 5)*

7. **Do not build the port's content-block model on eino's `schema.Message`.** `Content` and
   `ReasoningContent` are flat strings with no boundary information, so Pi's
   `text_start`/`text_end` block boundaries cannot be reconstructed from them, and adjacent
   text blocks collapse into one. `ToolCall.Index` covers only tool calls and is a separate
   space. *(Claims 6, 7)*

8. **If eino interop is required, `schema.AgenticMessage` is the only type with a usable
   block model.** `ContentBlock.Type` + `ContentBlock.StreamingMeta.Index` give a unified
   heterogeneous space over reasoning, assistant text and tool calls, with globally grouped,
   sorted, type-checked merging. Two constraints follow from the merge code: indices must be
   assigned consistently for every block in a stream (mixing set and unset `StreamingMeta`
   is a hard error, `schema/agentic_message.go:933-946`), and all chunks sharing an index
   must share a `Type` (`schema/agentic_message.go:1259-1261`). *(Claims 6, 7)*

9. **`MessageStreamingMeta` does not survive serialization** (`json:"-"`,
   `schema/message.go:294`), unlike `StreamingMeta` on `ContentBlock`
   (`json:"streaming_meta,omitempty"`, `schema/agentic_message.go:169`). Any persistence or
   cross-process hop through `schema.Message` loses part indices entirely. *(Claim 6)*

10. **`MessageOutputPart` merging is adjacency-based and never sorts**
    (`schema/message.go:1387-1406`), whereas `ToolCall` merging and `ContentBlock` merging
    both group globally and sort. A port that assumes uniform "group by index" behaviour
    across eino's three merge paths will produce wrong results for interleaved
    `AssistantGenMultiContent` chunks. *(Claim 6)*
