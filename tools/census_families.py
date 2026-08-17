"""One extractor per feature family.

Each function names its own membership authority and returns None (after calling
`fail`) rather than a short set, because a short set looks like a good one.
"""

from __future__ import annotations

import re

from census_source import Source, SourceView, fail, normalize

def union_literals(view: SourceView, declaration: str, what: str) -> list[str] | None:
    """Collect `type: "..."` literals from one TypeScript union declaration.

    The union is bounded by its own STRUCTURE - consecutive lines belonging to
    the declaration - rather than by a text marker or a line range. A marker
    assumes something follows the union, which is false when it ends the file;
    a line range silently truncates when the file moves.

    Returns None when the declaration cannot be located, so the caller can skip
    emitting a set rather than publish a short one.
    """
    match = re.search(rf"^{re.escape(declaration)}\s*$", view.structural, re.M)
    if not match:
        fail(f"cannot locate {what}: declaration {declaration!r} not found")
        return None

    collected: list[str] = []
    offset = match.end()
    for line in view.structural[match.end():].splitlines(keepends=True):
        stripped = line.strip()
        if stripped and not (stripped.startswith("//") or stripped.startswith("*")
                             or stripped.startswith("/*")):
            # A union member, or a continuation of one, always begins with `|` or
            # is indented inside a member's braces. Anything at column zero that
            # is not a pipe has ended the declaration.
            if not (stripped.startswith("|") or line.startswith((" ", "\t"))):
                break
            collected.extend(
                view.quoted_after(r"type:\s*", offset, offset + len(line))
            )
        offset += len(line)

    if not collected:
        fail(f"{what} yielded no type literals - the union shape probably changed")
        return None
    return collected


def coding_agent_tool_names(src: Source) -> list[str] | None:
    """Membership from the registry set, never from a list supplied by a caller.

    A hardcoded list can agree with the registry today and diverge silently the
    moment a tool is added upstream, because nothing would detect the drift.
    """
    view = src.view("packages/coding-agent/src/core/tools/index.ts")
    declared = re.search(
        r"export const allToolNames: Set<ToolName> = new Set\(\[(.*?)\]\)",
        view.structural, re.S,
    )
    if not declared:
        fail("cannot locate allToolNames in core/tools/index.ts")
        return None
    names = view.quoted_in(*declared.span(1))
    if not names:
        fail("allToolNames yielded no entries - its shape probably changed")
        return None
    return names


def harness_tool_names(src: Source) -> list[str] | None:
    """Membership from every exported tool creator in the harness barrel file.

    Two export forms both count and both must be parsed: a re-export block
    (`export { createXTool } from ...`) and a direct exported declaration
    (`export const createXTool = ...`). A reference that is not exported -- an
    internal alias, for instance -- is not a member.

    Both forms are discovered on the STRUCTURAL view. An export written inside a
    string (`const s = "export const createPhantomTool = 1"`) has had its
    contents blanked there, so it cannot fabricate a member; discovering on the
    extraction view accepted exactly that.
    """
    view = src.view("packages/agent/src/harness/tools/index.ts")

    creators: set[str] = set()

    # Re-export blocks, including `as` aliases: the exported name is what counts.
    # The alias separator is matched as `as` between whitespace RUNS, because a
    # clause may be split across lines or indented with tabs, and requiring the
    # exact substring `" as "` silently dropped those aliases.
    alias = re.compile(r"\bas\s+([A-Za-z_$][\w$]*)\s*$", re.S)
    for block in view.discover(re.compile(r"\bexport\s*\{([^}]*)\}", re.S)):
        for clause in view.value(*block.span(1)).split(","):
            aliased = alias.search(clause)
            exported = aliased.group(1) if aliased else clause.strip()
            found = re.fullmatch(r"create([A-Z][A-Za-z]*)Tool", exported)
            if found:
                creators.add(found.group(1))

    # Exported declarations in every form the language allows here, including
    # `async function`, `declare`, and `default`.
    declaration = re.compile(
        r"\bexport\s+(?:default\s+)?(?:declare\s+)?(?:async\s+)?"
        r"(?:const|let|var|function\*?|class)\s+create([A-Z][A-Za-z]*)Tool\b"
    )
    for match in view.discover(declaration):
        creators.add(match.group(1))

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
    view = src.view("packages/coding-agent/src/core/project-trust.ts")
    declared = re.search(r"export type AppMode = ([^;]+);", view.structural)
    if not declared:
        fail("cannot locate the AppMode type in core/project-trust.ts")
        return None
    modes = view.quoted_in(*declared.span(1))
    if not modes:
        fail("AppMode yielded no literals - its shape probably changed")
        return None
    return modes


