# ADR-0006: Native wire with semantic equivalence, not Pi interoperability

**Status:** accepted
**Date:** 2026-08-15
**Decision owner:** @qy-liang
**Related:** `docs/product/PRD.md` §5.5, §8 A13, §10.2; `docs/architecture/architecture.md` §6.4; `docs/product/parity-matrix.md` · **Supersedes nothing**

## Context

Pi exposes protocol, client, server, and RPC/session-control capabilities. A Go reimplementation can
either adopt Pi's wire schema, maintain a live compatibility adapter beside a native protocol, or
define its own protocol and preserve only the remote capabilities and observable semantics.

This choice is hard to reverse because it determines who owns schema evolution, what compatibility
claims releases may make, and whether every protocol change must be mapped across two live wire
surfaces. It also changes the evidence for parity: direct interoperability is a strong binary test,
while semantic equivalence requires a more explicit capability inventory.

## Decision

**pi-go owns a native wire protocol and does not provide live Pi wire interoperability.** Existing Pi
clients cannot connect directly to pi-go servers, and pi-go clients cannot connect directly to Pi.
pi-go does not bundle a Pi compatibility endpoint, adapter, or gateway.

The decision owner selected option C from the documented A/B/C trade-off; no further personal
rationale was stated. This ADR records the consequences accepted by that selection rather than
inventing an unstated motivation.

“Semantic equivalence” means every remote capability in the pinned Pi inventory is accounted for and
classified as one of:

1. implemented through a pi-go-native command/event with equivalent observable behavior;
2. an explicit accepted deviation with user impact and rationale; or
3. incomplete and therefore blocking the relevant parity release.

It does **not** mean compatible framing, hello/version negotiation, schema, error codes, CBOR bytes,
or unchanged-client operation. Documentation and release notes must never call the native protocol
“Pi wire-compatible.”

## Required evidence

Because unchanged-client interoperability is no longer available as the oracle, v3 acceptance must
use a pinned, itemized capability matrix rather than a general “remote control works” claim. At
minimum it covers:

- connection/version behavior, request correlation, typed errors, snapshots, progress, and removal;
- session list/create/attach/detach and lifecycle/locking;
- prompt, steering, abort, model selection, and thinking-level control;
- transcript/message/tool state, usage, queued input, and event ordering, including C4/C4.0 and C6;
- coding-agent RPC command families for state, queue modes, compaction/retry, bash, session
  navigation, export, resource commands, and extension-UI degradation.

The approved source pin is Pi `086c32e74530564922d011ade23ff582c9d63116`; @qy-liang approved it as
the capability denominator on 2026-08-15 in the separate PRD/baseline decision. Capabilities added
by upstream Pi after the pin do not enter the denominator automatically. Re-pinning is an explicit
reviewed product decision, with the capability matrix regenerated against the new commit.

A13 additionally proves the incompatibility boundary: an unchanged Pi client is not presented as a
supported client, and migration documentation does not imply a bundled live adapter.

## Consequences

**Positive**

- The wire schema can be designed for Go and pi-go's own domain model instead of inheriting Pi's
  TypeScript-era shape.
- Pi protocol evolution does not force parallel changes in pi-go.
- The project avoids operating and testing two live protocol stacks plus a lossless mapper.

**Negative**

- Existing Pi clients, servers, and integrations cannot be reused unchanged.
- “Complete Pi reimplementation” must be explained as capability and behavioral parity, not wire
  interoperability.
- Capability-based evidence is easier to weaken accidentally, so the command/event matrix is a
  release gate and omissions cannot be summarized away.
- Migration and explicit incompatibility documentation are required wherever users might otherwise
  assume direct connection.

Building a gateway later is a separate product and architecture decision. It is not implied by this
ADR. Reconsidering live Pi interoperability requires a new ADR and new bidirectional compatibility
tests; it must not silently reshape the native protocol.

## Alternatives considered

- **A — Pi-native wire.** Strongest interoperability proof, but makes Pi's public wire the schema
  source and follows its protocol evolution. Not selected.
- **B — pi-go-native wire plus bundled live Pi adapter.** Preserves unchanged clients without making
  Pi's schema the internal truth, but requires two maintained wire surfaces and a lossless mapping.
  Not selected.
- **C — semantic equivalence without a live adapter.** Selected by @qy-liang on 2026-08-15 with the
  explicit loss of direct interoperability recorded above.
