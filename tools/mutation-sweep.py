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

MUTATIONS = [
    ("discover reads extraction",
     "pattern, self.structural))", "pattern, self.extraction))"),
    ("quoted_after anchors on extraction",
     "self.structural[begin:stop]", "self.extraction[begin:stop]"),
    ("quoted_in walks extraction",
     "char = self.structural[index]", "char = self.extraction[index]"),
    ("alias falls back to exact ' as '",
     "aliased = alias.search(clause)", "aliased = None"),
    ("derivation-change guard removed",
     "if len(derived) != 2:", "if len(derived) < 0:"),
    # These two target the READ PATTERNS, not the docstring that quotes them.
    # The docstring writes the lookahead with a doubled backslash, so anchoring
    # on the single-backslash form reaches only the code.
    ("assignment lookahead dropped",
     r'(PI_[A-Z0-9_]+)\b(?!\s*=[^=])"', r'(PI_[A-Z0-9_]+)\b"'),
    ("lookahead too broad, drops comparisons",
     r'\b(?!\s*=[^=])"', r'\b(?!\s*=)"'),
    ("type span ends at the first semicolon",
     'elif char == ";" and depth == 0:', 'elif char == ";":'),
    ("type span scans the extraction view",
     "char = view.structural[index]", "char = view.extraction[index]"),
    # The real upstream-shaped mistake is reading the event literal from the SAME
    # LINE as `on(`. The wrapped overloads still start a line, so anchoring `on(`
    # is not what truncates -- requiring the literal beside it is. Upstream that
    # drops the three cancellable hooks.
    ("hook literal read from one line only",
     "open_paren + 1 + len(argument))",
     'view.structural.find("\\n", open_paren))'),
    # NOT tested: "read the payload union instead". Widening the scan range still
    # contains the on() overloads, so behaviour does not change and no fixture
    # could detect it. The 25-vs-33 confusion is a prose error, not a code path.
    ("thinking-level agreement check removed",
     "if not (sorted(set(from_agent)) == sorted(set(from_ai)) == sorted(set(from_protocol))):",
     "if False:"),
    # The two lexical defects, one in each direction.
    ("union member pattern rejects generics and extends",
     r'rf"^export interface {re.escape(name)}\s*(?:<[^>]*>)?\s*"',
     r'rf"^export interface {re.escape(name)}\s*"'),
    # The parser-backed lexing: dropping either span kind must be caught.
    # NOT tested: "leave comments and regexes in the EXTRACTION view". Every read
    # of that view happens at a span already located on the structural view, where
    # those regions are blanked, so no extractor can reach into one. There is no
    # observable behaviour to catch, and a control that cannot fail is decoration.
    ("string/template text not blanked structurally",
     'self.structural = blank(source, spans["dead"] + spans["text"])',
     'self.structural = blank(source, spans["dead"])'),
    # The environment rules.
    # The TUI registry/export sets.
    ("barrel misses `export type {...}`",
     r'r"\bexport\s+(type\s+)?\{([^}]*)\}"',
     r'r"\bexport\s*\{([^}]*)\}"'),
    ("barrel flattens the two declaration spaces",
     "(types if (whole_clause_is_type or per_clause_type) else values).append(exported)",
     "values.append(exported)"),
    ("barrel accepts a wildcard re-export",
     'if re.search(r"^export \\*", view.structural, re.M):',
     'if False:'),
    ("keybinding authorities not compared",
     "if sorted(set(from_interface)) != sorted(set(from_table)):",
     "if False:"),
    # The offset conversion and the per-receiver pairing.
    ("utf16 offsets emitted unconverted",
     "\t\tif (!/[\\uD800-\\uDBFF]/.test(source)) return spans; // BMP only: identical",
     "\t\treturn spans;"),
    ("env accesses resolved by name, not by scope",
     "\t\t\tconst binding = resolve(receiver.text);",
     "\t\t\tconst binding = [...objects.keys()].length ? { __objectId: [...objects.keys()][0] } : undefined;"),
]


# The sweep NEVER writes to a tracked file. Mutating in place and restoring
# afterwards leaves a mutated extractor on disk whenever the process is
# interrupted, and no handler is guaranteed to run: a verification tool must not be
# able to damage the thing it verifies. Every write goes to a throwaway copy, so an
# interrupt at any moment leaves the repository untouched by construction rather
# than by cleanup.
TOOLS = pathlib.Path("tools")
COPIES = ["census_source.py", "census_families.py", "gen-feature-ids.py",
          "ts-spans.mjs", "ts-env-facts.mjs", "test_gen_feature_ids.py"]

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
