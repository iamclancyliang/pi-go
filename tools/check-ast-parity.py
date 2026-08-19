#!/usr/bin/env python3
"""Compare AST-derived member sets against the sets the generator publishes.

The migration from pattern matching to the compiler API is only safe if each
family's membership does not move while the method changes. This computes both
sets and reports the exact difference, so "migrated" means an empty difference
rather than a similar count.

It also states which families are NOT yet derived from the AST. A parity report
that silently covers a subset would read as full coverage, which is the same
failure as a short member set.

Run from the repo root:
    python3 tools/check-ast-parity.py --pi-repo /path/to/pi
"""

from __future__ import annotations

import argparse
import json
import pathlib
import re
import subprocess
import sys

TOOLS = pathlib.Path(__file__).resolve().parent
ROOT = TOOLS.parent
BASELINE = "086c32e74530564922d011ade23ff582c9d63116"

# Families whose membership is derived here from compiler-API facts. Each entry
# names the file to parse and a function turning that file's facts into the
# member literals, so the membership authority stays readable next to the family.
AST_DERIVED: dict[str, tuple[str, "callable"]] = {}


def derive(family: str, path: str):
    def register(function):
        AST_DERIVED[family] = (path, function)
        return function
    return register


def _tui_space(facts, wanted):
    """Every space a name occupies, from the stated surface or the target's meanings.

    A name is not assigned ONE space: a class is a value and a type, and both
    memberships are real.
    """
    names = []
    for export in facts["exports"]:
        if export["exportTypeOnly"]:
            spaces = {"type"}          # what the source states about its own surface
        elif export["externalTarget"]:
            spaces = {"external"}      # target outside the pinned inputs
        else:
            spaces = set(export["meanings"]) or {"unknown"}
        if wanted in spaces:
            names.append(export["name"])
    return names


@derive("tui.export.value", "packages/tui/src/index.ts")
def _tui_values(facts):
    return _tui_space(facts, "value")


@derive("tui.export.type", "packages/tui/src/index.ts")
def _tui_types(facts):
    return _tui_space(facts, "type")


@derive("tui.export.external", "packages/tui/src/index.ts")
def _tui_externals(facts):
    return _tui_space(facts, "external")


@derive("tui.keybinding", "packages/tui/src/keybindings.ts")
def _keybindings(facts):
    interface = facts["interfaceKeys"].get("Keybindings", [])
    table = facts["objectKeys"].get("TUI_KEYBINDINGS", [])
    if sorted(interface) != sorted(table):
        raise ValueError("the interface and the default table disagree")
    return interface


@derive("coding-agent.mode", "packages/coding-agent/src/core/project-trust.ts")
def _app_modes(facts):
    return facts["typeAliasUnions"]["AppMode"]["literals"]


@derive("ai.stop-reason", "packages/ai/src/types.ts")
def _stop_reasons(facts):
    return facts["typeAliasUnions"]["StopReason"]["literals"]


@derive("ai.auth.type", "packages/ai/src/auth/types.ts")
def _auth_types(facts):
    return facts["typeAliasUnions"]["AuthType"]["literals"]


@derive("ai.auth.prompt", "packages/ai/src/auth/types.ts")
def _auth_prompts(facts):
    return facts["typeAliasUnions"]["AuthPrompt"]["literals"]


@derive("ai.auth.event", "packages/ai/src/auth/types.ts")
def _auth_events(facts):
    return facts["typeAliasUnions"]["AuthEvent"]["literals"]


@derive("agent-harness.compaction-reason", "packages/agent/src/harness/session/types.ts")
def _compaction_reasons(facts):
    return facts["typeAliasUnions"]["CompactionReason"]["literals"]


@derive("coding-agent.setting", "packages/coding-agent/src/core/settings-manager.ts")
def _settings(facts):
    return facts["interfaceKeys"]["Settings"]


@derive("agent.thinking-level", "packages/agent/src/types.ts")
def _thinking_levels(facts):
    return facts["typeAliasUnions"]["ThinkingLevel"]["literals"]


