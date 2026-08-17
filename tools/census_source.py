"""Pinned source access and the two offset-aligned views.

Split out of the generator because three concerns were sharing one file: reading
a pinned commit, deciding what in a TypeScript file is code, and choosing which
families to emit. Only the first two live here.

The `errors` list and `fail()` are defined here and imported everywhere else, so
every module appends to the SAME list and the entry point sees one verdict.
"""

from __future__ import annotations

import argparse
import json
import os
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
        self._views: dict[str, "SourceView"] = {}
        self._spans = Spans(repo)
        self._env_facts = EnvFacts(repo)
        self._member_facts = MemberFacts(repo)
        self._members: dict[str, dict] = {}

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

    def view(self, path: str) -> "SourceView":
        """The scanned views of one file, read and parsed at most once.

        Caching is not only about cost: it makes one file yield one view, so two
        extractors reading the same path cannot silently disagree about it.
        """
        cached = self._views.get(path)
        if cached is None:
            self.prefetch([path])
            cached = self._views[path]
        return cached

    def prefetch(self, paths: list[str]) -> None:
        """Read and parse several files in ONE helper process.

        The environment scan covers every source file in the tree; one process per
        file would make the parser cost dominate the run, and the point of using
        the real parser is that it be affordable enough to always use.
        """
        wanted = {p: self.read(p) for p in paths if p not in self._views}
        if not wanted:
            return
        for path, spans in self._spans.of(wanted).items():
            self._views[path] = SourceView(path, wanted[path], spans)

    def members(self, path: str) -> dict:
        """Compiler-API facts for one file, fetched at most once."""
        if path not in self._members:
            self._members.update(self._member_facts.of({path: self.read(path)}))
        return self._members[path]

    def members_graph(self, paths: list[str], wanted: str) -> dict:
        """Facts for one file, parsed together with a graph it depends on.

        A re-export can only be classified if the module it comes from is in the
        program; without it the alias resolves to a synthetic symbol and every
        export collapses into one kind. The caller supplies the graph because it
        knows which one is relevant.
        """
        if wanted not in self._members:
            self._members.update(
                self._member_facts.of({path: self.read(path) for path in paths}))
        return self._members[wanted]

    def env_facts(self, paths: list[str]) -> dict[str, dict]:
        """Scope-resolved environment facts for several files, in one process."""
        return self._env_facts.of({path: self.read(path) for path in paths})

    def paths(self, pattern: str) -> list[str]:
        """Every tracked path at the baseline matching a regex, sorted.

        Listing the tree at the pinned commit -- rather than walking the working
        directory -- keeps the file set as pinned as the file contents.
        """
        listed = self._git("ls-tree", "-r", "--name-only", self.baseline)
        if listed.returncode != 0:
            raise SourceUnavailable(
                f"cannot list the tree at {self.baseline}: {listed.stderr.strip()}"
            )
        matcher = re.compile(pattern)
        return sorted(p for p in listed.stdout.splitlines() if matcher.match(p))


SPANS_HELPER = os.path.join(os.path.dirname(os.path.abspath(__file__)), "ts-spans.mjs")


class Spans:
    """Non-code spans for a batch of files, from TypeScript's own parser.

    Whether `/` opens a regular expression or divides is decided by grammatical
    position, not by the preceding character or token. `)` ends a value when it
    closes a call and does not when it closes an if-head, so both of these are
    legal and differ:

        if (ready) /pattern/.test(s);     // regex
        const r = compute(x) / 2;         // division

    A scanner that answers this from local context leaves regular expressions
    unblanked and their contents become members. The decision therefore belongs to
    the parser that defines the grammar; `tools/ts-spans.mjs` walks the AST and
    reports:

      * `dead` - comments and regular expression literals;
      * `text` - string bodies and template TEXT, delimiters EXCLUDED so the
        quotes and braces the extractors anchor on stay visible.

    A template's `${}` expressions are absent from `text` by construction: the
    parser exposes them as ordinary expressions, so nothing has to remember to
    treat them as code.
    """

    def __init__(self, repo: str) -> None:
        self.repo = repo

    def of(self, files: dict[str, str]) -> dict[str, dict[str, list]]:
        helper = SPANS_HELPER
        if not os.path.exists(helper):
            print(f"ERROR: missing {helper}: the lexical helper is required, and "
                  f"falling back to a heuristic is what it replaced", file=sys.stderr)
            sys.exit(2)
        try:
            finished = subprocess.run(
                ["node", helper, self.repo],
                input=json.dumps(files), capture_output=True, text=True,
            )
        except OSError as exc:
            print(f"ERROR: cannot run node for {helper}: {exc.strerror}\n"
                  f"       Node and the pinned checkout's typescript are required; "
                  f"there is deliberately no heuristic fallback.", file=sys.stderr)
            sys.exit(2)
        if finished.returncode != 0:
            print(f"ERROR: {SPANS_HELPER} failed ({finished.returncode}): "
                  f"{finished.stderr.strip()}", file=sys.stderr)
            sys.exit(2)
        return json.loads(finished.stdout)


