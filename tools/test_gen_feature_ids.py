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


def test_regex_after_a_keyword_is_blanked() -> None:
    """`return /.../` is a REGEX, not a division.

    Deciding from the previous CHARACTER classifies it as division, leaves the
    regex unblanked, and lets the code inside it become a member. The pinned
    source has 18 such sites, so this is not hypothetical.
    """
    for keyword in ("return", "throw", "typeof", "case", "in", "of", "new",
                    "delete", "await", "yield", "void", "instanceof", "default"):
        source = f'x = {keyword} /output({{ type: "phantom" }})/;\n'
        view = gen.SourceView("f.ts", source)
        assert "output(" not in view.structural, (
            f"regex after `{keyword}` was not blanked: {view.structural!r}")
        assert "phantom" not in view.structural


def test_division_is_still_division() -> None:
    """The mirror case: `/` after a value divides and must not blank the rest."""
    for expression in ("const r = total / count;",
                       "const r = arr[0] / 2;",
                       "const r = f() / 2;",
                       "const r = x.y / 2;"):
        view = gen.SourceView("f.ts", expression + "\n")
        assert view.structural == expression + "\n", (
            f"division was treated as a regex: {view.structural!r}")


def test_template_expression_is_code_and_template_text_is_not() -> None:
    """`${...}` holds CODE; the text around it does not.

    Blanking a whole template hides real reads inside its expressions -- a false
    negative -- while keeping the text would let pseudo-code in the literal part
    become a member. Both halves are asserted here.
    """
    view = gen.SourceView("f.ts", 'const s = `dir=${process.env.PI_REAL}/x`;\n')
    assert "process.env.PI_REAL" in view.structural, "code inside ${} was blanked"
    assert "dir=" not in view.structural, "template TEXT was not blanked"

    phantom = gen.SourceView("f.ts", 'const s = `export const createFakeTool = 1`;\n')
    assert "createFakeTool" not in phantom.structural


def test_nested_template_expression() -> None:
    """A template inside its own expression must not end the outer one early."""
    view = gen.SourceView("f.ts", 'const s = `a${ `b${ process.env.PI_DEEP }c` }d`;\n')
    assert "process.env.PI_DEEP" in view.structural
    for text in ("a", "b", "c", "d"):
        assert f"`{text}" not in view.structural.replace("PI_DEEP", "")


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
process.env.PI_SELF_SET = "true";
const on = process.env.PI_COMPARED === "1";
''',
    # A child environment built the way bash.ts builds one: cleared, then set.
    # The deletion must NOT read as a read, which is the defect that filed all
    # five session variables as reads and then documented them as such.
    "packages/coding-agent/src/child.ts": '''
