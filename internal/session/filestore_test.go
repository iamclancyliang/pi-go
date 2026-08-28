package session_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/session"
	"github.com/iamclancyliang/pi-go/internal/tools"
)

func openStore(t *testing.T, dir string) *session.FileStore {
	t.Helper()
	path := filepath.Join(dir, session.FileName("s1", time.Unix(0, 0)))
	store, err := session.OpenFileStore(path, dir, "s1")
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// TestAConversationOutlivesTheProcessThatWroteIt is the whole point: the file is
// closed and reopened, which is what a restart does.
func TestAConversationOutlivesTheProcessThatWroteIt(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	path := store.Path()

	want := []session.Entry{
		{Message: &ai.Message{Role: ai.RoleUser, Content: "a question"}},
		{Message: &ai.Message{Role: ai.RoleAssistant, Content: "an answer"}},
	}
	if err := store.Append(context.Background(), want...); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := session.OpenFileStore(path, dir, "s1")
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	defer reopened.Close()

	got, err := reopened.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("two entries were written, %d came back", len(got))
	}
	if got[0].Message.Content != "a question" || got[1].Message.Content != "an answer" {
		t.Fatalf("the conversation came back as %+v", got)
	}
	if got[0].Message.Role != ai.RoleUser || got[1].Message.Role != ai.RoleAssistant {
		t.Fatalf("who said what was lost: %+v", got)
	}
}

// TestEveryKindOfEntrySurvives. A store that keeps only messages loses the tool
// calls and the compaction, and the conversation read back is one that never
// happened.
func TestEveryKindOfEntrySurvives(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)

	cached := 3
	written := []session.Entry{
		{Message: &ai.Message{Role: ai.RoleUser, Content: "hello"}},
		{Checkpoint: &session.Checkpoint{
			Summary:      "what came before",
			RetainedTail: []ai.Message{{Role: ai.RoleUser, Content: "kept"}},
		}},
		{Overflow: &session.OverflowAttempt{
			SpendOnly: true,
			Detail:    "a transport attempt",
			Usage:     ai.Usage{InputTokens: 5, CacheReadTokens: &cached, Reported: true},
		}},
		{Intent: &session.ToolIntent{
			OperationID: "op-1", CallID: "call-1", ResultID: "res-1",
			Tool: "read", ToolVersion: "v1", Args: `{"path":"a"}`,
			Replay: tools.ReplaySafe,
		}},
		{Settlement: &session.ToolSettlement{
			CallID: "call-1", ResultID: "res-1", Result: "contents",
			Interrupted: true, Terminate: true,
		}},
		{Failure: &session.OperationFailure{Code: "refused", Detail: "the provider said no"}},
	}
	if err := store.Append(context.Background(), written...); err != nil {
		t.Fatalf("Append: %v", err)
	}

	got, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != len(written) {
		t.Fatalf("%d entries written, %d came back", len(written), len(got))
	}

	if got[1].Checkpoint.Summary != "what came before" ||
		len(got[1].Checkpoint.RetainedTail) != 1 ||
		got[1].Checkpoint.RetainedTail[0].Content != "kept" {
		t.Fatalf("the checkpoint came back as %+v", got[1].Checkpoint)
	}
	// A checkpoint whose retained tail was lost reads as a conversation that
	// forgot its recent turns.
	if !got[2].Overflow.SpendOnly || got[2].Overflow.Usage.CacheReadTokens == nil ||
		*got[2].Overflow.Usage.CacheReadTokens != 3 {
		t.Fatalf("the overflow record came back as %+v", got[2].Overflow)
	}
	// The replay policy is what recovery compares against, so it must come back
	// as the policy that was recorded, not as whatever the enum's zero is.
	if got[3].Intent.Replay != tools.ReplaySafe {
		t.Fatalf("a safe-to-repeat intent came back as %v", got[3].Intent.Replay)
	}
	if got[3].Intent.ResultID != "res-1" || got[3].Intent.Args != `{"path":"a"}` {
		t.Fatalf("the intent came back as %+v", got[3].Intent)
	}
	if !got[4].Settlement.Interrupted || !got[4].Settlement.Terminate {
		t.Fatalf("the settlement came back as %+v", got[4].Settlement)
	}
	if got[5].Failure.Code != "refused" {
		t.Fatalf("the failure came back as %+v", got[5].Failure)
	}
}