def cli_mode_literals(src: Source) -> list[str]:
    """The literals `--mode` accepts, cross-checked against the Mode type.

    The type and the parser must agree; if they diverge the parser is what a
    user experiences, so a mismatch is an error rather than a silent choice.
    """
    view = src.view("packages/coding-agent/src/cli/args.ts")
    declared = re.search(r'export type Mode = ([^;]+);', view.structural)
    if not declared:
        fail("cannot locate the Mode type in cli/args.ts")
        return []
    from_type = view.quoted_in(*declared.span(1))

    guard = re.search(r'const mode = args\[\+\+i\];\s*if \(([^)]+)\)', view.structural)
    if not guard:
        fail("cannot locate the --mode acceptance test in cli/args.ts")
        return []
    from_parser = view.quoted_after(r"mode\s*===\s*", *guard.span(1))

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
    # Keys are identifiers, so the structural view holds them intact and is also
    # the view where a key written inside a string cannot be mistaken for one.
    source = src.view("packages/coding-agent/src/core/settings-manager.ts").structural
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


def extension_hook_names(src: Source) -> list[str] | None:
    """Hook names an extension can subscribe to: the `on()` overload set.

    **The payload union is NOT the authority.** `ExtensionEvent` lists 25 payload
    types, but one payload serves several hook names -- `SessionEvent` covers the
    whole session family -- so counting the union undercounts the hooks a port has
    to implement.

    **Nor is a line-anchored pattern.** Several overloads are wrapped across lines
    because their handler type is long, so matching `on(event: "..."` at the start
    of a line silently drops exactly those, and the ones it drops are the
    interesting ones: the cancellable session hooks. Each `on(` is therefore
    discovered structurally and its argument list read as a balanced span.
    """
    view = src.view("packages/coding-agent/src/core/extensions/types.ts")
    interface = re.search(r"^export interface ExtensionAPI \{$", view.structural, re.M)
    if not interface:
        fail("cannot locate the ExtensionAPI interface")
        return None

    # The interface ends at the first line that closes it at column zero.
    end = view.structural.find("\n}", interface.end())
    if end == -1:
        fail("the ExtensionAPI interface has no closing brace")
        return None

    names: list[str] = []
    for call in re.finditer(r"\bon\(", view.structural[interface.end():end]):
        open_paren = interface.end() + call.end() - 1
        argument = view.balanced_argument(open_paren)
        if argument is None:
            fail("an on( overload in ExtensionAPI has unbalanced parentheses")
            continue
        literal = view.quoted_after(r"event:\s*", open_paren,
                                   open_paren + 1 + len(argument))
        if not literal:
            fail(f"an on( overload has no readable event literal: "
                 f"{' '.join(argument[:60].split())!r}")
            continue
        names.append(literal[0])

    if not names:
        fail("ExtensionAPI yielded no on() overloads - its shape probably changed")
        return None
    return names


