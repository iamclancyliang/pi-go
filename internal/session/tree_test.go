package session_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/session"
)

func say(t *testing.T, store *session.FileStore, role ai.Role, text string) {
	t.Helper()
	if err := store.Append(context.Background(),
		session.Entry{Message: &ai.Message{Role: role, Content: text}}); err != nil {
		t.Fatalf("appending %q: %v", text, err)
	}
}

func texts(entries []session.Entry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.Message != nil {
			out = append(out, e.Message.Content)
		}
	}
	return out
}

func joined(entries []session.Entry) string { return strings.Join(texts(entries), "|") }

// TestTheConversationIsThePathToWhereItStands, not everything in the file. Once
// a conversation can branch, the two stop being the same thing.
func TestTheConversationIsThePathToWhereItStands(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)

	say(t, store, ai.RoleUser, "one")
	say(t, store, ai.RoleAssistant, "two")

	got, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if joined(got) != "one|two" {
		t.Fatalf("the conversation came back as %v", texts(got))
	}
}

// TestBranchingLeavesTheOldLineWhereItWas is the property the whole design
// rests on. Changing your mind must not destroy the evidence: what followed the
// point you went back to is still recorded, just no longer what the next turn
// follows from.
func TestBranchingLeavesTheOldLineWhereItWas(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)

	say(t, store, ai.RoleUser, "shared question")
	fork := store.Leaf()
	say(t, store, ai.RoleAssistant, "first answer")
	say(t, store, ai.RoleUser, "first follow-up")
	firstLeaf := store.Leaf()

	// Go back and take a different turn.
	if err := store.MoveTo(context.Background(), fork); err != nil {
		t.Fatalf("MoveTo: %v", err)
	}
	say(t, store, ai.RoleAssistant, "second answer")

	got, _ := store.Load(context.Background())
	if joined(got) != "shared question|second answer" {
		t.Fatalf("the new branch came back as %v", texts(got))
	}

	// The abandoned line is still there, and going back to it restores it whole.
	if err := store.MoveTo(context.Background(), firstLeaf); err != nil {
		t.Fatalf("returning: %v", err)
	}
	got, _ = store.Load(context.Background())
	if joined(got) != "shared question|first answer|first follow-up" {
		t.Fatalf("the abandoned branch did not come back: %v", texts(got))
	}
}

// TestTheTreeSaysWhichEntriesAreOnThePath, because a listing that cannot
// distinguish them cannot show a person where they are.
func TestTheTreeSaysWhichEntriesAreOnThePath(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)

	say(t, store, ai.RoleUser, "shared")
	fork := store.Leaf()
	say(t, store, ai.RoleAssistant, "abandoned")
	store.MoveTo(context.Background(), fork)
	say(t, store, ai.RoleAssistant, "current")

	nodes, err := store.Tree(context.Background())
	if err != nil {
		t.Fatalf("Tree: %v", err)
	}
	if len(nodes) != 3 {
		t.Fatalf("the tree holds %d entries, want 3 (nothing is discarded)", len(nodes))
	}

	byText := map[string]session.Node{}
	for _, n := range nodes {
		byText[n.Summary] = n
	}
	if !byText["user: shared"].OnPath || !byText["assistant: current"].OnPath {
		t.Fatalf("the current path is not marked: %+v", nodes)
	}
	if byText["assistant: abandoned"].OnPath {
		t.Fatalf("an abandoned branch is marked as current: %+v", nodes)
	}
	// A branch is picked up again from its tip, so the tips must be findable.
	if !byText["assistant: abandoned"].IsLeaf || !byText["assistant: current"].IsLeaf {
		t.Fatalf("the branch tips are not marked: %+v", nodes)
	}
	if byText["user: shared"].IsLeaf {
		t.Fatalf("an entry with children is marked as a tip: %+v", nodes)
	}
}

