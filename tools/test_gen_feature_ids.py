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

import os
import re
import subprocess
import sys
import types
from pathlib import Path

# The tools are COMPILED FROM SOURCE TEXT rather than imported, and bytecode
# writing is disabled. Python validates cached bytecode by mtime-and-size, so an
# edit that preserves file size -- swapping `structural` for `extraction`, exactly
# what these tests exist to catch -- can be served from a stale `.pyc`. The suite
# would then grade code that is not on disk, and a live mutation would read as
# reverted. Compiling the text makes the files on disk the only thing under test.
_TOOLS = Path(__file__).parent
sys.path.insert(0, str(_TOOLS))

sys.dont_write_bytecode = True
_PATH = _TOOLS / "gen-feature-ids.py"
gen = types.ModuleType("gen_feature_ids")
gen.__file__ = str(_PATH)
exec(compile(_PATH.read_text(), str(_PATH), "exec"), gen.__dict__)


# The extractor delegates lexing to TypeScript's own parser, so these tests need a
# checkout that provides it. Resolved from PI_REPO, else the conventional sibling
# path. If it is missing the tests FAIL rather than skip: a negative control that
# quietly does not run is worse than no control, because the suite still reports
# green.
TS_REPO = os.environ.get("PI_REPO") or str(Path.home() / "Project" / "github" / "pi")
if not (Path(TS_REPO) / "node_modules" / "typescript").exists():
    print(f"FATAL: no typescript under {TS_REPO}; set PI_REPO to a Pi checkout "
          f"whose dependencies are installed. Refusing to run a suite whose "
          f"lexical controls cannot execute.", file=sys.stderr)
    raise SystemExit(2)


class FakeSource(gen.Source):
    """A Source whose files come from a dict instead of a pinned commit."""

    def __init__(self, files: dict[str, str]) -> None:
        super().__init__(repo=TS_REPO, baseline="0" * 40)
        self._files = files

    def read(self, path: str) -> str:
        if path not in self._files:
            raise gen.SourceUnavailable(f"fixture has no {path}")
        return self._files[path]

    def paths(self, pattern: str) -> list[str]:
        """List the fixture rather than a commit, for extractors that scan a graph."""
        matcher = re.compile(pattern)
        return sorted(path for path in self._files if matcher.match(path))


def view_of(source: str, path: str = "f.ts") -> "gen.SourceView":
    """Build a view the way the tool does: real spans from the real parser."""
    spans = gen.Spans(TS_REPO).of({path: source})[path]
    return gen.SourceView(path, source, spans)


def test_a_failing_helper_stops_the_run() -> None:
    """A helper that fails must not be read as an empty result.

    Treating a non-zero status as "no facts" turns a broken toolchain into a short
    member set, which looks exactly like a small family.
    """
    finished = subprocess.run(
        [sys.executable, "-B", str(_TOOLS / "gen-feature-ids.py"),
         "--pi-repo", "/nonexistent-checkout"],
        capture_output=True, text=True, cwd=_TOOLS.parent)
    assert finished.returncode != 0, finished.stdout[-400:]
    assert "ERROR" in finished.stderr, finished.stderr[-400:]


def test_no_tool_defines_the_same_name_twice() -> None:
    """A duplicate definition silently wins, and the earlier one becomes dead.

    Editing by slicing a file can leave two copies of a function; Python uses the
    last, so a corrected version can sit above a stale one that is what actually
    runs. Nothing else in this suite would notice.
    """
    for name in ("census_source.py", "census_families.py", "gen-feature-ids.py"):
        text = (_TOOLS / name).read_text()
        defined = re.findall(r"^def (\w+)\(", text, re.M)
        duplicates = {n for n in defined if defined.count(n) > 1}
        assert not duplicates, f"{name} defines {sorted(duplicates)} more than once"


def test_regex_in_expression_position_is_blanked() -> None:
    """A regex is blanked wherever the grammar allows one.

    The fixtures are REAL code for each position, which the parser now enforces:
    A fixture like `x = return /re/;` is not valid TypeScript at all, so it proves
    nothing about any construct that can appear in the source.
    """
    snippets = [
        "function f() { return /export const createGhostTool/.source; }",
        "function f() { throw /export const createGhostTool/; }",
        "const t = typeof /export const createGhostTool/;",
        "switch (x) { case /export const createGhostTool/.source: break; }",
        "const b = x instanceof /export const createGhostTool/.constructor;",
        "async function f() { await /export const createGhostTool/.source; }",
        "function* g() { yield /export const createGhostTool/; }",
        "void /export const createGhostTool/;",
        "for (const c of /export const createGhostTool/.source) { use(c); }",
        "delete /export const createGhostTool/.lastIndex;",
        "if (ready) /export const createGhostTool/.test(s);",
        "while (ready) /export const createGhostTool/.test(s);",
        "const v = ready ? /export const createGhostTool/ : null;",
        "const list = [/export const createGhostTool/];",
        "call(/export const createGhostTool/);",
        "const r = 1 + /export const createGhostTool/.source.length;",
    ]
    for snippet in snippets:
        view = view_of(snippet + "\n")
        assert "createGhostTool" not in view.structural, f"not blanked: {snippet}"
        assert "export const" not in view.structural, f"not blanked: {snippet}"


def test_regex_after_a_closing_paren_is_position_dependent() -> None:
    """`)` does NOT settle it: an if-head is followed by a regex, a call by division.

    Both lines below are legal. Treating every `)` as ending a value leaves the
    regex unblanked, and `harness_tool_names` then produces a member from its
    contents. Only the grammar decides this, which is why the
    parser does.
    """
    regex_case = view_of("function f(s) {\n\tif (ready) /export const createGhostTool/.test(s);\n}\n")
    assert "createGhostTool" not in regex_case.structural, regex_case.structural
    assert "export const" not in regex_case.structural

    division_case = view_of("const ratio = compute(x) / total;\n")
    assert division_case.structural == "const ratio = compute(x) / total;\n"


def test_no_harness_member_from_a_regex_after_an_if_head() -> None:
    source = ("function f(s) {\n\tif (ready) /export const createGhostTool/.test(s);\n}\n"
              "export const createBashTool = () => {};\n")
    names = gen.harness_tool_names(FakeSource({HARNESS_PATH: source}))
    assert names == ["bash"], names


def test_division_is_still_division() -> None:
    """The mirror case: `/` after a value divides and must not blank the rest."""
    for expression in ("const r = total / count;",
                       "const r = arr[0] / 2;",
                       "const r = f() / 2;",
                       "const r = x.y / 2;"):
        view = view_of(expression + "\n")
        assert view.structural == expression + "\n", (
            f"division was treated as a regex: {view.structural!r}")


def test_template_expression_is_code_and_template_text_is_not() -> None:
    """`${...}` holds CODE; the text around it does not.

    Blanking a whole template hides real reads inside its expressions -- a false
    negative -- while keeping the text would let pseudo-code in the literal part
    become a member. Both halves are asserted here.
    """
    view = view_of('const s = `dir=${process.env.PI_REAL}/x`;\n')
    assert "process.env.PI_REAL" in view.structural, "code inside ${} was blanked"
    assert "dir=" not in view.structural, "template TEXT was not blanked"

    phantom = view_of('const s = `export const createFakeTool = 1`;\n')
    assert "createFakeTool" not in phantom.structural


