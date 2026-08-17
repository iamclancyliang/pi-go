#!/usr/bin/env python3
"""Generate pi-go feature ID sets from a pinned Pi checkout.

Usage:
    python3 tools/gen-feature-ids.py --pi-repo /path/to/pi [--baseline <sha>]

Every set carries TWO separate authorities, and conflating them silently
produces a set with the right size and the wrong members:

  * membership authority - who is a member (a registry, a union type)
  * name authority       - what that member is called (the identifier the
                           source itself declares for it)

A file name or a function symbol is neither. Neither is "the first thing in
the file that looks like an id".

Normalization, source literal -> feature ID, applied in this order so a
consumer reproduces it rather than inverting it:

  1. camelCase boundary -> '-', only between [a-z0-9] and [A-Z]
  2. '_' -> '-'
  3. lowercase

The source literal is preserved wherever it differs from the ID.

Exit status is 0 only when every set was fully resolved. Any unreadable
source, unresolved member name, duplicate normalized ID, or membership/name
count mismatch exits non-zero. A short set must never look like a good one.
"""

from __future__ import annotations

import argparse
import re
import subprocess
import sys

# Full SHA: an abbreviation can become ambiguous as the object store grows.
DEFAULT_BASELINE = "086c32e74530564922d011ade23ff582c9d63116"

errors: list[str] = []


class SourceUnavailable(Exception):
    """A required source could not be read; parsing must not continue."""


def fail(message: str) -> None:
    errors.append(message)
    print(f"ERROR: {message}", file=sys.stderr)


class Source:
    """Reads files out of a pinned commit, checking git actually succeeded."""

    def __init__(self, repo: str, baseline: str) -> None:
        self.repo = repo
        self.baseline = baseline

    def _git(self, *args: str) -> subprocess.CompletedProcess:
        """Run git in the repo, surviving an unusable working directory.

        subprocess raises OSError before git ever runs when cwd does not exist
        or cannot be entered, which would surface as a traceback rather than the
        named diagnostic this tool promises.
        """
        try:
            return subprocess.run(
                ["git", *args], capture_output=True, text=True, cwd=self.repo,
            )
        except OSError as exc:
            print(f"ERROR: cannot use --pi-repo {self.repo!r}: {exc.strerror}",
                  file=sys.stderr)
            sys.exit(2)

    def verify(self) -> None:
        # Let Git decide what a repository is. Testing for a `.git` DIRECTORY
        # assumes a primary worktree; in a linked worktree `.git` is a file, and
        # such a checkout is exactly what a reproducible pinned read wants.
        probe = self._git("rev-parse", "--git-dir")
        if probe.returncode != 0:
            print(f"ERROR: {self.repo} is not inside a git repository", file=sys.stderr)
            sys.exit(2)

        # The baseline must be a full 40-hex commit id. A symbolic ref such as
        # HEAD, or an abbreviation, would let the read follow a moving branch
        # and silently defeat the point of pinning.
        if not re.fullmatch(r"[0-9a-f]{40}", self.baseline):
            print(
                f"ERROR: baseline {self.baseline!r} is not a full 40-character commit id.\n"
                f"       Symbolic refs and abbreviations are rejected: a pinned read must "
                f"name one immutable commit.",
                file=sys.stderr,
            )
            sys.exit(2)

        resolved = self._git("rev-parse", "--verify", f"{self.baseline}^{{commit}}")
        if resolved.returncode != 0:
            print(
                f"ERROR: baseline {self.baseline} is not a commit in {self.repo}\n"
                f"       {resolved.stderr.strip()}",
                file=sys.stderr,
            )
            sys.exit(2)
        canonical = resolved.stdout.strip()
        if canonical != self.baseline:
            print(
                f"ERROR: baseline {self.baseline} resolved to {canonical}; refusing to "
                f"read a commit other than the one named",
                file=sys.stderr,
            )
            sys.exit(2)

    def read(self, path: str) -> str:
        result = self._git("show", f"{self.baseline}:{path}")
        if result.returncode != 0:
            # Returning an empty string here would let the caller index into
            # nothing and raise deep in the parse, long after the real cause.
            raise SourceUnavailable(
                f"cannot read {path} at {self.baseline}: {result.stderr.strip()}"
            )
        return result.stdout


