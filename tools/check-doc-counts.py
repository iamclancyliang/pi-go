#!/usr/bin/env python3
"""Fail when a published count disagrees with the generator or with itself.

Counts kept in step by hand drift, and they drift independently: a heading against
its own body, one document against another, a quoted total against the suite it
quotes. A reader cannot tell which figure is newer, so a disagreement has to break a
gate rather than be left to them.

This checks what can be checked mechanically:

  * the inventory's summary row against the generator's own family and member
    totals;
  * every embedded set block against the generator's blocks, name and count;
  * the environment role counts, the name total and the membership total, all
    derived from the generator rather than read from prose;
  * the same environment totals as stated in both Chinese documents;
  * the test and mutation totals quoted in the Chinese feature list, against the
    suites themselves.

Run from the repo root:
    python3 tools/check-doc-counts.py --pi-repo /path/to/pi
"""

from __future__ import annotations

import argparse
import pathlib
import os
import re
import subprocess
import sys

TOOLS = pathlib.Path(__file__).resolve().parent
ROOT = TOOLS.parent
INVENTORY = ROOT / "docs/product/pi-feature-inventory.md"
FEATURE_LIST = ROOT / "docs/product/pi-功能清单.md"
USAGE = ROOT / "docs/product/pi-使用说明.md"

problems: list[str] = []


def complain(message: str) -> None:
    problems.append(message)
    print(f"MISMATCH: {message}", file=sys.stderr)


def expect(label: str, found: int | None, wanted: int) -> None:
    if found is None:
        complain(f"{label}: no figure found in the document at all")
    elif found != wanted:
        complain(f"{label}: document says {found}, generator says {wanted}")


