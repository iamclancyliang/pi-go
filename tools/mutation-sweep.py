#!/usr/bin/env python3
"""Mutation sweep: every mutation below must make the control tests fail.

A green test suite proves nothing on its own -- it may be testing the wrong
thing. Each entry breaks the extractor in a way that a real mistake would, and
the suite must go red. Two lessons are baked in:

  * The baseline is MEASURED, not hardcoded. Comparing against a literal count
    means every green run is scored as "caught" as soon as the suite grows, so the
    sweep reports a success it never observed.
  * A mutation must model the actual failure. "Anchor `on(` to a line start" left
    the suite green, because the wrapped overloads do start a line -- what
    truncates upstream is reading the event literal from the same line.

Run from the repo root: python3 tools/mutation-sweep.py
Exit status is non-zero if any mutation survives.
"""
from __future__ import annotations

import atexit, pathlib, shutil, subprocess, sys, tempfile

from collections import namedtuple

# A bare triple leaves a reader counting positions to tell the searched text from
# the replacement. Naming the fields makes each entry read as what it claims: this
# label describes breaking `find` into `replace`.
Mutation = namedtuple("Mutation", "label find replace")

MUTATIONS = [
    Mutation("discover reads extraction",
     "pattern, self.structural))", "pattern, self.extraction))"),
    Mutation("quoted_after anchors on extraction",
     "self.structural[begin:stop]", "self.extraction[begin:stop]"),
    Mutation("quoted_in walks extraction",
     "char = self.structural[index]", "char = self.extraction[index]"),
    Mutation("alias falls back to exact ' as '",
     "aliased = alias.search(clause)", "aliased = None"),
    Mutation("derivation-change guard removed",
     "if len(derived) != 2:", "if len(derived) < 0:"),
    # These two target the READ PATTERNS, not the docstring that quotes them.
    # The docstring writes the lookahead with a doubled backslash, so anchoring
    # on the single-backslash form reaches only the code.
    Mutation("assignment lookahead dropped",
     r'(PI_[A-Z0-9_]+)\b(?!\s*=[^=])"', r'(PI_[A-Z0-9_]+)\b"'),
    Mutation("lookahead too broad, drops comparisons",
     r'\b(?!\s*=[^=])"', r'\b(?!\s*=)"'),
    Mutation("type span ends at the first semicolon",
     'elif char == ";" and depth == 0:', 'elif char == ";":'),
    Mutation("type span scans the extraction view",
     "char = view.structural[index]", "char = view.extraction[index]"),
    # The real upstream-shaped mistake is reading the event literal from the SAME
    # LINE as `on(`. The wrapped overloads still start a line, so anchoring `on(`
    # is not what truncates -- requiring the literal beside it is. Upstream that
    # drops the three cancellable hooks.
    Mutation("hook literal read from one line only",
     "open_paren + 1 + len(argument))",
     'view.structural.find("\\n", open_paren))'),
    # NOT tested: "read the payload union instead". Widening the scan range still
    # contains the on() overloads, so behaviour does not change and no fixture
    # could detect it. The 25-vs-33 confusion is a prose error, not a code path.
    Mutation("thinking-level agreement check removed",
     "if not (sorted(set(from_agent)) == sorted(set(from_ai)) == sorted(set(from_protocol))):",
     "if False:"),
    # The two lexical defects, one in each direction.
    Mutation("union member pattern rejects generics and extends",
     r'rf"^export interface {re.escape(name)}\s*(?:<[^>]*>)?\s*"',
     r'rf"^export interface {re.escape(name)}\s*"'),
    # The parser-backed lexing: dropping either span kind must be caught.
    # NOT tested: "leave comments and regexes in the EXTRACTION view". Every read
    # of that view happens at a span already located on the structural view, where
    # those regions are blanked, so no extractor can reach into one. There is no
    # observable behaviour to catch, and a control that cannot fail is decoration.
    Mutation("string/template text not blanked structurally",
     'self.structural = blank(source, spans["dead"] + spans["text"])',
     'self.structural = blank(source, spans["dead"])'),
    # The environment rules.
    # The TUI registry/export sets.
    Mutation("keybinding authorities not compared",
     "if sorted(set(from_interface)) != sorted(set(from_table)):",
     "if False:"),
    # The offset conversion and the per-receiver pairing.
    Mutation("utf16 offsets emitted unconverted",
     "\t\tif (!/[\\uD800-\\uDBFF]/.test(source)) return spans; // BMP only: identical",
     "\t\treturn spans;"),
    Mutation("env accesses resolved by name, not by scope",
     "\t\t\tconst binding = resolve(receiver.text);",
     "\t\t\tconst binding = [...objects.keys()].length ? { __objectId: [...objects.keys()][0] } : undefined;"),
    Mutation("parameters do not shadow an outer binding",
             "\t\t\tfor (const parameter of node.parameters) {",
             "\t\t\tfor (const parameter of []) {"),
    Mutation("unresolved seed treated as exempt",
             '            if write["object"] in unknown_seed:',
             "            if False:"),
    Mutation("unknown-value binding read as not seeded",
             "\t\t\t\tif (!binding || !binding.__known) {",
             "\t\t\t\tif (false) {"),
    # The environment invariants, each targeted at the code that now carries it.
    # These moved from patterns to parser facts; the invariant did not move, so
    # neither does the obligation to prove a control notices when it breaks.
    Mutation("clear-then-set guard removed",
             '            if not earlier:\n                unguarded.append',
             '            if False:\n                unguarded.append'),
    Mutation("clear-then-set order ignored",
             '                       and d["offset"] < write["offset"]]',
             "                       ]"),
    Mutation("delete matched on the wrong object",
             '                       if d["object"] == write["object"] and d["name"] == write["name"]',
             '                       if d["name"] == write["name"]'),
    Mutation("seeded scoping dropped, every object must clear",
             '            if write["object"] not in seeded:\n                continue',
             "            if True:\n                pass"),
    Mutation("process writes filed as child writes",
             '            if write["object"] == "process":\n                on_self.add(write["name"])\n            else:\n                exposed.add(write["name"])',
             '            exposed.add(write["name"])'),
    Mutation("process deletes counted as child clears",
             '            if deletion["object"] != "process":\n                cleared.add(deletion["name"])',
             '            cleared.add(deletion["name"])'),
    Mutation("unresolved accesses dropped from the sets",
             '        for access in facts["unresolved"]:',
             "        for access in []:"),
    Mutation("alias seeding not followed",
             "\t\t\t\tif (binding && binding.__inherited) return { seeded: true };",
             "\t\t\t\tif (false) return { seeded: true };"),
    Mutation("property spread decided instead of reported unknown",
             "\t\t\tif (!ts.isIdentifier(spread)) {\n\t\t\t\treturn { seeded: false, unresolvedSeed: true };",
             "\t\t\tif (!ts.isIdentifier(spread)) {\n\t\t\t\treturn { seeded: false };"),
    Mutation("deletion counted as an input read",
             r'r"(?<!delete )process\s*\.\s*env\s*\.\s*(PI_[A-Z0-9_]+)\b(?!\s*=[^=])"',
             r'r"process\s*\.\s*env\s*\.\s*(PI_[A-Z0-9_]+)\b(?!\s*=[^=])"'),
    # The first AST-migrated family: mode.
    Mutation("mode comparisons taken file-wide, not per binding",
             '                          if comparison.get("leftBinding") == binding}',
             "                          }"),
    Mutation("mode binding selected without checking its initialiser",
             '              if binding["name"] == "mode" and binding["initializer"] == "args[++i]"]',
             '              if binding["name"] == "mode"]'),
    Mutation("AppMode type-reference guard removed",
             '    if union["members"]:',
             "    if False:"),
    Mutation("type and parser agreement not required",
             "    if sorted(from_type) != sorted(from_parser):",
             "    if False:"),
    Mutation("comparison binding resolution disabled",
             "\t\t\tconst leftBinding = ts.isIdentifier(node.left)",
             "\t\t\tconst leftBinding = false && ts.isIdentifier(node.left)"),
    Mutation("names registered on traversal, not hoisted",
             "\t\tif (scoped) hoist(node, isFunctionLevel(node));",
             "\t\tif (false) hoist(node, isFunctionLevel(node));"),
    Mutation("var not hoisted to the function scope",
             "		if (!functionLevel) return;", "		if (true) return;"),
    Mutation("case clauses collected recursively",
             "\t\t\tif (ts.isCaseClause(child) || ts.isDefaultClause(child)) {\n\t\t\t\tfor (const statement of child.statements) scopeStatements.push(statement);\n\t\t\t\treturn;",
             "\t\t\tif (ts.isCaseClause(child) || ts.isDefaultClause(child)) {\n\t\t\t\tts.forEachChild(child, (n) => scopeStatements.push(n));\n\t\t\t\treturn;"),
    # The compiler-API member facts: each guarantee the checker path rests on.
    Mutation("unresolved alias accepted instead of failing",
             '        fail(f"the checker could not classify',
             '        pass  # fail(f"the checker could not classify'),
    Mutation("synthetic alias target treated as resolved",
             "\t\t\t\t\t!(target.declarations ?? []).length) {",
             "\t\t\t\t\tfalse) {"),
    Mutation("nested declarations overwrite the top-level authority",
             "		if (ts.isInterfaceDeclaration(node) && topLevel.has(node)) recordInterfaceKeys(node);",
             "		if (ts.isInterfaceDeclaration(node)) recordInterfaceKeys(node);"),
    Mutation("nested object literal overwrites a top-level registry",
             "		if (ts.isVariableStatement(node) && topLevel.has(node)) {",
             "		if (ts.isVariableStatement(node)) {"),
    # NOT tested: "disallow .ts extension imports". Bundler resolution already
    # permits them, so the option changes nothing observable and a control for it
    # could not fail.
    Mutation("in-memory files keyed relatively, resolution misses them",
             "\t\tObject.entries(files).map(([path, text]) => [`${repoRoot}/${path}`, text]));",
             "\t\tObject.entries(files).map(([path, text]) => [path, text]));"),
    Mutation("barrel takes exports from syntax, not the module symbol",
             "\t\tconst moduleSymbol = checker.getSymbolAtLocation(file);",
             "\t\tconst moduleSymbol = undefined && checker.getSymbolAtLocation(file);"),
    Mutation("protocol literals taken file-wide, not per schema",
             '                     if call["enclosing"] == "ThinkingLevelSchema"',
             "                     if True"),
    Mutation("ModelThinkingLevel derivation not verified",
             '    if "ThinkingLevel" not in model["members"]:',
             "    if False:"),
    Mutation("call arguments not collected",
             "\t\t\t\t\t\tcallLiterals.push({", "\t\t\t\t\t\t[].push({"),
    Mutation("for-header declarations not hoisted",
             "\t\t\tif ((ts.isForStatement(node) || ts.isForOfStatement(node) ||",
             "\t\t\tif (false && (ts.isForStatement(node) || ts.isForOfStatement(node) ||"),
    Mutation("compiler host falls back to the file system",
             "\t\tif (isLib(fileName)) {\n\t\t\treturn original(fileName, languageVersion, onError, shouldCreate);\n\t\t}\n\t\treturn undefined;",
             "\t\treturn original(fileName, languageVersion, onError, shouldCreate);"),
    Mutation("initialiser not unwrapped where identity is decided",
             "\t\t\tconst initializer = unwrapParens(node.initializer);",
             "\t\t\tconst initializer = node.initializer;"),
    # NOT tested: "do not unwrap inside the seed test". The caller unwraps first, so
    # a second unwrap there is unreachable; the load-bearing ones are the declaration
    # initializer and the spread operand, which have their own mutations.
    Mutation("spread operand not unwrapped",
             "\t\t\tconst spread = unwrapParens(property.expression);",
             "\t\t\tconst spread = property.expression;"),
    Mutation("helper failure treated as an empty result",
             "        if finished.returncode != 0:", "        if False:"),
    # The three orthogonal export facts, and the scoping fixes.
    Mutation("stated type-only surface ignored",
             '        if export["exportTypeOnly"]:', "        if False:"),
    Mutation("target meanings collapsed to one",
             '\t\t\t\tif (flags & ts.SymbolFlags.Value) meanings.push("value");',
             "\t\t\t\tif (false) meanings.push(\"value\");"),
    Mutation("dependency target not marked external",
             "\t\t\tconst externalTarget = declarations.some((node) => {",
             "\t\t\tconst externalTarget = false && declarations.some((node) => {"),
    Mutation("case clauses not flattened into the switch scope",
             "\t\t\tif (ts.isCaseClause(child) || ts.isDefaultClause(child)) {",
             "\t\t\tif (false) {"),
    Mutation("loop header absorbed by the parent block",
             "\t\t\tif (ownList(child)) return;   // the loop hoists its own header",
             "\t\t\tif (false) return;   // the loop hoists its own header"),
    Mutation("enclosure not popped after its initializer",
             "\t\t\t\t\t\tif (name) enclosure.pop();", "\t\t\t\t\t\tif (false) enclosure.pop();"),
    Mutation("initializer walked twice",
             "\t\tif (alreadyDescended.has(node)) return;", "\t\tif (false) return;"),
    Mutation("initializer root skipped",
             "\t\t\t\t\t\tvisit(declaration.initializer, true);",
             "\t\t\t\t\t\tts.forEachChild(declaration.initializer, visit);"),
]