def union_discriminants(src: Source, path: str, union: str) -> list[str] | None:
    """Discriminants of a union whose members are interface NAMES.

    Shared by the two session entry models, which is the point: they are
    different unions in different packages and must be extracted the same way to
    be comparable at all.
    """
    view = src.view(path)
    span = type_alias_span(view, union)
    if span is None:
        return None

    names = re.findall(r"\|\s*(\w+)", view.structural[span[0]:span[1]])
    if not names:
        fail(f"the {union} union in {path} yielded no interface names")
        return None

    kinds: list[str] = []
    for name in names:
        # Members may be generic and may extend a base; requiring neither found
        # five of nine in one union and the tool refused to emit, which is how
        # this surfaced.
        declaration = re.search(
            rf"^export interface {re.escape(name)}\s*(?:<[^>]*>)?\s*"
            rf"(?:extends\s+[\w<>, ]+?\s*)?\{{$",
            view.structural, re.M)
        if not declaration:
            fail(f"cannot locate the {name} interface that {union} references")
            continue
        end = view.structural.find("\n}", declaration.end())
        literal = view.quoted_after(r"\btype:\s*", declaration.end(), end)
        if not literal:
            fail(f"{name} declares no `type` discriminant")
            continue
        kinds.append(literal[0])

    if len(kinds) != len(names):
        return None
    return kinds


def session_entry_kinds(src: Source) -> list[str] | None:
    """Discriminants of the coding-agent session file's entry union.

    `SessionHeader` is deliberately excluded: `FileEntry = SessionHeader |
    SessionEntry` marks it as a file-level record rather than a branch entry, and
    it is the one record that cannot repeat.

    A stale copy of this union -- five members, with a `firstKeptEntryIndex`
    field that no longer exists -- is embedded in a test fixture's captured tool
    output. Anything that searched the repository rather than this declaration
    would find it.
    """
    return union_discriminants(
        src, "packages/coding-agent/src/core/session-manager.ts", "SessionEntry")


def harness_entry_kinds(src: Source) -> list[str] | None:
    """Discriminants of the AGENT HARNESS's own entry union — a different set.

    This is not the same model as the coding-agent's, and the overlap in names
    makes that easy to miss. `CompactionEntry` exists in both with the same
    `"compaction"` discriminant and DIFFERENT fields: the coding-agent stores
    `firstKeptEntryId`, a boundary pointer, while the harness stores
    `retainedTail`, the retained messages inline. A port that reads one shape and
    writes the other produces sessions that load with the right discriminants and
    the wrong content.
    """
    return union_discriminants(
        src, "packages/agent/src/harness/session/types.ts", "Entry")


def thinking_levels(src: Source) -> list[str] | None:
    """The thinking-level set, cross-checked across THREE declarations.

    `ThinkingLevel` is declared three times with two different memberships, so
    naming one of them as the authority would be a coin toss:

      * `agent/src/types.ts` -- 7 members, `off` included;
      * `ai/src/types.ts` -- 6 members, because there `off` is the ABSENCE of a
        level; `off` is added back by `ModelThinkingLevel`;
      * `protocol/src/schemas.ts` -- 7 `Type.Literal` members, the wire form.

    The user-visible set is the 7. All three spellings must agree on it, so the
    agreement is verified and a divergence fails rather than being resolved by
    preferring whichever file was read first.
    """
    agent = src.view("packages/agent/src/types.ts")
    declared = type_alias_span(agent, "ThinkingLevel")
    if declared is None:
        return None
    from_agent = agent.quoted_in(*declared)

    ai = src.view("packages/ai/src/types.ts")
    ai_span = type_alias_span(ai, "ThinkingLevel")
    model_span = type_alias_span(ai, "ModelThinkingLevel")
    if ai_span is None or model_span is None:
        return None
    # `ModelThinkingLevel = "off" | ThinkingLevel` contributes only its own
    # literals; the referenced union is read separately and unioned here.
    from_ai = ai.quoted_in(*ai_span) + ai.quoted_in(*model_span)

    protocol = src.view("packages/protocol/src/schemas.ts")
    schema = re.search(r"export const ThinkingLevelSchema = Type\.Union\(\[",
                       protocol.structural)
    if not schema:
        fail("cannot locate ThinkingLevelSchema in protocol/schemas.ts")
        return None
    closing = protocol.balanced_argument(schema.end() - 2)
    if closing is None:
        fail("ThinkingLevelSchema has unbalanced parentheses")
        return None
    from_protocol = protocol.quoted_after(
        r"Type\.Literal\(\s*", schema.end(), schema.end() + len(closing))

    if not (sorted(set(from_agent)) == sorted(set(from_ai)) == sorted(set(from_protocol))):
        fail(f"thinking levels disagree across declarations: "
             f"agent={sorted(set(from_agent))}, ai+model={sorted(set(from_ai))}, "
             f"protocol={sorted(set(from_protocol))}")
        return None
    return from_agent