def normalize(literal: str) -> str:
    with_boundaries = re.sub(r"(?<=[a-z0-9])(?=[A-Z])", "-", literal)
    return with_boundaries.replace("_", "-").lower()


def emit(family: str, membership_authority: str, name_authority: str,
         literals: list[str]) -> None:
    members = sorted({(literal, normalize(literal)) for literal in literals})

    seen: dict[str, str] = {}
    for literal, feature_id in members:
        if feature_id in seen and seen[feature_id] != literal:
            fail(f"{family}: normalized ID collision {feature_id!r} from "
                 f"{seen[feature_id]!r} and {literal!r}")
        seen[feature_id] = literal

    print(f"### `{family}` — {len(members)} members\n")
    # Two trailing spaces would be a CommonMark hard break, but they also make
    # `git diff --check` report trailing whitespace on every regeneration.
    # A list keeps the two authorities visually paired without that.
    print(f"- **Membership authority:** {membership_authority}")
    print(f"- **Name authority:** {name_authority}\n")
    print("```")
    for literal, feature_id in members:
        suffix = "" if feature_id == literal else f"    # source literal: {literal}"
        print(f"{family}.{feature_id}{suffix}")
    print("```\n")


def balanced_argument(source: str, open_paren: int) -> str | None:
    """Return the text inside a call's parentheses, matching them properly.

    A fixed-size window is unsound: an unclassifiable call can borrow the type
    literal belonging to the NEXT call and appear classified. Only the argument
    text actually inside this call may be inspected.
    """
    depth = 0
    for index in range(open_paren, len(source)):
        char = source[index]
        if char == "(":
            depth += 1
        elif char == ")":
            depth -= 1
            if depth == 0:
                return source[open_paren + 1: index]
    return None


def union_literals(source: str, declaration: str, what: str) -> list[str] | None:
    """Collect `type: "..."` literals from one TypeScript union declaration.

    The union is bounded by its own STRUCTURE - consecutive lines belonging to
    the declaration - rather than by a text marker or a line range. A marker
    assumes something follows the union, which is false when it ends the file;
    a line range silently truncates when the file moves.

    Returns None when the declaration cannot be located, so the caller can skip
    emitting a set rather than publish a short one.
    """
    match = re.search(rf"^{re.escape(declaration)}\s*$", source, re.M)
    if not match:
        fail(f"cannot locate {what}: declaration {declaration!r} not found")
        return None

    collected: list[str] = []
    for line in source[match.end():].splitlines():
        stripped = line.strip()
        if not stripped or stripped.startswith("//") or stripped.startswith("*") \
                or stripped.startswith("/*"):
            continue
        # A union member, or a continuation of one, always begins with `|` or is
        # indented inside a member's braces. Anything at column zero that is not
        # a pipe has ended the declaration.
        if not (stripped.startswith("|") or line.startswith((" ", "\t"))):
            break
        collected.extend(re.findall(r'type: "([a-z_]+)"', line))

    if not collected:
        fail(f"{what} yielded no type literals - the union shape probably changed")
        return None
    return collected


def coding_agent_tool_names(src: Source) -> list[str] | None:
    """Membership from the registry set, not from a module list passed in here.

    An earlier version took a hardcoded list of seven modules. It produced the
    right answer only because the list happened to match; adding a tool
    upstream would have left the set stale while the run still succeeded.
    """
    source = src.read("packages/coding-agent/src/core/tools/index.ts")
    declared = re.search(
        r"export const allToolNames: Set<ToolName> = new Set\(\[(.*?)\]\)", source, re.S
    )
    if not declared:
        fail("cannot locate allToolNames in core/tools/index.ts")
        return None
    names = re.findall(r'"([a-z_]+)"', declared.group(1))
    if not names:
        fail("allToolNames yielded no entries - its shape probably changed")
        return None
    return names