def test_nested_template_expression() -> None:
    """A template inside its own expression must not end the outer one early."""
    view = view_of('const s = `a${ `b${ process.env.PI_DEEP }c` }d`;\n')
    assert "process.env.PI_DEEP" in view.structural
    for text in ("a", "b", "c", "d"):
        assert f"`{text}" not in view.structural.replace("PI_DEEP", "")


THINK_AGENT = "packages/agent/src/types.ts"
THINK_AI = "packages/ai/src/types.ts"
THINK_PROTOCOL = "packages/protocol/src/schemas.ts"

# Three declarations, two memberships, and three different SHAPES: a flat union, a
# union plus a reference, and members carried as call arguments.
THINKING_FIXTURE = {
    THINK_AGENT: 'export type ThinkingLevel = "off" | "low" | "high";\n',
    THINK_AI: ('export type ThinkingLevel = "low" | "high";\n'
               'export type ModelThinkingLevel = "off" | ThinkingLevel;\n'),
    THINK_PROTOCOL: "\n".join([
        "export const ThinkingLevelSchema = Type.Union([",
        '\tType.Literal("off"),',
        '\tType.Literal("low"),',
        '\tType.Literal("high"),',
        "]);",
        "export const OtherSchema = Type.Union([",
        '\tType.Literal("unrelated"),',
        "]);",
        "",
    ]),
}


def test_thinking_levels_agree_across_three_shapes() -> None:
    assert gen.thinking_levels(FakeSource(THINKING_FIXTURE)) == ["off", "low", "high"]


def test_the_protocol_schema_is_scoped_to_its_own_binding() -> None:
    """Another schema's literals in the same file must not join the set.

    The members are call arguments, so a file-wide view of `Type.Literal(...)` merges
    every schema in the file.
    """
    levels = gen.thinking_levels(FakeSource(THINKING_FIXTURE))
    assert "unrelated" not in levels, levels


def test_a_protocol_divergence_fails() -> None:
    diverged = dict(THINKING_FIXTURE)
    diverged[THINK_PROTOCOL] = THINKING_FIXTURE[THINK_PROTOCOL].replace(
        '\tType.Literal("high"),\n', "")
    gen.errors.clear()
    assert gen.thinking_levels(FakeSource(diverged)) is None
    assert any("disagree" in m for m in gen.errors), gen.errors
    gen.errors.clear()


def test_a_changed_model_level_derivation_fails() -> None:
    """`ModelThinkingLevel` must still be built from `ThinkingLevel`."""
    changed = dict(THINKING_FIXTURE)
    changed[THINK_AI] = ('export type ThinkingLevel = "low" | "high";\n'
                         'export type ModelThinkingLevel = "off" | "low" | "high";\n')
    gen.errors.clear()
    assert gen.thinking_levels(FakeSource(changed)) is None
    assert any("no longer references" in m for m in gen.errors), gen.errors
    gen.errors.clear()


MODE_TRUST = "packages/coding-agent/src/core/project-trust.ts"
MODE_ARGS = "packages/coding-agent/src/cli/args.ts"

# Two variables named `mode` in one file: the one taken from `--mode`, and another
# flag's whose values are unrelated. A file-wide view of `mode === "..."` merges
# them, so the accepted set must be selected by the binding.
MODE_FIXTURE = {
    MODE_TRUST: 'export type AppMode = "interactive" | "print" | "json" | "rpc";\n',
    MODE_ARGS: "\n".join([
        'export type Mode = "text" | "json" | "rpc";',
        "function parse(args) {",
        "  if (arg === '--mode') {",
        "    const mode = args[++i];",
        '    if (mode === "text" || mode === "json" || mode === "rpc") { use(mode); }',
        "  }",
        "  if (arg === '--screen') {",
        "    const mode = args[i + 1];",
        '    if (mode === "fullscreen" || mode === "regular") { use(mode); }',
        "  }",
        "}",
        "",
    ]),
}


def test_app_modes_come_from_the_type_union() -> None:
    assert gen.app_modes(FakeSource(MODE_FIXTURE)) == \
        ["interactive", "print", "json", "rpc"]


def test_cli_mode_literals_select_the_right_binding() -> None:
    """Another `mode` in the same file must not contribute its values.

    A file-wide scan of `mode === "..."` returns five literals here; only three
    belong to `--mode`.
    """
    assert gen.cli_mode_literals(FakeSource(MODE_FIXTURE)) == ["text", "json", "rpc"]


def test_cli_mode_fixture_really_is_adversarial() -> None:
    """Proof the control has teeth: the unrelated values ARE in the file."""
    source = MODE_FIXTURE[MODE_ARGS]
    assert '"fullscreen"' in source and '"regular"' in source
    facts = gen.MemberFacts(TS_REPO).of({MODE_ARGS: source})[MODE_ARGS]
    all_values = {c["value"] for c in facts["comparisons"] if c["left"] == "mode"}
    assert {"fullscreen", "regular"} <= all_values, all_values


def test_a_type_and_parser_disagreement_fails() -> None:
    """The type and what the parser accepts must agree, or the run fails."""
    diverged = dict(MODE_FIXTURE)
    diverged[MODE_ARGS] = MODE_FIXTURE[MODE_ARGS].replace(
        'export type Mode = "text" | "json" | "rpc";',
        'export type Mode = "text" | "json";')
    gen.errors.clear()
    assert gen.cli_mode_literals(FakeSource(diverged)) == []
    assert any("disagrees with the parser" in m for m in gen.errors), gen.errors
    gen.errors.clear()


def test_an_app_mode_referencing_another_type_fails() -> None:
    """If AppMode stops being a union of literals, membership is no longer here."""
    indirect = dict(MODE_FIXTURE)
    indirect[MODE_TRUST] = 'export type AppMode = "interactive" | SomeOtherModes;\n'
    gen.errors.clear()
    assert gen.app_modes(FakeSource(indirect)) is None
    assert any("references other types" in m for m in gen.errors), gen.errors
    gen.errors.clear()


HARNESS_PATH = "packages/agent/src/harness/tools/index.ts"

# Three real exports, and three things that must NOT become members:
#   - a phantom export inside a string literal
#   - a phantom export inside a template literal
#   - a non-exported internal alias
# The `as` alias is split across lines and indented with a tab, which the
# a pattern requiring the exact substring drops because it required the exact substring
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
    view = view_of(HARNESS_FIXTURE, HARNESS_PATH)
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
    view = view_of(RPC_FIXTURE[RPC_MODE], RPC_MODE)
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
    # A deletion must NOT count as a read; treating it as one files every cleared
    # name among the configuration inputs.
    "packages/coding-agent/src/child.ts": '''
const env = { ...getShellEnv() };
delete env.PI_EXPOSED_ONE;
delete env.PI_EXPOSED_TWO;
env.PI_EXPOSED_ONE = ctx.one;
env.PI_EXPOSED_TWO = ctx.two;
''',
}


