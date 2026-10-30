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
)

func user(text string) session.Entry {
	return session.Entry{Message: &ai.Message{Role: ai.RoleUser, Content: text}}
}

func assistant(text string) session.Entry {
	return session.Entry{Message: &ai.Message{Role: ai.RoleAssistant, Content: text}}
}

func ids(nodes []session.Node) []string {
	out := make([]string, len(nodes))
	for i, n := range nodes {
		out[i] = n.ID
	}
	return out
}

func TestALabelIsABookmarkThatTheModelNeverSees(t *testing.T) {
	ctx := context.Background()
	store := openStore(t, t.TempDir())
	if err := store.Append(ctx, user("q1"), assistant("a1")); err != nil {
		t.Fatal(err)
	}
	nodes, _ := store.Tree(ctx)
	target := nodes[0].ID
	if err := store.SetLabel(ctx, target, "the start"); err != nil {
		t.Fatal(err)
	}
	nodes, _ = store.Tree(ctx)
	if nodes[0].Label != "the start" || nodes[0].LabelAt.IsZero() {
		t.Fatalf("the label is not on the entry: %+v", nodes[0])
	}
	if len(nodes) != 3 || nodes[2].Kind != "label" || !nodes[2].OnPath {
		t.Fatalf("the label record is not on the line: %v", ids(nodes))
	}
	sess, err := session.Restore(ctx, "s", store)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(sess.Snapshot().Messages); n != 2 {
		t.Fatalf("the model would see %d messages", n)
	}
	if err := store.SetLabel(ctx, target, ""); err != nil {
		t.Fatal(err)
	}
	nodes, _ = store.Tree(ctx)
	if nodes[0].Label != "" {
		t.Fatalf("clearing left %q", nodes[0].Label)
	}
	if err := store.SetLabel(ctx, "no-such", "x"); err == nil {
		t.Fatal("labelling nothing succeeded")
	}
}