def harness_tool_names(src: Source) -> list[str] | None:
    """Membership from the exported tool creators in the harness barrel file."""
    source = src.read("packages/agent/src/harness/tools/index.ts")

    # Only names inside `export { ... }` blocks count. Scanning the whole file
    # for any create*Tool reference would pick up an internal alias or a
    # commented mention and emit a tool that is not exported at all.
    creators: set[str] = set()
    for block in re.finditer(r"export\s*\{([^}]*)\}", source, re.S):
        for name in re.findall(r"\bcreate([A-Z][A-Za-z]*)Tool\b", block.group(1)):
            creators.add(name)

    if not creators:
        fail("no exported tool creators found in harness tools/index.ts")
        return None
    return [normalize(name) for name in sorted(creators)]


def app_modes(src: Source) -> list[str] | None:
    """The modes the application actually runs as.

    Two sets exist here and are easy to conflate:

      * `AppMode` - what the app runs as: interactive, print, json, rpc
      * the `--mode` flag's accepted literals - text, json, rpc

    They are different, and neither contains "json-event". Membership is
    `AppMode`, because that is the product surface; the CLI literals are its
    input mapping and are recorded as evidence rather than as members.
    """
    source = src.read("packages/coding-agent/src/core/project-trust.ts")
    declared = re.search(r"export type AppMode = ([^;]+);", source)
    if not declared:
        fail("cannot locate the AppMode type in core/project-trust.ts")
        return None
    modes = re.findall(r'"([a-z-]+)"', declared.group(1))
    if not modes:
        fail("AppMode yielded no literals - its shape probably changed")
        return None
    return modes


def cli_mode_literals(src: Source) -> list[str]:
    """The literals `--mode` accepts, cross-checked against the Mode type.

    The type and the parser must agree; if they diverge the parser is what a
    user experiences, so a mismatch is an error rather than a silent choice.
    """
    source = src.read("packages/coding-agent/src/cli/args.ts")
    declared = re.search(r'export type Mode = ([^;]+);', source)
    if not declared:
        fail("cannot locate the Mode type in cli/args.ts")
        return []
    from_type = re.findall(r'"([a-z-]+)"', declared.group(1))

    guard = re.search(r'const mode = args\[\+\+i\];\s*if \(([^)]+)\)', source)
    if not guard:
        fail("cannot locate the --mode acceptance test in cli/args.ts")
        return []
    from_parser = re.findall(r'mode === "([a-z-]+)"', guard.group(1))

    if sorted(from_type) != sorted(from_parser):
        fail(f"Mode type {sorted(from_type)} disagrees with the parser's accepted "
             f"values {sorted(from_parser)}")
        return []
    return from_type


def setting_keys(src: Source) -> list[str] | None:
    """Top-level keys of the Settings interface.

    Authority is the interface declaration, not the settings documentation:
    the two are separate artefacts and may disagree.
    """
    source = src.read("packages/coding-agent/src/core/settings-manager.ts")
    match = re.search(r"^export interface Settings \{$", source, re.M)
    if not match:
        fail("cannot locate the Settings interface declaration")
        return None

    keys: list[str] = []
    for line in source[match.end():].splitlines():
        if line.startswith("}"):
            break
        found = re.match(r"\t([a-zA-Z][a-zA-Z0-9]*)\??:", line)
        if found:
            keys.append(found.group(1))
    if not keys:
        fail("Settings interface yielded no keys - its shape probably changed")
        return None
    return keys