class EnvFakeSource(FakeSource):
    """Kept as a distinct name so the environment fixtures read explicitly."""


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

    Counting a deletion as a read puts every cleared name among the inputs, where it
    is then described as something the product reads.
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


def test_a_parenthesised_seed_still_carries_the_obligation() -> None:
    """Parentheses are punctuation, not part of the value.

    Unwrapping them where identity is noted but not where the seed is classified
    produces an object with no record: the write still resolves to it, so the
    clear-before-set obligation disappears without a trace.
    """
    parens = dict(ENV_FIXTURE)
    parens["packages/coding-agent/src/paren.ts"] = (
        "const env = ({ ...getShellEnv() });\n"
        "env.PI_PAREN = value;\n")
    gen.errors.clear()
    gen.environment_names(EnvFakeSource(parens))
    assert any("PI_PAREN" in m for m in gen.errors), gen.errors
    gen.errors.clear()


def test_a_parenthesised_seed_can_still_be_guarded() -> None:
    guarded = dict(ENV_FIXTURE)
    guarded["packages/coding-agent/src/parenok.ts"] = (
        "const env = ({ ...getShellEnv() });\n"
        "delete env.PI_PAREN_OK;\n"
        "env.PI_PAREN_OK = value;\n")
    gen.errors.clear()
    roles = gen.environment_names(EnvFakeSource(guarded))
    assert not gen.errors, gen.errors
    assert "PI_PAREN_OK" in roles["exposed"], roles["exposed"]
    gen.errors.clear()


def test_a_parenthesised_alias_chain_is_followed() -> None:
    """`{ ...(base) }` where `base` holds an inherited environment still seeds."""
    chained = dict(ENV_FIXTURE)
    chained["packages/coding-agent/src/parenchain.ts"] = (
        "const base = (getShellEnv());\n"
        "const env = { ...(base) };\n"
        "env.PI_PAREN_CHAIN = value;\n")
    gen.errors.clear()
    gen.environment_names(EnvFakeSource(chained))
    assert any("PI_PAREN_CHAIN" in m for m in gen.errors), gen.errors
    gen.errors.clear()


def test_a_loop_binding_is_invisible_after_the_loop() -> None:
    """`for (let env = ...)` binds inside the loop only.

    If the enclosing block absorbs the header's binding, a write after the loop is
    attributed to the loop's fresh object and the outer object's obligation vanishes.
    """
    loops = dict(ENV_FIXTURE)
    loops["packages/coding-agent/src/afterloop.ts"] = (
        "function g() {\n"
        "  const env = { ...getShellEnv() };\n"
        "  for (let env = {}; cond; ) {}\n"
        "  env.PI_AFTER_LOOP = value;\n"
        "}\n")
    gen.errors.clear()
    gen.environment_names(EnvFakeSource(loops))
    # The write belongs to the OUTER seeded object, which was never cleared.
    assert any("PI_AFTER_LOOP" in m for m in gen.errors), gen.errors
    gen.errors.clear()


def test_a_loop_binding_is_invisible_before_the_loop() -> None:
    """The same at the other end: a write above the loop is not the loop's."""
    loops = dict(ENV_FIXTURE)
    loops["packages/coding-agent/src/beforeloop.ts"] = (
        "function h() {\n"
        "  const env = { ...getShellEnv() };\n"
        "  env.PI_BEFORE_LOOP = value;\n"
        "  for (let env = {}; cond; ) {}\n"
        "}\n")
    gen.errors.clear()
    gen.environment_names(EnvFakeSource(loops))
    assert any("PI_BEFORE_LOOP" in m for m in gen.errors), gen.errors
    gen.errors.clear()


def test_call_enclosure_does_not_leak_past_its_initializer() -> None:
    """Enclosure is a stack: a later call is not inside the last binding walked.

    A variable set on entering a top-level binding and never cleared makes every
    subsequent call in the file report that binding, so a family scoping by enclosure
    absorbs literals from unrelated code.
    """
    facts = _facts({"schema.ts": (
        'export const ThinkingLevelSchema = Type.Union([Type.Literal("off")]);\n'
        'function unrelated() { Type.Literal("phantom"); }\n')}, "schema.ts")
    by_value = {c["value"]: c["enclosing"] for c in facts["callLiterals"]}
    assert by_value["off"] == "ThinkingLevelSchema", by_value
    assert by_value["phantom"] is None, by_value


def test_call_literals_are_reported_once() -> None:
    """Walking an initializer twice reports every fact inside it twice."""
    facts = _facts({"once.ts": 'export const S = Type.Union([Type.Literal("a")]);\n'}, "once.ts")
    values = [c["value"] for c in facts["callLiterals"]]
    assert values == ["a"], values


def test_a_for_header_declaration_is_a_binding() -> None:
    """`for (let env = ...)` declares in the scope the loop opens.

    A declaration list appears bare in a `for` header, while elsewhere a statement
    wraps one. Matching only the statement form leaves the loop's binding
    unregistered, so writes inside resolve outward to whatever else is named `env`
    and an unrelated delete appears to guard them.
    """
    loops = dict(ENV_FIXTURE)
    loops["packages/coding-agent/src/forlet.ts"] = (
        "const env = { ...getShellEnv() };\n"
        "delete env.PI_LOOP;\n"
        "function f() {\n"
        "  for (let env = { ...getShellEnv() }; ; ) { env.PI_LOOP = value; }\n"
        "}\n")
    gen.errors.clear()
    gen.environment_names(EnvFakeSource(loops))
    # The loop's own object is seeded and never cleared, so the guard must fire --
    # attributing the write to the outer, cleared object would silence it.
    assert any("PI_LOOP" in m for m in gen.errors), gen.errors
    gen.errors.clear()


def test_a_for_of_binding_shadows_an_outer_object() -> None:
    """`for (const env of list)` binds `env` to an element, not to the outer object."""
    loops = dict(ENV_FIXTURE)
    loops["packages/coding-agent/src/forof.ts"] = (
        "const env = { ...getShellEnv() };\n"
        "delete env.PI_ELEMENT;\n"
        "function g(list) {\n"
        "  for (const env of list) { env.PI_ELEMENT = value; }\n"
        "}\n")
    gen.errors.clear()
    roles = gen.environment_names(EnvFakeSource(loops))
    # Unresolvable to an object literal, so counted without a claim -- and NOT
    # credited to the outer delete.
    assert not any("PI_ELEMENT" in m for m in gen.errors), gen.errors
    assert "PI_ELEMENT" in roles["exposed"], roles["exposed"]
    gen.errors.clear()


def test_a_for_var_binding_is_function_scoped() -> None:
    """`for (var env = ...)` is visible after the loop, like any other `var`."""
    loops = dict(ENV_FIXTURE)
    loops["packages/coding-agent/src/forvar.ts"] = (
        "function h() {\n"
        "  for (var env = { ...getShellEnv() }; ; ) { }\n"
        "  env.PI_AFTER_LOOP = value;\n"
        "}\n")
    gen.errors.clear()
    gen.environment_names(EnvFakeSource(loops))
    assert any("PI_AFTER_LOOP" in m for m in gen.errors), gen.errors
    gen.errors.clear()


