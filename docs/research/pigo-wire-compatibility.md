# pigo wire-compatibility assessment

## Conclusion

`smallnest/pigo` is a **semantic and feature reimplementation of Pi**, not an implementation of Pi's `packages/protocol` wire contract. At the inspected commit, it does not encode or decode Pi's length-prefixed CBOR messages, does not expose equivalents of Pi's standalone protocol/client/server packages, and cannot directly interoperate with the original Pi client or server.

It does have related capabilities—headless event streaming, subprocess JSON-RPC, a browser remote-control server, and a Pi-extension bridge—but each uses a pigo-specific JSON protocol rather than Pi's client/server CBOR schema.

| Question | Finding |
|---|---|
| Implements Pi `packages/protocol` CBOR framing/schema? | **No** |
| Original Pi client can connect directly to pigo? | **No** |
| pigo can connect directly to original Pi server? | **No** |
| Equivalent standalone protocol/client/server packages? | **No** |
| Pi-like RPC or remote capabilities? | **Partial analogues, different wire contracts** |
| Best classification | **Semantic/feature reimplementation** |

## Pinned sources

- Pi: [`earendil-works/pi@086c32e74530564922d011ade23ff582c9d63116`](https://github.com/earendil-works/pi/tree/086c32e74530564922d011ade23ff582c9d63116)
- pigo: [`smallnest/pigo@ef2c447b754b114b0eea87ff2ad1228bcb11dc84`](https://github.com/smallnest/pigo/tree/ef2c447b754b114b0eea87ff2ad1228bcb11dc84), described by Git as `v0.5.4-4-gef2c447`

## What Pi interoperability requires

Pi protocol version 1 is not generic JSON or generic RPC. Its wire record is a four-byte unsigned big-endian payload length followed by one definite-length CBOR item. The package also mandates a client-first `hello`, correlated request/response envelopes, server events, strict schemas, and rejection of unknown properties ([protocol README, lines 3–10](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/protocol/README.md#L3-L10) and [44–69](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/protocol/README.md#L44-L69)).

The implementation performs that framing explicitly ([`framing.ts`, lines 27–38](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/protocol/src/framing.ts#L27-L38)) and validates, CBOR-encodes, and frames every protocol message ([`codec.ts`, lines 60–85](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/protocol/src/codec.ts#L60-L85)). The schema fixes protocol version 1 ([`schemas.ts`, lines 1–8](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/protocol/src/schemas.ts#L1-L8)) and defines commands including `list`, `create`, `attach`, `detach`, `prompt`, `steer`, `abort`, `set_model`, and `set_thinking` ([lines 291–325](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/protocol/src/schemas.ts#L291-L325)). It then wraps them in typed hello, request, response, and event envelopes ([lines 384–450](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/protocol/src/schemas.ts#L384-L450)).

The original client sends the framed CBOR hello and requires a server hello as the first response ([`packages/client/src/connection.ts`, lines 119–170](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/client/src/connection.ts#L119-L170)). The original server uses the matching decoder ([`packages/server/src/server.ts`, lines 130–149](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/server/src/server.ts#L130-L149)), requires the first client message to be hello, checks the protocol version, and replies with a server hello and snapshot ([lines 170–240](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/server/src/server.ts#L170-L240)). A peer must therefore match the wire framing/encoding contract and message semantics to interoperate. Pi does not require canonical CBOR, so interoperability does not imply that every equivalent map must serialize to one identical byte sequence.

## What pigo implements instead

### 1. No Pi CBOR codec, framing, or schema package

At the pinned pigo commit, a repository-tree and source search finds no Pi protocol package, CBOR codec, CBOR framing implementation, `PROTOCOL_VERSION`, `ClientHello`, or `ServerHello`. Its module dependencies likewise contain no CBOR implementation ([`go.mod`, lines 1–23](https://github.com/smallnest/pigo/blob/ef2c447b754b114b0eea87ff2ad1228bcb11dc84/go.mod#L1-L23)).

Reproducible checks run against that commit:

```bash
git ls-files | grep -Ei 'cbor|packages/(protocol|client|server)|(^|/)protocol/'
git grep -nEi 'CBOR|pi-protocol|PROTOCOL_VERSION|ClientHello|ServerHello|SessionSnapshot'
```

Both searches return no Pi-wire implementation. Files named `protocol.go` in pigo belong to unrelated provider, hook, or remote-control protocols.

### 2. Headless output is one-way newline-delimited JSON

pigo's headless mode runs a single prompt and describes `stream-json` as serializing `AgentEvent` values into line-delimited JSON ([`internal/runtime/headless.go`, lines 1–14](https://github.com/smallnest/pigo/blob/ef2c447b754b114b0eea87ff2ad1228bcb11dc84/internal/runtime/headless.go#L1-L14)). Its encoder creates a pigo-owned map, marshals it with Go's JSON encoder, and appends `\n` ([lines 173–192](https://github.com/smallnest/pigo/blob/ef2c447b754b114b0eea87ff2ad1228bcb11dc84/internal/runtime/headless.go#L173-L192)). The event vocabulary deliberately mirrors Pi behavior ([`internal/agentcore/event.go`, lines 3–34](https://github.com/smallnest/pigo/blob/ef2c447b754b114b0eea87ff2ad1228bcb11dc84/internal/agentcore/event.go#L3-L34)), but sharing names such as `agent_start` or `tool_execution_start` does not make the framing or envelope schema compatible.

This is not Pi's bidirectional coding-agent RPC mode either. Pi RPC consumes JSON commands on stdin and emits responses/events on stdout using strict JSONL framing ([Pi `rpc.md`, lines 20–37](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/docs/rpc.md#L20-L37)); pigo's normal headless path accepts one CLI prompt and only streams output events. Pi's RPC command union covers prompting/steering/follow-up, abort/new-session, state, model and thinking changes, queue modes, compaction/retry, bash control, session switch/fork/clone/tree operations, message/command queries, and extension-UI responses ([`rpc-types.ts`, lines 17–107](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src/modes/rpc/rpc-types.ts#L17-L107) and [276–284](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/coding-agent/src/modes/rpc/rpc-types.ts#L276-L284)). pigo's output-only JSONL is therefore a partial analogue, not capability-equivalent RPC.

### 3. pigo's JSON-RPC is an internal subprocess protocol

pigo does implement JSON-RPC 2.0, but the shared transport is explicitly for subprocess stdio used by plugins and isolated sub-agents, with newline-delimited JSON ([`internal/jsonrpc/message.go`, lines 1–13](https://github.com/smallnest/pigo/blob/ef2c447b754b114b0eea87ff2ad1228bcb11dc84/internal/jsonrpc/message.go#L1-L13) and [68–84](https://github.com/smallnest/pigo/blob/ef2c447b754b114b0eea87ff2ad1228bcb11dc84/internal/jsonrpc/message.go#L68-L84); [`transport.go`, lines 1–7](https://github.com/smallnest/pigo/blob/ef2c447b754b114b0eea87ff2ad1228bcb11dc84/internal/jsonrpc/transport.go#L1-L7) and [31–72](https://github.com/smallnest/pigo/blob/ef2c447b754b114b0eea87ff2ad1228bcb11dc84/internal/jsonrpc/transport.go#L31-L72)).

The only CLI RPC server flag is internal `--subagent-rpc` ([`cmd/pigo/main.go`, lines 96–99](https://github.com/smallnest/pigo/blob/ef2c447b754b114b0eea87ff2ad1228bcb11dc84/cmd/pigo/main.go#L96-L99) and [311–321](https://github.com/smallnest/pigo/blob/ef2c447b754b114b0eea87ff2ad1228bcb11dc84/cmd/pigo/main.go#L311-L321)). It accepts the pigo-specific `subagent/run` method over newline-delimited JSON-RPC ([`internal/cli/headless/subagent_rpc.go`, lines 1–10](https://github.com/smallnest/pigo/blob/ef2c447b754b114b0eea87ff2ad1228bcb11dc84/internal/cli/headless/subagent_rpc.go#L1-L10) and [30–85](https://github.com/smallnest/pigo/blob/ef2c447b754b114b0eea87ff2ad1228bcb11dc84/internal/cli/headless/subagent_rpc.go#L30-L85)). This is neither Pi's CBOR session protocol nor Pi coding-agent RPC.

### 4. The remote server is a pigo browser-control protocol

pigo's remote-control server is an in-process HTTP/WebSocket server for mirroring one CLI session to one paired browser ([`internal/remotecontrol/server.go`, lines 80–98](https://github.com/smallnest/pigo/blob/ef2c447b754b114b0eea87ff2ad1228bcb11dc84/internal/remotecontrol/server.go#L80-L98)). Its JSON frame types are `output`, `input`, `confirm`, `decide`, and `status` ([`internal/remotecontrol/protocol.go`, lines 3–18](https://github.com/smallnest/pigo/blob/ef2c447b754b114b0eea87ff2ad1228bcb11dc84/internal/remotecontrol/protocol.go#L3-L18) and [27–46](https://github.com/smallnest/pigo/blob/ef2c447b754b114b0eea87ff2ad1228bcb11dc84/internal/remotecontrol/protocol.go#L27-L46)), and the server reads/writes them with `wsjson` ([`server.go`, lines 313–362](https://github.com/smallnest/pigo/blob/ef2c447b754b114b0eea87ff2ad1228bcb11dc84/internal/remotecontrol/server.go#L313-L362) and [444–450](https://github.com/smallnest/pigo/blob/ef2c447b754b114b0eea87ff2ad1228bcb11dc84/internal/remotecontrol/server.go#L444-L450)). It has no Pi hello/version negotiation, request envelopes, server/session snapshots, or Pi command schema.

### 5. Pi extension support is an adapter, not client/server compatibility

pigo can load a real Pi TypeScript extension runtime and re-expose selected tools and commands through **pigo's** JSON-RPC plugin protocol ([`internal/pihost/embed.go`, lines 1–9](https://github.com/smallnest/pigo/blob/ef2c447b754b114b0eea87ff2ad1228bcb11dc84/internal/pihost/embed.go#L1-L9)). The host explicitly states that unsupported Pi session/model/UI/provider/widget actions become inert no-ops ([`internal/pihost/pihost.mjs`, lines 2–22](https://github.com/smallnest/pigo/blob/ef2c447b754b114b0eea87ff2ad1228bcb11dc84/internal/pihost/pihost.mjs#L2-L22)) and frames its own traffic as newline-delimited JSON-RPC ([lines 284–304](https://github.com/smallnest/pigo/blob/ef2c447b754b114b0eea87ff2ad1228bcb11dc84/internal/pihost/pihost.mjs#L284-L304)). This is evidence of a targeted compatibility bridge, not implementation of Pi's remote session protocol.

## Implication for pi-go

pigo is useful as primary-source evidence for Go implementation techniques and feature-level parity, but it provides no shortcut to Pi client/server interoperability. If pi-go wants existing Pi clients to connect directly, it must implement the pinned Pi CBOR framing, strict schemas, handshake, envelopes, and session semantics—or ship an explicit compatibility adapter that does so.

Because Pi labels this protocol experimental and gives it no compatibility guarantee ([protocol README, line 69](https://github.com/earendil-works/pi/blob/086c32e74530564922d011ade23ff582c9d63116/packages/protocol/README.md#L69)), any wire-compatible pi-go target should remain pinned to an exact Pi commit/protocol version and be guarded by cross-implementation conformance tests.