class MemberFacts:
    """Compiler-API member facts for a batch of files.

    Separate helper from the span and environment ones because it answers a third
    question: not "what is code" or "which object", but "what does this declaration
    declare". A family's membership authority stays in its own extractor; this only
    supplies the facts it reads.
    """

    HELPER = os.path.join(os.path.dirname(os.path.abspath(__file__)), "ts-members.mjs")

    def __init__(self, repo: str) -> None:
        self.repo = repo

    def of(self, files: dict[str, str]) -> dict[str, dict]:
        if not os.path.exists(self.HELPER):
            print(f"ERROR: missing {self.HELPER}: member extraction requires it, and "
                  f"a pattern fallback is what it replaced", file=sys.stderr)
            sys.exit(2)
        try:
            finished = subprocess.run(
                ["node", self.HELPER, self.repo],
                input=json.dumps(files), capture_output=True, text=True,
            )
        except OSError as exc:
            print(f"ERROR: cannot run node for {self.HELPER}: {exc.strerror}", file=sys.stderr)
            sys.exit(2)
        if finished.returncode != 0:
            print(f"ERROR: ts-members.mjs failed ({finished.returncode}): "
                  f"{finished.stderr.strip()}", file=sys.stderr)
            sys.exit(2)
        return json.loads(finished.stdout)


class EnvFacts:
    """Environment-object facts with scope identity, from the parser.

    Separate from `Spans` because it answers a different question: not "what is
    code" but "which object is this access on". Receiver TEXT is not identity --
    two functions may each declare a local `env` -- so the helper resolves each
    reference to the innermost binding that declares it and reports accesses it
    cannot resolve instead of guessing.
    """

    HELPER = os.path.join(os.path.dirname(os.path.abspath(__file__)), "ts-env-facts.mjs")

    def __init__(self, repo: str) -> None:
        self.repo = repo

    def of(self, files: dict[str, str]) -> dict[str, dict]:
        if not os.path.exists(self.HELPER):
            print(f"ERROR: missing {self.HELPER}: environment scope identity requires "
                  f"it, and a text-level fallback is what it replaced", file=sys.stderr)
            sys.exit(2)
        try:
            finished = subprocess.run(
                ["node", self.HELPER, self.repo],
                input=json.dumps(files), capture_output=True, text=True,
            )
        except OSError as exc:
            print(f"ERROR: cannot run node for {self.HELPER}: {exc.strerror}",
                  file=sys.stderr)
            sys.exit(2)
        if finished.returncode != 0:
            print(f"ERROR: ts-env-facts.mjs failed ({finished.returncode}): "
                  f"{finished.stderr.strip()}", file=sys.stderr)
            sys.exit(2)
        return json.loads(finished.stdout)


def blank(source: str, spans: list) -> str:
    """Blank the given spans, preserving length and every newline."""
    result = list(source)
    for begin, finish in spans:
        for position in range(max(begin, 0), min(finish, len(source))):
            if result[position] != "\n":
                result[position] = " "
    return "".join(result)