def test_a_binding_shadows_its_whole_scope_not_just_below_itself() -> None:
    """`env.X` above `const env = ...` in the same scope refers to THAT binding.

    A lexical binding shadows its entire scope. Registering names as traversal
    reaches them attributes earlier references to an outer object, so the outer
    object's delete appears to guard them.
    """
    hoisted = dict(ENV_FIXTURE)
    hoisted["packages/coding-agent/src/hoisted.ts"] = (
        "const env = { ...getShellEnv() };\n"
        "delete env.PI_EARLY;\n"
        "function f() {\n"
        "  env.PI_EARLY = value;\n"
        "  const env = {};\n"
        "}\n")
    gen.errors.clear()
    roles = gen.environment_names(EnvFakeSource(hoisted))
    # The write belongs to the inner, unseeded object, so no obligation applies and
    # the OUTER delete must not be credited to it.
    assert not gen.errors, gen.errors
    assert "PI_EARLY" in roles["exposed"], roles["exposed"]
    gen.errors.clear()


def test_var_is_function_scoped_not_block_scoped() -> None:
    """A `var` declared in a block is visible after the block closes.

    Popping the block's scope loses it, and the later access becomes unresolved
    rather than being attributed to the object it actually names.
    """
    hoisted = dict(ENV_FIXTURE)
    hoisted["packages/coding-agent/src/varscope.ts"] = (
        "function g() {\n"
        "  { var env = { ...getShellEnv() }; }\n"
        "  env.PI_VAR = value;\n"
        "}\n")
    gen.errors.clear()
    gen.environment_names(EnvFakeSource(hoisted))
    # Seeded object, written without a prior delete on it: the guard must fire,
    # which it can only do if the access resolved to that object at all.
    assert any("PI_VAR" in m for m in gen.errors), gen.errors
    gen.errors.clear()


def test_a_block_scoped_binding_does_not_escape_its_block() -> None:
    """`let` in a block is NOT visible after it closes, unlike `var`.

    Collecting block-scoped names recursively leaks them into the enclosing
    function, which makes a later access resolve to an object it cannot name.
    """
    leaked = dict(ENV_FIXTURE)
    leaked["packages/coding-agent/src/letblock.ts"] = (
        "function h() {\n"
        "  { let env = { ...getShellEnv() }; }\n"
        "  env.PI_LET = value;\n"
        "}\n")
    gen.errors.clear()
    roles = gen.environment_names(EnvFakeSource(leaked))
    # Unresolvable, so counted without a claim -- and NOT attributed to the block's
    # seeded object, which would otherwise raise a spurious guard failure.
    assert not any("PI_LET" in m for m in gen.errors), gen.errors
    assert "PI_LET" in roles["exposed"], roles["exposed"]
    gen.errors.clear()


def test_a_nested_function_owns_its_own_var() -> None:
    """A `var` inside a nested function does not hoist into the outer one."""
    nested = dict(ENV_FIXTURE)
    nested["packages/coding-agent/src/nestedvar.ts"] = (
        "function outer() {\n"
        "  function inner() { var env = { ...getShellEnv() }; }\n"
        "  env.PI_NESTED = value;\n"
        "}\n")
    gen.errors.clear()
    gen.environment_names(EnvFakeSource(nested))
    assert not any("PI_NESTED" in m for m in gen.errors), gen.errors
    gen.errors.clear()


def test_a_delete_on_this_process_is_not_a_child_clear() -> None:
    """`delete process.env.X` clears OUR environment, not a child's.

    Counting it as a child clear would let it satisfy the clear-then-set obligation
    for a completely different object.
    """
    fixture = dict(ENV_FIXTURE)
    fixture["packages/coding-agent/src/selfdelete.ts"] = (
        "delete process.env.PI_SELF_CLEARED;\n"
        "function f() {\n"
        "  const env = { ...getShellEnv() };\n"
        "  env.PI_SELF_CLEARED = value;\n"
        "}\n")
    gen.errors.clear()
    roles = gen.environment_names(EnvFakeSource(fixture))
    # The self-delete must not appear as a child clear, and must not excuse the write.
    assert "PI_SELF_CLEARED" not in roles["cleared"], roles["cleared"]
    assert any("PI_SELF_CLEARED" in m for m in gen.errors), gen.errors
    gen.errors.clear()


def test_a_delete_on_this_process_is_not_an_input_read() -> None:
    """A deletion is not a read, on `process.env` as much as on a child object."""
    fixture = dict(ENV_FIXTURE)
    fixture["packages/coding-agent/src/selfdel2.ts"] = "delete process.env.PI_ONLY_DELETED;\n"
    roles = gen.environment_names(EnvFakeSource(fixture))
    assert "PI_ONLY_DELETED" not in roles["input"], roles["input"]


def test_a_spread_of_a_property_is_unknown_not_unseeded() -> None:
    """`{ ...execution.env }` may or may not carry an inherited environment.

    Reporting it as not seeded decides a question the resolver cannot answer, and
    the write then passes with no obligation.
    """
    fixture = dict(ENV_FIXTURE)
    fixture["packages/coding-agent/src/propspread.ts"] = (
        "function f() {\n"
        "  const env = { ...execution.env };\n"
        "  env.PI_FROM_PROPERTY = value;\n"
        "}\n")
    gen.errors.clear()
    gen.environment_names(EnvFakeSource(fixture))
    assert any("could not be resolved" in m for m in gen.errors), gen.errors
    assert any("PI_FROM_PROPERTY" in m for m in gen.errors), gen.errors
    gen.errors.clear()


def test_a_parameter_shadows_an_outer_environment_object() -> None:
    """A parameter named `env` is NOT the outer `env`.

    Without declaring parameters in the scope a function opens, every access in the
    body resolves outward, and an outer object's delete appears to guard a write to
    the parameter.
    """
    shadowed = dict(ENV_FIXTURE)
    shadowed["packages/coding-agent/src/shadow.ts"] = (
        "const env = { ...getShellEnv() };\n"
        "delete env.PI_OUTER;\n"
        "function inner(env) {\n"
        "  env.PI_OUTER = value;\n"
        "}\n")
    gen.errors.clear()
    roles = gen.environment_names(EnvFakeSource(shadowed))
    # The parameter cannot be resolved to an object literal, so nothing is claimed
    # about it -- but it must not be credited to the outer object's delete either.
    assert roles["unclaimed_accesses"] >= 1, roles
    assert "PI_OUTER" in roles["exposed"], roles["exposed"]
    gen.errors.clear()


