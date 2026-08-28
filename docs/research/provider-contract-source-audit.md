# Provider contract source audit

Reviewed contract head: `14300ee7e7dc457d7f95ad11ed8240a1773e1970`

Pi source pin: `086c32e74530564922d011ade23ff582c9d63116`

Verdict: **NO-GO as an implementation precondition, solely pending an owner decision.** The branch
is one owner-authored commit, DeepSeek's official status mapping is correct, the numeric overflow and
request-count contracts are coherent, and the unknown up-front rejection shape is now described
without inventing 400/422 facts. The selected provider still cannot satisfy that rejection-path
overflow obligation until a real response is recorded, so implementation remains held for either an
authorized probe or an explicit owner scope waiver.

## Sources checked

- Pi auth contracts and implementation:
  `packages/ai/src/auth/{types,credential-store,helpers,context,resolve}.ts`
- Pi retry implementation and call sites:
  `packages/ai/src/utils/{provider-retry,retry}.ts`,
  `packages/ai/src/api/openai-completions.ts`, and all `retryAssistantCall` call sites
- Pi OpenAI-compatible request, stream, tool-call, reasoning, usage, and compatibility conversion:
  `packages/ai/src/api/openai-completions.ts`
- Pi DeepSeek provider definition:
  `packages/ai/src/providers/deepseek.ts` and the tracked wrapper
  `packages/ai/src/providers/deepseek.models.ts`
- pi-go owned model boundary:
  `internal/ai/{port,stream}.go`
- DeepSeek's official Chat Completions API reference:
  <https://api-docs.deepseek.com/api/create-chat-completion>
- DeepSeek's official error-code reference:
  <https://api-docs.deepseek.com/quick_start/error_codes>

## Successor findings at `14300ee`

### 1. The proposed DeepSeek text detector has no provider source and its safety argument is false

The contract now correctly treats `ErrContextOverflow` as an existing port obligation rather than an
optional v1 feature. It proposes to identify provider-rejected overflow by matching text in one named
function, while keeping text out of retry, billing, and credential decisions.

No checked DeepSeek primary source establishes that oversized requests arrive as 400 or 422 with a
stable matchable wording. The official table defines those statuses only as generic invalid format
and invalid parameters, and the docs-only range contains no recorded DeepSeek overflow response.
A synthetic fixture invented from an assumed message would test the matcher, not the provider fact.

`6227c54` now states the cost boundary correctly: a false positive can cause one additional billable
provider request, while a miss remains an ordinary refusal. It also makes the current pattern empty
until a real response is observed. That avoids guessing, but it does not satisfy the port contract:
until the evidence appears, the DeepSeek adapter cannot produce `ErrContextOverflow` for an up-front
rejection, so compact-before-retry, typed second-overflow failure, and failed-attempt projection do
not run for that case.

The AST ownership check, `errors.Is` preservation check, adaptive positive/negative fixtures, and
broad-matcher mutation are useful once the response shape is real. `e19fa11` also removes the
unsupported 400/422 inference and explicitly lists the recovery behaviors absent on this path.
Planning those controls and waiting for ordinary post-implementation use is still not source evidence
for this gate. Require a primary/recorded DeepSeek rejection, or an explicit owner amendment to the
upstream v1 requirements.

`db79684` also records the exact boundary for an authorized probe: one counted request with no retry,
locally generated oversized input, an injected credential with no output path, a redacted response
fixture plus reported usage, and no rerun after failure. Those conditions are sufficient; they do
not alter the need for owner authorization.

Require a provider-authoritative or committed observed response, narrow positive and negative
fixtures around that exact shape, and state the additional-request/cost bound honestly. Otherwise do
not claim the text detector closes provider-rejected overflow.

### 2. Configured-window detection is now specified coherently

The successor now requires a positive configured window, a completed reply, provider-reported
input/cache-read/output usage, stop reason, consistent Generate/Stream propagation, and explicit
disabled behavior for missing window or usage. It also correctly says these cases cannot preflight a
request or classify a rejection that arrives before usage. This portion is source-backed and closed.

### 3. The request-count invariant is now closed

The document and commit now distinguish one transport request per model call from a logical call that
may make one additional overflow-recovery call. The live smoke remains one request because its prompt
is deliberately too small to overflow. This finding is closed.

### 4. DeepSeek's quota mapping remains closed

`77d1981` correctly treats DeepSeek's own documentation, rather than the Pi pin, as the authority
for DeepSeek's status codes:

- 400 invalid format;
- 401 authentication failure;
- 402 insufficient balance;
- 422 invalid parameters;
- 429 rate limiting;
- 500 server error and 503 overload, both documented to retry.

The contract now maps 402 directly to quota/billing and 429 to ordinary throttling, so no live
insufficient-balance request is needed. The synthetic same-status quota/throttle pair remains as a
stronger generic guard for a future provider that encodes both outcomes in 429; the DeepSeek-specific
control is correctly 402 versus 429.

### 5. Commit language is closed

The commit and document now distinguish a structural one-recovery cap from a configurable retry
budget. A false-positive overflow verdict can add one billed attempt; a retry misclassification is
bounded only by the configured retry budget, which is zero in v1 but can be raised. This is accurate.

## Findings at `c983da1`, closed by `8fc4508`

The following findings are retained as audit history. The successor text materially closes them;
they are not blockers on `8fc4508`.

### 1. The provider-to-owned-port mapping is still absent

The contract does not select a supported/default DeepSeek model or say how an arbitrary
`Request.Model` is validated and routed. This became more important after correctly rejecting the
gitignored generated catalogue as an authority: no replacement model authority is named.