const env = { ...getShellEnv() };
delete env.PI_EXPOSED_ONE;
delete env.PI_EXPOSED_TWO;
env.PI_EXPOSED_ONE = ctx.one;
env.PI_EXPOSED_TWO = ctx.two;
''',
}


class EnvFakeSource(FakeSource):
    def paths(self, pattern: str) -> list[str]:
        matcher = re.compile(pattern)
        return sorted(p for p in self._files if matcher.match(p))


def test_environment_names_cover_every_read_form() -> None:
    roles = gen.environment_names(EnvFakeSource(ENV_FIXTURE))
    names = roles["input"]
    assert "PI_OFFLINE" in names, names            # process.env.X
    assert "PI_TELEMETRY" in names, names          # process.env["X"]
    assert "PI_TUI_ESC_TIMEOUT" in names, names    # injected env.X
    assert "PI_CACHE_RETENTION" in names, names    # getProviderEnvValue("X")


def test_environment_includes_the_derived_names_at_their_default_spelling() -> None:
    names = gen.environment_names(EnvFakeSource(ENV_FIXTURE))["input"]
    assert "PI_CODING_AGENT_DIR" in names, names
    assert "PI_CODING_AGENT_SESSION_DIR" in names, names


def test_no_environment_name_from_a_comment_string_or_template() -> None:
    names = gen.environment_names(EnvFakeSource(ENV_FIXTURE))["input"]
    for phantom in ("PI_FROM_A_COMMENT", "PI_FROM_A_STRING", "PI_FROM_A_TEMPLATE"):
        assert phantom not in names, f"{phantom} must not be a member: {names}"


def test_a_deletion_is_not_a_read() -> None:
    """`delete env.X` must not file X as configuration input.

    This is the defect that put all five session variables in the read set and
    then described them, in prose, as reads.
    """
    roles = gen.environment_names(EnvFakeSource(ENV_FIXTURE))
    assert "PI_EXPOSED_ONE" not in roles["input"], roles["input"]
    assert "PI_EXPOSED_ONE" in roles["cleared"], roles["cleared"]


def test_child_write_and_self_write_are_different_roles() -> None:
    """`env.X =` builds a child's environment; `process.env.X =` mutates ours."""
    roles = gen.environment_names(EnvFakeSource(ENV_FIXTURE))
    assert roles["exposed"] == ["PI_EXPOSED_ONE", "PI_EXPOSED_TWO"], roles["exposed"]
    assert roles["self"] == ["PI_SELF_SET"], roles["self"]
    assert "PI_SELF_SET" not in roles["exposed"]
    assert "PI_EXPOSED_ONE" not in roles["self"]


def test_exposed_without_being_cleared_is_an_error() -> None:
    """The clear-then-set pairing is a guarantee, so losing it must fail loudly."""
    unguarded = dict(ENV_FIXTURE)
    unguarded["packages/coding-agent/src/child.ts"] = (
        "const env = { ...getShellEnv() };\nenv.PI_LEAKY = ctx.one;\n")
    gen.errors.clear()
    gen.environment_names(EnvFakeSource(unguarded))
    assert any("cleared first" in message for message in gen.errors), gen.errors
    gen.errors.clear()


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


def test_a_self_assignment_is_not_an_input_but_a_comparison_is() -> None:
    """Both halves matter.

    Dropping the assignment lookahead files the write-only name as configuration
    input; writing it too broadly (`=` instead of `=[^=]`) silently drops every
    `=== "..."` comparison, which IS a read.
    """
    roles = gen.environment_names(EnvFakeSource(ENV_FIXTURE))
    assert "PI_SELF_SET" not in roles["input"], roles["input"]
    assert "PI_SELF_SET" in roles["self"], roles["self"]
    assert "PI_COMPARED" in roles["input"], roles["input"]


def test_environment_fails_when_the_derivation_rule_changes() -> None:
    """A changed derivation must be an error, not a silently stale pair of names."""
    broken = dict(ENV_FIXTURE)
    broken[CONFIG_PATH] = 'export const ENV_AGENT_DIR = "PI_CODING_AGENT_DIR";\n'
    gen.errors.clear()
    result = gen.environment_names(EnvFakeSource(broken))
    assert result is None, result
    assert any("derivation rule changed" in message for message in gen.errors), gen.errors
    gen.errors.clear()


EXT_PATH = "packages/coding-agent/src/core/extensions/types.ts"

# Two real traps from the source, plus a phantom:
#   - one overload is WRAPPED across lines because its handler type is long, and
#     a line-anchored pattern drops exactly those (upstream, the three it drops
#     are the cancellable hooks);
#   - the payload union is smaller than the hook set and must not be the source;
#   - an `on(` inside a template must contribute nothing.
EXT_FIXTURE = {
    EXT_PATH: '''
export type ExtensionEvent =
\t| SessionEvent
\t| ContextEvent;

const docs = `api.on({ event: "phantom_hook" })`;