def test_an_unresolved_seed_is_not_treated_as_exempt() -> None:
    """"Could not tell" must not pass as "no obligation".

    An object spread from something the resolver cannot follow has unknown
    provenance, so whether the clear-then-set obligation applies is unknown.
    """
    unknown = dict(ENV_FIXTURE)
    unknown["packages/coding-agent/src/unknown.ts"] = (
        "function f(base) {\n"
        "  const env = { ...base };\n"
        "  env.PI_UNKNOWN = value;\n"
        "}\n")
    gen.errors.clear()
    gen.environment_names(EnvFakeSource(unknown))
    assert any("could not be resolved" in m for m in gen.errors), gen.errors
    assert any("PI_UNKNOWN" in m for m in gen.errors), gen.errors
    gen.errors.clear()


def test_clear_then_set_needs_the_SAME_object_not_the_same_name() -> None:
    """Two functions may each declare a local `env`; they are different objects.

    Pairing by the receiver's text lets a delete in one vouch for a write in the
    other, which passes a guarantee that does not hold.
    """
    cross = dict(ENV_FIXTURE)
    cross["packages/coding-agent/src/scopes.ts"] = (
        "function overlay() {\n"
        "  const env = {};\n"
        "  delete env.PI_SHARED;\n"
        "}\n"
        "function final() {\n"
        "  const env = { ...getShellEnv() };\n"
        "  env.PI_SHARED = value;\n"
        "}\n")
    gen.errors.clear()
    gen.environment_names(EnvFakeSource(cross))
    assert any("PI_SHARED" in m for m in gen.errors), gen.errors
    gen.errors.clear()


def test_a_delete_on_the_same_object_does_guard() -> None:
    guarded = dict(ENV_FIXTURE)
    guarded["packages/coding-agent/src/guarded.ts"] = (
        "function f() {\n"
        "  const env = { ...getShellEnv() };\n"
        "  delete env.PI_OK;\n"
        "  env.PI_OK = value;\n"
        "}\n")
    gen.errors.clear()
    roles = gen.environment_names(EnvFakeSource(guarded))
    assert not gen.errors, gen.errors
    assert "PI_OK" in roles["exposed"] and "PI_OK" in roles["cleared"]
    gen.errors.clear()


def test_a_write_after_its_own_delete_is_ordered_correctly() -> None:
    """Order is per object: deleting AFTER the write protects nothing."""
    late = dict(ENV_FIXTURE)
    late["packages/coding-agent/src/late.ts"] = (
        "function f() {\n"
        "  const env = { ...getShellEnv() };\n"
        "  env.PI_LATE = value;\n"
        "  delete env.PI_LATE;\n"
        "}\n")
    gen.errors.clear()
    gen.environment_names(EnvFakeSource(late))
    assert any("PI_LATE" in m for m in gen.errors), gen.errors
    gen.errors.clear()


def test_an_unresolvable_receiver_is_counted_but_not_claimed_guarded() -> None:
    """`execution.env.X` is a property, so its host object cannot be identified.

    The name is still exposed, but no clear-then-set claim is made about it -- the
    difference between "no obligation" and "could not tell" has to stay visible.
    """
    opaque = dict(ENV_FIXTURE)
    opaque["packages/coding-agent/src/opaque.ts"] = (
        "function f(execution) {\n  execution.env.PI_OPAQUE = value;\n}\n")
    gen.errors.clear()
    roles = gen.environment_names(EnvFakeSource(opaque))
    assert not gen.errors, gen.errors
    assert "PI_OPAQUE" in roles["exposed"], roles["exposed"]
    gen.errors.clear()


def test_an_override_map_needs_no_clearing() -> None:
    """A fresh object merged OVER the inherited environment does not need deletes.

    Assignment already wins there. Demanding a delete produced a false alarm
    against a real site in the pinned source, so the rule is scoped by whether the
    file seeds a final map -- read from the file, not from a list of exemptions.
    """
    override = dict(ENV_FIXTURE)
    override["packages/coding-agent/src/overlay.ts"] = (
        "const execution = { env: {} };\nexecution.env.PI_OVERRIDE = ctx.one;\n")
    gen.errors.clear()
    roles = gen.environment_names(EnvFakeSource(override))
    assert roles is not None and not gen.errors, gen.errors
    assert "PI_OVERRIDE" in roles["exposed"], roles["exposed"]
    gen.errors.clear()


def test_a_written_name_outside_the_pi_namespace_is_still_counted() -> None:
    """Writes are not prefix-limited: the product also sets `AI_AGENT=pi`.

    A `PI_`-only scan missed it while the census prose listed it, so source and
    documentation could not close against each other.
    """
    fixture = dict(ENV_FIXTURE)
    fixture["packages/coding-agent/src/entry.ts"] = 'process.env.AI_AGENT = "pi";\n'
    roles = gen.environment_names(EnvFakeSource(fixture))
    assert "AI_AGENT" in roles["self"], roles["self"]


def test_environment_fixture_really_is_adversarial() -> None:
    """Each phantom is a COMPLETE pseudo-read, so it would match if scanned raw.

    A fixture holding only a bare name in a string would pass this control for
    the wrong reason: no read pattern could ever match a bare name.
    """
    source = ENV_FIXTURE["packages/coding-agent/src/reads.ts"]
    view = view_of(source, "reads.ts")
    raw = re.compile(r"(?:process\.)?env\.(PI_[A-Z0-9_]+)")
    on_raw = set(raw.findall(source))
    assert {"PI_FROM_A_COMMENT", "PI_FROM_A_STRING", "PI_FROM_A_TEMPLATE"} <= on_raw, on_raw
    on_structural = set(raw.findall(view.structural))
    assert "PI_FROM_A_COMMENT" not in on_structural
    assert "PI_FROM_A_STRING" not in on_structural
    assert "PI_FROM_A_TEMPLATE" not in on_structural


def test_seeding_through_an_alias_is_still_a_final_map() -> None:
    """`const inherited = getShellEnv(); const env = { ...inherited };` inherits.

    Matching only a direct spread of the call exempted this shape, so an unclear-ed
    write would have passed. Chains of aliases are covered by repeating the pass.
    """
    for body in (
        "const inherited = getShellEnv();\nconst env = { ...inherited };\nenv.PI_ALIASED = v;\n",
        "const a = getShellEnv();\nconst b = { ...a };\nconst env = { ...b };\nenv.PI_ALIASED = v;\n",
    ):
        fixture = dict(ENV_FIXTURE)
        fixture["packages/coding-agent/src/alias.ts"] = body
        gen.errors.clear()
        gen.environment_names(EnvFakeSource(fixture))
        assert any("PI_ALIASED" in m for m in gen.errors), (body, gen.errors)
        gen.errors.clear()


def test_a_fresh_object_is_not_promoted_by_an_alias_elsewhere() -> None:
    """The alias tracking must not turn every receiver in the file into a final map."""
    fixture = dict(ENV_FIXTURE)
    fixture["packages/coding-agent/src/mixed2.ts"] = (
        "const inherited = getShellEnv();\n"
        "const env = { ...inherited };\n"
        "delete env.PI_SEEDED;\n"
        "env.PI_SEEDED = a;\n"
        "const execution = { env: {} };\n"
        "execution.env.PI_FRESH = b;\n")
    gen.errors.clear()
    roles = gen.environment_names(EnvFakeSource(fixture))
    assert not gen.errors, gen.errors
    assert "PI_FRESH" in roles["exposed"] and "PI_SEEDED" in roles["exposed"]
    gen.errors.clear()


