package cli_test

import (
	"strings"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/cli"
)

func TestBareArgumentsBecomePromptsInOrder(t *testing.T) {
	got := cli.ParseArgs([]string{"first thing", "second thing"})
	if len(got.Messages) != 2 || got.Messages[0] != "first thing" || got.Messages[1] != "second thing" {
		t.Fatalf("messages came back as %v", got.Messages)
	}
}

// TestPrintMayCarryItsPromptDirectly is how `pi -p "do a thing"` is written,
// and the exceptions are what keep it from swallowing the next flag.
func TestPrintMayCarryItsPromptDirectly(t *testing.T) {
	cases := map[string]struct {
		argv     []string
		messages []string
		files    []string
	}{
		"the prompt follows the flag": {
			argv:     []string{"-p", "do a thing"},
			messages: []string{"do a thing"},
		},
		"a flag is not a prompt": {
			argv:     []string{"-p", "--model", "m"},
			messages: nil,
		},
		"an @path is a file, not a prompt": {
			argv:     []string{"-p", "@notes.txt"},
			messages: nil,
			files:    []string{"notes.txt"},
		},
		"three dashes cannot be a flag, so it is text": {
			argv:     []string{"-p", "---"},
			messages: []string{"---"},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got := cli.ParseArgs(c.argv)
			if !got.Print {
				t.Fatal("--print was not set")
			}
			if strings.Join(got.Messages, "|") != strings.Join(c.messages, "|") {
				t.Fatalf("messages %v, want %v", got.Messages, c.messages)
			}
			if strings.Join(got.FileArgs, "|") != strings.Join(c.files, "|") {
				t.Fatalf("files %v, want %v", got.FileArgs, c.files)
			}
		})
	}
}

// TestAnUnacceptedModeValueIsNotSilentlyDropped.
//
// Pi ignores it and decides from the terminal instead, which this keeps —
// but silently doing so leaves `--mode interactive` looking like it worked when
// it named the one mode the flag rejects. The value is refused AND said aloud.
func TestAnUnacceptedModeValueIsNotSilentlyDropped(t *testing.T) {
	got := cli.ParseArgs([]string{"--mode", "interactive"})
	if got.Mode != "" {
		t.Fatalf("an unaccepted --mode value was kept as %q", got.Mode)
	}
	if len(got.Diagnostics) == 0 {
		t.Fatal("an unaccepted --mode value produced no diagnostic")
	}
	if !strings.Contains(got.Diagnostics[0].Message, "text, json or rpc") {
		t.Fatalf("the diagnostic does not say what is accepted: %q", got.Diagnostics[0].Message)
	}
}

func TestTheAcceptedModeValues(t *testing.T) {
	for _, value := range []string{"text", "json", "rpc"} {
		got := cli.ParseArgs([]string{"--mode", value})
		if string(got.Mode) != value {
			t.Fatalf("--mode %s parsed as %q", value, got.Mode)
		}
		if len(got.Diagnostics) != 0 {
			t.Fatalf("--mode %s produced %v", value, got.Diagnostics)
		}
	}
}

// TestAPiFlagThisBuildLacksIsSaidAloud. Letting it fall through as unknown
// would leave a user believing the flag took effect, which is the one thing a
// parser must never do.
func TestAPiFlagThisBuildLacksIsSaidAloud(t *testing.T) {
	got := cli.ParseArgs([]string{"--skill", "./s"})
	if len(got.Diagnostics) != 1 || !got.Diagnostics[0].Warning {
		t.Fatalf("--skill produced %v", got.Diagnostics)
	}
	if !strings.Contains(got.Diagnostics[0].Message, "does not implement") {
		t.Fatalf("the diagnostic does not say why: %q", got.Diagnostics[0].Message)
	}
	if _, kept := got.Unknown["skill"]; kept {
		t.Fatal("a known-but-unimplemented flag was also kept as unknown")
	}
	if got.Failed() {
		t.Fatal("an unimplemented flag was treated as a fatal error")
	}
}

// TestAnUnknownShortOptionIsAnError, while an unknown long one is kept for an
// extension to claim. That asymmetry is Pi's, and it exists because extensions
// declare long flags only.
func TestAnUnknownShortOptionIsAnError(t *testing.T) {
	got := cli.ParseArgs([]string{"-z"})
	if !got.Failed() {
		t.Fatalf("-z was not an error: %v", got.Diagnostics)
	}

	long := cli.ParseArgs([]string{"--custom-thing", "value"})
	if long.Failed() {
		t.Fatalf("--custom-thing was treated as an error: %v", long.Diagnostics)
	}
	if long.Unknown["custom-thing"] != "value" {
		t.Fatalf("unknown flags came back as %v", long.Unknown)
	}
}

func TestUnknownFlagsTakeAValueEitherWay(t *testing.T) {
	got := cli.ParseArgs([]string{"--a=1", "--b", "2", "--c"})
	if got.Unknown["a"] != "1" || got.Unknown["b"] != "2" {
		t.Fatalf("valued unknown flags came back as %v", got.Unknown)
	}
	if v, present := got.Unknown["c"]; !present || v != "" {
		t.Fatalf("a bare unknown flag came back as %q, present=%v", v, present)
	}
}

func TestTheFlagsThisBuildActsOn(t *testing.T) {
	got := cli.ParseArgs([]string{
		"--provider", "deepseek", "--model", "deepseek-chat",
		"--api-key", "k", "--system-prompt", "be brief", "--no-tools", "hello",
	})
	if got.Provider != "deepseek" || got.Model != "deepseek-chat" ||
		got.APIKey != "k" || got.SystemPrompt != "be brief" || !got.NoTools {
		t.Fatalf("parsed as %+v", got)
	}
	if len(got.Messages) != 1 || got.Messages[0] != "hello" {
		t.Fatalf("messages came back as %v", got.Messages)
	}
}

