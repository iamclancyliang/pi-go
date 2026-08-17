#!/usr/bin/env python3
"""Tests for the feature-ID extractor, centred on what it must REFUSE to find.

The extractor's failure mode is not crashing, it is succeeding with fabricated
members: pseudo-code written inside a string or template literal looks exactly
like the real declaration it imitates. Every test below that starts `test_no_`
is a negative control, and each one is paired with a proof that the fixture is
actually adversarial -- that the same search DOES find the phantom on the
extraction view. Without that pairing a negative control can pass because the
fixture was toothless rather than because the code is right.

Run: python3 tools/test_gen_feature_ids.py
"""

import re
import sys
import types
from pathlib import Path

# The extractor is COMPILED FROM SOURCE TEXT here rather than imported, and that
# is deliberate. Python validates cached bytecode by mtime-and-size, so an edit
# that preserves file size -- swapping `structural` for `extraction`, say, which
# is exactly the mutation these tests exist to catch -- can be served from a
# stale `.pyc` and the tests then grade code that is not on disk. That happened,
# and it made a mutation look reverted when it was not. Compiling the text makes
# the file on disk the only thing under test.
_PATH = Path(__file__).with_name("gen-feature-ids.py")
gen = types.ModuleType("gen_feature_ids")
gen.__file__ = str(_PATH)
exec(compile(_PATH.read_text(), str(_PATH), "exec"), gen.__dict__)


class FakeSource(gen.Source):
    """A Source whose files come from a dict instead of a pinned commit."""

    def __init__(self, files: dict[str, str]) -> None:
        super().__init__(repo="/nonexistent", baseline="0" * 40)
        self._files = files

    def read(self, path: str) -> str:
        if path not in self._files:
            raise gen.SourceUnavailable(f"fixture has no {path}")
        return self._files[path]


HARNESS_PATH = "packages/agent/src/harness/tools/index.ts"

# Three real exports, and three things that must NOT become members:
#   - a phantom export inside a string literal
#   - a phantom export inside a template literal
#   - a non-exported internal alias
# The `as` alias is split across lines and indented with a tab, which the
# previous implementation dropped because it required the exact substring
# " as ".
HARNESS_FIXTURE = '''
export { createBashTool } from "./bash.ts";
export {
\tcreateInternalReadTool
\t\tas
\t\tcreateReadTool,
} from "./read.ts";
export const createWriteTool = () => {};

const helpText = "export const createPhantomTool = 1";
const template = `export const createTemplatePhantomTool = 2`;
const createUnexportedTool = () => {};
'''


def test_harness_exports_are_exactly_the_real_ones() -> None:
    names = gen.harness_tool_names(FakeSource({HARNESS_PATH: HARNESS_FIXTURE}))
    assert names == ["bash", "read", "write"], names


def test_no_phantom_harness_export_from_string_or_template() -> None:
    names = gen.harness_tool_names(FakeSource({HARNESS_PATH: HARNESS_FIXTURE}))
    for phantom in ("phantom", "template-phantom", "unexported"):
        assert phantom not in names, f"{phantom} must not be a member: {names}"


def test_harness_fixture_really_is_adversarial() -> None:
    """Proof the control has teeth: on the extraction view the phantoms DO match.

    If discovery is moved back to the extraction view, the assertions above turn
    red -- which is the property that makes them a control rather than decoration.
    """
    view = gen.SourceView(HARNESS_PATH, HARNESS_FIXTURE)
    declaration = re.compile(
        r"\bexport\s+(?:default\s+)?(?:declare\s+)?(?:async\s+)?"
        r"(?:const|let|var|function\*?|class)\s+create([A-Z][A-Za-z]*)Tool\b"
    )
    on_extraction = set(declaration.findall(view.extraction))
    on_structural = set(declaration.findall(view.structural))
    assert "Phantom" in on_extraction, "fixture is toothless: no phantom to catch"
    assert "TemplatePhantom" in on_extraction, "template phantom missing from fixture"
    assert "Phantom" not in on_structural
    assert "TemplatePhantom" not in on_structural


AGENT_TYPES = "packages/agent/src/types.ts"
AGENT_SESSION = "packages/coding-agent/src/core/agent-session.ts"
RPC_MODE = "packages/coding-agent/src/modes/rpc/rpc-mode.ts"