def test_offsets_survive_an_astral_character() -> None:
    """TypeScript reports UTF-16 code UNITS; Python indexes code POINTS.

    One emoji shifts every later span by one per astral character, which blanked
    string delimiters and moved every following span. The views stayed the right
    LENGTH, so nothing looked broken -- only the content was wrong.
    """
    source = 'const emoji = "\U0001F680\U0001F680";\nconst s = "export const createGhostTool = 1";\n'
    view = view_of(source)
    assert len(view.structural) == len(source)
    # Both string bodies blanked, both pairs of quotes intact.
    assert view.structural.count('"') == 4, view.structural
    assert "createGhostTool" not in view.structural, view.structural
    assert view.quoted_in(0, len(view.structural)) == ["\U0001F680\U0001F680",
                                                       "export const createGhostTool = 1"]


def test_astral_character_inside_a_template_keeps_expression_code() -> None:
    view = view_of("const t = `\U0001F680${ process.env.PI_AFTER }`;\n")
    assert "process.env.PI_AFTER" in view.structural, view.structural


def test_a_self_write_is_not_collected_as_a_child_write() -> None:
    """`process.env.X =` must not be read as filling a child's environment.

    The receiver pattern accepts a dotted path, so it can match `process.env`
    itself; the lookbehind cannot prevent that.
    """
    fixture = dict(ENV_FIXTURE)
    fixture["packages/coding-agent/src/selfonly.ts"] = 'process.env.PI_ONLY_SELF = "1";\n'
    roles = gen.environment_names(EnvFakeSource(fixture))
    assert "PI_ONLY_SELF" in roles["self"], roles["self"]
    assert "PI_ONLY_SELF" not in roles["exposed"], roles["exposed"]


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
    view = view_of(source, EXT_PATH)
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


ENTRY_PATH = "packages/coding-agent/src/core/session-manager.ts"

# Two member forms a naive pattern misses: a GENERIC member and one that EXTENDS a
# base. Requiring plain `interface X {` matches neither, and the count check then
# refuses to emit rather than publishing a short set.
ENTRY_FIXTURE = {
    ENTRY_PATH: "\n".join([
        "export type SessionEntry =",
        "\t| SessionMessageEntry",
        "\t| CompactionEntry",
        "\t| LabelEntry;",
        "",
        "export interface SessionMessageEntry extends SessionEntryBase {",
        '\ttype: "message";',
        "}",
        "",
        "export interface CompactionEntry<T = unknown> extends SessionEntryBase {",
        '\ttype: "compaction";',
        "\tdetails?: T;",
        "}",
        "",
        "export interface LabelEntry {",
        '\ttype: "label";',
        "}",
        "",
    ]),
}


def test_union_discriminants_handles_generic_and_extends_members() -> None:
    kinds = gen.union_discriminants(FakeSource(ENTRY_FIXTURE), ENTRY_PATH, "SessionEntry")
    assert kinds == ["message", "compaction", "label"], kinds


def test_union_discriminants_refuses_a_short_set() -> None:
    """A member whose interface cannot be found must fail, not shorten the set."""
    broken = {ENTRY_PATH: ENTRY_FIXTURE[ENTRY_PATH].replace(
        "export interface LabelEntry {", "interface LabelEntry {")}
    gen.errors.clear()
    result = gen.union_discriminants(FakeSource(broken), ENTRY_PATH, "SessionEntry")
    assert result is None, result
    assert any("LabelEntry" in message for message in gen.errors), gen.errors
    gen.errors.clear()


BARREL_PATH = "packages/tui/src/index.ts"

# All three export forms, an alias, a name that differs from another only in
# leading case, and a non-export that must not be collected.
BARREL_FIXTURE = {
    BARREL_PATH: "\n".join([
        'export { Box, type BoxOptions } from "./box.ts";',
        'export type { EditorComponent } from "./editor.ts";',
        'export { fuzzyMatch, type FuzzyMatch } from "./fuzzy.ts";',
        'export { internalName as publicName } from "./alias.ts";',
        # An interface exported through a plain clause: syntax says value, the
        # checker says type.
        "interface Foo {}",
        "export { Foo };",
        # A namespace's own exports are not this module's exports.
        "namespace N { export const Hidden = 1; }",
        "export const Visible = 2;",
        # A re-exported namespace belongs to neither declaration space.
        "export namespace Space { export const inner = 1; }",
        "",
    ]),
    "packages/tui/src/box.ts": "export class Box {}\nexport interface BoxOptions { x: number }\n",
    "packages/tui/src/editor.ts": "export interface EditorComponent { y: number }\n",
    "packages/tui/src/fuzzy.ts": "export function fuzzyMatch() {}\nexport type FuzzyMatch = string;\n",
    "packages/tui/src/alias.ts": "export const internalName = 1;\n",
}


def _facts(files: dict[str, str], wanted: str) -> dict:
    """Compiler-API facts for one fixture file, parsed with the others."""
    return gen.MemberFacts(TS_REPO).of(files)[wanted]


def test_export_facts_are_three_orthogonal_fields() -> None:
    """Surface, meanings and locality answer different questions.

    Compressed into one label, a class reads as a value only, an enum as a
    namespace, and a type-only alias to a dependency loses the surface the source
    states.
    """
    facts = _facts({"three.ts": ('export { Marked, type Token } from "marked";\n'
                                 "export class C {}\n"
                                 "export enum E { a }\n"),}, "three.ts")
    by_name = {e["name"]: e for e in facts["exports"]}
    assert by_name["Token"]["exportTypeOnly"] and by_name["Token"]["externalTarget"]
    assert by_name["Marked"]["exportTypeOnly"] is False
    assert by_name["C"]["meanings"] == ["value", "type"], by_name["C"]
    assert set(by_name["E"]["meanings"]) == {"value", "type", "namespace"}, by_name["E"]


def test_a_type_only_export_of_a_dependency_stays_a_type() -> None:
    """`export { type X } from "pkg"` states its surface regardless of the target.

    Filing it as undeterminable discards evidence the pinned source gives directly.
    """
    barrel = dict(BARREL_FIXTURE)
    barrel[BARREL_PATH] += 'export { Plain, type Marked } from "outside";\n'
    result = gen.tui_barrel_names(FakeSource(barrel))
    assert "Marked" in result["type"], result["type"]
    assert "Plain" in result["external"], result["external"]


def test_a_bare_case_clause_declaration_is_hoisted() -> None:
    """A `switch` body is the scope, but declarations sit inside its clauses.

    Without flattening them, `case 1: const env = ...` is never registered and a
    write in that clause resolves outward to an object that was cleared elsewhere.
    """
    switches = dict(ENV_FIXTURE)
    switches["packages/coding-agent/src/switch.ts"] = (
        "const env = { ...getShellEnv() };\n"
        "delete env.PI_CASE;\n"
        "function g(x) {\n"
        "  switch (x) {\n"
        "    case 1:\n"
        "      const env = { ...getShellEnv() };\n"
        "      env.PI_CASE = value;\n"
        "      break;\n"
        "  }\n"
        "}\n")
    gen.errors.clear()
    gen.environment_names(EnvFakeSource(switches))
    # The inner object is seeded and never cleared, so the guard must fire.
    assert any("PI_CASE" in m for m in gen.errors), gen.errors
    gen.errors.clear()


