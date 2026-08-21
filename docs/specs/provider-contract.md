# Provider and credential contract

**Status:** implemented against, in `internal/provider/deepseek`. Sections 1-13 state what holds.
Each is established from source at the pin, except where a section marks a claim as a deliberate
divergence or as not established. Section 14 states how each is checked. Section 15 states what this
contract does not cover.

**Pi baseline:** [`086c32e74530564922d011ade23ff582c9d63116`](https://github.com/earendil-works/pi/commit/086c32e74530564922d011ade23ff582c9d63116)

**Scope:** how a real model provider is reached and how its credential is stored, refreshed and kept
out of places it does not belong.

This is the first thing in this repository to touch the network, a secret, and a bill. The contract
is written before the code because every one of those is easier to get wrong quietly than loudly.

## 1. A credential is type-tagged, and there is one per provider

`packages/ai/src/auth/types.ts:17-37`.

```
Credential = ApiKeyCredential | OAuthCredential
ApiKeyCredential = { type: "api_key", key?, env? }
OAuthCredential  = { type: "oauth", refresh, access, expires, ... }
```

Keyed by the provider's id, one credential per provider. The tag is part of the stored value, so a
reader never has to infer which kind it holds.

**Checkable:** a stored credential round-trips with its type intact, and a provider cannot hold two.

## 2. One serialized write path, and delete is serialized against it

`packages/ai/src/auth/credential-store.ts:30-60`, interface at `auth/types.ts:57-90`.

The store has four operations, and the distinctions between them are load-bearing:

- **`modify`** is the only path that writes a credential value. It hands the current value to a
  function and stores what comes back, under mutual exclusion per provider id — "cross-process too
  where the backing store supports it (e.g. a file lock)". OAuth refresh runs *inside* it, which is
  what stops two concurrent requests from both refreshing and the second presenting a token the first
  has already rotated away.
- **`delete`** also mutates, and the interface requires it to be serialized *against* `modify`. This
  is not incidental: a logout racing a refresh, unserialized, lets the refresh write the credential
  back after the delete removed it, and the user stays logged in.
- **`read`** returns the stored credential "possibly expired", and is explicitly for display and
  status — **not** the request path. Auth for an actual request is resolved separately (section 7).
  Using `read` to authenticate a request would send expired credentials and skip refresh entirely.
- **`list`** is covered in section 3.

**Why it matters here:** a refresh outside the serialized path can be entered twice, and the second
attempt presents a token the first has already rotated. That fails as an authentication error and
looks like a bad credential rather than a race.

**Checkable:** concurrent callers that both find an expired token produce exactly one refresh; a
delete concurrent with a refresh leaves the credential absent, not restored.

## 3. Enumeration is non-secret, and must not have side effects

`auth/types.ts:40-43`, and the `list` contract at `auth/types.ts:63-67`.

Listing yields `{ providerId, type }` — never the key, never the tokens.

The interface adds a second requirement that is easy to miss: implementations "must not execute
configured API-key commands while listing". Some credentials are produced by running a command, and
enumerating accounts must not run them. Otherwise merely asking which providers are configured
executes arbitrary configured commands, once per provider.

**Checkable:** nothing that enumerates credentials returns a secret, by any argument; and
enumeration performs no work beyond reading what is already stored.

## 4. Retry: the SDK's own retry is switched off, and the topology is decided here

`api/openai-completions.ts:242-254`, `utils/provider-retry.ts`, `utils/retry.ts`.

The provider SDK would retry by itself. Pi turns that off — `maxRetries: 0` in the per-request
options — and wraps the call in `retryProviderRequest` instead. The SDK is left as transport only.

That leaves exactly two retry layers, both Pi's own:

**Inner, around one request.** Keyed on typed values: an `x-should-retry` header wins outright, an
absent status is retried, otherwise 408, 409, 429 and any 5xx. That list is *Pi's*, spanning every
provider it supports; this repository retries what section 5's table says for the provider in front
of it, which is not the same list — DeepSeek documents no 408 or 409. Delay comes from `retry-after-ms` or
`retry-after` when the server sends one, else exponential with jitter; a server-requested delay above
the cap (60s default) is refused rather than slept.

**Outer, around a whole assistant call.** This one is the caller's, not the API's, and at the pin it
appears in two places sharing one configured budget: the session's agent-turn auto-retry
(`coding-agent/src/core/agent-session.ts:2645-2649`), and the summarization choke point
(`coding-agent/src/core/compaction/compaction.ts:562-580`) which uses the `retryAssistantCall`
helper. Both inspect the finished `AssistantMessage` and retry only when it ended in `error`.
Bounded attempts, `baseDelayMs * 2^(attempt-1)`.

The distinction matters: the inner layer is inherent to making a request, while the outer one exists
only because a caller chose to add it. It is a decision, not an inheritance — and this contract makes
it below.

The two compose multiplicatively. `maxRetries` counts attempts *after* the first, so the worst-case
number of requests for one *model call* is `(inner + 1) * (outer + 1)`, and every one of them is
billable. A logical call that recovers from overflow makes one further model call — see section 5.

**Pi does not guarantee that a quota 429 is never retried.** The inner layer decides from status and
headers alone: 429 is retryable, whatever the body says (`provider-retry.ts:24-34`). The quota and
billing text is only consulted at the outer layer (`retry.ts:226`), which runs after the inner layer
has already exhausted its own attempts. A 429 that means "your balance is gone" is therefore retried
by Pi at the transport, and only then classified as terminal.

**This repository's topology, decided rather than deferred:**

- **No outer layer in v1.** The worst case is `inner + 1` requests, and with the inner budget at zero
  that is exactly one.
- **Quota and billing classification happens before any retry decision, not after.** This is where
  the typed divergence of section 6 earns its keep: the classification exists at the boundary, so it
  can be consulted before the first retry rather than after the last.

**Checkable, by counting requests rather than reading configuration:** a quota-exhausted 429 produces
exactly one request — not one plus the inner budget. This is the control that would fail if the
classification were made where Pi makes it.

A transport that counts calls proves the bound; configuration values do not.

## 5. The typed outcome set

Every failure a provider can produce leaves this repository as one of a closed set of typed values.
The set is closed so that a caller can exhaust it, and typed so that decisions about retrying,
billing and credentials never read text (section 6). Context overflow is the single exception,
detailed below: its detection may read text, and it decides nothing else.

| Outcome | Retryable | DeepSeek's documented status |
| --- | --- | --- |
| Quota or billing exhausted | **never** | **402** — "You have run out of balance" |
| Authentication rejected | never | **401** — "Authentication fails due to the wrong API key" |
| Provider refused the request | never | **400** invalid format, **422** invalid parameters |
| Cancelled | never | none — originates locally |
| Rate limited (ordinary throttle) | yes | **429** — "You are sending requests too quickly" |
| Server or transient provider failure | yes | **500** server error, **503** server overloaded |
| Timed out | yes | none documented — the deadline elapsing locally |
| Transport failure | yes | none — no response was produced |
| Interrupted by the provider | **no** — see below | **200** with `finish_reason: insufficient_system_resource` |
| Provider refused the content | never | **200** with `finish_reason: content_filter` |

Sources, read directly rather than inferred: the status codes come from DeepSeek's published
error-code table, <https://api-docs.deepseek.com/quick_start/error_codes>; the interruption stop
reason comes from the API reference,
<https://api-docs.deepseek.com/api/create-chat-completion>, which is where it is documented and where
the error table does not mention it. Both are the provider's own documentation, which is the right
authority for the provider's behaviour — the Pi pin is not, and never was, evidence about DeepSeek's
status codes.

The retryability column is this repository's decision, not DeepSeek's: the documentation states what
each condition means, not what a caller should do about it. Where it says nothing — the interruption
— the decision is recorded in the paragraphs below rather than inferred from the wording.

DeepSeek documents no 403, 408 or 409. Those remain in the general rules of section 4 because the
transport layer acts on them, but nothing here claims this provider emits them.

The set is closed over what can actually arrive: a status is 4xx (split above), 5xx, a 200 whose stop
reason reports a failure — an interruption or a filtered reply — or there is no response at all; the
rest originate locally. Nothing falls through to a default.

**A 200 is not by itself a success.** The API reference documents five stop reasons, and two of them
report failure inside a successful HTTP response:

- `insufficient_system_resource` — "if the request is interrupted due to insufficient resource of
  the inference system".
- `content_filter` — "if content was omitted due to a flag from our content filters".

A mapping that reads only the status returns either as a completed reply, and the caller treats a
truncated or emptied answer as the model's final word. The filtered case takes the existing refusal
outcome — the provider declined to produce this content, and asking again unchanged would be
declined again — so it needs no new type, only recognition.

All five map, so that "closed" means what it says:

| DeepSeek `finish_reason` | This repository | Outcome |
| --- | --- | --- |
| `stop` | `StopEnd` | the reply finished |
| `length` | `StopLength` | truncated; reaches the existing shortening path |
| `tool_calls` | `StopToolUse` | the reply is asking for tools |
| `content_filter` | failure | provider refused the content — never retried |
| `insufficient_system_resource` | failure | interrupted by the provider — not retried |

**An unrecognised stop reason is a failure, not a success.** A value outside this list means the
provider is reporting something this contract has not mapped, and the safe reading of an unknown
terminal state is that the reply cannot be trusted to be complete — not that it is fine.

**Whether it is retryable is not established, so it is not retried.** The documentation says the
request was interrupted; it does not say a repeat would succeed. Treating "sounds transient" as
"retry it" is how a doomed request gets sent again at cost. It is surfaced as a failure — the part
the evidence supports — and left for a caller to decide about.

One override sits above the **status-derived** rows. An explicit `x-should-retry` header decides
outright, either way (`provider-retry.ts:25-27`) — a provider stating its own answer outranks an
inference drawn from its status code. Two limits on it:

- It does not override the quota classification, the one case where retrying is known to be futile
  and to consume balance.
- It does not reach the message-level outcomes at all. A header describes the HTTP exchange; an
  interruption or a filtered reply is reported *inside* a 200 that the header has already called
  successful. Letting a transport header decide those would be answering a question it was never
  asked, and would contradict the non-retry rules stated for them.

**Context overflow is an obligation this port already carries, not an optional feature.**
`internal/ai/port.go:136-143` defines `ErrContextOverflow`, and the runtime branches on it
(`loop.go:728-730`) to shorten and try once more. A port that never produces it does not fail
loudly — it silently removes a recovery path that is already built and tested: an overflow shortens
the context and asks once more, a second overflow is a durable terminal failure rather than a loop,
and the failed attempt stays auditable. This contract therefore states how a real provider produces
the error that path depends on — and, where it cannot yet, says so.

Pi detects overflow three ways (`utils/overflow.ts:134-165`): matching the error text against
per-provider patterns, or — holding the context window — noticing that input exceeded it, or that a
length-stop produced zero output against a filled window.

**Two of the three need a context window, which is configuration here, not a catalog** — and a window
alone is not enough to make them work. A per-model context window is supplied by configuration
alongside the model id. That is not the generated catalog of section 10: it is a value this
repository is given and controls.

What each numeric case actually requires:

- **A reply that came back.** Both cases read the usage the provider reported, so both fire *after* a
  response. A window is not a tokenizer: it cannot judge a request before sending it, and it cannot
  say anything about a request the provider rejected before reporting any usage. These two cases
  therefore do not cover the rejection path at all — that is the third case, the one needing text.
- **Usage broken down, not totalled.** The first case compares input plus cache-read against the
  window; the second needs output as well, to see that a length-stop produced none. A provider that
  reports only a total cannot drive either.
- **A stop reason.** The first case applies to a reply that ended normally, the second to one that
  ended on length. Without the stop reason the two are indistinguishable.

Consequences, each stated because each is silent when wrong:

- **Configuration is validated at construction**: a window must be a positive number. A missing or
  zero window disables both numeric cases — it does not mean "window of zero", which would classify
  every reply as an overflow.
- **Missing usage disables them too.** Section 8 keeps unreported distinct from zero precisely so
  this decision can be made: absent usage means the case cannot be evaluated, not that the numbers
  were zero. Treating unreported as zero would make the second case fire on every reply.
- **Usage must survive both paths.** The breakdown has to reach the detector identically through
  `Generate` and through `Stream`, or overflow would be detectable in one mode and not the other —
  which is the same defect as not detecting it at all, hidden behind a mode switch.

**Checkable:** each numeric case has a fixture that fires and one just below its threshold that does
not; a zero or absent window disables them rather than triggering them; absent usage disables them
rather than reading as zero; and the same recorded response yields the same verdict through
`Generate` and `Stream`.

**The third case: an oversized request rejected up front — and its shape is entirely unknown.**
DeepSeek's published error table (section 5) has no entry for exceeding the context. That absence
says nothing about what such a rejection looks like: not its status, not its body, not whether it is
one of the documented client errors at all. This contract makes no assumption about any of them.

If the rejection turns out to carry a distinguishing status, no text needs reading. If it does not,
recognising it requires reading what the provider said. Which of those is true is not known, so the
provision in section 6 permitting text in one place is written to cover the worse case, not to
assert that it is the actual one.

**What DeepSeek actually returns for an oversized request is not established.** Neither the error
table nor the pricing documentation mentions context length. This contract does not guess the status
or the wording, and the detector is not designed against a guess: its pattern for this provider stays
empty until a real rejection is recorded, at which point adding it is a small change against controls
that already exist.

**Measured, not assumed: what one recorded request actually establishes.** A request carrying
1,015,083 prompt tokens was **accepted** — status 200, all of it reported as cache-miss usage —
against a published context length of "1M".

What that proves is a **lower bound**: the enforced limit is above 1,015,083. It does **not** prove
the published figure is wrong. "1M" may well be shorthand for 1,048,576, which is above the recording
and consistent with it. The threshold itself remains unknown.

What it does settle is narrower and still useful: **configuring a window as decimal 1,000,000 on the
strength of that "1M" is unsafe.** A threshold set below the real limit turns replies the provider
accepts into overflows — and each such verdict buys a shortening and a second request. This
recording is not itself an example of that (see the controls in section 14), but it sits in the
range where the mistake would occur.

So: **a configured window is usable only if it is a value someone has measured or been given
authoritatively**, and a rounded figure from documentation is neither. Where no such value exists,
the numeric cases stay off — failing to detect is the safe direction; inventing detections that cost
money is not.

**The remainder is deferred by decision, not by oversight.** This version does not classify an
oversized request that the provider rejects up front. That is a deliberate limit on v1's scope, taken
knowing what it costs: such a rejection is reported as a refusal, and the automatic shorten-and-retry
does not run for it. It is recorded here so that a reader meets the limit in the contract rather than
in production.

The mechanism, its confinement to one function, and its controls are all built. What is missing is a
single recorded rejection, and adding it is a change to a pattern list rather than to a design.

**So the obligation is fulfilled in part, and the remainder is deferred rather than
declined.** Stated plainly, because the distinction is the whole point:

- The two numeric cases produce `ErrContextOverflow` from typed numbers, and the recovery path runs.
- The rejection case does not, and will not until a real rejection is recorded. Such a rejection is
  reported as a refusal: no shortening, no second-overflow terminal failure, no failed-attempt
  record. For that path the existing recovery behaviour is simply absent, and calling the mechanism
  "in place" does not make the mapping exist.

This is not the same as the port simply never producing the error. The mechanism, its confinement to
one function, and its controls all exist; what is missing is one recorded response, and adding it is
a change to a pattern list, not to a design.

No published source states the limit: the error table has no entry for it, the token-usage page
gives none, and the API reference defers to the pricing page, whose "1M" is a rounded figure the
recorded response has already passed without being refused. There is currently nothing credible to
size a deliberate probe against, and guessing is what one recording has already been spent on.

Two routes to that recording, with their costs stated rather than guessed:

- **Ordinary use.** Any real conversation that outgrows the window produces one, at no cost beyond
  the request that was going to be made anyway. This is the intended route.
- **A deliberate probe.** Exceeding the window means sending more tokens than it holds, and **how
  many that is is not known** — one recording of 1,015,083 tokens was accepted, so a probe must
  exceed at least that, by an unknown margin. Cost scales with the tokens sent, at the published
  cache-miss input price, and may be nothing if a rejected request is not charged, which is not
  documented either. Sizing a probe without a credible bound is guesswork of the kind that has
  already produced one accepted request instead of the rejection it was aimed at. A probe is a
  decision for whoever owns the key, not something this contract takes on its own.

  If one is authorised, it runs under these conditions, which are the same ones that bound any live
  call (section 14) plus what this particular request needs:

  1. Exactly one request, asserted by counting, with no retry at any layer.
  2. The oversized input is generated locally. No real conversation, file, or user data is sent to
     make a request large.
  3. The key comes from the injected environment and nothing else, and appears in no output.
  4. The response is recorded verbatim as a fixture — status, headers, body — with anything
     credential-shaped removed, and the reported usage recorded alongside it. That fixes the token
     usage as a known quantity; the money remains an estimate from published prices, because the
     response carries no currency field.
  5. A failure is not retried. If the probe does not produce a usable recording, that outcome is
     reported and the decision returns to whoever authorised it.

This is a deliberate narrowing of section 6, and the boundary is where the consequences differ:

- A retry or billing decision made from text is unsafe because a change of wording spends money —
  a non-retryable failure silently becomes retryable.
- Overflow detection from text is wrong in bounded ways, but **not free**: a miss reports a refusal
  the caller already handles, and a false positive shortens and sends **one more real, billed
  request**. The recovery rule bounds that to one further attempt per input boundary, so it cannot
  repeat or compound — but it is a request, and this contract does not pretend otherwise.

The asymmetry is not between costly and free — both cost. It is between what bounds them. A
text-driven overflow verdict is capped at one further attempt by the recovery rule itself, whatever
the configuration says. A text-driven retry verdict is capped only by the retry budget, which is a
setting: zero here, but a number someone can raise, and every attempt it permits is a billed request
sent at a failure that will not succeed.

So: text may be read **only** to decide overflow, in one named function, tested against recorded
provider responses. It may never be read to decide whether to retry, what to bill, or whether a
credential is valid. That restriction is the point of section 6 and is unchanged.

**Quota is a distinct status code here, so no body inspection is needed.** This is the fact that was
missing, and having it changes the design rather than merely filling a blank. Pi matches quota
wording inside a 429 because several of its providers report exhaustion that way. DeepSeek does not:
an exhausted balance is **402**, and a throttle is **429**. Two different codes, no text, no
ambiguity.

That also settles the ordering worry for this provider specifically. 402 is not in the retryable
status set at all, so a transport layer following Pi's rule would not retry it either. The ordering
rule — classify before retrying, never after — is kept as the general guarantee, because it is what
protects a provider that *does* encode exhaustion inside a 429. For DeepSeek it costs nothing and is
satisfied by the status code alone.

**The request bound, stated correctly.** One *model call* sends one request, from the retry budget and
the absent outer layer (section 4). A *logical call* may send two: if overflow recovery runs, it
shortens and calls once more, and the recovery rule allows only that one extra attempt per input
boundary. So the honest worst case is two billed requests, not one.

This does not weaken the guarantee that matters — nothing loops, nothing compounds, and no path sends
an unbounded number — but "one logical call is one request" was arithmetic that ignored the recovery
path, and it is corrected here rather than left to be discovered on an invoice.

Two of the remaining entries carry consequences worth stating, because each is silent when wrong:

- **Quota and billing before rate limiting.** Pi tests quota text *after* the transport has already
  retried a 429 (section 4). Here it is classified first: retrying a request whose balance is gone
  costs money and cannot succeed. A quota 429 and an ordinary 429 carry the same status code, so the
  distinction cannot come from the status.
- **Cancellation is terminal and preserves what was produced.** Pi normalises an abort during backoff
  into the same aborted shape, spreading the existing response (`retry.ts:177-181`), so partial
  content survives — as it already does in this repository's streaming contract.
**Checkable:** every entry in the table is produced by a test and matched by value, with no default
branch; a 402 is classified terminal and a 429 as retryable, with the retry *observed* under a
positive test-only budget — under the shipped budget of zero neither is retried, so a test that did
not raise it would be asserting nothing; and a `length` stop still reaches the shortening path.

The paired same-status control still applies to any provider that reports exhaustion inside a 429.
DeepSeek does not, so for this provider the pair is 402 against 429 — different codes, opposite
outcomes, neither decided by reading text.

## 6. Divergence: this repository classifies by value, not by text

Pi's outer classifier matches a regular expression against `errorMessage` — roughly a hundred
provider-specific strings. Pi has little choice: by the time the error reaches that layer it has been
flattened to a string by one of a dozen heterogeneous SDKs.

This repository will not do that. The classification is made once, at the provider boundary, where
the status code and headers still exist, and is carried as a value.

The reason is not taste. Deciding by text means a provider's change of wording silently changes
behaviour: a non-retryable billing failure becomes a retryable one, requests are sent that cannot
succeed, and each is billed. Nothing fails loudly when it happens — the classification simply
becomes wrong, and stays wrong.

**Checkable:** no decision about retrying a transient failure, about billing, or about a credential
reads the text of an error message. There is exactly one exception, stated in section 5 and enforced
mechanically: the overflow detector. Nothing else may read it, and the detector may decide nothing
else.

## 7. A credential is resolved through one function, and the environment is injected

`packages/ai/src/auth/helpers.ts:9-31` and `packages/ai/src/auth/context.ts:22-46`.

`envApiKeyAuth(name, envVars).resolve` is the single read path for request auth — distinct from the
store's `read`, which is for display and may return an expired value (section 2). Order: a stored credential's key
wins; otherwise the first env var in the declared list that is set. Nothing found returns
`undefined` — absence is a value, not a thrown error. Abort is checked between lookups.

An env var counts as set only when it is a string that is non-empty after trimming
(`context.ts:26-27`). An exported-but-empty variable is treated as absent, not as an empty key, so it
falls through to the next candidate instead of sending a blank credential to the provider.

**The environment is not read directly.** `AuthContext` supplies `env(name)` and `fileExists(path)`,
and `defaultProviderAuthContext()` is only the default implementation. This is the seam that keeps
credential resolution testable without touching the real environment, and this repository takes it.

**Resolution reports its provenance, not its secret.** `resolve` returns `source` — either
`"stored credential"` or the name of the env var that supplied it. That is the value that may appear
in a log or a diagnostic; the key itself has no such path.

**Resolution is not on the request path.** A port is given a resolved value, not something to ask.
The order above answers "which configured source wins", which is one question for the process rather
than one per request: a port that resolved per call could authenticate two calls a second apart as
different identities with nothing recording that it had. It also keeps a caller's own resolver, and
whatever that holds, out of the path a request takes.

**A failed lookup does not carry what it found.** A source can return a value and an error together,
and one that names what it was holding hands the key to whatever logs the failure. Removal is by
exact value first — the only removal that is certain — and by shape second, for a key that arrived
from somewhere the call could not see. Neither half alone is enough: a key that does not look like
one survives the shape pass, and the exact pass has nothing to match when the value was never
returned.

**Checkable:** resolution order holds; an empty env var is skipped; resolution stops when the caller
does, between lookups and not only before them; nothing in this repository reads the process
environment for a credential except through the injected context; what is reportable is the source,
and the key has no route to a log, an event, or the session.

## 8. Usage: what is carried, and what "absent" is allowed to mean

`packages/ai/src/types.ts:370-391`, `api/openai-completions.ts:1375-1411`.

Pi's `Usage` carries `input`, `output`, `cacheRead`, `cacheWrite`, `totalTokens`, optional
`cacheWrite1h` and `reasoning`, and a parallel cost breakdown with a total.

**`reasoning` is a subset of `output`**, not an addition. `output` already includes it
(`openai-completions.ts:1398-1399`), so adding them double-counts.

**Pi's type allows "unreported"; this API path does not produce it.** `reasoning` is optional on the
type, but `parseChunkUsage` writes `reasoning_tokens || 0` (`:1404`), which collapses an unreported
count into a reported zero. Absent-means-unreported is therefore *not* an observable of this path.

This repository keeps the distinction, which is a **deliberate divergence, stronger than Pi**:

- A field that the provider did not report is represented as absent, not as zero. Plain integers
  cannot express this, so the optional fields are represented as such — `internal/ai.Usage` changes
  accordingly, and that change is part of this work rather than assumed.
- The reason is that zero is a real, meaningful answer: a model that did no reasoning reports zero
  reasoning tokens. Collapsing "did none" and "did not say" into one value makes a ledger unable to
  distinguish a model that reasons for free from one that does not tell you what it charged for.

**Attempts and totals.** A failed attempt keeps the usage it already incurred: the error path emits
the same accumulated object with `usage` intact (`api/openai-completions.ts:591-609`). So attempts
accumulate, and the representation must keep them apart:

- Each attempt's usage is recorded as its own value.
- A logical call's total is **derived by summing attempts**, never accumulated in place. Summing a
  derived total back into itself is the double-count this rule exists to prevent.
- A caller that records only the final attempt undercounts every earlier one — precisely the spend a
  retry loop creates and nobody intended.

**The provider reports no cost.** The recorded response carries token counts and no currency field
of any kind. Any money figure attached to a call here is therefore computed from published prices,
which is an estimate and must never be recorded as something the provider stated.

**Cost in currency is unknown in v1, and unknown is not zero.** Pi computes cost
(`calculateCost`, `:1409`) from per-token prices that live in the generated, unpinned catalog of
section 10. This repository does not read that catalog, so it does not compute currency. The cost
fields are absent — the same distinction as above, applied to money: a call whose price is unknown
must not be reported as a call that cost nothing.

**Checkable:** an unreported field stays distinguishable from a reported zero, through the port and
into the session; no term is counted twice; a call that fails after two attempts reports the tokens
both attempts consumed rather than only the last; and no cost is reported as zero merely because it
was not computed.

## 9. The transport is injected, and that is the seam tests use

`api/openai-completions.ts:236, 640-679`. The client is constructed with `apiKey`, `baseURL` from the
model, default headers, and a `fetch` passed in from options. A caller that supplies `fetch` decides
what the network is.

This is the seam that makes "no test reaches the network" true by construction rather than by
convention: a test supplies the transport, so a test that would have made a real request cannot
accidentally make one. It is also where requests are counted for the bound in section 4.

Per-request timeout comes from the same options and is applied to the request, not to the client.

**Checkable:** no code path constructs a provider client without an injected transport, and the
default test transport fails loudly if asked for a real network address.

## 10. "OpenAI-compatible" is a family, not a shape

`api/openai-completions.ts:1461-1512`, `1549-1561`, `providers/deepseek.ts`.

One API module serves many providers, and the differences between them are carried in a
compatibility record resolved per model rather than by branching at each call site. A provider that
speaks "the OpenAI API" still differs in named, enumerable ways.

**The record has two sources, and only one of them is pinned.** Values are *detected* from the
provider id and base URL (`1461-1512`) — that function is in the pinned tree. A model's catalog entry
may then override any of them (`1549-1561`: `model.compat.X ?? detected.X`), and the catalog is
**generated and excluded from version control** (`.gitignore:11`, `packages/ai/src/providers/data/`).
It is not in the pinned tree and can differ between checkouts.

So: the values below are what detection yields for DeepSeek at the pin. The generated catalog present
locally agrees with them for `deepseek-v4-flash`, but that agreement is an observation about one
generated file, not a guarantee.

**What this repository takes from that:** the values are stated here and asserted against the request
actually sent. Reading them out of a generated file this repository does not control would make its
behaviour depend on an artefact nobody pinned.

| Field | Value | Consequence |
| --- | --- | --- |
| `baseUrl` | `https://api.deepseek.com` | — |
| env var | `DEEPSEEK_API_KEY` | the only one; nothing else resolves |
| `maxTokensField` | **`max_tokens`** | see below |
| `supportsStore` | false | `store` must not be sent |
| `supportsDeveloperRole` | false | the developer role is not available |
| `supportsUsageInStreaming` | true | usage arrives in-stream, but only if asked for |
| `requiresReasoningContentOnAssistantMessages` | true | assistant history must carry reasoning back |
| `thinkingFormat` | `deepseek` | reasoning arrives on its own field, not as content |

**The token cap is the one that costs money — and one half of that is not established.**

*Established:* Pi selects `max_tokens` for DeepSeek and `max_completion_tokens` otherwise
(`1480-1487`, `1502`). Sending the modern OpenAI field to DeepSeek is, by Pi's own selection, wrong.

*Not established:* what DeepSeek does on receiving the wrong field. Nothing in Pi's source says, and
Pi's source is not evidence about DeepSeek in any case. DeepSeek documents a 422 for invalid
parameters, which is the shape a rejection would take, but the documentation does not say whether an
unrecognised field counts as invalid — so this stays open rather than being inferred from it. The
two possibilities differ sharply — a server that **ignores** an unknown field leaves the reply
uncapped, so a test written to be cheap has no cap at all and the bill is the first evidence; a
server that **rejects** it fails loudly and costs nothing. This contract does not assert which.

The control is written so that it does not need to know: the field actually sent is asserted, and the
cap is demonstrated to bound a reply rather than assumed to. That holds under either behaviour.

It stays unresolved deliberately. Answering it would mean sending a request carrying the wrong field
— which, on the branch where the field is ignored, is a real request with no cap on its length. The
question is not worth a possibly unbounded call against someone's key, and the correct behaviour does
not depend on the answer.

**Usage must be asked for.** `stream_options: { include_usage: true }` is what makes usage appear
(`buildParams`). Without it the reply is fine and the ledger is empty, so a cost check would be
comparing against nothing.

**Confirmed against one real response**, rather than only predicted from source: reasoning arrived in
`reasoning_content` with `content` empty and `completion_tokens_details.reasoning_tokens` counted
separately; usage carried `prompt_cache_hit_tokens` and `prompt_cache_miss_tokens` alongside
`prompt_tokens_details.cached_tokens`; `finish_reason` was `length`. The provider also reported 83 more prompt
tokens than the content measured — role and structure — so a content count is not a request count.

**Replies can fail through `finish_reason` rather than through a status code — there are two.** The
API reference documents `insufficient_system_resource`, "if the request is interrupted due to
insufficient resource of the inference system", and `content_filter`, "if content was omitted due to
a flag from our content filters". Both arrive on a **200** carrying a stop reason, so a mapping that
classifies failures only by status reads them as ordinary completions and hands back a truncated or
emptied reply as though the model had finished by choice. Both are typed and handled in section 5.

**Reasoning is a separate field on the wire — and only on the wire.** With
`thinkingFormat: "deepseek"`, reasoning arrives as its own delta rather than as text, and assistant
history must carry it back. Pi then presents it as an ordinary thinking block in its public message
type, so "not a content block" is true of the wire format and false of what a consumer sees. This
repository's streaming contract already distinguishes thinking from text, so the mapping exists — but
it is a mapping, not a pass-through, in both directions.

**Checkable:** the request this repository sends to DeepSeek carries `max_tokens` and not
`max_completion_tokens`, carries no `store`, and asks for usage — asserted against the recorded
request, not against configuration; a low cap demonstrably bounds the reply. No assertion here reads
a generated catalog.

## 11. The mapping onto this repository's own ports

**Streaming is the source; the single reply is derived from it.** Pi has exactly one production
path: `completeSimple` is `streamSimple(...).result()` (`compat.ts:291-297`). The non-streaming form
is not a second implementation, it is the streamed one collected.

This repository takes the same shape. `StreamingPort.Stream` is where a provider is implemented, and
`Port.Generate` is that stream collected. There is no second request-building path, because two paths
drift and only one of them is exercised by the tests that matter.

**Checkable, against one recorded provider response:** driving `Generate` and driving `Stream` yield
the same final content, the same tool calls in the same order, the same usage, the same stop reason,
and the same served model. Any divergence is a defect in the derivation, not a difference of mode.

**Model routing is explicit, and there is no default.** `Request.Model` is required and non-empty; an
empty model is a typed failure, never a silently chosen default. The id is sent verbatim as the
provider's `model` — this repository does not maintain a model catalog, for the reason in section 10,
so it neither validates ids against a list nor rewrites them. A model the provider does not know is
the provider's own error, surfaced as one of the typed outcomes in section 5.

v1 configures exactly one provider, so routing is answerable by construction: a request either
reaches it or fails. Nothing is inferred from the model id's shape.

**Tool calls are governed by the existing contract.** A streamed tool call from a real provider is
subject to the same ownership already established for streamed replies — history, policy, source
order, record-before-act. Arriving from a network rather than a fake buys no exemptions.

**Checkable:** an empty model fails with a typed error and sends no request; a tool call from a real
provider is refused by a policy that refuses it, and is recorded before it runs.

## 12. Model substitution: the existing constraint governs, and a provider does not relax it

`internal/runtime/loop.go:339-352`, measured by `TestWrapModelCompositionOrder`.

A real provider arrives as a model instance, which changes nothing about composition order. The
constraint already recorded here still decides the outcome: handlers compose outermost-first, and a
handler that substitutes the model never calls through, so every handler registered after it is
silently skipped. Per-turn model selection is such a substitution and must be registered last.

Adding a provider does not create an exception. If provider selection is ever expressed as a
handler, it goes in the same single list that decides order.

**Checkable:** the existing order test still governs, and nothing in the provider work registers a
handler outside that list.

## 13. Credentials at rest: nothing is written to disk in this version

Pi's default store is in-memory and apps inject persistence (`auth/credential-store.ts:9-14`). This
repository takes the default and injects nothing.

A credential therefore comes from the environment on the machine that runs the process, lives in
memory for the life of the process, and is never written anywhere. There is no file to leak, no file
to get the permissions wrong on, and no file to accidentally commit.

This is a v1 decision, not a permanent one. Persistence is what OAuth needs — a refresh token has to
survive a restart to be worth anything — so the store interface is the shape to implement against
when that arrives. Until then, absent persistence is the safer default and the honest one.

**Checkable:** no code path in this repository writes a credential to a file.

## 14. Mechanical controls

What each claim is checked by. Configuration values are not evidence; every entry below is checked
by observing behaviour.

**Retry and request count** — two complementary controls, because either alone can pass while the
system is wrong:

1. The constructed client is asserted to disable the SDK's own retry. This catches the third,
   invisible layer at its source.
2. A counting transport asserts the number of requests a logical call produces. This catches a
   hidden request whatever its origin — including one the first control cannot see.

A live call must produce exactly one request: the inner budget is zero, there is no outer layer
(section 4), and a minimal prompt cannot overflow, so the recovery path in section 5 cannot add a
second. It is asserted by counting either way, because a topology decision is not a guarantee about
behaviour.

**Quota before rate limit** — two controls, because they check different things:

1. *For this provider:* a 402 is terminal and produces exactly one request; a 429 is retried under a
   positive test-only budget, and produces one request under the shipped budget of zero. Reading the
   status is the correct implementation here, so the test is entitled to reflect that.
2. *For the ordering rule:* a fixture in which exhaustion is reported inside a 429 must still be
   classified terminal. No supported provider sends that today — it exists to hold the rule that
   classification precedes the retry decision, which is what protects the next provider that does.

The second control is the one that fails if the classification is moved to where Pi makes it.

**Typed cause survives** — the classification made at the provider boundary is asserted to be
recoverable at the point that acts on it, after any wrapping. A cause intact at the boundary and
lost by the time it is acted on is indistinguishable, from the outside, from one never made.

**Credential precedence** — four cases, each distinct:

1. A stored key wins over a set env var.
2. No stored key: the first set env var resolves.
3. The first env var is set but empty: resolution continues to the next, and an empty value is never
   returned as a key.
4. Nothing is set anywhere: a typed missing-credential failure.

Case 4 is a deliberate divergence — Pi returns `undefined` (`auth/helpers.ts:28`); this repository
returns a typed failure so the reason survives to whatever reports it.

A fifth control covers all four: the key's value never appears in the error, in a log line, in an
emitted event, or in the session. Only the source's name may.

**An accepted reply is not treated as a rejection** — the recorded 1,015,083-token response, which
the provider accepted with status 200, is a negative fixture for the rejection matcher: it must
never be read as an overflow refusal.

Two things this fixture does *not* do, stated so it is not asked to carry more than it can. It does
not trigger either numeric case — it ended on `length` with one output token, while the first case
needs a normal stop and the second needs `length` with no output at all. And it cannot forbid a
threshold detector from firing, because a detector configured with a window below the response's
size is doing exactly what a threshold means.

The control is therefore on the configuration, not on the fixture: **the authoritative window
configured for this model must not classify this recorded response as an overflow**, and a
configuration with a window at or below 1,015,083 is rejected at construction rather than obeyed.

**A 200 that reports failure is not a completed reply** — two fixtures, one per stop reason. A reply
whose `finish_reason` is `insufficient_system_resource` produces the interruption outcome; one whose
`finish_reason` is `content_filter` produces the refusal outcome. Neither is reported as success and
neither is retried: one request each, asserted by counting. A control that checked only the status
would pass while the defect it exists to catch went straight through.

**The recorded response fixes the usage and reasoning mapping** — reasoning arrives in its own field
with `content` empty and its own token count; usage carries the cache hit and miss split;
`finish_reason: "length"` reaches the existing truncation path. Asserted against the recording rather
than against the source it was predicted from.

**Overflow reaches the recovery path** — a recorded provider rejection for an oversized request
produces `ai.ErrContextOverflow`, and the runtime shortens and asks once more — exactly one further
attempt, as the existing recovery rule requires. A recorded rejection for an ordinary malformed request must *not* produce it, since both
arrive as the same status code. The pair is what makes the detection meaningful rather than a
substring that matches everything.

**Text is read in one place only** — and the check is mechanical, not a review habit. A test walks
the package's syntax tree and fails if any function other than the one named overflow detector reads
an error's message: no `.Error()` inspection, no substring or pattern match against error text,
anywhere in a retry, billing, or credential path. Adding a second reader turns it red, whoever adds
it and whatever their reason.

**The overflow cause survives to the code that acts on it** — `errors.Is(err, ai.ErrContextOverflow)`
must hold at the point the runtime branches (`loop.go:728`), through whatever wrapping happens in
between. A sentinel recognised at the boundary and lost before that branch is indistinguishable from
one never produced, and the recovery silently does not happen.

**The overflow matcher cannot be broad** — three controls, because one positive case proves almost
nothing:

1. A recorded oversized-request rejection produces the sentinel, and the runtime shortens and retries
   exactly once — two requests in total, asserted by counting, which is the bound stated in
   section 5 rather than the single-request bound that applies to a call that does not overflow.
2. Recorded ordinary rejections — malformed body, invalid parameter — must *not* produce it. Whether
   they share a status code with an oversized rejection is unknown, so the control is written to
   hold either way: it separates them by whatever the recording shows actually differs, and if that
   is the status alone, then no text is read at all.
3. **Mutation:** widening the matcher until it accepts any rejection must turn control 2 red. A negative
   control that survives that mutation was never testing the matcher's precision, and a matcher that
   accepts everything would otherwise pass control 1 perfectly while sending every malformed request
   into a pointless shortening attempt.

**Payload** — the request recorded by the test transport is asserted directly: `max_tokens` present,
`max_completion_tokens` absent, no `store`, usage requested. A low cap is shown to bound a reply
rather than assumed to.

**Usage** — reported usage reaches the ledger with no term counted twice, and an unreported field
stays distinguishable from a reported zero.

**Live provider calls** — the conditions are part of this contract, not a convention:

1. Disabled by default, and enabled only by an explicit opt-in that cannot be set accidentally.
2. Never in CI.
3. Credentials only from the injected context — no other route exists (section 7).
4. Shortest usable prompt, low token cap, single request.
5. No outer retry layer and the inner budget at zero. With a prompt too small to overflow, that is
   one billable request — asserted by counting, not by configuration.
6. Provider-reported usage is checked after the run and reported.
7. A failure is not re-run. A live test that fails has produced its information already.

These bound a real key against a real bill. "It's just a smoke test" is not a bound.

## 15. What is not covered

Stated so the table's edges are visible:

- Only one provider. The compatibility record in section 10 is DeepSeek's; another provider needs its
  own row, not an assumption that this one generalises.
- OAuth is not implemented. Section 1's credential model admits it and section 13 explains what
  persistence it would need, but no OAuth flow, refresh, or persistence is built here.
- Cost in currency is not computed. Token counts are carried; the per-token prices live in the
  generated catalog described in section 10, which this repository does not read.