RPC_FIXTURE = {
    AGENT_TYPES: '''
export type AgentEvent =
\t| { type: "agent_start" }
\t| { type: "agent_end" };
''',
    AGENT_SESSION: '''
export type AgentSessionEvent =
\t| { type: "model_changed" };
''',
    # One real emission, three payload kinds that are catalogued elsewhere, and
    # two phantoms: a template literal and a string that both contain a complete
    # `output({ type: "..." })` call.
    RPC_MODE: '''
const doc = `output({ type: "phantom_from_template" })`;
const help = "output({ type: \\"phantom_from_string\\" })";
function run() {
\toutput({ type: "extension_error", message: m });
\toutput(toJsonEvent(event));
\toutput(success(id, result));
\toutput(response);
}
''',
}


def test_rpc_events_are_the_union_of_the_three_real_sources() -> None:
    events = gen.rpc_event_ids(FakeSource(RPC_FIXTURE))
    assert events == ["agent_start", "agent_end", "model_changed", "extension_error"], events


def test_no_phantom_rpc_event_from_string_or_template() -> None:
    events = gen.rpc_event_ids(FakeSource(RPC_FIXTURE))
    assert "phantom_from_template" not in events, events
    assert "phantom_from_string" not in events, events


def test_rpc_fixture_really_is_adversarial() -> None:
    """The phantom calls are discoverable on the extraction view, and only there."""
    view = gen.SourceView(RPC_MODE, RPC_FIXTURE[RPC_MODE])
    assert len(re.findall(r"\boutput\(", view.extraction)) == 6, "fixture lost a phantom"
    assert len(re.findall(r"\boutput\(", view.structural)) == 4, (
        "structural view must see only the four real calls")


CONFIG_PATH = "packages/coding-agent/src/config.ts"

ENV_FIXTURE = {
    CONFIG_PATH: '''
export const ENV_AGENT_DIR = `${APP_NAME.toUpperCase()}_CODING_AGENT_DIR`;
export const ENV_SESSION_DIR = `${APP_NAME.toUpperCase()}_CODING_AGENT_SESSION_DIR`;
const dir = process.env[ENV_AGENT_DIR];
''',
    "packages/coding-agent/src/reads.ts": '''
// A commented-out read: process.env.PI_FROM_A_COMMENT
const doc = "process.env.PI_FROM_A_STRING";
const template = `env.PI_FROM_A_TEMPLATE`;
const offline = process.env.PI_OFFLINE;
const bracket = process.env["PI_TELEMETRY"];
const injected = env.PI_TUI_ESC_TIMEOUT;
const helper = getProviderEnvValue("PI_CACHE_RETENTION", env);
process.env.PI_ASSIGNED_ONLY = "true";
const on = process.env.PI_COMPARED === "1";
''',
}


class EnvFakeSource(FakeSource):
    def paths(self, pattern: str) -> list[str]:
        matcher = re.compile(pattern)
        return sorted(p for p in self._files if matcher.match(p))


def test_environment_names_cover_every_read_form() -> None:
    names = gen.environment_names(EnvFakeSource(ENV_FIXTURE))
    assert "PI_OFFLINE" in names, names            # process.env.X
    assert "PI_TELEMETRY" in names, names          # process.env["X"]
    assert "PI_TUI_ESC_TIMEOUT" in names, names    # injected env.X
    assert "PI_CACHE_RETENTION" in names, names    # getProviderEnvValue("X")


def test_environment_includes_the_derived_names_at_their_default_spelling() -> None:
    names = gen.environment_names(EnvFakeSource(ENV_FIXTURE))
    assert "PI_CODING_AGENT_DIR" in names, names
    assert "PI_CODING_AGENT_SESSION_DIR" in names, names


def test_no_environment_name_from_a_comment_string_or_template() -> None:
    names = gen.environment_names(EnvFakeSource(ENV_FIXTURE))
    for phantom in ("PI_FROM_A_COMMENT", "PI_FROM_A_STRING", "PI_FROM_A_TEMPLATE"):
        assert phantom not in names, f"{phantom} must not be a member: {names}"