def test_a_name_occupies_every_space_its_symbol_carries() -> None:
    """A class is a value AND a type; both memberships are real.

    Filing each name once under a dominant space describes a bucketing of names,
    not the declaration spaces, and the losing meanings vanish silently.
    """
    result = gen.tui_barrel_names(FakeSource(dict(BARREL_FIXTURE)))
    assert "Box" in result["value"], result["value"]
    assert "Box" in result["type"], result["type"]


def _write_receiver(source: str, name: str) -> str:
    """Which object a write is attributed to: an object id, or "unresolved".

    Asserted on the FACT rather than on a role set, because a write bound to the
    wrong object and a write bound to none produce the same published roles and
    the same silence from the guard -- the two are only distinguishable here.
    """
    facts = gen.EnvFacts(TS_REPO).of({"scope.ts": source})["scope.ts"]
    for write in facts["writes"]:
        if write["name"] == name:
            return f"object {write['object']}"
    if any(entry["name"] == name for entry in facts["unresolved"]):
        return "unresolved"
    raise AssertionError(f"{name} was not reported at all: {facts}")


def test_a_destructured_var_hoists_like_any_other_var() -> None:
    """`var` reaches the whole function even when it destructures.

    Registered wherever the walk happened to be standing, the binding leaves a
    later write resolving OUTWARD to the file-level object -- which was cleared --
    so the write is reported as guarded by a delete that never touched it.
    """
    for inner in (
        "function f(o) {\n  { var { env } = o; }\n  env.PI_PATTERN = value;\n}\n",
        "function f(xs) {\n  for (var { env } of xs) {}\n  env.PI_PATTERN = value;\n}\n",
    ):
        source = ("const env = { ...getShellEnv() };\n"
                  "delete env.PI_PATTERN;\n" + inner)
        # The receiver is the inner binding, whose contents are unknowable -- not
        # the outer object, which is the only one the file declares.
        assert _write_receiver(source, "PI_PATTERN") == "unresolved", inner


def test_an_enum_declares_a_name_in_its_scope() -> None:
    """An enum binds its name like a class or a function does.

    Left unregistered, a reference to it resolves outward to whatever else holds
    that name, and the write is attributed to an object it never touched.
    """
    source = ("const env = { ...getShellEnv() };\n"
              "delete env.PI_ENUM;\n"
              "function g() {\n  enum env { a }\n  env.PI_ENUM = value;\n}\n")
    assert _write_receiver(source, "PI_ENUM") == "unresolved"


def test_a_namespace_keeps_its_own_space() -> None:
    """A namespace is not a value that happens to hold things.

    Folded into another space, `export namespace X` reads as an ordinary binding
    and the namespace space silently empties.
    """
    result = gen.tui_barrel_names(FakeSource(dict(BARREL_FIXTURE)))
    assert "Space" in result["namespace"], result


def test_a_parenthesised_seed_is_still_a_seeded_map() -> None:
    """Parentheses change grouping, not meaning.

    `({ ...getShellEnv() })` inherits the ambient environment exactly as the
    unwrapped form does; missing that reads it as a fresh override map, which owes
    no delete, so an unguarded write passes silently.
    """
    for source in (
        "const env = ({ ...getShellEnv() });\nenv.PI_PAREN = value;\n",
        "const env = { ...(getShellEnv()) };\nenv.PI_PAREN = value;\n",
    ):
        facts = gen.EnvFacts(TS_REPO).of({"paren.ts": source})["paren.ts"]
        # Asserted on `seeded` itself: an unclassifiable spread also raises an
        # error mentioning the name, so the error alone does not distinguish
        # "inherits the environment" from "could not tell".
        assert [object_["seeded"] for object_ in facts["objects"]] == [True], facts
        assert not any(object_["unresolvedSeed"] for object_ in facts["objects"]), facts


def test_a_failing_helper_is_fatal_not_an_empty_result() -> None:
    """A helper that exits non-zero has no answer; it does not have an empty one.

    Read as "nothing found", a crashed helper removes every member it would have
    reported and the census still exits green -- the loudest possible failure
    becomes the quietest.
    """
    facts = gen.MemberFacts(TS_REPO)
    try:
        facts.helper.of({"broken.ts": "export const x = (;\n"})
    except SystemExit as exit_:
        assert exit_.code == 2, exit_.code
    else:
        raise AssertionError("a helper that exits non-zero was treated as a result")


def test_a_namespace_star_export_names_its_dependency() -> None:
    """`export * as ns from "pkg"` sits one level below its clause, not two.

    Read at a fixed depth the module specifier is missed, and a name whose whole
    meaning lives in an unavailable dependency is published as a local unknown.
    """
    barrel = dict(BARREL_FIXTURE)
    barrel[BARREL_PATH] += 'export * as marked from "outside";\n'
    result = gen.tui_barrel_names(FakeSource(barrel))
    assert result is not None, gen.errors
    assert "marked" in result["external"], result


def test_a_star_export_that_cannot_be_enumerated_is_refused() -> None:
    """`export * from "pkg"` names nothing, so an unresolved one is invisible.

    The checker reports no exports for a module it cannot see, so the set comes
    back short by an unknown number of names with nothing marking the gap.
    """
    barrel = dict(BARREL_FIXTURE)
    barrel[BARREL_PATH] += 'export * from "outside";\n'
    gen.errors.clear()
    result = gen.tui_barrel_names(FakeSource(barrel))
    assert result is None, result
    assert any("outside" in m for m in gen.errors), gen.errors
    gen.errors.clear()


def test_a_relative_import_that_is_not_supplied_stays_unknown() -> None:
    """A file present in the CHECKOUT but not supplied must not resolve.

    This is the only shape that discriminates between a host that fails closed and
    one that reads the working tree: `./keys.ts` exists in the Pi checkout, so a host
    falling back to the file system answers from it. A bare specifier does not
    discriminate, because it is classified as external before resolution matters, and
    a path absent from disk resolves in neither host.
    """
    facts = _facts({"packages/tui/src/index.ts":
                    'export { type KeyId } from "./keys.ts";\n'},
                   "packages/tui/src/index.ts")
    by_name = {e["name"]: e for e in facts["exports"]}
    assert by_name["KeyId"]["meanings"] == [], by_name
    assert by_name["KeyId"]["externalTarget"] is False, by_name


def test_a_call_at_the_initializer_root_is_reported() -> None:
    """`export const D = Type.Literal("x")` has its only call at the root.

    Walking an initializer's children alone loses it entirely, which silently
    shortens any family whose members sit at the root of a binding.
    """
    facts = _facts({"root.ts": 'export const Direct = Type.Literal("direct");\n'}, "root.ts")
    values = {c["value"]: c["enclosing"] for c in facts["callLiterals"]}
    assert values == {"direct": "Direct"}, values


