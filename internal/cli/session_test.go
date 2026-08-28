package cli_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/cli"
)

// converse runs one print-mode exchange in workingDir and returns what the
// conversation held afterwards.
func converse(t *testing.T, args cli.Args, workingDir, prompt string) (*cli.Conversation, int, string) {
	t.Helper()
	conversation, err := cli.OpenConversation(args, workingDir, cli.DefaultSystemPrompt)
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	t.Cleanup(func() { conversation.Close() })

	rt := runtimeFor(t, scripted("an answer"))
	rt.Conversation = conversation

	var out, errOut bytes.Buffer
	code := cli.RunPrint(context.Background(), rt,
		cli.Streams{In: strings.NewReader(""), Out: &out, Err: &errOut}, []string{prompt})
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	return conversation, code, out.String()
}

// TestAConversationIsRecordedWithoutBeingAskedFor. Recording only when asked
// cannot work: the asking happens on the NEXT run, after the conversation worth
// keeping has already gone.
func TestAConversationIsRecordedWithoutBeingAskedFor(t *testing.T) {
	work := t.TempDir()
	args := cli.Args{SessionDir: t.TempDir()}

	first, _, _ := converse(t, args, work, "remember this")
	if first.Path == "" {
		t.Fatal("a run with no session flags recorded nothing")
	}
	if first.Resumed {
		t.Fatal("a fresh run reported itself as resumed")
	}
	first.Close()

	// A second run asking to continue must find it.
	resumed, err := cli.OpenConversation(
		cli.Args{SessionDir: args.SessionDir, Continue: true}, work, cli.DefaultSystemPrompt)
	if err != nil {
		t.Fatalf("continuing: %v", err)
	}
	defer resumed.Close()
	if !resumed.Resumed {
		t.Fatal("a continued run did not report itself as resumed")
	}

	var found bool
	for _, m := range resumed.Session.Snapshot().Messages {
		if m.Role == ai.RoleUser && strings.Contains(m.Content, "remember this") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the earlier conversation did not come back: %+v", resumed.Session.Snapshot().Messages)
	}
}

// TestNoSessionLeavesNothingBehind, which is what a scripted caller wants and
// what a throwaway question deserves.
func TestNoSessionLeavesNothingBehind(t *testing.T) {
	work := t.TempDir()
	dir := t.TempDir()
	conversation, _, _ := converse(t, cli.Args{NoSession: true, SessionDir: dir}, work, "throwaway")

	if conversation.Path != "" {
		t.Fatalf("--no-session recorded to %s", conversation.Path)
	}
	if _, err := cli.OpenConversation(
		cli.Args{SessionDir: dir, Continue: true}, work, cli.DefaultSystemPrompt); err == nil {
		t.Fatal("a --no-session run left something to continue")
	}
}

// TestContinuingWithNothingToContinueSaysSo. A user handed a blank conversation
// after asking to continue will assume the history was lost, not that there was
// none.
func TestContinuingWithNothingToContinueSaysSo(t *testing.T) {
	_, err := cli.OpenConversation(
		cli.Args{SessionDir: t.TempDir(), Continue: true}, t.TempDir(), cli.DefaultSystemPrompt)
	if err == nil {
		t.Fatal("continuing an empty directory succeeded")
	}
	if !strings.Contains(err.Error(), "no conversation to continue") {
		t.Fatalf("the failure does not say what happened: %v", err)
	}
}

// TestResumeMatchesAnIdByItsPrefix, because that is what a person types from a
// listing.
func TestResumeMatchesAnIdByItsPrefix(t *testing.T) {
	work := t.TempDir()
	dir := t.TempDir()
	first, _, _ := converse(t, cli.Args{SessionDir: dir}, work, "the one to find")
	id := first.ID
	if id == "" {
		t.Fatal("a recorded conversation has no id, so nothing can ask for it")
	}
	// The id a listing shows must be the one the file is named with, or
	// --resume takes a name that finds nothing.
	if !strings.Contains(first.Path, id) {
		t.Fatalf("the file %s is not named for the conversation id %s", first.Path, id)
	}
	first.Close()

	resumed, err := cli.OpenConversation(
		cli.Args{SessionDir: dir, Resume: id[:8]}, work, cli.DefaultSystemPrompt)
	if err != nil {
		t.Fatalf("resuming by prefix: %v", err)
	}
	defer resumed.Close()
	if !resumed.Resumed || resumed.Path != first.Path {
		t.Fatalf("resuming by prefix opened %s, want %s", resumed.Path, first.Path)
	}
}

// TestAnUnknownSessionIsRefusedRatherThanStartedFresh.
func TestAnUnknownSessionIsRefusedRatherThanStartedFresh(t *testing.T) {
	_, err := cli.OpenConversation(
		cli.Args{SessionDir: t.TempDir(), Resume: "nothing-like-this"},
		t.TempDir(), cli.DefaultSystemPrompt)
	if err == nil {
		t.Fatal("resuming a session that does not exist succeeded")
	}
	if !strings.Contains(err.Error(), "starts with") {
		t.Fatalf("the failure does not say what was looked for: %v", err)
	}
}

// TestSessionsAreKeptApartByDirectory: --continue in one project must not
// reopen another project's work.
func TestSessionsAreKeptApartByDirectory(t *testing.T) {
	dir := t.TempDir()
	here, there := t.TempDir(), t.TempDir()

	conversation, _, _ := converse(t, cli.Args{SessionDir: dir}, there, "the other project")
	conversation.Close()

	if _, err := cli.OpenConversation(
		cli.Args{SessionDir: dir, Continue: true}, here, cli.DefaultSystemPrompt); err == nil {
		t.Fatal("--continue reached into another directory's conversation")
	}
}