# Families still extracted by pattern. Listed so the report cannot imply coverage
# it does not have; each needs a fact kind the emitter does not yet produce.
NOT_YET_DERIVED = {
    "coding-agent.tool": "members are bare strings in `new Set([...])`; needs an array-literal fact",
    "agent-harness.tool": "membership is the set of exported creator symbols, filtered by name shape",
    "coding-agent.rpc.command": "discriminants of a union spanning a file region",
    "coding-agent.rpc.ui": "keyed literals inside a request union",
    "coding-agent.rpc.event": "union of three sources plus emission-site classification",
    "wire.protocol.command": "literals inside `Type.Literal(...)` calls",
    "ai.provider.builtin": "registry order plus a per-provider id read",
    "coding-agent.slash": "keyed literals in an array of object literals",
    "coding-agent.flag": "comparison operands in the argument parser",
    "extension.hook": "event literals in `on()` overload signatures; needs a signature fact",
    "coding-agent.session-entry": "union member names resolved to each interface's discriminant",
    "agent-harness.session-entry": "same, in the harness package",
    "coding-agent.environment.input": "reads across the whole tree, in four access forms",
    "coding-agent.environment.exposed": "already parser-derived, via ts-env-facts",
    "coding-agent.environment.self": "already parser-derived, via ts-env-facts",
    "coding-agent.environment.cleared": "already parser-derived, via ts-env-facts",
}


def normalise(literal: str) -> str:
    with_boundaries = re.sub(r"(?<=[a-z0-9])(?=[A-Z])", "-", literal)
    return with_boundaries.replace("_", "-").lower()


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__,
                                     formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--pi-repo", required=True)
    parser.add_argument("--baseline", default=BASELINE)
    args = parser.parse_args()

    published = subprocess.run(
        [sys.executable, "-B", str(TOOLS / "gen-feature-ids.py"),
         "--pi-repo", args.pi_repo, "--baseline", args.baseline],
        capture_output=True, text=True)
    if published.returncode != 0:
        print("the generator failed; parity cannot be judged:\n" + published.stderr.strip(),
              file=sys.stderr)
        return 2

    current: dict[str, set[str]] = {}
    for family, count in re.findall(r"^### `([a-z][^`]+)` — (\d+) members", published.stdout, re.M):
        members = re.findall(rf"^{re.escape(family)}\.(\S+)", published.stdout, re.M)
        current[family] = set(members)

    wanted = sorted({path for path, _ in AST_DERIVED.values()})
    # A re-export can only be classified when its module is in the program, so the
    # TUI package's sources travel with the barrel. Without them every alias resolves
    # to a synthetic symbol and the declaration spaces collapse into one.
    listed = subprocess.run(["git", "ls-tree", "-r", "--name-only", args.baseline,
                             "--", "packages/tui/src"],
                            cwd=args.pi_repo, capture_output=True, text=True)
    if listed.returncode == 0:
        wanted = sorted(set(wanted) | {p for p in listed.stdout.split()
                                       if p.endswith((".ts", ".tsx"))})
    sources = {}
    for path in wanted:
        shown = subprocess.run(["git", "show", f"{args.baseline}:{path}"],
                               cwd=args.pi_repo, capture_output=True, text=True)
        if shown.returncode != 0:
            print(f"cannot read {path}: {shown.stderr.strip()}", file=sys.stderr)
            return 2
        sources[path] = shown.stdout

    facts_run = subprocess.run(["node", str(TOOLS / "ts-members.mjs"), args.pi_repo],
                               input=json.dumps(sources), capture_output=True, text=True)
    if facts_run.returncode != 0:
        print(f"ts-members.mjs failed ({facts_run.returncode}): {facts_run.stderr.strip()}",
              file=sys.stderr)
        return 2
    facts = json.loads(facts_run.stdout)

    differences = 0
    for family, (path, derive_members) in sorted(AST_DERIVED.items()):
        try:
            literals = derive_members(facts[path])
        except (KeyError, ValueError) as exc:
            print(f"DIFFER {family}: cannot derive from the AST: {exc}", file=sys.stderr)
            differences += 1
            continue
        from_ast = {normalise(literal) for literal in literals}
        from_generator = current.get(family)
        if from_generator is None:
            print(f"DIFFER {family}: the generator publishes no such family", file=sys.stderr)
            differences += 1
            continue
        if from_ast != from_generator:
            print(f"DIFFER {family}: only in AST {sorted(from_ast - from_generator)}; "
                  f"only in generator {sorted(from_generator - from_ast)}", file=sys.stderr)
            differences += 1
        else:
            print(f"same   {family:<34} {len(from_ast)} members")

    covered = set(AST_DERIVED)
    uncovered = sorted(set(current) - covered)
    print(f"\nAST-derived and identical: {len(covered) - differences}/{len(covered)} families")
    print(f"NOT yet AST-derived: {len(uncovered)} families")
    for family in uncovered:
        reason = NOT_YET_DERIVED.get(family, "no reason recorded")
        print(f"    {family:<34} {reason}")

    if differences:
        print(f"\n{differences} family(ies) differ; the migration is not lossless for them",
              file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