def rpc_event_ids(src: Source) -> list[str] | None:
    """The RPC stdout event set is the union of THREE sources.

    Enumerating it from the documentation table gives 21 and misses three
    events that are emitted but undocumented, so each source is read directly.
    """
    agent_events = union_literals(
        src.read("packages/agent/src/types.ts"),
        "export type AgentEvent =", "AgentEvent",
    )
    session_events = union_literals(
        src.read("packages/coding-agent/src/core/agent-session.ts"),
        "export type AgentSessionEvent =", "AgentSessionEvent",
    )
    if agent_events is None or session_events is None:
        return None

    # RPC forwards session events to stdout and adds one of its own. The same
    # stdout stream also carries UI *requests*, which are a different kind:
    # a request expects a reply, an event does not. Those are catalogued as
    # `coding-agent.rpc.ui` and must not be counted again here.
    request_envelopes = {"extension_ui_request"}

    rpc_mode = src.read("packages/coding-agent/src/modes/rpc/rpc-mode.ts")

    # Every `output(` call must be classifiable, not just the ones whose object
    # literal fits on one line. A single-line pattern silently ignores
    # multi-line objects, variables and helpers, so a payload added later would
    # leave this set stale while the run still succeeded.
    # rpc-mode writes THREE kinds of payload to one stdout stream, and only the
    # first contributes members here:
    #   events            - literal `{ type: "..." }`, plus session events
    #                       forwarded verbatim by toJsonEvent
    #   UI requests        - expect a reply; catalogued as coding-agent.rpc.ui
    #   command responses  - built by the success/error helpers; catalogued by
    #                        the response union
    #
    # Helpers are named rather than pattern-matched, so a new helper is an error
    # instead of a silent omission.
    forwarders = {"toJsonEvent"}          # already counted via AgentSessionEvent
    response_helpers = {"success", "error"}
    # `output(response)` writes the awaited handleCommand result: a command
    # response, catalogued by the response union. Classified by name because it
    # is a variable, which means a NEW variable-form emission still fails here
    # and has to be classified deliberately.
    response_variables = {"response"}

    written: set[str] = set()
    for call in re.finditer(r"\boutput\(", rpc_mode):
        argument = balanced_argument(rpc_mode, call.end() - 1)
        if argument is None:
            fail("rpc-mode has an output( call with unbalanced parentheses")
            continue

        called = re.match(r"\s*([A-Za-z_][A-Za-z0-9_]*)\(", argument)
        if called and called.group(1) in forwarders | response_helpers:
            continue

        bare = re.fullmatch(r"\s*([A-Za-z_][A-Za-z0-9_]*)\s*", argument)
        if bare and bare.group(1) in response_variables:
            continue

        literal = re.search(r'type: "([a-z_]+)"', argument)
        if literal:
            written.add(literal.group(1))
            continue

        snippet = " ".join(argument[:80].split())
        fail(f"rpc-mode has an output() call that is neither a known helper nor a "
             f"readable payload type: {snippet!r} - classify it before this set is trusted")

    if "extension_error" not in written:
        fail("expected rpc-mode to emit extension_error; not found")

    unexpected = written - request_envelopes - {"extension_error"}
    if unexpected:
        fail(f"rpc-mode writes unclassified payloads to stdout: {sorted(unexpected)} - "
             f"each must be classified as an event or a request before it is counted")

    rpc_only = sorted(written - request_envelopes)
    return agent_events + session_events + rpc_only