export interface ExtensionAPI {
\ton(event: "session_start", handler: ExtensionHandler<SessionStartEvent>): void;
\ton(
\t\tevent: "session_before_switch",
\t\thandler: ExtensionHandler<SessionBeforeSwitchEvent, SessionBeforeSwitchResult>,
\t): void;
\ton(event: "context", handler: ExtensionHandler<ContextEvent, ContextEventResult>): void;
}
'''
}


def test_hook_names_include_a_wrapped_overload() -> None:
    names = gen.extension_hook_names(FakeSource(EXT_FIXTURE))
    assert names == ["session_start", "session_before_switch", "context"], names


def test_hook_names_are_not_the_payload_union() -> None:
    """The union has 2 members and the hook set has 3; they must not be confused."""
    names = gen.extension_hook_names(FakeSource(EXT_FIXTURE))
    assert len(names) == 3, names
    assert "SessionEvent" not in names and "ContextEvent" not in names, names


def test_no_hook_name_from_a_template() -> None:
    names = gen.extension_hook_names(FakeSource(EXT_FIXTURE))
    assert "phantom_hook" not in names, names


def test_hook_fixture_really_is_adversarial() -> None:
    """The wrapped overload is invisible to a line-anchored pattern, and the
    phantom IS matchable on the extraction view."""
    source = EXT_FIXTURE[EXT_PATH]
    line_anchored = re.findall(r'^\ton\(event: "([a-z_]+)"', source, re.M)
    assert "session_before_switch" not in line_anchored, (
        "fixture's wrapped overload is not actually wrapped")
    assert len(line_anchored) == 2, line_anchored
    view = gen.SourceView(EXT_PATH, source)
    assert 'event: "phantom_hook"' in view.extraction
    assert 'phantom_hook' not in view.structural


THINKING = {
    "packages/agent/src/types.ts":
        'export type ThinkingLevel = "off" | "low" | "high";\n',
    "packages/ai/src/types.ts":
        'export type ThinkingLevel = "low" | "high";\n'
        'export type ModelThinkingLevel = "off" | ThinkingLevel;\n',
    "packages/protocol/src/schemas.ts":
        "export const ThinkingLevelSchema = Type.Union([\n"
        '\tType.Literal("off"),\n\tType.Literal("low"),\n\tType.Literal("high"),\n]);\n',
}


def test_thinking_levels_agree_across_three_declarations() -> None:
    assert gen.thinking_levels(FakeSource(THINKING)) == ["off", "low", "high"]


def test_thinking_levels_fail_when_declarations_diverge() -> None:
    """A divergence must be an error, not silently resolved by read order.

    The three spellings live in three packages; preferring whichever was read
    first would publish one package's view as the product's.
    """
    diverged = dict(THINKING)
    diverged["packages/protocol/src/schemas.ts"] = (
        "export const ThinkingLevelSchema = Type.Union([\n"
        '\tType.Literal("off"),\n\tType.Literal("low"),\n]);\n')
    gen.errors.clear()
    result = gen.thinking_levels(FakeSource(diverged))
    assert result is None, result
    assert any("disagree" in message for message in gen.errors), gen.errors
    gen.errors.clear()


AUTH_PATH = "packages/ai/src/auth/types.ts"

# Mirrors the real shape: the object union's own `options: readonly { id: string;
# label: string }` contains semicolons INSIDE braces, so a span that ends at the
# first `;` truncates the union and silently returns a short set. A phantom union
# in a template must contribute nothing.
AUTH_FIXTURE = {
    AUTH_PATH: '''
export type AuthType = "api_key" | "oauth";

export type AuthPrompt = { signal?: AbortSignal } & (
\t| { type: "text"; message: string }
\t| { type: "select"; message: string; options: readonly { id: string; label: string }[] }
\t| { type: "hint"; placeholder: "type ; then }" }
\t| { type: "manual_code"; message: string }
);

const docs = `export type AuthPrompt = { type: "phantom_prompt" };`;
'''
}


def test_auth_type_reads_a_union_of_bare_literals() -> None:
    assert gen.auth_literals(FakeSource(AUTH_FIXTURE), "AuthType", keyed=False) \
        == ["api_key", "oauth"]


def test_auth_prompt_span_survives_a_semicolon_inside_braces() -> None:
    """The member AFTER the nested braces is the one a shallow span loses."""
    members = gen.auth_literals(FakeSource(AUTH_FIXTURE), "AuthPrompt", keyed=True)
    assert members == ["text", "select", "hint", "manual_code"], members


def test_no_auth_member_from_a_template() -> None:
    members = gen.auth_literals(FakeSource(AUTH_FIXTURE), "AuthPrompt", keyed=True)
    assert "phantom_prompt" not in members, members


def test_auth_fixture_really_is_adversarial() -> None:
    """Both traps are present: a nested semicolon, and a phantom that would match."""
    source = AUTH_FIXTURE[AUTH_PATH]
    assert "options: readonly { id: string; label: string }" in source
    # A brace and a semicolon INSIDE a string value: on the extraction view these
    # act as delimiters and the span never terminates, so scanning the wrong view
    # is detectable. Without this the two views agree and the mutation survives.
    assert 'placeholder: "type ; then }"' in source
    view = gen.SourceView(AUTH_PATH, source)
    declaration = view.structural.index("export type AuthPrompt")
    shallow = view.structural.index(";", declaration)
    deep = gen.type_alias_span(view, "AuthPrompt")
    assert deep is not None and deep[1] > shallow, (
        "fixture has no nested semicolon for the depth tracking to matter")
    assert 'type: "phantom_prompt"' in view.extraction


def test_auth_literals_rejects_the_wrong_shape() -> None:
    """Asking for bare literals on an object union must fail, not return a subset.

    `quoted_in` over an object union would happily return `message`-adjacent
    strings and look like a plausible member set.
    """
    gen.errors.clear()
    result = gen.auth_literals(FakeSource(AUTH_FIXTURE), "NoSuchUnion", keyed=True)
    assert result is None
    assert any("NoSuchUnion" in message for message in gen.errors), gen.errors
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