def type_alias_span(view: SourceView, name: str) -> tuple[int, int] | None:
    """The span of one `export type NAME = ...;` declaration.

    The end is the first `;` at nesting depth zero, found on the structural view.
    A line range would truncate when the file moves, and the first `;` in the raw
    text lands inside `options: readonly { id: string; label: string }` -- which
    is why depth is tracked rather than the first semicolon taken.
    """
    match = re.search(rf"export type {re.escape(name)}\s*=", view.structural)
    if not match:
        fail(f"cannot locate the {name} type declaration")
        return None

    depth = 0
    for index in range(match.end(), len(view.structural)):
        char = view.structural[index]
        if char in "{([":
            depth += 1
        elif char in "})]":
            depth -= 1
        elif char == ";" and depth == 0:
            return match.end(), index
    fail(f"the {name} declaration has no terminating semicolon at depth zero")
    return None


def auth_literals(src: Source, name: str, keyed: bool) -> list[str] | None:
    """Members of one auth union: either bare string literals or `type:` tags.

    `AuthType` is a union OF strings, so the quotes are the anchor; `AuthPrompt`
    and `AuthEvent` are unions of objects, so the `type` key is. Passing the
    wrong one yields a plausible short set rather than an error, so the caller
    states which shape it expects.
    """
    view = src.view("packages/ai/src/auth/types.ts")
    span = type_alias_span(view, name)
    if span is None:
        return None
    members = (view.quoted_after(r"type:\s*", *span) if keyed
               else view.quoted_in(*span))
    if not members:
        fail(f"{name} yielded no members - its shape probably changed")
        return None
    return members


def is_process_env(receiver: str) -> bool:
    """Whether a captured receiver denotes this process's own environment."""
    segments = receiver.split(".")
    return len(segments) >= 2 and segments[-2] == "process" and segments[-1] == "env"