def provider_ids(src: Source) -> list[str]:
    """Registry decides membership; each provider's createProvider id names it."""
    registry_source = src.read("packages/ai/src/providers/all.ts")
    symbol_to_file = dict(
        re.findall(r'import \{ (\w+Provider) \} from "\./([\w.-]+)\.ts"', registry_source)
    )
    body = re.search(
        r"function builtinProviders\(\): Provider\[\] \{\s*return \[(.*?)\];",
        registry_source, re.S,
    )
    if not body:
        fail("cannot locate builtinProviders() in providers/all.ts")
        return []

    resolved_by_factory: list[tuple[str, str]] = []
    factories = re.findall(r"(\w+Provider)\(\)", body.group(1))
    for symbol in factories:
        module = symbol_to_file.get(symbol)
        if not module:
            fail(f"provider factory {symbol} has no matching import")
            continue
        provider_source = src.read(f"packages/ai/src/providers/{module}.ts")

        # Anchor on createProvider's own id. Searching for the first `id:` in
        # the file picks up auth-option ids instead, which yields a set of the
        # right size whose members are not providers at all.
        anchored = re.search(r'createProvider(?:<[^>]*>)?\(\{\s*id:\s*"([a-z0-9-]+)"', provider_source)
        if anchored:
            resolved_by_factory.append((symbol, anchored.group(1)))
            continue

        # Some providers take a caller-supplied id with a literal default.
        configurable = re.search(r'const id = options\.id \?\? "([a-z0-9-]+)"', provider_source)
        if configurable:
            resolved_by_factory.append((symbol, configurable.group(1)))
            continue

        fail(f"cannot resolve provider id for {module} - refusing to guess")

    # Detect duplicates BEFORE anything deduplicates them. Two factories
    # declaring the same id would keep the membership/name counts equal, then
    # collapse to one entry inside a set, leaving no trace.
    owners: dict[str, list[str]] = {}
    for symbol, name in resolved_by_factory:
        owners.setdefault(normalize(name), []).append(f"{symbol} -> {name!r}")
    for feature_id, claimants in owners.items():
        if len(claimants) > 1:
            fail(f"duplicate provider ID {feature_id!r} claimed by "
                 f"{len(claimants)} factories: {'; '.join(claimants)}")

    if len(resolved_by_factory) != len(factories):
        fail(f"provider membership/name mismatch: {len(factories)} registered, "
             f"{len(resolved_by_factory)} named")
    return [name for _factory, name in resolved_by_factory]


def check_against_expected(extracted: list[str], path: str) -> str | None:
    """Diff the extracted provider set against a committed expected set.

    Regenerating and comparing to your own previous output proves only that the
    extractor is deterministic. A wrong-but-stable extractor passes that every
    time, which is how a set containing auth-option ids once passed unnoticed.
    Correctness needs an independently maintained expectation.
    """
    has_duplicates = False
    try:
        with open(path) as handle:
            entries = [
                line.strip() for line in handle
                if line.strip() and not line.startswith("#")
            ]
        expected = set(entries)
        has_duplicates = len(entries) != len(expected)
        if has_duplicates:
            repeated = sorted({e for e in entries if entries.count(e) > 1})
            fail(f"expected provider set {path} has duplicate entries: {', '.join(repeated)}")
    except OSError as exc:
        fail(f"cannot read expected provider set {path}: {exc}")
        return None

    actual = {f"ai.provider.builtin.{name}" for name in extracted}
    missing = sorted(expected - actual)
    extra = sorted(actual - expected)
    if missing:
        fail(f"provider set is MISSING {len(missing)}: {', '.join(missing)}")
    if extra:
        fail(f"provider set has {len(extra)} UNEXPECTED: {', '.join(extra)}")

    # Return the verdict rather than printing it. Announcing success here would
    # land BEFORE the later sources are read, so a failure after this point
    # would still produce a run that reported success and then failure.
    if missing or extra or has_duplicates:
        return None
    return f"provider set matches {path}: {len(actual)} members, no difference"


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__,
                                     formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--pi-repo", required=True,
                        help="path to a Pi checkout containing the baseline commit")
    parser.add_argument("--baseline", default=DEFAULT_BASELINE,
                        help="full 40-character commit SHA to read from")
    parser.add_argument("--check-providers", metavar="FILE",
                        help="compare the extracted provider set against FILE "
                             "(one ID per line) and exit non-zero on any difference")
    args = parser.parse_args()

    src = Source(args.pi_repo, args.baseline)
    src.verify()

    try:
        return generate(src, args)
    except SourceUnavailable as exc:
        fail(str(exc))
        print(f"\n{len(errors)} unresolved problem(s); output is NOT authoritative",
              file=sys.stderr)
        return 1


