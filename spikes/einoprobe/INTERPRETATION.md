# Pre-registered interpretation table

**Written before the TurnLoop observation runs.** Its purpose is to fix "observing X means Y"
*ahead* of seeing X, so the trace functions as evidence rather than as material for a conclusion
already reached.

**Review this table before reading the trace.** If the table is only judged after the results are
known, it provides no protection.

Related: `docs/specs/eino-verification-plan.md` (spikes #4/#5/#6),
`docs/specs/behaviour-contracts.md` (C1, C8), eino baseline v0.9.14 / `32759e6`.

---

## Scenario preconditions

A trace can be perfectly recorded and still fail to exercise the thing being judged. These three
conditions must hold or the run is void — the verdict is not "inconclusive", it is **invalid**, and
the scenario is fixed and re-run.

**P1 — the nesting scenario must force a second model call.**
The first model reply must *deterministically* produce a tool call, and the tool's completion must be
followed by a second model call. A single toolless reply cannot distinguish N1 from N2: with only one
model call there is nothing to count between `PrepareAgent` instances.

**P2 — `Push` must be triggered by a deterministic barrier, never a sleep.**
The injection point must be marked explicitly — first `GenInput` consumed; tool running or completed
but the next model call not yet begun. Timing-based injection lets scheduling races decide the
result, which would contaminate both C1a and C1b.

**P3 — "tool results not discarded" is asserted on later context, not on history.**
C1b's third requirement is satisfied only if the tool result is still present in **the next model
call's input** (or in the post-recovery context). Having appeared earlier in the timeline proves the
event was emitted, not that it survived — which is the actual question.

---

## Why the rows are observable criteria, not definitions

An earlier draft used rows like "eino turn *equals* pi's turn" and "eino turn *wraps* the
model→tool→model cycle". That makes the verdict depend on how we translate the word "turn", which
is exactly the ambiguity the spike exists to remove.

Each row below is instead decided by **counts and orderings visible in the raw timeline**, using the
three numbered layers: `GenInput iteration` · `PrepareAgent instance` · `model call / tool event`.

---

## Table 1 — nesting of the three layers

Decide by: **(a)** how many model calls occur between two consecutive `PrepareAgent` instances,
**(b)** whether tool events appear between model calls *within* one `PrepareAgent` instance, and
**(c)** whether `GenInput`/`PrepareAgent` is re-entered before the next model call.

| # | Observable criteria | Meaning for **C8** (per-turn model swap) | Meaning for layer mapping |
| --- | --- | --- | --- |
| **N1** | exactly **1** model call per `PrepareAgent` instance; no tool events between model calls inside one instance; `PrepareAgent` re-entered before each next model call | `PrepareAgent` is a viable landing point for a per-model-call configuration change | eino's unit is at or below one model call |
| **N2** | **>1** model call per `PrepareAgent` instance, **with** tool events between them; `PrepareAgent` **not** re-entered before the next model call | `PrepareAgent` is **coarser than C8 requires** — a per-turn swap has no landing point here; needs another mechanism or our own loop | eino's unit wraps the whole model→tool→model cycle |
| **N3** | `PrepareAgent` instances occur **more often** than model calls | landing point exists but is finer than pi's turn; must then check whether repeated instances break context continuity | eino's unit is finer than pi's turn |
| **N4** | **none of the above**, mixed, or varies between runs | **inconclusive — do not force-fit.** Return to eino source or split into a smaller probe | unresolved |

**N4 exists deliberately.** A three-way table implies one of three answers must be true, which would
push an awkward observation into the nearest box. Recording "the categories did not fit" is a
legitimate and useful result.

---

## Table 2 — C1, judged as two independent questions

`GenInput` covering follow-up does **not** settle whether steering is covered, and vice versa. The
two mechanisms can hold or fail independently, so they get separate verdicts.

### C1a — does `GenInput` cover **follow-up**?
Decide by: an item pushed **while a turn is in flight** — is it (a) buffered and consumed at the
next `GenInput` iteration, (b) consumed within the current turn, or (c) dropped?

| Observation | Verdict |
| --- | --- |
| buffered, then consumed at the next `GenInput` iteration | follow-up **covered** by `GenInput` |
| consumed within the current turn, before the next model call | that is steering behaviour, not follow-up — re-examine C1b |
| dropped, or requires the loop to exit first | follow-up **not covered**; pi's outer loop needs our own implementation |

### C1b — does `Push` + `WithPreempt(safePoint)` cover **steering**?
Decide by pi's three requirements, each independently:

| Requirement | Observable check |
| --- | --- |
| enters context **after a safe point and before the next model call** | pushed content appears in the input of the *next* model call, and a safe-point boundary is recorded before it |
| **not** treated as a new session | no session/context reset in the timeline; prior messages still present in that next model call's input |
| completed tool results **not** discarded | the tool result is present in **the next model call's input** (or post-recovery context) — per **P3**, presence earlier in the timeline is not sufficient |

**All three must hold** for C1b to pass. Partial satisfaction is recorded as partial, not rounded up.

---

## M1-M4 — multi-handler `WrapModel` composition (registered before running)

**Question.** When two `WrapModel` handlers are registered and one **substitutes** the model
outright (discarding the model it was given), does it silently bypass the other?

This matters because pi's per-turn model selection *is* a substitution. ADR-0002 lists the bypass
risk in Consequences; this probe decides whether the risk is real at the pinned baseline.

**Why option values alone cannot answer it.** "No temperature observed" looks identical whether the
injecting wrapper was discarded or simply never constructed. So the probe records two independent
things: which model **instance served** the call (`servingModel=`), and whether the injector's
wrapper was **traversed at call time** (`chain:injectorTraversed`) rather than merely built.

| Row | Observation | Reading |
| --- | --- | --- |
| **M1** bypass | serving model = substitute, injector traversed **0** times, no injected option | the substituter discarded the injector's wrapper — ordering/ownership must be made explicit in pi-go, since a model-selection handler would silently void other middleware |
| **M2** survives | serving model = substitute, injector traversed **1** time, injected option arrives | the injector composes **outside** the substituter; substitution does not void it |
| **M3** no substitution | serving model = **original** | substitution did not take effect at all — invalidates the probe's premise, re-probe |
| **M4** anything else | injector traversed ≠ 0 or 1 for a single model call | unregistered outcome — record raw, no verdict |

**Coherence checks (failable).** M1 with an injected option present, or M2 with it absent, is
self-contradictory and fails rather than being reported.

**Both registration orders are run.** If the verdict differs between them, order is
caller-controlled and pi-go must pin it; if identical, composition order is fixed by eino.

### M-result (recorded after running `TestWrapModelCompositionOrder`)

| Registration order | Verdict | Observed |
| --- | --- | --- |
| `[injector, substituter]` | **M2** | serving = substitute, injector traversed 1x, `observedTemperature=7.0` |
| `[substituter, injector]` | **M1** | serving = substitute, injector traversed 0x, `observedTemperature=nil` |

**Registration order is outermost-first**, and the verdict differs by order — so per the row above,
**pi-go must pin handler order**; it is caller-controlled, not fixed by eino.

**The table's M1 row was too weak, and the run falsified an assumption behind it.** M1 was written
as "constructed then discarded", which presumes handlers are all composed up front. They are not.
Composition is **lazy**: in the M2 arm the injector's `Generate` ran at seq 4, *before* the
substituter's `WrapModel` fired at seq 5. Each handler is reached only if the handler outside it
calls through to it. So in the M1 arm the injector's `WrapModel` **was never invoked at all** — a
strictly more severe bypass than the row anticipated.

Recorded rather than retro-fitted: the original M1/M2 wording is left above unedited, because a
pre-registration that gets quietly rewritten to match the result stops being evidence.

---

## Standing rule

If the trace does not clearly satisfy a row's criteria, the verdict is **inconclusive** and the next
step is a smaller probe or a source read — **not** a judgement call about which row it "basically"
matches.