# The sweep NEVER writes to a tracked file. Mutating in place and restoring
# afterwards leaves a mutated extractor on disk whenever the process is
# interrupted, and no handler is guaranteed to run: a verification tool must not be
# able to damage the thing it verifies. Every write goes to a throwaway copy, so an
# interrupt at any moment leaves the repository untouched by construction rather
# than by cleanup.
TOOLS = pathlib.Path("tools")
COPIES = ["census_source.py", "census_families.py", "gen-feature-ids.py",
          "ts-spans.mjs", "ts-env-facts.mjs", "ts-members.mjs", "ts-shared.mjs",
          "test_gen_feature_ids.py"]

workspace = pathlib.Path(tempfile.mkdtemp(prefix="census-mutation-")) / "tools"
workspace.mkdir(parents=True)
for name in COPIES:
    shutil.copy(TOOLS / name, workspace / name)
ORIGINALS = {name: (TOOLS / name).read_text() for name in COPIES}
atexit.register(lambda: shutil.rmtree(workspace.parent, ignore_errors=True))


def run() -> str:
    shutil.rmtree(workspace / "__pycache__", ignore_errors=True)
    out = subprocess.run([sys.executable, "-B", str(workspace / "test_gen_feature_ids.py")],
                         capture_output=True, text=True)
    return out.stdout.strip().splitlines()[-1] if out.stdout.strip() else "NO OUTPUT"