func TestLabelsSurviveAForkOnTheEntriesCopied(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := openStore(t, dir)
	if err := store.Append(ctx, user("q1"), assistant("a1"), user("q2")); err != nil {
		t.Fatal(err)
	}
	nodes, _ := store.Tree(ctx)
	if err := store.SetLabel(ctx, nodes[0].ID, "kept"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetLabel(ctx, nodes[2].ID, "dropped"); err != nil {
		t.Fatal(err)
	}
	forked, err := store.BranchInto(ctx, filepath.Join(dir, "fork.jsonl"), dir, "fork", nodes[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	defer forked.Close()
	copied, _ := forked.Tree(ctx)
	var labels []string
	for _, n := range copied {
		if n.Label != "" {
			labels = append(labels, n.Kind+":"+n.Label)
		}
	}
	if strings.Join(labels, " ") != "message:kept" {
		t.Fatalf("labels in the copy: %v (%v)", labels, ids(copied))
	}
	entries, _ := forked.Load(ctx)
	kinds := 0
	for _, e := range entries {
		if e.Label != nil {
			kinds++
		}
	}
	if kinds != 1 || len(entries) != 3 {
		t.Fatalf("the copy's line has %d entries and %d labels", len(entries), kinds)
	}
}

func TestResettingTheLeafStartsANewRootBesideTheOld(t *testing.T) {
	ctx := context.Background()
	store := openStore(t, t.TempDir())
	if err := store.Append(ctx, user("old"), assistant("answer")); err != nil {
		t.Fatal(err)
	}
	store.ResetLeaf()
	if loaded, _ := store.Load(ctx); len(loaded) != 0 {
		t.Fatalf("at the root the conversation has %d entries", len(loaded))
	}
	nodes, _ := store.Tree(ctx)
	for _, n := range nodes {
		if n.OnPath {
			t.Fatalf("%s is on the path at the root", n.ID)
		}
	}
	if err := store.Append(ctx, user("new")); err != nil {
		t.Fatal(err)
	}
	nodes, _ = store.Tree(ctx)
	if nodes[2].ParentID != "" || !nodes[2].OnPath || nodes[0].OnPath {
		t.Fatalf("the new entry is not a root of its own: %+v", nodes[2])
	}
}

func TestABranchSummaryIsAUserMessageAtTheNewPosition(t *testing.T) {
	ctx := context.Background()
	store := openStore(t, t.TempDir())
	if err := store.Append(ctx, user("q1"), assistant("a1"), user("q2"), assistant("a2")); err != nil {
		t.Fatal(err)
	}
	nodes, _ := store.Tree(ctx)
	left, common, err := store.LeftBehind(ctx, nodes[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if common != nodes[1].ID || strings.Join(ids(left), " ") != nodes[2].ID+" "+nodes[3].ID {
		t.Fatalf("left behind %v, common %s", ids(left), common)
	}
	if err := store.MoveTo(ctx, nodes[1].ID); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(ctx, session.Entry{BranchSummary: &session.BranchSummary{FromID: nodes[1].ID, Summary: "tried q2"}}); err != nil {
		t.Fatal(err)
	}
	sess, err := session.Restore(ctx, "s", store)
	if err != nil {
		t.Fatal(err)
	}
	messages := sess.Snapshot().Messages
	if len(messages) != 3 {
		t.Fatalf("the model would see %d messages", len(messages))
	}
	summary, ok := session.BranchSummaryOf(messages[2])
	if !ok || summary != "tried q2" || messages[2].Role != ai.RoleUser {
		t.Fatalf("the summary reaches the model as %+v", messages[2])
	}
	if nothing, _, _ := store.LeftBehind(ctx, nodes[0].ID); len(nothing) != 2 {
		// From the summary back to q1: a1 and the summary itself.
		t.Fatalf("left behind towards the root: %v", ids(nothing))
	}
}

func TestForkingBeforeAMessageCopiesUpToItsParent(t *testing.T) {
	ctx := context.Background()
	store := openStore(t, t.TempDir())
	if err := store.Append(ctx, user("q1"), assistant("a1"), user("q2")); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(ctx, session.Entry{Message: &ai.Message{Role: ai.RoleUser, BashExecution: &ai.BashExecution{Command: "ls"}}}); err != nil {
		t.Fatal(err)
	}
	messages, err := store.ForkMessages(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].Text != "q1" || messages[1].Text != "q2" {
		t.Fatalf("fork points %+v", messages)
	}
	leaf, text, err := store.Forked(ctx, messages[1].EntryID)
	if err != nil || text != "q2" {
		t.Fatalf("forked %q %v", text, err)
	}
	nodes, _ := store.Tree(ctx)
	if leaf != nodes[1].ID {
		t.Fatalf("the fork point is %s, not the reply before q2", leaf)
	}
	if first, _, _ := store.Forked(ctx, messages[0].EntryID); first != "" {
		t.Fatalf("forking before the first message copies up to %q", first)
	}
	if _, _, err := store.Forked(ctx, nodes[1].ID); err == nil {
		t.Fatal("forking before a reply succeeded")
	}
}

func TestListingAllSessionsAndRenamingOne(t *testing.T) {
	ctx := context.Background()
	agentDir := t.TempDir()
	work1, work2 := t.TempDir(), t.TempDir()
	write := func(work, id, text string, at time.Time) string {
		path := filepath.Join(session.DirFor(agentDir, work), session.FileName(id, at))
		store, err := session.OpenFileStore(path, work, id)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Append(ctx, user(text), assistant("ok")); err != nil {
			t.Fatal(err)
		}
		store.Close()
		return path
	}
	older := write(work1, "older", "first thing", time.Unix(100, 0))
	write(work2, "newer", "second thing", time.Unix(200, 0))

	all, err := session.ListAll(agentDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].ID != "newer" || all[1].ID != "older" {
		t.Fatalf("listed %v", all)
	}
	if all[1].MessageCount != 2 || all[1].AllText != "first thing ok" || all[1].Dir != work1 {
		t.Fatalf("described %+v", all[1])
	}
	if err := session.Rename(older, "the older one"); err != nil {
		t.Fatal(err)
	}
	info, err := session.Describe(older)
	if err != nil || info.Name != "the older one" {
		t.Fatalf("after renaming: %+v %v", info, err)
	}
	progress := 0
	if _, err := session.ListAllProgress(agentDir, func(loaded, total int) { progress = loaded }); err != nil || progress != 2 {
		t.Fatalf("progress reached %d, %v", progress, err)
	}
	if err := os.Remove(older); err != nil {
		t.Fatal(err)
	}
}