def environment_names(src: Source) -> dict[str, list[str]] | None:
    """`PI_*` environment variables, separated BY ROLE.

    One set covering "environment variables" was wrong, and wrong in a way that
    produced a confident set and a false explanation. `delete env.PI_SESSION_ID`
    matched the read pattern, so the five session variables entered the set as
    reads and were then DOCUMENTED as reads -- when in fact the product writes
    them for child processes and clears them first. FOUR roles, four
    authorities, stated separately:

      * `input`   -- read as configuration: `process.env.X`, `process.env["X"]`,
                     a member of an injected `env` object, or via
                     `getProviderEnvValue("X", env)`.
      * `exposed` -- assigned into a FRESHLY BUILT environment object handed to a
                     child process (`env.X = ...`).
      * `self`    -- assigned into `process.env`, which mutates THIS process and
                     is inherited by everything it spawns. A different mechanism
                     with different blast radius, so a different role: `--offline`
                     sets two variables this way, and the entry points set a
                     marker.
      * `cleared` -- DELETED from a child environment object, which upstream
                     always precedes the assignment so a parent's stale value
                     cannot leak into a tool call.

    A name may hold more than one role, and several do: `PI_MODEL` and
    `PI_PROVIDER` are exposed to tools AND read by the eval entry point;
    `PI_OFFLINE` is read in six places and also set on this process. Reporting
    each name under one role would hide the others.

    Two further facts each cost a wrong count before this:

    1. Not every name is a literal. `config.ts` builds two of them from the
       configurable app name (`${APP_NAME.toUpperCase()}_CODING_AGENT_DIR`), so
       grepping for `PI_` misses them entirely and a fork named `tau` reads
       `TAU_*`. They are recorded under their default `pi` spelling, with the
       derivation as their authority.
    2. There are four read FORMS, not one. Scanning only `process.env.X` missed
       `PI_TUI_ESC_TIMEOUT` and `PI_CACHE_RETENTION`, both documented.
    """
    # `(?!\s*=[^=])` keeps comparisons (`=== "1"`) and rejects assignment;
    # `(?<!delete\s)` keeps a deletion out of the read set.
    reads = [
        r"(?<!delete )process\s*\.\s*env\s*\.\s*(PI_[A-Z0-9_]+)\b(?!\s*=[^=])",
        r"(?<!delete )(?<!process\.)\benv\s*\.\s*(PI_[A-Z0-9_]+)\b(?!\s*=[^=])",
    ]
    quoted_reads = [
        r"process\s*\.\s*env\s*\[\s*",
        r"(?<!process\.)\benv\s*\[\s*",
        r"getProviderEnvValue\s*\(\s*",
    ]
    # The two write forms are deliberately distinguished by whether `process.`
    # precedes them: `process.env.X = ` mutates this process, `env.X = ` fills an
    # object built for a child.
    # WRITES ARE NOT RESTRICTED TO THE `PI_` NAMESPACE. The product also sets
    # `AI_AGENT=pi`, which a prefix-limited scan missed entirely while the census
    # prose listed it -- source and documentation could not close.
    self_writes = [r"\bprocess\s*\.\s*env\s*\.\s*([A-Z][A-Z0-9_]*)\s*=[^=]"]
    # The RECEIVER is captured, not just the name: `env.X` and `execution.env.X`
    # are different objects, and the clear-then-set guarantee belongs to an object.
    # Aggregating by file instead let an unrelated delete elsewhere in the file
    # excuse a write, and flagged a legitimate override that merely shared a file.
    child_writes = [r"(?<!process\.)\b((?:\w+\s*\.\s*)*env)\s*\.\s*([A-Z][A-Z0-9_]*)\s*=[^=]"]
    deletes = [r"\bdelete\s+(?<!process\.)\b((?:\w+\s*\.\s*)*env)\s*\.\s*([A-Z][A-Z0-9_]*)\b"]

    names: set[str] = set()
    exposed: set[str] = set()
    on_self: set[str] = set()
    cleared: set[str] = set()
    write_sites: dict[tuple[str, str, str], list[int]] = {}
    delete_sites: dict[tuple[str, str, str], list[int]] = {}
    final_receivers: set[tuple[str, str]] = set()
    scanned = src.paths(r"packages/[^/]+/src/.*\.tsx?$")
    src.prefetch(scanned)
    for path in scanned:
        view = src.view(path)
        for pattern in reads:
            for match in view.discover(pattern):
                names.add(match.group(1))
        for prefix in quoted_reads:
            for value in view.quoted_after(prefix):
                if re.fullmatch(r"PI_[A-Z0-9_]+", value):
                    names.add(value)
        # Offsets are kept, not just names: the guarantee is that a delete
        # precedes the write AT THAT SITE. A global set difference let a delete in
        # one file vouch for a write in another, which is exactly what happens
        # here -- two different files write the same five names and only one
        # clears them.
        for pattern in child_writes:
            for match in view.discover(pattern):
                receiver = re.sub(r"\s+", "", match.group(1))
                # The receiver pattern allows a dotted path, which lets it match
                # `process.env` itself -- the lookbehind cannot prevent that, since
                # the path may legitimately begin with any identifier. A write to
                # this process is a different role, already collected as `self`.
                if is_process_env(receiver):
                    continue
                exposed.add(match.group(2))
                write_sites.setdefault((path, receiver, match.group(2)), []).append(match.start())
        for pattern in self_writes:
            for match in view.discover(pattern):
                on_self.add(match.group(1))
        for pattern in deletes:
            for match in view.discover(pattern):
                receiver = re.sub(r"\s+", "", match.group(1))
                if is_process_env(receiver):
                    continue
                cleared.add(match.group(2))
                delete_sites.setdefault((path, receiver, match.group(2)), []).append(match.start())
        # A receiver is a FINAL map only where that receiver is bound to an object
        # seeded from the inherited environment. Scoping by file was too coarse in
        # both directions.
        for seed in view.discover(
                r"\b(?:const|let|var)\s+(\w+)\s*=\s*\{\s*\.\.\.\s*"
                r"(?:getShellEnv\(\)|process\s*\.\s*env)"):
            final_receivers.add((path, seed.group(1)))

    # The two derived names, recorded at their default spelling. Read from the
    # declaration rather than hardcoded, so a change to the derivation is an
    # error here instead of a stale entry.
    # The DECLARATION is discovered structurally; the template body is then read
    # from the extraction view, because a template's contents are blanked on the
    # structural view exactly like any other string.
    config = src.view("packages/coding-agent/src/config.ts")
    derived: list[str] = []
    for declaration in config.discover(r"export const ENV_[A-Z_]+ = "):
        line_end = config.structural.find("\n", declaration.end())
        body = config.value(
            declaration.end(),
            len(config.extraction) if line_end == -1 else line_end,
        )
        suffix = re.match(r"`\$\{APP_NAME\.toUpperCase\(\)\}([A-Z_]+)`", body)
        if suffix:
            derived.append(suffix.group(1))
    if len(derived) != 2:
        fail(f"config.ts no longer derives exactly two env names from APP_NAME "
             f"(found {len(derived)}) - the derivation rule changed")
        return None
    for suffix in derived:
        names.add("PI" + suffix)

    if not names:
        fail("no PI_* environment reads found - the scan patterns are stale")
        return None
    if not exposed or not on_self:
        fail(f"environment write patterns are stale: {len(exposed)} child writes, "
             f"{len(on_self)} self writes")
        return None

    # The clear-then-set guarantee is per OBJECT and per ORDER, and it applies only
    # where the object being written IS the child environment. Two mechanisms exist
    # and conflating them produces a false alarm:
    #
    #   * a FINAL map, bound to an object seeded from the inherited environment
    #     (`const env = { ...getShellEnv() }`). A conditional write leaves an
    #     inherited value in place, so each name must be deleted from THAT object
    #     first.
    #   * an OVERRIDE map, a fresh object merged over the inherited environment
    #     later (the harness builds `env: {}` and applies it with `inheritEnv`).
    #     Assignment already wins, so a delete would be redundant.
    #
    # Both the receiver and the seeding are read from the source, so neither the
    # mechanism nor the exemption is maintained by hand.
    unguarded: list[str] = []
    for (path, receiver, name), writes in sorted(write_sites.items()):
        if (path, receiver) not in final_receivers:
            continue
        deletes_here = delete_sites.get((path, receiver, name), [])
        if not any(offset < min(writes) for offset in deletes_here):
            unguarded.append(f"{path}:{receiver}.{name}")
    if unguarded:
        fail("a FINAL child environment is written without clearing that name "
             "first, at: " + ", ".join(unguarded)
             + " - an inherited value would survive a conditional write")

    return {"input": sorted(names), "exposed": sorted(exposed),
            "self": sorted(on_self), "cleared": sorted(cleared)}