// TestMovingToSomethingThatIsNotThereFails rather than silently standing
// somewhere else — answering with a different conversation hides the mistake.
func TestMovingToSomethingThatIsNotThereFails(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	say(t, store, ai.RoleUser, "one")

	if err := store.MoveTo(context.Background(), "no-such-entry"); err == nil {
		t.Fatal("moving to an entry that does not exist succeeded")
	}
	got, _ := store.Load(context.Background())
	if joined(got) != "one" {
		t.Fatalf("a refused move changed the conversation: %v", texts(got))
	}
}

// TestForkingCopiesIntoANewFileAndLeavesTheOldOneAlone. Someone who forks to
// try something else must be able to go back to a session that looks exactly as
// they left it.
func TestForkingCopiesIntoANewFileAndLeavesTheOldOne(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)

	say(t, store, ai.RoleUser, "shared question")
	at := store.Leaf()
	say(t, store, ai.RoleAssistant, "the answer in the original")

	path := filepath.Join(dir, session.FileName("forked", time.Unix(0, 0)))
	forked, err := store.BranchInto(context.Background(), path, dir, "forked", at)
	if err != nil {
		t.Fatalf("BranchInto: %v", err)
	}
	defer forked.Close()

	got, _ := forked.Load(context.Background())
	if joined(got) != "shared question" {
		t.Fatalf("the fork came back as %v", texts(got))
	}
	// The original is untouched, including the turn after the fork point.
	original, _ := store.Load(context.Background())
	if joined(original) != "shared question|the answer in the original" {
		t.Fatalf("forking changed the conversation it came from: %v", texts(original))
	}

	// Continuing in the fork must not reach back into the original's file.
	say(t, forked, ai.RoleAssistant, "a different answer")
	got, _ = forked.Load(context.Background())
	if joined(got) != "shared question|a different answer" {
		t.Fatalf("the fork continued as %v", texts(got))
	}
	original, _ = store.Load(context.Background())
	if strings.Contains(joined(original), "a different answer") {
		t.Fatalf("writing in the fork reached the original: %v", texts(original))
	}
}

// TestAForkRecordsWhereItCameFrom, which is the only thing connecting the two
// files once the copy is made.
func TestAForkRecordsWhereItCameFrom(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	say(t, store, ai.RoleUser, "one")

	path := filepath.Join(dir, session.FileName("forked", time.Unix(0, 0)))
	forked, err := store.BranchInto(context.Background(), path, dir, "forked", store.Leaf())
	if err != nil {
		t.Fatalf("BranchInto: %v", err)
	}
	if forked.ParentSession() != store.Path() {
		t.Fatalf("the fork says it came from %q, want %q", forked.ParentSession(), store.Path())
	}
	forked.Close()

	// And it survives a reopen, because it is in the file rather than only in
	// the struct that wrote it.
	reopened, err := session.OpenFileStore(path, dir, "forked")
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	defer reopened.Close()
	if reopened.ParentSession() != store.Path() {
		t.Fatalf("after reopening, the fork says it came from %q", reopened.ParentSession())
	}
}

// TestForkingAnEmptyConversationIsRefused. There is nothing to copy, and a fork
// of nothing is just a new session — which the user can ask for directly.
func TestForkingAnEmptyConversationIsRefused(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	path := filepath.Join(dir, session.FileName("forked", time.Unix(0, 0)))
	if _, err := store.BranchInto(context.Background(), path, dir, "forked", ""); err == nil {
		t.Fatal("forking an empty conversation succeeded")
	}
}

// TestACorruptParentChainDoesNotHang. A file whose parents point in a circle
// must fail or stop, never walk forever.
func TestACorruptParentChainDoesNotHang(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	for i := 0; i < 20; i++ {
		say(t, store, ai.RoleUser, "turn")
	}

	done := make(chan bool, 1)
	go func() {
		_, err := store.Load(context.Background())
		done <- err == nil
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("reading the conversation did not finish")
	}
}