// TestResumeTakesAnOptionalName. Bare --resume asks for the same thing
// --continue does; a following flag is a flag, not a session name.
func TestResumeTakesAnOptionalName(t *testing.T) {
	named := cli.ParseArgs([]string{"--resume", "0198-abc"})
	if named.Resume != "0198-abc" || named.Continue {
		t.Fatalf("a named resume parsed as %+v", named)
	}

	bare := cli.ParseArgs([]string{"--resume"})
	if bare.Resume != "" || !bare.Continue {
		t.Fatalf("a bare resume parsed as %+v", bare)
	}

	followed := cli.ParseArgs([]string{"-r", "--no-tools"})
	if followed.Resume != "" || !followed.Continue || !followed.NoTools {
		t.Fatalf("--resume swallowed the flag after it: %+v", followed)
	}
}

func TestTheSessionFlags(t *testing.T) {
	got := cli.ParseArgs([]string{"-c", "--no-session", "--session-dir", "/tmp/s"})
	if !got.Continue || !got.NoSession || got.SessionDir != "/tmp/s" {
		t.Fatalf("parsed as %+v", got)
	}
	if len(got.Diagnostics) != 0 {
		t.Fatalf("implemented flags warned: %v", got.Diagnostics)
	}
}

func TestModelsFlagKeepsItsPatternsForCycleResolution(t *testing.T) {
	got := cli.ParseArgs([]string{"--models", "ollama/alpha, *beta ,ollama/qwen2.5:7b"})
	if got.Failed() {
		t.Fatalf("--models produced a diagnostic: %v", got.Diagnostics)
	}
	want := []string{"ollama/alpha", "*beta", "ollama/qwen2.5:7b"}
	if strings.Join(got.Models, "|") != strings.Join(want, "|") {
		t.Fatalf("model scope parsed as %v, want %v", got.Models, want)
	}
	if _, ok := got.Unknown["models"]; ok {
		t.Fatalf("--models was also recorded as unknown: %v", got.Unknown)
	}
}

func TestModelsFlagKeepsEmptyEntriesForLaterDiagnostics(t *testing.T) {
	got := cli.ParseArgs([]string{"--models", "alpha,,beta"})
	if strings.Join(got.Models, "|") != "alpha||beta" {
		t.Fatalf("empty model pattern disappeared: %v", got.Models)
	}
}

// TestAnUnportedSwitchLeavesThePromptAlone. It takes no value, so the word after
// it is the question being asked.
func TestAnUnportedSwitchLeavesThePromptAlone(t *testing.T) {
	got := cli.ParseArgs([]string{"--no-skills", "hello"})
	if len(got.Messages) != 1 || got.Messages[0] != "hello" {
		t.Fatalf("the prompt came back as %v", got.Messages)
	}
	short := cli.ParseArgs([]string{"-ns", "-e", "./ext.ts", "hello"})
	if short.Failed() || len(short.Messages) != 1 || short.Messages[0] != "hello" {
		t.Fatalf("short spellings parsed as %+v", short)
	}
}

func TestTheToolFlags(t *testing.T) {
	got := cli.ParseArgs([]string{"-t", "read, grep,", "-xt", "bash", "-nbt", "-nt", "-nc"})
	if strings.Join(got.Tools, " ") != "read grep" || strings.Join(got.ExcludeTools, " ") != "bash" {
		t.Fatalf("lists parsed as %q and %q", got.Tools, got.ExcludeTools)
	}
	if !got.NoBuiltinTools || !got.NoTools || !got.NoContextFiles || got.Failed() {
		t.Fatalf("switches parsed as %+v", got)
	}
	if empty := cli.ParseArgs([]string{"--tools", ""}); empty.Tools == nil {
		t.Fatal("a list naming nothing came back as no list at all")
	}
}

func TestTheConversationFlags(t *testing.T) {
	got := cli.ParseArgs([]string{"-n", "auth", "--session", "a", "--session-id", "b", "--fork", "c", "--export", "d"})
	if got.Name != "auth" || !got.NameGiven || got.Session != "a" || got.SessionID != "b" || got.Fork != "c" || got.Export != "d" {
		t.Fatalf("parsed as %+v", got)
	}
	if !cli.ParseArgs([]string{"--name"}).Failed() {
		t.Fatal("--name without a value was accepted")
	}
}

func TestListModelsTakesAnOptionalPattern(t *testing.T) {
	bare := cli.ParseArgs([]string{"--list-models", "--offline"})
	if !bare.ListModels || bare.ListModelsPattern != "" || !bare.Offline {
		t.Fatalf("parsed as %+v", bare)
	}
	searched := cli.ParseArgs([]string{"--list-models", "chat"})
	if !searched.ListModels || searched.ListModelsPattern != "chat" || len(searched.Messages) != 0 {
		t.Fatalf("parsed as %+v", searched)
	}
}

func TestTheApproveFlagsOverrideTrustEitherWay(t *testing.T) {
	if got := cli.ParseArgs([]string{"-a"}); got.ProjectTrust == nil || !*got.ProjectTrust {
		t.Fatalf("-a parsed as %v", got.ProjectTrust)
	}
	if got := cli.ParseArgs([]string{"--no-approve"}); got.ProjectTrust == nil || *got.ProjectTrust {
		t.Fatalf("--no-approve parsed as %v", got.ProjectTrust)
	}
	if got := cli.ParseArgs(nil); got.ProjectTrust != nil {
		t.Fatalf("no flag parsed as %v", *got.ProjectTrust)
	}
}