def rpc_event_ids(src: Source) -> list[str] | None:
    """The RPC stdout event set is the union of THREE sources.

    Enumerating it from the documentation table gives 21 and misses three
    events that are emitted but undocumented, so each source is read directly.
    """
    agent_events = union_literals(
        src.view("packages/agent/src/types.ts"),
        "export type AgentEvent =", "AgentEvent",
    )
    session_events = union_literals(
        src.view("packages/coding-agent/src/core/agent-session.ts"),
        "export type AgentSessionEvent =", "AgentSessionEvent",
    )
    if agent_events is None or session_events is None:
        return None

    # RPC forwards session events to stdout and adds one of its own. The same
    # stdout stream also carries UI *requests*, which are a different kind:
    # a request expects a reply, an event does not. Those are catalogued as
    # `coding-agent.rpc.ui` and must not be counted again here.
    request_envelopes = {"extension_ui_request"}

    # Discovery runs on the STRUCTURAL view, so neither a commented-out call nor
    # one written inside a string or template literal is discovered at all --
    # `const s = \`output({ type: "extension_error" })\`` must not contribute a
    # member, and on the extraction view it did.
    rpc_mode = src.view("packages/coding-agent/src/modes/rpc/rpc-mode.ts")

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
    for call in rpc_mode.discover(r"\boutput\("):
        open_paren = call.end() - 1
        argument = rpc_mode.balanced_argument(open_paren)
        if argument is None:
            fail("rpc-mode has an output( call with unbalanced parentheses")
            continue

        called = re.match(r"\s*([A-Za-z_][A-Za-z0-9_]*)\(", argument)
        if called and called.group(1) in forwarders | response_helpers:
            continue

        bare = re.fullmatch(r"\s*([A-Za-z_][A-Za-z0-9_]*)\s*", argument)
        if bare and bare.group(1) in response_variables:
            continue

        # The `type` KEY is located on the structural view too, so a payload
        # that merely mentions `type: "..."` inside one of its own string values
        # cannot supply the event name.
        literals = rpc_mode.quoted_after(
            r"type:\s*", open_paren, open_paren + 1 + len(argument)
        )
        if literals:
            written.add(literals[0])
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
    registry = src.view("packages/ai/src/providers/all.ts")
    symbol_to_file: dict[str, str] = {}
    for statement in registry.discover(r'import \{ (\w+Provider) \} from ["\']'):
        # The window must reach past the module literal's CLOSING quote, which is
        # on the next character after the match at the earliest and end-of-line at
        # the latest; a window that stops at the opening quote finds nothing.
        line_end = registry.structural.find("\n", statement.start())
        module = registry.quoted_after(
            r'import \{ ' + statement.group(1) + r' \} from \s*',
            statement.start(),
            len(registry.structural) if line_end == -1 else line_end,
        )
        if module and module[0].startswith("./") and module[0].endswith(".ts"):
            symbol_to_file[statement.group(1)] = module[0][2:-3]

    body = re.search(
        r"function builtinProviders\(\): Provider\[\] \{\s*return \[(.*?)\];",
        registry.structural, re.S,
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
        provider = src.view(f"packages/ai/src/providers/{module}.ts")

        # Anchor on createProvider's own id. The first `id:` in a provider file
        # may belong to an auth option, which would yield a set of the right
        # size whose members are not providers.
        anchored = provider.quoted_after(r'createProvider(?:<[^>]*>)?\(\{\s*id:\s*')
        if anchored:
            resolved_by_factory.append((symbol, anchored[0]))
            continue

        # Some providers take a caller-supplied id with a literal default.
        configurable = provider.quoted_after(r'const id = options\.id \?\? ')
        if configurable:
            resolved_by_factory.append((symbol, configurable[0]))
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




def keybinding_actions(src: Source) -> list[str] | None:
    """TUI keybinding action IDs, from the registry interface AND the default table.

    Two authorities exist in one file and they must agree, so both are read:

      * `Keybindings` -- the interface whose KEYS are the action IDs. It is
        declared for declaration merging, so downstream packages can add to it;
        the set here is the TUI's own.
      * `TUI_KEYBINDINGS` -- the default table, mapping each action to its keys.

    A port needs the action set, not the key set: `defaultKeys: []` is a real
    member with no binding (prompt-history navigation ships unbound), so counting
    bound keys would drop it, and one action may hold several keys.
    """
    view = src.view("packages/tui/src/keybindings.ts")

    declaration = re.search(r"^export interface Keybindings \{$", view.structural, re.M)
    if not declaration:
        fail("cannot locate the Keybindings interface")
        return None
    interface_end = view.structural.find("\n}", declaration.end())
    from_interface = view.quoted_after(r"\n\t", declaration.end(), interface_end)

    table = re.search(r"^export const TUI_KEYBINDINGS = \{$", view.structural, re.M)
    if not table:
        fail("cannot locate the TUI_KEYBINDINGS default table")
        return None
    table_end = view.structural.find("\n}", table.end())
    from_table = view.quoted_after(r"\n\t", table.end(), table_end)

    if not from_interface:
        fail("the Keybindings interface yielded no action IDs")
        return None
    if sorted(set(from_interface)) != sorted(set(from_table)):
        missing = sorted(set(from_interface) - set(from_table))
        extra = sorted(set(from_table) - set(from_interface))
        fail(f"Keybindings interface and TUI_KEYBINDINGS disagree: "
             f"absent from the table {missing}, absent from the interface {extra}")
        return None
    return from_interface


def tui_barrel_names(src: Source) -> dict[str, list[str]] | None:
    """Names the TUI package's barrel exports, split by DECLARATION SPACE.

    This is the smaller of two defensible denominators, and the difference matters
    for parity:

      * the **barrel** (`tui/src/index.ts`) names each export explicitly. There
        are no `export *` statements, so the list is enumerable rather than
        implied.
      * the **published surface** is larger. `package.json` has `main` and
        `files: ["dist/**/*", ...]` and **no `exports` map**, so every compiled
        module is importable by path whether or not the barrel names it.

    The barrel is emitted because it is what the package declares as its API. The
    wider surface is reachable by an accident of packaging rather than by design,
    and treating it as the parity denominator would commit a port to reproducing
    module layout as though it were contract. That is a decision, recorded with
    its reason rather than settled by whoever counts first.

    **Values and types are separate sets because TypeScript has two declaration
    spaces.** `fuzzyMatch` and `FuzzyMatch` both exist and differ only in leading
    case, which one flat set cannot represent: they normalise to the same ID, and
    the collision guard rejected the run rather than silently dropping one. The
    split is not a workaround for the naming scheme, it is what the language says.

    Three export FORMS all count, and missing one cost a member: `export { X }`,
    `export { type X }`, and `export type { X }`. The last is a whole-clause type
    export -- omitting it made this set 132 where the barrel names 133.
    """
    view = src.view("packages/tui/src/index.ts")

    if re.search(r"^export \*", view.structural, re.M):
        fail("the TUI barrel has an `export *` statement; the name list is no "
             "longer enumerable from this file alone")
        return None

    alias = re.compile(r"\bas\s+([A-Za-z_$][\w$]*)\s*$", re.S)
    values: list[str] = []
    types: list[str] = []
    for block in view.discover(re.compile(r"\bexport\s+(type\s+)?\{([^}]*)\}", re.S)):
        whole_clause_is_type = block.group(1) is not None
        for clause in view.value(*block.span(2)).split(","):
            stripped = clause.strip()
            if not stripped:
                continue
            per_clause_type = bool(re.match(r"^type\s+", stripped))
            aliased = alias.search(clause)
            exported = (aliased.group(1) if aliased
                        else re.sub(r"^type\s+", "", stripped))
            if not re.fullmatch(r"[A-Za-z_$][\w$]*", exported):
                continue
            (types if (whole_clause_is_type or per_clause_type) else values).append(exported)

    if not values and not types:
        fail("the TUI barrel yielded no exported names")
        return None
    return {"value": values, "type": types}