// TestAReplayPolicyIsStoredAsAWord, not as the number the enum happens to use.
// Renumbering the enum would otherwise turn every stored "0" into a different
// policy, and a call recorded as unsafe to repeat would quietly become safe.
func TestAReplayPolicyIsStoredAsAWord(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	if err := store.Append(context.Background(), session.Entry{Intent: &session.ToolIntent{
		CallID: "c", ResultID: "r", Tool: "write", Replay: tools.ReplayNever,
	}}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	raw, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatalf("reading the file: %v", err)
	}
	if !strings.Contains(string(raw), `"replay":"never"`) {
		t.Fatalf("the policy was not written as a word:\n%s", raw)
	}
}

// TestAppendingIsAllOrNone. A torn write leaves a half-line, and the failure
// then arrives at the START of the next session rather than the end of this
// one — a conversation that cannot be read at all.
func TestAppendingIsAllOrNone(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)

	if err := store.Append(context.Background(),
		session.Entry{Message: &ai.Message{Role: ai.RoleUser, Content: "kept"}}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	before, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatalf("reading: %v", err)
	}

	// The second entry has nothing in it, so the batch cannot be encoded. None
	// of it may reach the file, including the entry before it that was fine.
	err = store.Append(context.Background(),
		session.Entry{Message: &ai.Message{Role: ai.RoleUser, Content: "should not land"}},
		session.Entry{})
	if err == nil {
		t.Fatal("a batch holding an empty entry was accepted")
	}

	after, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("a rejected batch changed the file:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// TestAFileFromAnotherVersionIsRefusedRatherThanMisread.
func TestAFileFromAnotherVersionIsRefusedRatherThanMisread(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "old.jsonl")
	if err := os.WriteFile(path,
		[]byte(`{"kind":"session","version":99,"id":"x","timestamp":"","dir":""}`+"\n"), 0o600); err != nil {
		t.Fatalf("writing: %v", err)
	}
	if _, err := session.OpenFileStore(path, dir, "x"); err == nil {
		t.Fatal("a file from another version was opened")
	}
}

// TestAnUnknownRecordIsRefusedRatherThanSkipped. Reading past one hands the
// model a conversation that never happened — and the record skipped may be the
// tool call that changed a file.
func TestAnUnknownRecordIsRefusedRatherThanSkipped(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	path := store.Path()
	if err := store.Append(context.Background(),
		session.Entry{Message: &ai.Message{Role: ai.RoleUser, Content: "one"}}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	store.Close()

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	f.WriteString(`{"kind":"from_the_future","id":"z","timestamp":""}` + "\n")
	f.Close()

	reopened, err := session.OpenFileStore(path, dir, "s1")
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	defer reopened.Close()
	if _, err := reopened.Load(context.Background()); err == nil {
		t.Fatal("an unknown record was read past")
	}
}

// TestReopeningContinuesOneConversation rather than starting a second inside
// the same file.
func TestReopeningContinuesOneConversation(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	path := store.Path()
	id := store.ID()
	store.Append(context.Background(), session.Entry{Message: &ai.Message{Role: ai.RoleUser, Content: "first"}})
	store.Close()

	reopened, err := session.OpenFileStore(path, dir, "a-different-id")
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	defer reopened.Close()
	if reopened.ID() != id {
		t.Fatalf("reopening changed the session id from %q to %q", id, reopened.ID())
	}
	reopened.Append(context.Background(), session.Entry{Message: &ai.Message{Role: ai.RoleUser, Content: "second"}})

	got, _ := reopened.Load(context.Background())
	if len(got) != 2 {
		t.Fatalf("continuing produced %d entries, want 2", len(got))
	}

	raw, _ := os.ReadFile(path)
	if n := strings.Count(string(raw), `"kind":"session"`); n != 1 {
		t.Fatalf("the file holds %d headers, want 1:\n%s", n, raw)
	}
}