def test_a_nested_call_keeps_its_enclosure() -> None:
    facts = _facts({"nested.ts":
                    'export const Wrapped = Type.Union([Type.Literal("nested")]);\n'}, "nested.ts")
    values = {c["value"]: c["enclosing"] for c in facts["callLiterals"]}
    assert values == {"nested": "Wrapped"}, values


def test_every_call_literal_is_reported_exactly_once() -> None:
    """Visiting the root forced past the guard must not reintroduce double-walking."""
    facts = _facts({"once2.ts": ('export const Direct = Type.Literal("direct");\n'
                                 'export const Wrapped = Type.Union([Type.Literal("nested")]);\n'
                                 'function f() { Type.Literal("loose"); }\n')}, "once2.ts")
    values = [c["value"] for c in facts["callLiterals"]]
    assert sorted(values) == ["direct", "loose", "nested"], values


def test_facts_report_only_top_level_interfaces() -> None:
    """A nested declaration must not replace the module-level authority.

    Facts keyed by text name keep whichever occurrence is seen last, so an interface
    inside a function replaces the exported one that a family reads.
    """
    facts = _facts({"a.ts": "\n".join([
        "export interface Settings { real?: string }",
        "function helper() {",
        "\tinterface Settings { decoy?: string }",
        "\treturn null as unknown as Settings;",
        "}",
        "",
    ])}, "a.ts")
    assert facts["interfaceKeys"]["Settings"] == ["real"], facts["interfaceKeys"]


def test_facts_report_only_top_level_object_registries() -> None:
    """The same hazard for an object literal used as a registry."""
    facts = _facts({"b.ts": "\n".join([
        'export const TABLE = { "real.key": { x: 1 } };',
        "function decoy() {",
        '\tconst TABLE = { "decoy.key": { x: 2 } };',
        "\treturn TABLE;",
        "}",
        "",
    ])}, "b.ts")
    assert facts["objectKeys"]["TABLE"] == ["real.key"], facts["objectKeys"]


def test_facts_exclude_a_namespace_members_exports() -> None:
    """A namespace's exports are not the module's exports."""
    facts = _facts({"d.ts": "namespace N { export const Hidden = 1; }\nexport const Visible = 2;\n"}, "d.ts")
    assert [e["name"] for e in facts["exports"]] == ["Visible"]


def test_a_nested_interface_does_not_replace_the_exported_one() -> None:
    """Facts keyed by text name let a nested declaration win.

    A `Settings` interface inside a function has the same name as the exported one
    that this family reads, so keeping the last occurrence publishes the wrong keys.
    """
    shadowed = {
        "packages/coding-agent/src/core/settings-manager.ts": "\n".join([
            "export interface Settings {",
            "\treal?: string;",
            "}",
            "function helper() {",
            "\tinterface Settings {",
            "\t\tdecoy?: string;",
            "\t}",
            "\treturn null as unknown as Settings;",
            "}",
            "",
        ]),
    }
    assert gen.setting_keys(FakeSource(shadowed)) == ["real"]


def test_a_nested_registry_does_not_replace_the_exported_one() -> None:
    """The same hazard for an object literal: a nested one must not win."""
    shadowed = {
        "packages/tui/src/keybindings.ts": "\n".join([
            "export interface Keybindings {",
            '\t"tui.editor.cursorUp": true;',
            "}",
            "",
            "export const TUI_KEYBINDINGS = {",
            '\t"tui.editor.cursorUp": { defaultKeys: "up" },',
            "}",
            "",
            "function decoy() {",
            "\tconst TUI_KEYBINDINGS = {",
            '\t\t"tui.decoy.action": { defaultKeys: "x" },',
            "\t};",
            "\treturn TUI_KEYBINDINGS;",
            "}",
            "",
        ]),
    }
    assert gen.keybinding_actions(FakeSource(shadowed)) == ["tui.editor.cursorUp"]


def test_barrel_fails_on_an_export_it_cannot_classify() -> None:
    """An unresolvable alias must not be folded into a declaration space."""
    unresolvable = dict(BARREL_FIXTURE)
    unresolvable["packages/tui/src/index.ts"] += \
        'export { Missing } from "./nowhere.ts";\n'
    gen.errors.clear()
    assert gen.tui_barrel_names(FakeSource(unresolvable)) is None
    assert any("could not classify" in m for m in gen.errors), gen.errors
    gen.errors.clear()


KEYS_PATH = "packages/tui/src/keybindings.ts"


def test_keybinding_actions_require_the_two_authorities_to_agree() -> None:
    """The interface and the default table are two authorities in one file."""
    agreeing = {KEYS_PATH: "\n".join([
        "export interface Keybindings {",
        '\t"tui.editor.cursorUp": true;',
        '\t"tui.input.submit": true;',
        "}",
        "",
        "export const TUI_KEYBINDINGS = {",
        '\t"tui.editor.cursorUp": { defaultKeys: "up" },',
        '\t"tui.input.submit": { defaultKeys: [] },',
        "}",
        "",
    ])}
    actions = gen.keybinding_actions(FakeSource(agreeing))
    assert actions == ["tui.editor.cursorUp", "tui.input.submit"], actions

    diverged = {KEYS_PATH: agreeing[KEYS_PATH].replace(
        '\t"tui.input.submit": { defaultKeys: [] },', "")}
    gen.errors.clear()
    assert gen.keybinding_actions(FakeSource(diverged)) is None
    assert any("disagree" in m for m in gen.errors), gen.errors
    gen.errors.clear()


def test_an_unbound_action_is_still_a_member() -> None:
    """`defaultKeys: []` is a real action that ships without a binding.

    Counting bound keys instead of actions would drop it.
    """
    actions = gen.keybinding_actions(FakeSource({KEYS_PATH: "\n".join([
        "export interface Keybindings {",
        '\t"tui.editor.historyPrevious": true;',
        "}",
        "",
        "export const TUI_KEYBINDINGS = {",
        '\t"tui.editor.historyPrevious": { defaultKeys: [] },',
        "}",
        "",
    ])}))
    assert actions == ["tui.editor.historyPrevious"], actions


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
    view = view_of(source, AUTH_PATH)
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
    view = view_of(source)
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
    view = view_of(source)
    assert '"inner"' in view.extraction, "fixture is toothless"
    span = view.structural.index("["), len(view.structural)
    assert view.quoted_in(*span) == ["text", "json"]


def test_regex_literal_does_not_hide_following_code() -> None:
    """A regex containing quotes and parens must not swallow the code after it."""
    view = view_of('''
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
    view = view_of(source)
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
        except (Exception, SystemExit) as exc:
            # Anything else a test raises is also a failure, not a reason to stop.
            # Aborting leaves every later test unrun and prints no summary at all,
            # so a run whose helper crashed reports a test NAME where a count
            # belongs -- which reads as a broken harness rather than as a control
            # doing its job. KeyboardInterrupt still propagates.
            failures += 1
            print(f"FAIL {name}: {type(exc).__name__}: {exc}", file=sys.stderr)
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
