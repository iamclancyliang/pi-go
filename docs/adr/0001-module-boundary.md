# ADR-0001: Module boundary — `internal/` plus one public package

**Status:** **accepted** — approved by @qy-liang 2026-08-15
**Date:** 2026-08-15
**Related:** `docs/architecture/architecture.md` §1, issue #2 · **Supersedes nothing**

## Context

pi-go is a complete Go reimplementation of pi. "Complete" means the surface eventually spans ~20
parity areas, built over multiple releases by more than one contributor. Two forces act on that:

1. **Behaviour contracts must be preservable.** `docs/specs/behaviour-contracts.md` defines C1–C8 as
   *observable* semantics. They are only meaningful if the boundary they are observed across is
   stable.
2. **Almost everything else should stay free to change.** The eino ownership question (ADR-0002),
   the extension transport (ADR-0003), and the wire strategy (architecture §6.4) are all open. A
   large published surface would freeze internals before those land.

pi enforces its own equivalent of this split **by convention** — TypeScript cannot do better. Its
`AGENTS.md` and package layout describe which boundaries matter, and nothing stops a contributor
crossing them.

**Go can do better**: the compiler enforces `internal/` visibility by directory parent tree, so the
boundary is checked at build time rather than left to review discipline. (Precise mechanism and its
effect for this repository are stated in the Decision below.)

## Decision

**Product implementation lives under `internal/`, except for `cmd/` composition roots, one public
facade, and generated/distribution files. The public surface exposes Go standard-library types and
pi-go-owned types, and never third-party types.**

- `internal/...` — every module from architecture §1: runtime core, session, ai, tui, wire schema,
  extension host, telemetry.
- `cmd/...` — composition roots deliberately live here. They only **assemble** modules and carry no
  business behaviour. This is a discoverability and thin-entry-point convention, **not** a compiler
  constraint: Go permits a `main` package under `internal/` and will build it, since the `internal`
  rule restricts *imports*, not package names.
- **one public package** — the SDK facade. Its contents are an explicit decision each time something
  is added, not a side effect of where a file was placed.

**Type policy for the public surface.** Go **standard-library** types (`context.Context`,
`io.Reader`, `time.Duration`, …) and pi-go-owned types are allowed — PRD NFR-2 requires
`context.Context` on concurrent and streaming interfaces, so a "built-ins only" rule would be both
wrong and unusable. What is prohibited is **re-exporting third-party types, notably eino's**: that
leaks a framework choice into our published contract and undermines ADR-0002's reversibility.

**Release scope of "one public package".** This is the **Phase 0 / v0 initial shape**, not a
permanent cap. Adding a further public package or module requires a **real external consumer or a
parity requirement**, and goes through separate review (an ADR when it changes the module
partition). This prevents guessing at interfaces today without pre-closing v2/v3 — full parity
plausibly wants an independently consumable TUI library and protocol/client packages later
(architecture §6.2).

**Mechanism, precisely.** Go enforces `internal/` by **directory parent tree**: a package under
`.../internal/...` may only be imported by code rooted at that `internal/`'s parent. With
`internal/` at the repository root, the practical effect is that **nothing outside this repository
can import it** — which is the property we want, achieved by the compiler rather than by review.

## Consequences

**Positive**
- The contract/implementation split is **enforced by the compiler**, not by review vigilance. A
  contributor cannot accidentally widen the public surface; they must move code deliberately.
- ADR-0002 and ADR-0003 stay genuinely reversible — no consumer can depend on internals of a
  decision that isn't final.
- Refactoring inside `internal/` never breaks a consumer, which is what makes an incremental
  20-area parity effort survivable.

**Negative**
- Anything a consumer legitimately needs must be *deliberately promoted* to the public package. That
  is friction, and it is the intended cost.
- Test code outside this repository (including any separate test module) cannot import `internal/`;
  conformance tests for C1–C8 must therefore
  run **inside** the module, or exercise behaviour through the public package. This is a real
  constraint on how the conformance suite is structured — decide it when the suite is built.
- A single public package can become a grab-bag. Mitigation: every addition names the consumer that
  required it.

**Precedent.** `smallnest/pigo` uses the same structure — implementation under `internal/`, with one
officially supported public interface package that deliberately restricts its exported types to
avoid pulling internals into its published surface. (Its own README describes this as using "Go
built-in types"; that phrasing is loose in the same way ours was — the operative property is *not
leaking internal or third-party types*, not literal built-ins.) Independent arrival at the same
shape for the same problem. Note this is precedent, not proof: pigo also made choices we are explicitly not copying
(see `docs/research/pigo-wire-compatibility.md`).

## Alternatives considered

- **Conventional packages, no `internal/`** — what pi does, because TypeScript offers nothing
  better. Rejected: it reproduces a limitation rather than a design, and gives up the one place
  where the Go port is structurally stronger than the original.
- **Multiple public packages from the start** — rejected as premature *for v0*. Package boundaries
  are published contracts; with ADR-0003 still open we cannot yet know which are stable. (ADR-0002
  was accepted 2026-08-17 and does not weaken this: the extension transport, not loop ownership, is
  what would reshape the public surface.) Consistent
  with the release scope above: promotion is expected later, gated on a real consumer, not
  forbidden.
- **Nested Go modules per subsystem** — rejected for now (architecture §6.2). Independence is
  enforced by import direction and an architecture test; a nested `go.mod` is version-management
  cost that should follow a real need, such as an independent release cadence.

## Open

The **name and exact contents** of the public package are not decided here. That should follow the
v0 tracer bullet, once there is a real consumer to justify each export.
