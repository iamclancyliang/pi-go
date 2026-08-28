package cli_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/cli"
)

// session drives the interactive loop over a script of typed lines.
func interactive(t *testing.T, args cli.Args, workingDir string, typed ...string) (string, string) {
	t.Helper()
	conversation, err := cli.OpenConversation(args, workingDir, cli.DefaultSystemPrompt)
	if err != nil {
		t.Fatalf("opening: %v", err)
	}

	rt := runtimeFor(t, scripted("an answer"))
	rt.Conversation = conversation
	rt.Args = args
	rt.WorkingDir = workingDir

	var out, errOut bytes.Buffer
	cli.RunInteractive(context.Background(), rt, cli.Streams{
		In:  strings.NewReader(strings.Join(typed, "\n") + "\n"),
		Out: &out, Err: &errOut,
	})
	return out.String(), errOut.String()
}

// TestACommandIsNotSentToTheModel. Anything typed with a leading slash acts on
// the session, and forwarding it would spend a request on text meant for the
// program.
func TestACommandIsNotSentToTheModel(t *testing.T) {
	model := scripted("an answer")
	conversation, err := cli.OpenConversation(cli.Args{NoSession: true}, t.TempDir(), cli.DefaultSystemPrompt)
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	defer conversation.Close()

	rt := runtimeFor(t, model)
	rt.Conversation = conversation
	rt.WorkingDir = t.TempDir()

	var out, errOut bytes.Buffer
	cli.RunInteractive(context.Background(), rt, cli.Streams{
		In:  strings.NewReader("/help\n/session\n"),
		Out: &out, Err: &errOut,
	})

	if n := len(model.Requests()); n != 0 {
		t.Fatalf("commands produced %d model requests", n)
	}
}

// TestHelpListsWhatThereIsAndWhatThereIsNot. Showing only what works leaves a
// user to discover each gap by typing into it.
func TestHelpListsWhatThereIsAndWhatThereIsNot(t *testing.T) {
	out, _ := interactive(t, cli.Args{NoSession: true}, t.TempDir(), "/help")

	for _, want := range []string{"/help", "/quit", "/session", "/new", "/resume", "/export"} {
		if !strings.Contains(out, want) {
			t.Fatalf("help does not mention %s:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "not here yet:") || !strings.Contains(out, "/fork") {
		t.Fatalf("help does not say what is missing:\n%s", out)
	}
}

// TestAPiCommandThisBuildLacksSaysWhy, rather than reporting it as a typo the
// user did not make.
func TestAPiCommandThisBuildLacksSaysWhy(t *testing.T) {
	_, errOut := interactive(t, cli.Args{NoSession: true}, t.TempDir(), "/fork")

	if !strings.Contains(errOut, "does not have") {
		t.Fatalf("a known Pi command was not recognised: %q", errOut)
	}
	if strings.Contains(errOut, "unknown command") {
		t.Fatalf("a real Pi command was reported as a typo: %q", errOut)
	}
}

// TestAnActualTypoIsReportedAsOne, and points at where to look.
func TestAnActualTypoIsReportedAsOne(t *testing.T) {
	_, errOut := interactive(t, cli.Args{NoSession: true}, t.TempDir(), "/sesion")
	if !strings.Contains(errOut, "unknown command") || !strings.Contains(errOut, "/help") {
		t.Fatalf("a typo was reported as %q", errOut)
	}
}

// TestSessionInfoDistinguishesUnreportedUsageFromZero. A provider that said
// nothing has not said the session was free.
func TestSessionInfoDistinguishesUnreportedUsageFromZero(t *testing.T) {
	out, _ := interactive(t, cli.Args{NoSession: true}, t.TempDir(), "/session")
	if !strings.Contains(out, "not reported") {
		t.Fatalf("usage nobody reported was shown as a number:\n%s", out)
	}
	if !strings.Contains(out, "recorded   no") {
		t.Fatalf("a --no-session run did not say it was keeping nothing:\n%s", out)
	}
}

// TestNewStartsASeparateConversation, and the next turn lands in it rather than
// in the one the user left.
func TestNewStartsASeparateConversation(t *testing.T) {
	dir, work := t.TempDir(), t.TempDir()
	out, errOut := interactive(t, cli.Args{SessionDir: dir}, work,
		"first question", "/new", "second question")

	if strings.Contains(errOut, "could not") {
		t.Fatalf("starting a new conversation failed: %q", errOut)
	}
	if !strings.Contains(out, "started ") {
		t.Fatalf("/new did not report a new conversation:\n%s", out)
	}

	all, err := listSessions(dir, work)
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("/new left %d conversations, want 2", len(all))
	}
	// One turn each: the second question must not have landed in the first.
	for _, info := range all {
		if info.Entries != 2 {
			t.Fatalf("a conversation holds %d entries, want 2 (one exchange): %+v", info.Entries, info)
		}
	}
}

// TestExportWritesSomethingAPersonReads, not a copy of the record: an export is
// for reading, and the session file is a format for this program to reopen.
func TestExportWritesSomethingAPersonReads(t *testing.T) {
	work := t.TempDir()
	target := filepath.Join(t.TempDir(), "conversation.md")

	out, errOut := interactive(t, cli.Args{NoSession: true}, work,
		"a question worth keeping", "/export "+target)
	if strings.Contains(errOut, "could not") {
		t.Fatalf("export failed: %q", errOut)
	}
	if !strings.Contains(out, "exported to") {
		t.Fatalf("/export did not report where it went:\n%s", out)
	}

	raw, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("reading the export: %v", err)
	}
	body := string(raw)
	if !strings.Contains(body, "a question worth keeping") || !strings.Contains(body, "an answer") {
		t.Fatalf("the export lost the conversation:\n%s", body)
	}
	if strings.Contains(body, `"kind":`) {
		t.Fatalf("the export is the record rather than something to read:\n%s", body)
	}
}

// TestExportWithNoPathSaysSo rather than writing somewhere the user did not
// choose.
func TestExportWithNoPathSaysSo(t *testing.T) {
	_, errOut := interactive(t, cli.Args{NoSession: true}, t.TempDir(), "/export")
	if !strings.Contains(errOut, "needs a path") {
		t.Fatalf("/export with no path said %q", errOut)
	}
}

// TestQuitEndsTheSessionWithoutReadingOn.
func TestQuitEndsTheSessionWithoutReadingOn(t *testing.T) {
	model := scripted("an answer")
	conversation, _ := cli.OpenConversation(cli.Args{NoSession: true}, t.TempDir(), cli.DefaultSystemPrompt)
	defer conversation.Close()

	rt := runtimeFor(t, model)
	rt.Conversation = conversation
	rt.WorkingDir = t.TempDir()

	var out, errOut bytes.Buffer
	cli.RunInteractive(context.Background(), rt, cli.Streams{
		In:  strings.NewReader("/quit\nnever asked\n"),
		Out: &out, Err: &errOut,
	})
	if n := len(model.Requests()); n != 0 {
		t.Fatalf("input after /quit was still sent: %d requests", n)
	}
}