It also does not map Pi's actual stream conversion onto pi-go's owned contracts. Pi exposes
`streamSimple`; `completeSimple` consumes that stream's result (`packages/ai/src/models.ts:690-703`).
The OpenAI-compatible adapter constructs ordered text, thinking, and tool-call events, accumulates
tool arguments, maps finish reasons, and records the served model. pi-go has both `Port.Generate`
and `StreamingPort.Stream`, plus its own block/tool-call identity rules. The contract needs to state
whether one implementation is derived from the other and mechanically prove identical final
content, tool calls, usage, stop reason, and served model.

Without these rows, the original implementation questions “which model reaches which provider” and
“Generate/Stream/tool calls map onto the existing contract” remain unanswered.

### 2. Retry ownership is described but not decided, and the request bound remains incomplete

`retryProviderRequest` wraps the SDK request after setting SDK `maxRetries: 0`; that part is correct.
The revised contract also correctly distinguishes the caller-owned outer policies: agent-turn
auto-retry in the session and `retryAssistantCall` at the summarization choke point. But the text
then says whether pi-go has an outer layer is still a decision. A document marked ready to implement
must make that decision and name its owner rather than leave retry topology open.

The request-count formula also needs attempt semantics. Both APIs define `maxRetries` as retries in
addition to the initial attempt. When the layers actually compose, the upper bound is
`(innerMaxRetries + 1) * (outerMaxRetries + 1)`, not merely “the product of the budgets.” The
zero/zero control proves one case but does not make nonzero billing predictable.

Finally, Pi's inner request retry classifies a 429 from status/headers before the outer
message-text quota classifier exists. A quota-bearing 429 can therefore be retried by the inner
layer. pi-go's proposed typed boundary classification can intentionally improve this, but the
contract must say that this is a behavioral divergence and prove that quota classification happens
before any provider retry, not only before an outer classifier.

### 3. Usage and failed-attempt accounting are not closed

The type contract makes `reasoning` optional, but the actual OpenAI-compatible parser at the pin
sets it with `rawUsage.completion_tokens_details?.reasoning_tokens || 0`. On the DeepSeek path,
unreported reasoning is therefore materialized as reported zero. If pi-go preserves absence, that
is a stronger intentional divergence and must be labeled as such.

The current pi-go `ai.Usage` has plain integer input/output fields, so it cannot distinguish an
unreported value from a reported zero. The contract currently promises that distinction without
naming the required owned-type change.

The revised contract correctly says that a failed Pi attempt keeps usage accumulated before the
failure and that retry attempts must add. It still does not name the pi-go owned representation or
ledger operation that keeps attempt boundaries while deriving a logical-call total. That matters
because one aggregate can hide which attempt was reported or double-counted, while only the final
attempt loses precisely the retry spend the control is meant to expose.

Section 8 describes a currency-cost breakdown while section 14 says currency cost is not computed.
The v1 representation must say whether cost fields are omitted, unknown, or carried as zero; zero
must not silently mean “free.”

### 4. The DeepSeek unsupported-field uncertainty is now stated, but the proposed smoke cannot settle it

Pi's detection and request builder establish that DeepSeek requests use `max_tokens`, not
`max_completion_tokens`. DeepSeek's official API reference also documents `max_tokens` as the
generation bound. The revised text now correctly says neither source establishes whether the wrong
field is ignored or rejected.

Keep the payload control (`max_tokens` present, `max_completion_tokens` absent). A live smoke with
that correct payload can demonstrate that one correctly formed bounded request stops within its
cap, but it cannot “settle” what DeepSeek would do with the unsupported field. Settling that
counterfactual requires a versioned primary source or a separate intentionally wrong request, which
is unnecessary and risks the exact unbounded-billing case the contract is designed to avoid.

Pi also converts DeepSeek `reasoning_content` into a public `ThinkingContent` block. The wire field
is separate from text, but “not a content block” is false at Pi's observable message boundary. The
contract should name both layers explicitly.

### 5. The error taxonomy and live-smoke controls remain incomplete

The contract gives typed/value rules for quota, cancellation, and overflow, but does not define the
owned typed outcomes for authentication, ordinary throttling, timeout, and transport failure. These
were part of the implementation gate and are required to decide retry, reporting, and overflow
recovery without text parsing.

The thread's live-test safety conditions are also missing from the artifact: live network is
explicitly opt-in, CI never runs it, credentials enter only through the injected environment seam,
the prompt and output cap are minimal, both retry budgets are zero, exactly one request is counted,
provider-reported usage is shown, and failures are not repeatedly rerun. A generic “smoke test” is
not enough for a test using a human's billable credential.

## Source-accurate portions

- Type-tagged credentials and one entry per provider.
- `modify` as the credential-value replacement path, `delete` serialized against it, `read` as a
  possibly-expired display/status operation, and side-effect-free non-secret `list`.
- OAuth double-checked refresh under `modify`.
- Stored-key then injected-environment resolution, including blank environment values as absent.
- SDK retry explicitly disabled and caller-injected transport/timeout seams.
- Pinned DeepSeek detection values, with generated catalogue overrides correctly described as
  unpinned.
- Existing pi-go handler-order/model-substitution constraint and v1 no-persistence decision.

## Required closure before implementation

1. Map DeepSeek's actual 400/401/402/422/429/500/503 statuses into the closed typed set, with 402
   classified as quota/billing without message-text parsing.
2. Give context overflow a realizable typed source compatible with “no catalogue” and “no message
   text parsing,” then control that source through the existing shortening path.