def apply(old: str, new: str) -> tuple[str, int] | None:
    """Put the mutation in whichever COPY contains it; None if nowhere does."""
    for name in COPIES:
        text = ORIGINALS[name]
        if old in text:
            (workspace / name).write_text(text.replace(old, new))
            return name, text.count(old)
    return None


def restore() -> None:
    for name, text in ORIGINALS.items():
        if (workspace / name).read_text() != text:
            (workspace / name).write_text(text)


BASELINE = run()
print(f"  {'BASELINE (must be green)':<40} -> {BASELINE}")
assert "passed" in BASELINE and BASELINE.split("/")[0] == BASELINE.split("/")[1].split()[0], \
    f"baseline is not green: {BASELINE}"
escaped = 0
for label, old, new in MUTATIONS:
    placed = apply(old, new)
    if placed is None:
        print(f"  {label:<40} -> TARGET MISSING")
        escaped += 1
        continue
    name, count = placed
    result = run()
    survived = (result == BASELINE)   # green means the mutation was NOT caught
    if survived:
        escaped += 1
    print(f"  {label:<40} -> {result}  ({count} site{'s' if count > 1 else ''} "
          f"in {name}){'   <-- NOT CAUGHT' if survived else ''}")
    restore()

print(f"  {'RESTORED':<40} -> {run()}")
print(f"\n{len(MUTATIONS) - escaped}/{len(MUTATIONS)} mutations caught")
sys.exit(1 if escaped else 0)
