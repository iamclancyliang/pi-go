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

Run from the repo root: python3 tools/mutation-sweep.py [--pi-repo PATH]

The checkout is named ONCE and passed to every child suite. Accepting an unknown
argument silently would be worse than rejecting it: a run told to sweep one checkout
while actually reading another reports a green result about a tree nobody asked
about.

Exit status is non-zero if any mutation survives.
"""
from __future__ import annotations

import argparse, atexit, os, pathlib, shutil, subprocess, sys, tempfile

from collections import namedtuple

# A bare triple leaves a reader counting positions to tell the searched text from
# the replacement. Naming the fields makes each entry read as what it claims: this
# label describes breaking `find` into `replace`.
# An EDIT is one search paired with its replacement. Held as two parallel sequences
# and zipped, a missing entry on one side silently drops the last edit, and the sweep
# then reports a weaker mutation than the one written as caught. Pairing them at
# construction makes that unrepresentable.
Edit = namedtuple("Edit", "find replace")


class Mutation:
    """One named break, as the edits that produce it, applied together.

    Several edits mean a guarantee held at several sites at once: reverting one of
    them changes no answer because the others still hold the line, so mutated one
    site at a time, redundancy scores as an untested guarantee.
    """

    def __init__(self, label: str, find, replace) -> None:
        searches = find if isinstance(find, tuple) else (find,)
        replacements = replace if isinstance(replace, tuple) else (replace,)
        if len(searches) != len(replacements):
            raise ValueError(
                f"{label}: {len(searches)} search string(s) against "
                f"{len(replacements)} replacement(s) -- every edit must be a pair")
        self.label = label
        self.edits = tuple(Edit(f, r) for f, r in zip(searches, replacements))

parser = argparse.ArgumentParser(description="mutation sweep over the census tools")
parser.add_argument("--pi-repo", default=os.environ.get("PI_REPO"),
                    help="the pinned Pi checkout each suite run reads TypeScript from; "
                         "defaults to $PI_REPO, then to the suite's own fallback")
options = parser.parse_args()

# One environment for every child, so the sweep and the suite cannot read different
# trees. Unset means the suite applies its own documented fallback.
CHILD_ENV = dict(os.environ)
if options.pi_repo:
    CHILD_ENV["PI_REPO"] = options.pi_repo

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
             "    if unknown:", "    if False:"),
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
    # All three reads must fail closed together: with any one of them still
    # refusing, resolution stops there and the other two are never reached.
    Mutation("compiler host falls back to the file system",
             ("\t\tif (isLib(fileName)) {\n\t\t\treturn original(fileName, languageVersion, onError, shouldCreate);\n\t\t}\n\t\treturn undefined;",
              "\t\tsupplied.has(fileName) || (isLib(fileName) && ts.sys.fileExists(fileName));",
              "\t\tsupplied.get(fileName) ?? (isLib(fileName) ? ts.sys.readFile(fileName) : undefined);"),
             ("\t\treturn original(fileName, languageVersion, onError, shouldCreate);",
              "\t\tsupplied.has(fileName) || ts.sys.fileExists(fileName);",
              "\t\tsupplied.get(fileName) ?? ts.sys.readFile(fileName);")),
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
             "\t\tif (!force && alreadyDescended.has(node)) return;",
             "\t\tif (!force && false) return;"),
    Mutation("catch variable not bound",
             "\t\tif (ts.isCatchClause(node) && node.variableDeclaration) {",
             "\t\tif (false && node.variableDeclaration) {"),
    Mutation("catch variable leaks to the enclosing scope",
             "\t\t\tdeclarePattern(node.variableDeclaration.name, node.variableDeclaration);",
             "\t\t\tscopes[scopes.length - 2].names.set(\n"
             "\t\t\t\tnode.variableDeclaration.name.text, node.variableDeclaration);"),
    Mutation("block function declaration hoisted to the function",
             "\t\t\tif (ts.isFunctionDeclaration(child) && child.name) declare(child.name.text, child);",
             "\t\t\tif (ts.isFunctionDeclaration(child) && child.name) declareVar(child.name.text, child);"),
    Mutation("directory view taken from the working tree",
             "\t\treturn suppliedDirectories.has(trimmed) ||\n"
             "\t\t\t(isLib(trimmed) && ts.sys.directoryExists(trimmed));",
             "\t\treturn ts.sys.directoryExists(trimmed);"),
    Mutation("star walk skips modules already in the graph",
             "            if target and target not in visited:",
             "            if target and target not in graph:"),
    Mutation("import-equals does not bind its name",
             "\t\t\tif (ts.isImportEqualsDeclaration(child) && child.name) {",
             "\t\t\tif (false && child.name) {"),
    Mutation("using read as function-scoped",
             "const isBlockScoped = (list) => Boolean(list.flags & ts.NodeFlags.BlockScoped);",
             "const isBlockScoped = (list) => "
             "Boolean(list.flags & (ts.NodeFlags.Let | ts.NodeFlags.Const));"),
    Mutation("class static block is not a var boundary",
             "\t\t// A `var` in a class static block does not leave it, so the block is a var\n"
             "\t\t// boundary. The class around it is not one.\n"
             "\t\tts.isClassStaticBlockDeclaration(node);",
             "\t\tfalse;"),
    Mutation("named expression does not bind its own name",
             "\t\tif (scoped && (ts.isFunctionExpression(node) || ts.isClassExpression(node)) &&",
             "\t\tif (false && (ts.isFunctionExpression(node) || ts.isClassExpression(node)) &&"),
    Mutation("class expression opens no scope",
             "\t\tts.isClassExpression(node) || ts.isClassStaticBlockDeclaration(node);",
             "\t\tts.isClassStaticBlockDeclaration(node);"),
    Mutation("star guard stops at the barrel's own stars",
             "                visited.add(target)\n                pending.append(target)",
             "                visited.add(target)"),
    Mutation("resolution decided by the symbol table",
             "\t\t\t\tresolved: Boolean(resolvedFileName),",
             "\t\t\t\tresolved: Boolean(resolvedFileName) && "
             "Boolean(checker.getSymbolAtLocation(specifier)),"),
    Mutation("re-export specifier read at a fixed depth",
             "\t\t\t\tconst specifier = exportSpecifierOf(node);",
             "\t\t\t\tconst specifier = node.parent?.parent?.moduleSpecifier?.text;"),
    Mutation("unresolved star re-export ignored",
             "    if opaque:", "    if False:"),
    Mutation("destructured var left in the scope the walk stands in",
             "\t\t\t\t\t} else {\n\t\t\t\t\t\t// A destructured `var` introduces names too",
             "\t\t\t\t\t} else if (false) {\n\t\t\t\t\t\t// A destructured `var` introduces names too"),
    Mutation("enum name not declared in its scope",
             "\t\t\tif (ts.isEnumDeclaration(child) && child.name) declare(child.name.text, child);",
             "\t\t\tif (false && child.name) declare(child.name.text, child);"),
    Mutation("meanings compressed back to one dominant space",
             "        for meaning in meanings:\n            spaces[meaning].append(export[\"name\"])",
             "        spaces[meanings[0]].append(export[\"name\"])"),
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
                         capture_output=True, text=True, env=CHILD_ENV)
    return out.stdout.strip().splitlines()[-1] if out.stdout.strip() else "NO OUTPUT"


def apply(edits) -> tuple[str, int] | None:
    """Put the mutation in whichever COPY contains it; None if nowhere does."""
    for name in COPIES:
        text = ORIGINALS[name]
        if all(edit.find in text for edit in edits):
            count = 0
            for edit in edits:
                count += text.count(edit.find)
                text = text.replace(edit.find, edit.replace)
            (workspace / name).write_text(text)
            return name, count
    return None


def restore() -> None:
    for name, text in ORIGINALS.items():
        if (workspace / name).read_text() != text:
            (workspace / name).write_text(text)


def preflight() -> list[str]:
    """Every reason a mutation could score as caught without testing anything.

    Both checks run BEFORE the suite does, because both failure modes are invisible
    in a green report:

      * A search string that is no longer in the source tests nothing. The sweep
        scores it as an escape, but only after re-running the suite once per
        mutation -- so a rename removes coverage and the report that would say so is
        the one nobody waits for. This cannot be a test in the graded suite: a
        mutation removes its own search text by construction, so such a test would
        fail under every mutation and score all of them as caught.
      * A replacement that leaves the file unparseable is not a behaviour change.
        The suite cannot run at all, which differs from the baseline, so the
        mutation is credited to a control that was never reached. Commenting out
        the first line of a multi-line call does exactly this.
    """
    problems = []
    for mutation in MUTATIONS:
        label = mutation.label
        for name, text in ORIGINALS.items():
            if not all(edit.find in text for edit in mutation.edits):
                continue
            mutated = text
            for edit in mutation.edits:
                mutated = mutated.replace(edit.find, edit.replace)
            if name.endswith(".py"):
                try:
                    compile(mutated, name, "exec")
                except SyntaxError as exc:
                    problems.append(f"{label}: mutating {name} leaves it unparseable ({exc.msg})")
            else:
                with tempfile.NamedTemporaryFile("w", suffix=".mjs", delete=False) as handle:
                    handle.write(mutated)
                checked = subprocess.run(["node", "--check", handle.name],
                                         capture_output=True, text=True)
                os.unlink(handle.name)
                if checked.returncode:
                    problems.append(f"{label}: mutating {name} leaves it unparseable")
            break
        else:
            problems.append(f"{label}: search text is no longer in any source file")
    return problems


blocking = preflight()
if blocking:
    for problem in blocking:
        print(f"  {problem}")
    print(f"\n{len(blocking)} mutation(s) cannot test anything; nothing was run")
    sys.exit(1)

checkout = CHILD_ENV.get("PI_REPO") or "the suite's own fallback"
print(f"  {'checkout':<40} -> {checkout}")
BASELINE = run()
print(f"  {'BASELINE (must be green)':<40} -> {BASELINE}")
assert "passed" in BASELINE and BASELINE.split("/")[0] == BASELINE.split("/")[1].split()[0], \
    f"baseline is not green: {BASELINE}"
escaped = 0
for mutation in MUTATIONS:
    label = mutation.label
    placed = apply(mutation.edits)
    if placed is None:
        print(f"  {label:<40} -> TARGET MISSING")
        escaped += 1
        continue
    name, count = placed
    result = run()
    if "passed" not in result:
        # No summary means the suite could not RUN -- a mutation that breaks the
        # syntax of a file, say. Scoring that as caught credits a control that may
        # not exist: the harness never reached the assertion.
        print(f"  {label:<40} -> {result}  (in {name})   <-- DID NOT RUN, not a behaviour change")
        escaped += 1
        restore()
        continue
    survived = (result == BASELINE)   # green means the mutation was NOT caught
    if survived:
        escaped += 1
    print(f"  {label:<40} -> {result}  ({count} site{'s' if count > 1 else ''} "
          f"in {name}){'   <-- NOT CAUGHT' if survived else ''}")
    restore()

print(f"  {'RESTORED':<40} -> {run()}")
print(f"\n{len(MUTATIONS) - escaped}/{len(MUTATIONS)} mutations caught")
sys.exit(1 if escaped else 0)