def generate(src: "Source", args: argparse.Namespace) -> int:

    rpc_types_source = src.read("packages/coding-agent/src/modes/rpc/rpc-types.ts")
    request_union = rpc_types_source[
        rpc_types_source.index("RpcCommand"): rpc_types_source.index("RpcResponse")
    ]
    emit("coding-agent.rpc.command",
         "`modes/rpc/rpc-types.ts:20-73` request union",
         "the `type` string literal",
         sorted(set(re.findall(r'type: "([a-z_]+)"', request_union)) - {"response"}))

    emit("coding-agent.rpc.ui",
         "`modes/rpc/rpc-types.ts:238-273` RpcExtensionUIRequest",
         "the `method` string literal",
         re.findall(r'method: "([A-Za-z_]+)"', rpc_types_source))

    protocol_schemas = src.read("packages/protocol/src/schemas.ts")
    emit("wire.protocol.command",
         "`packages/protocol/src/schemas.ts:291-310`",
         "the command literal",
         re.findall(r'command: Type\.Literal\("([a-z_]+)"\)', protocol_schemas))

    providers = provider_ids(src)
    emit("ai.provider.builtin",
         "`providers/all.ts` `builtinProviders()`",
         "each provider's own `createProvider({ id })`",
         providers)

    pending_success = None
    if args.check_providers:
        pending_success = check_against_expected(providers, args.check_providers)

    events = rpc_event_ids(src)
    if events is not None:
        emit("coding-agent.rpc.event",
             "union of `agent/src/types.ts` AgentEvent, "
             "`core/agent-session.ts` AgentSessionEvent, and rpc-mode's own emission",
             "the `type` string literal",
             events)

    ca_tools = coding_agent_tool_names(src)
    if ca_tools is not None:
        emit("coding-agent.tool",
             "`core/tools/index.ts` `allToolNames` set",
             "the tool name literal in that set",
             ca_tools)

    harness_tools = harness_tool_names(src)
    if harness_tools is not None:
        emit("agent-harness.tool",
             "`agent/src/harness/tools/index.ts` exported `create*Tool` creators",
             "the creator name, minus its create/Tool affixes",
             harness_tools)

    modes = app_modes(src)
    cli_literals = cli_mode_literals(src)
    if modes is not None:
        emit("coding-agent.mode",
             "`core/project-trust.ts` `AppMode` union",
             f"the AppMode literal — the `--mode` flag accepts {sorted(cli_literals)}, "
             f"which map onto these rather than being members",
             modes)

    settings = setting_keys(src)
    if settings is not None:
        emit("coding-agent.setting",
             "`core/settings-manager.ts` `Settings` interface",
             "the declared key name",
             settings)

    slash_source = src.read("packages/coding-agent/src/core/slash-commands.ts")
    emit("coding-agent.slash",
         "`core/slash-commands.ts:20-41`",
         "the `name` field",
         re.findall(r'\{ name: "([a-z-]+)"', slash_source))

    cli_args_source = src.read("packages/coding-agent/src/cli/args.ts")
    emit("coding-agent.flag",
         "`cli/args.ts` `arg ===` comparisons",
         "the long-form flag literal",
         [flag.lstrip("-") for flag in re.findall(r'arg === "(--[a-z-]+)"', cli_args_source)])

    if errors:
        print(f"\n{len(errors)} unresolved problem(s); output is NOT authoritative",
              file=sys.stderr)
        return 1

    # Only now is the whole run known to be clean: every required source was
    # read and every set emitted and validated.
    if pending_success:
        print(pending_success, file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
