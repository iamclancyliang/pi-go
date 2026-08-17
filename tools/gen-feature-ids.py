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
        self._views: dict[str, "SourceView"] = {}

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
        """The scanned views of one file, read and scanned at most once.

        Caching is not only about cost: it makes one file yield one view, so two
        extractors reading the same path cannot silently disagree about it.
        """
        cached = self._views.get(path)
        if cached is None:
            cached = SourceView(path, self.read(path))
            self._views[path] = cached
        return cached

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


# A `/` is division when a value could already have ended, and starts a regex
# otherwise. These are the character classes that can END a value in this
# codebase's TypeScript: an identifier or numeric character, a closing bracket
# of any kind, or an identifier character that `isalnum` does not cover.
_CLOSES_A_VALUE = frozenset(")]}")
_IDENTIFIER_TAIL = frozenset("_$")


def _value_may_have_ended(previous: str) -> bool:
    """Whether the previous significant character could end a value.

    Naming this test is the point: at the call site the bare set membership read
    as an arbitrary punctuation list, which hid the one rule that decides
    division from regex.
    """
    if not previous:
        return False
    return (previous.isalnum()
            or previous in _CLOSES_A_VALUE
            or previous in _IDENTIFIER_TAIL)


def _scan(source: str, blank_string_contents: bool) -> str:
    """Return source with non-code regions blanked, preserving every offset.

    `SourceView` derives both views from this and documents which job each one
    does; see that class. Both preserve length and newlines, so an offset found
    in one view addresses the same character in the other.

    A `/` begins a regex only where a value cannot already have ended, which is
    the standard test applied to the previous significant character.
    """
    result = list(source)
    length = len(source)
    index = 0

    def blank(begin: int, finish: int) -> None:
        for position in range(begin, min(finish, length)):
            if result[position] != "\n":
                result[position] = " "

    def previous_significant(before: int) -> str:
        scan = before - 1
        while scan >= 0 and source[scan] in " \t\n\r":
            scan -= 1
        return source[scan] if scan >= 0 else ""

    while index < length:
        char = source[index]
        pair = source[index: index + 2]

        if pair == "//":
            newline = source.find("\n", index)
            stop = length if newline == -1 else newline
            blank(index, stop)
            index = stop
            continue

        if pair == "/*":
            close = source.find("*/", index + 2)
            stop = length if close == -1 else close + 2
            blank(index, stop)
            index = stop
            continue

        if char in "\"'`":
            quote = char
            scan = index + 1
            while scan < length:
                if source[scan] == "\\":
                    scan += 2
                    continue
                if source[scan] == quote:
                    break
                if source[scan] == "\n" and quote != "`":
                    break
                scan += 1
            if blank_string_contents:
                blank(index + 1, scan)
            index = min(scan + 1, length)
            continue

        previous = previous_significant(index)
        if char == "/" and not _value_may_have_ended(previous):
            scan = index + 1
            in_class = False
            while scan < length:
                if source[scan] == "\\":
                    scan += 2
                    continue
                if source[scan] == "[":
                    in_class = True
                elif source[scan] == "]":
                    in_class = False
                elif source[scan] == "/" and not in_class:
                    scan += 1
                    break
                elif source[scan] == "\n":
                    break
                scan += 1
            while scan < length and source[scan].isalpha():
                scan += 1
            blank(index, scan)
            index = scan
            continue

        index += 1

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

    def __init__(self, path: str, source: str) -> None:
        self.path = path
        self.extraction = _scan(source, blank_string_contents=False)
        self.structural = _scan(source, blank_string_contents=True)
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


def environment_names(src: Source) -> list[str] | None:
    """Every `PI_*` environment variable the product READS.

    Three facts make a naive scan wrong, and each one cost a wrong count first:

    1. Not every name is a literal. `config.ts` builds two of them from the
       configurable app name (`${APP_NAME.toUpperCase()}_CODING_AGENT_DIR`), so
       grepping for `PI_` misses them entirely and a fork named `tau` reads
       `TAU_*`. They are recorded under their default `pi` spelling, with the
       derivation as their authority.
    2. There are three read FORMS, not one: `process.env.X`, `process.env["X"]`
       and a member of an injected `env` object -- including through the
       `getProviderEnvValue("X", env)` helper. Scanning only `process.env.X`
       missed `PI_TUI_ESC_TIMEOUT` and `PI_CACHE_RETENTION`, both documented.
    3. A name that is only ASSIGNED is not a read, and an assignment matches a
       naive read pattern. `PI_CODING_AGENT` is set for child processes and never
       read back, so it is not a configuration input. The `(?!\\s*=[^=])`
       lookahead in each read pattern is what excludes it -- comparison (`===`)
       still counts, assignment does not. A name-specific exclusion list was
       tried first and turned out to be dead code behind this lookahead.

    Membership is every read across the pinned source tree, so a variable added
    in any package is picked up without this list being edited.
    """
    reads = [
        r"process\s*\.\s*env\s*\.\s*(PI_[A-Z0-9_]+)\b(?!\s*=[^=])",
        r"(?<!process\.)\benv\s*\.\s*(PI_[A-Z0-9_]+)\b(?!\s*=[^=])",
    ]
    quoted_reads = [
        r"process\s*\.\s*env\s*\[\s*",
        r"(?<!process\.)\benv\s*\[\s*",
        r"getProviderEnvValue\s*\(\s*",
    ]

    names: set[str] = set()
    for path in src.paths(r"packages/[^/]+/src/.*\.tsx?$"):
        view = src.view(path)
        for pattern in reads:
            for match in view.discover(pattern):
                names.add(match.group(1))
        for prefix in quoted_reads:
            for value in view.quoted_after(prefix):
                if re.fullmatch(r"PI_[A-Z0-9_]+", value):
                    names.add(value)

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

    return sorted(names)


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

    environment = environment_names(src)
    if environment is not None:
        emit("coding-agent.environment",
             "every `PI_*` read across `packages/*/src` at the baseline, in all "
             "three read forms, plus the two names derived from `APP_NAME`",
             "the variable name, at its default `pi` spelling for the two "
             "derived ones; assignment-only markers are excluded",
             environment)

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