class SourceView:
    """One file under two offset-aligned views, with distinct jobs.

    The two jobs must not be confused, and confusing them is a real defect this
    class exists to prevent:

      * `structural` -- comments, regex literals AND string contents blanked.
        **Discovery runs here.** Pseudo-code inside a string or template
        (`const s = "export const createPhantomTool = 1"`) has had its contents
        blanked, so it cannot match a declaration or call shape and cannot
        become a member.
      * `extraction` -- comments and regex literals blanked, string contents
        KEPT, because the values being read (ids, type names, command literals)
        live inside strings. **Values are read here**, at a span discovered on
        the structural view.

    Discovering on `extraction` would accept fabricated members written inside
    string literals; extracting from `structural` would read blanks. Neither
    view can do both jobs, which is why both exist and why offsets align.
    """

    __slots__ = ("path", "extraction", "structural")

    def __init__(self, path: str, source: str, spans: dict) -> None:
        self.path = path
        self.extraction = blank(source, spans["dead"])
        self.structural = blank(source, spans["dead"] + spans["text"])
        # Every guarantee below rests on offset alignment; assert it rather than
        # trust it, because a scanner change that broke it would otherwise show
        # up as values silently read from the wrong place.
        if not (len(self.extraction) == len(self.structural) == len(source)):
            fail(f"{path}: views lost offset alignment "
                 f"({len(source)} source, {len(self.extraction)} extraction, "
                 f"{len(self.structural)} structural)")

    def discover(self, pattern: "re.Pattern[str] | str") -> "list[re.Match[str]]":
        """Find code sites, on the view where strings cannot fake them."""
        return list(re.finditer(pattern, self.structural))

    def value(self, begin: int, finish: int) -> str:
        """Read the real text of a discovered span, string contents included."""
        return self.extraction[begin:finish]

    def quoted_after(self, prefix: str, begin: int = 0,
                     finish: int | None = None) -> list[str]:
        """Values of every `<prefix>"..."` whose PREFIX is real code.

        This is the safe way to read a string literal, and the only one used
        here. The prefix and both quotes are located on the structural view, so
        a pair written inside a string or template cannot contribute a value;
        the value itself is then read from the aligned extraction view, where
        string contents survive.

        `prefix` must end where the opening quote begins. Single and double
        quotes both count, since this codebase uses both.
        """
        stop = len(self.structural) if finish is None else finish
        found: list[str] = []
        for match in re.finditer(prefix + r"[\"']", self.structural[begin:stop]):
            opening = begin + match.end() - 1
            closing = self.structural.find(self.structural[opening], opening + 1)
            if closing == -1 or closing >= stop:
                # An unterminated literal is a scanner or source problem, not a
                # value; skipping it silently would publish a short set, so the
                # caller's own emptiness check has to catch it.
                continue
            found.append(self.value(opening + 1, closing))
        return found

    def quoted_in(self, begin: int, finish: int) -> list[str]:
        """Every string literal in a span, for declarations of bare literals.

        A type union (`export type Mode = "text" | "json"`) has no key to anchor
        on, so the quotes themselves are the anchor -- and they are walked on the
        structural view, pairing each opening quote with its own closing quote,
        so a quote inside a string can neither open nor close a literal here.
        """
        found: list[str] = []
        index = begin
        while index < finish:
            char = self.structural[index]
            if char in "\"'":
                closing = self.structural.find(char, index + 1)
                if closing == -1 or closing >= finish:
                    break
                found.append(self.value(index + 1, closing))
                index = closing + 1
                continue
            index += 1
        return found

    def balanced_argument(self, open_paren: int) -> str | None:
        """Return the text inside a call's parentheses.

        Depth is counted on the structural view, so a parenthesis inside a
        comment or string cannot shift it; the text returned comes from the
        extraction view, which still holds the values.
        """
        depth = 0
        for index in range(open_paren, len(self.structural)):
            char = self.structural[index]
            if char == "(":
                depth += 1
            elif char == ")":
                depth -= 1
                if depth == 0:
                    return self.value(open_paren + 1, index)
        return None


def normalize(literal: str) -> str:
    with_boundaries = re.sub(r"(?<=[a-z0-9])(?=[A-Z])", "-", literal)
    return with_boundaries.replace("_", "-").lower()
