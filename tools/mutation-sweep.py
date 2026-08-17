#!/usr/bin/env python3
"""Mutation sweep: every mutation below must make the control tests fail.

A green test suite proves nothing on its own -- it may be testing the wrong
thing. Each entry breaks the extractor in a way that a real mistake would, and
the suite must go red. Two lessons are baked in:

  * The baseline is MEASURED, not hardcoded. An earlier version compared against
    a literal "16/16", so once the suite grew, green runs were counted as caught
    and the sweep reported success it had not observed.
  * A mutation must model the actual failure. "Anchor `on(` to a line start" left
    the suite green, because the wrapped overloads do start a line -- what
    truncates upstream is reading the event literal from the same line.

Run from the repo root: python3 tools/mutation-sweep.py
Exit status is non-zero if any mutation survives.
"""
import pathlib, shutil, subprocess, sys, tempfile

TOOL = pathlib.Path("tools/gen-feature-ids.py")
GOOD = pathlib.Path(tempfile.gettempdir()) / "gen-feature-ids.good.py"
shutil.copy(TOOL, GOOD)

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
    # The lexical defects @gpt-codex found: both directions.
    ("regex-vs-division decided by character",
     'if token in _KEYWORDS_BEFORE_EXPRESSION:\n            return False',
     'if False:\n            return False'),
    ("template expression treated as text",
     'if self.source[scan: scan + 2] == "${":\n                break',
     'if False:\n                break'),
    ("deletion counted as a read",
     r'r"(?<!delete )(?<!process\.)\benv\s*\.\s*(PI_[A-Z0-9_]+)\b(?!\s*=[^=])"',
     r'r"(?<!process\.)\benv\s*\.\s*(PI_[A-Z0-9_]+)\b(?!\s*=[^=])"'),
    ("child write and self write merged",
     r'child_writes = [r"(?<!process\.)\benv\s*\.\s*(PI_[A-Z0-9_]+)\s*=[^=]"]',
     r'child_writes = [r"(?:process\s*\.\s*)?\benv\s*\.\s*(PI_[A-Z0-9_]+)\s*=[^=]"]'),
    ("clear-then-set guard removed",
     "unguarded = exposed - cleared", "unguarded = set()"),
    ("union member pattern rejects generics and extends",
     r'rf"^export interface {re.escape(name)}\s*(?:<[^>]*>)?\s*"',
     r'rf"^export interface {re.escape(name)}\s*"'),
]

def run() -> str:
    shutil.rmtree("tools/__pycache__", ignore_errors=True)
    out = subprocess.run([sys.executable, "-B", "tools/test_gen_feature_ids.py"],
                         capture_output=True, text=True)
    return out.stdout.strip().splitlines()[-1] if out.stdout.strip() else "NO OUTPUT"

BASELINE = run()
print(f"  {'BASELINE (must be green)':<40} -> {BASELINE}")
assert "passed" in BASELINE and BASELINE.split("/")[0] == BASELINE.split("/")[1].split()[0], \
    f"baseline is not green: {BASELINE}"
escaped = 0
for label, old, new in MUTATIONS:
    text = GOOD.read_text()
    count = text.count(old)
    if count == 0:
        print(f"  {label:<40} -> TARGET MISSING")
        escaped += 1
        continue
    TOOL.write_text(text.replace(old, new))
    result = run()
    survived = (result == BASELINE)   # green means the mutation was NOT caught
    if survived:
        escaped += 1
    print(f"  {label:<40} -> {result}  ({count} site{'s' if count > 1 else ''})"
          f"{'   <-- NOT CAUGHT' if survived else ''}")
    shutil.copy(GOOD, TOOL)

print(f"  {'RESTORED':<40} -> {run()}")
print(f"\n{len(MUTATIONS) - escaped}/{len(MUTATIONS)} mutations caught")
sys.exit(1 if escaped else 0)
