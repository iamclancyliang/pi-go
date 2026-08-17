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
import os
import re
import sys

# `tools/` is not a package and this entry point has a hyphenated name, so the
# sibling modules are made importable by path rather than by package.
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from census_families import (  # noqa: E402
    app_modes, auth_literals, cli_mode_literals, coding_agent_tool_names,
    environment_names, extension_hook_names, harness_entry_kinds,
    keybinding_actions, tui_barrel_names,
    harness_tool_names, provider_ids, rpc_event_ids, session_entry_kinds,
    setting_keys, thinking_levels, type_alias_span, union_discriminants,
    union_literals,
)
from census_source import (  # noqa: E402
    DEFAULT_BASELINE, Source, SourceUnavailable, SourceView, Spans, blank,
    errors, fail, normalize,
)

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


# Resolved from this file's own location so the helper is found however the tool
# is invoked. `__file__` is set by the interpreter for a normal run and by the
# test harness, which compiles this module from source text on purpose.
def check_against_expected(extracted: list[str], path: str) -> str | None:
    """Diff the extracted provider set against a committed expected set.

    Comparing output to a previous run of the same extractor tests determinism
    only: a wrong but stable extractor passes every time. Correctness requires
    an independently maintained expectation.
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

    rpc_types = src.view("packages/coding-agent/src/modes/rpc/rpc-types.ts")
    request_begin = rpc_types.structural.index("RpcCommand")
    request_end = rpc_types.structural.index("RpcResponse")
    emit("coding-agent.rpc.command",
         "`modes/rpc/rpc-types.ts:20-73` request union",
         "the `type` string literal",
         sorted(set(rpc_types.quoted_after(r"type:\s*", request_begin, request_end))
                - {"response"}))

    emit("coding-agent.rpc.ui",
         "`modes/rpc/rpc-types.ts:238-273` RpcExtensionUIRequest",
         "the `method` string literal",
         rpc_types.quoted_after(r"method:\s*"))

    protocol_schemas = src.view("packages/protocol/src/schemas.ts")
    emit("wire.protocol.command",
         "`packages/protocol/src/schemas.ts:291-310`",
         "the command literal",
         protocol_schemas.quoted_after(r"command:\s*Type\.Literal\(\s*"))

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

    hooks = extension_hook_names(src)
    if hooks is not None:
        emit("extension.hook",
             "`core/extensions/types.ts` `ExtensionAPI`'s `on()` overload set — "
             "NOT the `ExtensionEvent` payload union, which is smaller because one "
             "payload type serves several hooks",
             "the `event` string literal",
             hooks)

    harness_entries = harness_entry_kinds(src)
    if harness_entries is not None:
        emit("agent-harness.session-entry",
             "`agent/src/harness/session/types.ts` `Entry` union — a DIFFERENT "
             "model from the coding-agent's, sharing several discriminants with "
             "different field shapes",
             "the `type` discriminant",
             harness_entries)

    reasons = src.view("packages/agent/src/harness/session/types.ts")
    reason_span = type_alias_span(reasons, "CompactionReason")
    if reason_span is not None:
        literals = reasons.quoted_in(*reason_span)
        if literals:
            emit("agent-harness.compaction-reason",
                 "`agent/src/harness/session/types.ts` `CompactionReason` union",
                 "the reason literal",
                 literals)
        else:
            fail("CompactionReason yielded no literals - its shape probably changed")

    barrel = tui_barrel_names(src)
    if barrel is not None:
        # Two declaration spaces, two sets: `fuzzyMatch` and `FuzzyMatch` are both
        # exported and differ only in leading case, so one flat set cannot hold
        # them without an ID collision.
        emit("tui.export.value",
             "`tui/src/index.ts` barrel, value exports — the package's DECLARED "
             "public surface; the published `dist/**/*` without an `exports` map is "
             "wider and is recorded as a packaging risk rather than as members",
             "the exported name, aliases resolved to what a consumer imports",
             barrel["value"])
        emit("tui.export.type",
             "`tui/src/index.ts` barrel, type-only exports (`export type {...}` and "
             "`export { type ... }`)",
             "the exported name",
             barrel["type"])

    keys = keybinding_actions(src)
    if keys is not None:
        emit("tui.keybinding",
             "`tui/src/keybindings.ts` `Keybindings` interface keys, verified equal "
             "to the `TUI_KEYBINDINGS` default table's keys",
             "the action ID as declared",
             keys)

    entry_kinds = session_entry_kinds(src)
    if entry_kinds is not None:
        emit("coding-agent.session-entry",
             "`core/session-manager.ts` `SessionEntry` union, each member resolved "
             "to the `type` its interface declares; `SessionHeader` is a file-level "
             "record and not a member",
             "the `type` discriminant",
             entry_kinds)

    ai_types = src.view("packages/ai/src/types.ts")
    stop_span = type_alias_span(ai_types, "StopReason")
    if stop_span is not None:
        stop_reasons = ai_types.quoted_in(*stop_span)
        if not stop_reasons:
            fail("StopReason yielded no literals - its shape probably changed")
        else:
            emit("ai.stop-reason",
                 "`ai/src/types.ts` `StopReason` union",
                 "the reason literal",
                 stop_reasons)

    levels = thinking_levels(src)
    if levels is not None:
        emit("agent.thinking-level",
             "`agent/src/types.ts` `ThinkingLevel`, verified to agree with "
             "`ai`'s `ModelThinkingLevel` and the protocol schema",
             "the level literal",
             levels)

    for family, name, keyed in (
        ("ai.auth.type", "AuthType", False),
        ("ai.auth.prompt", "AuthPrompt", True),
        ("ai.auth.event", "AuthEvent", True),
    ):
        members = auth_literals(src, name, keyed)
        if members is not None:
            emit(family,
                 f"`ai/src/auth/types.ts` `{name}` union",
                 "the string literal" if not keyed else "the `type` tag",
                 members)

    environment = environment_names(src)
    if environment is not None:
        # Four families, not one: a name's ROLE is part of what it is, and the
        # earlier single set silently filed writes and deletions as reads.
        emit("coding-agent.environment.input",
             "every `PI_*` READ across `packages/*/src` at the baseline, in all "
             "four read forms, plus the two names derived from `APP_NAME`; "
             "assignments and deletions are excluded",
             "the variable name, at its default `pi` spelling for the two "
             "derived ones",
             environment["input"])
        emit("coding-agent.environment.exposed",
             "every `PI_*` ASSIGNED into an environment object built for a child "
             "process (`env.X =`) across `packages/*/src`",
             "the variable name",
             environment["exposed"])
        emit("coding-agent.environment.self",
             "every `PI_*` ASSIGNED onto this process (`process.env.X =`) across "
             "`packages/*/src` — a different mechanism from the above, inherited "
             "by everything spawned afterwards",
             "the variable name",
             environment["self"])
        emit("coding-agent.environment.cleared",
             "every `PI_*` DELETED from an environment object across "
             "`packages/*/src`; upstream this always precedes the assignment",
             "the variable name",
             environment["cleared"])

    settings = setting_keys(src)
    if settings is not None:
        emit("coding-agent.setting",
             "`core/settings-manager.ts` `Settings` interface",
             "the declared key name",
             settings)

    slash_source = src.view("packages/coding-agent/src/core/slash-commands.ts")
    emit("coding-agent.slash",
         "`core/slash-commands.ts:20-41`",
         "the `name` field",
         slash_source.quoted_after(r"\{\s*name:\s*"))

    # The same cached view the mode extractor used, so one file cannot be read
    # twice and yield two different pictures of itself.
    cli_args = src.view("packages/coding-agent/src/cli/args.ts")
    emit("coding-agent.flag",
         "`cli/args.ts` `arg ===` comparisons",
         "the long-form flag literal",
         [flag.lstrip("-") for flag in cli_args.quoted_after(r"arg\s*===\s*")
          if flag.startswith("--")])

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