def test_environment_fixture_really_is_adversarial() -> None:
    """Each phantom is a COMPLETE pseudo-read, so it would match if scanned raw.

    A fixture holding only a bare name in a string would pass this control for
    the wrong reason: no read pattern could ever match a bare name.
    """
    source = ENV_FIXTURE["packages/coding-agent/src/reads.ts"]
    view = gen.SourceView("reads.ts", source)
    raw = re.compile(r"(?:process\.)?env\.(PI_[A-Z0-9_]+)")
    on_raw = set(raw.findall(source))
    assert {"PI_FROM_A_COMMENT", "PI_FROM_A_STRING", "PI_FROM_A_TEMPLATE"} <= on_raw, on_raw
    on_structural = set(raw.findall(view.structural))
    assert "PI_FROM_A_COMMENT" not in on_structural
    assert "PI_FROM_A_STRING" not in on_structural
    assert "PI_FROM_A_TEMPLATE" not in on_structural


def test_environment_excludes_an_assignment_but_keeps_a_comparison() -> None:
    """A variable that is only written is not a configuration input.

    Both halves matter: dropping the assignment lookahead lets the write-only
    marker in, and writing the lookahead too broadly (`=` instead of `=[^=]`)
    would silently drop every `=== "..."` comparison, which IS a read.
    """
    names = gen.environment_names(EnvFakeSource(ENV_FIXTURE))
    assert "PI_ASSIGNED_ONLY" not in names, names
    assert "PI_COMPARED" in names, names


def test_environment_fails_when_the_derivation_rule_changes() -> None:
    """A changed derivation must be an error, not a silently stale pair of names."""
    broken = dict(ENV_FIXTURE)
    broken[CONFIG_PATH] = 'export const ENV_AGENT_DIR = "PI_CODING_AGENT_DIR";\n'
    gen.errors.clear()
    result = gen.environment_names(EnvFakeSource(broken))
    assert result is None, result
    assert any("derivation rule changed" in message for message in gen.errors), gen.errors
    gen.errors.clear()


def test_quoted_after_ignores_a_key_written_inside_a_template() -> None:
    """A template literal is the adversarial case, not an escaped string.

    Inside `"type: \\"fake\\""` the quote is backslash-escaped, so no pattern
    would match it and the control would pass for the wrong reason. Inside a
    template the quotes are literal, so on the extraction view the pair reads as
    genuine -- which is what must not happen.
    """
    source = '''
const doc = `type: "fake"`;
const real = { type: "genuine" };
'''
    view = gen.SourceView("f.ts", source)
    assert 'type: "fake"' in view.extraction, "fixture is toothless"
    assert view.quoted_after(r"type:\s*") == ["genuine"]


def test_quoted_in_ignores_quotes_that_live_inside_another_literal() -> None:
    """Only a quote that is a real delimiter may open a value.

    A quote inside a TEMPLATE is the case that separates the two views: the
    template's contents are blanked structurally, so the walker never sees those
    quotes, while on the extraction view it would open a value at the inner
    quote and every value after it would shift.
    """
    source = 'const members = ["text", `tpl "inner" tail`, \'json\'];'
    view = gen.SourceView("f.ts", source)
    assert '"inner"' in view.extraction, "fixture is toothless"
    span = view.structural.index("["), len(view.structural)
    assert view.quoted_in(*span) == ["text", "json"]


def test_regex_literal_does_not_hide_following_code() -> None:
    """A regex containing quotes and parens must not swallow the code after it."""
    view = gen.SourceView("f.ts", '''
const pattern = /output\\("[a-z]+"\\)/g;
const divided = total / count;
output({ type: "real_event" });
''')
    assert view.quoted_after(r"type:\s*") == ["real_event"]
    # Division must not be mistaken for the start of a regex; if it were, the
    # rest of the line would be blanked and the literal above would vanish.
    assert "divided" in view.structural


def test_views_stay_offset_aligned() -> None:
    source = HARNESS_FIXTURE + RPC_FIXTURE[RPC_MODE] + "// trailing comment\n"
    view = gen.SourceView("f.ts", source)
    assert len(view.extraction) == len(view.structural) == len(source)
    for index, char in enumerate(source):
        if char == "\n":
            assert view.extraction[index] == "\n"
            assert view.structural[index] == "\n"


def main() -> int:
    tests = [(name, function) for name, function in sorted(globals().items())
             if name.startswith("test_") and callable(function)]
    failures = 0
    for name, function in tests:
        gen.errors.clear()
        try:
            function()
        except AssertionError as exc:
            failures += 1
            print(f"FAIL {name}: {exc}", file=sys.stderr)
            continue
        if gen.errors:
            failures += 1
            print(f"FAIL {name}: extractor reported {gen.errors}", file=sys.stderr)
            continue
        print(f"ok   {name}")
    print(f"\n{len(tests) - failures}/{len(tests)} passed")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