def first_int(pattern: str, text: str) -> int | None:
    found = re.search(pattern, text)
    return int(found.group(1)) if found else None


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__,
                                     formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--pi-repo", required=True)
    parser.add_argument("--baseline")
    # The sweep re-runs the whole suite once per mutation, so checking the quoted
    # mutation total costs minutes. Off by default: a slow gate gets skipped, and a
    # gate that gets skipped protects nothing.
    parser.add_argument("--with-sweep", action="store_true",
                        help="also verify the quoted mutation total (slow)")
    args = parser.parse_args()

    command = [sys.executable, "-B", str(TOOLS / "gen-feature-ids.py"),
               "--pi-repo", args.pi_repo]
    if args.baseline:
        command += ["--baseline", args.baseline]
    generated = subprocess.run(command, capture_output=True, text=True)
    if generated.returncode != 0:
        print("the generator itself failed; counts cannot be checked:\n"
              + generated.stderr.strip(), file=sys.stderr)
        return 2

    blocks = re.findall(r"^### `([a-z][^`]+)` — (\d+) members", generated.stdout, re.M)
    families = len(blocks)
    members = len(re.findall(r"^[a-z-]+\.[a-z.-]+", generated.stdout, re.M))

    inventory = INVENTORY.read_text()

    # Summary row.
    row = re.search(r"\| Stable ID sets \| \*\*(\d+) families, (\d+) IDs\*\*", inventory)
    if not row:
        complain("the inventory has no `Stable ID sets` summary row to check")
    else:
        expect("inventory summary families", int(row.group(1)), families)
        expect("inventory summary IDs", int(row.group(2)), members)

    # Embedded blocks, block for block.
    embedded = re.findall(r"^### `([a-z][^`]+)` — (\d+) members", inventory, re.M)
    if embedded != blocks:
        only_doc = [b for b in embedded if b not in blocks]
        only_gen = [b for b in blocks if b not in embedded]
        complain(f"embedded set blocks differ from the generator; "
                 f"in the document only: {only_doc}; in the generator only: {only_gen}")

    # Environment roles, derived rather than transcribed.
    roles = {}
    for role in ("input", "exposed", "self", "cleared"):
        roles[role] = len(re.findall(rf"^coding-agent\.environment\.{role}\.",
                                     generated.stdout, re.M))
    memberships = sum(roles.values())
    names = len({
        m.group(1) for m in re.finditer(
            r"^coding-agent\.environment\.(?:input|exposed|self|cleared)\.(\S+?)(?:\s|$)",
            generated.stdout, re.M)
    })

    # Anchored to the HEADING LINE. An unanchored search matched prose elsewhere
    # that quotes the old, drifted figures while explaining why this check exists --
    # a checker that reads its own rationale as data reports a false mismatch.
    heading = re.search(r"^### 22\.7 [^\n]*$", inventory, re.M)
    heading_text = heading.group(0) if heading else ""
    if not heading:
        complain("the inventory has no §22.7 heading to check")
    expect("inventory §22.7 heading memberships",
           first_int(r"\*\*(\d+) memberships", heading_text), memberships)
    expect("inventory §22.7 heading names",
           first_int(r"memberships over (\d+) names", heading_text), names)
    expect("inventory §22.7 body names",
           first_int(r"\*\*(\d+) distinct names, \d+ memberships\*\*", inventory), names)
    expect("inventory §22.7 body memberships",
           first_int(r"\*\*\d+ distinct names, (\d+) memberships\*\*", inventory), memberships)
    for role, count in roles.items():
        expect(f"inventory §22.7 role table `{role}`",
               first_int(rf"`coding-agent\.environment\.{role}` \| \*\*(\d+)\*\*", inventory),
               count)

    feature_list = FEATURE_LIST.read_text()
    expect("feature list families",
           first_int(r"\*\*已经可以机器校验的\*\*：(\d+) 类", feature_list), families)
    expect("feature list members",
           first_int(r"共 (\d+) 个成员", feature_list), members)
    expect("feature list environment names",
           first_int(r"一共 \*\*(\d+) 个名字\*\*", feature_list), names)
    expect("feature list environment memberships",
           first_int(r"合计 (\d+) 个「用途条目」", feature_list), memberships)

    usage = USAGE.read_text()
    expect("usage document environment names",
           first_int(r"环境变量（一共 (\d+) 个名字", usage), names)

    # ONE checkout for every child. The generator is told which tree to read; a child
    # left to the ambient environment answers about a different one, and the gate then
    # prints a single verdict covering two trees.
    child_env = dict(os.environ)
    child_env["PI_REPO"] = args.pi_repo

    # The suites' own totals, since the feature list quotes them.
    tests = subprocess.run([sys.executable, "-B", str(TOOLS / "test_gen_feature_ids.py")],
                           capture_output=True, text=True, cwd=ROOT, env=child_env)
    passed = first_int(r"(\d+)/\d+ passed", tests.stdout)
    expect("feature list test count",
           first_int(r"这样的测试共 (\d+) 个", feature_list), passed or -1)

    # The DEFINED mutation count is checkable instantly; only proving each one is
    # caught needs the slow run. Leaving both to the slow gate meant a stale
    # published figure could sit there indefinitely, because the fast gate reported
    # the total as unchecked.
    defined = len(re.findall(r"^    Mutation\(",
                             (TOOLS / "mutation-sweep.py").read_text(), re.M))
    expect("feature list mutation count",
           first_int(r"(\d+) 种改法全部被抓到", feature_list), defined)

    caught = None
    if args.with_sweep:
        # The same checkout the fast checks used. Left to the ambient environment, the
        # generator answers about the pinned tree while the sweep answers about
        # whatever PI_REPO happens to be, and the combined gate has two inputs.
        sweep = subprocess.run([sys.executable, "-B", str(TOOLS / "mutation-sweep.py"),
                                "--pi-repo", args.pi_repo],
                               capture_output=True, text=True, cwd=ROOT, env=child_env)
        caught = first_int(r"(\d+)/\d+ mutations caught", sweep.stdout)
        if caught != defined:
            complain(f"the sweep caught {caught} of {defined} defined mutations")

    if problems:
        print(f"\n{len(problems)} published count(s) disagree; the documents are not "
              f"consistent with the generator", file=sys.stderr)
        return 1
    tail = f", {caught} mutations" if caught is not None else " (mutation total unchecked)"
    print(f"counts agree: {families} families, {members} IDs, "
          f"{names} environment names over {memberships} memberships, "
          f"{passed} tests" + tail)
    return 0


if __name__ == "__main__":
    sys.exit(main())
