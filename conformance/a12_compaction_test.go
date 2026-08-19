package conformance

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/session"
)

// TestA12CompactionProjectsSummaryAndTail pins that a checkpoint is
// self-contained.
//
// Durable history and the model's context are different data. Compaction changes
// only the second: what happened stays exactly where it was, and the model is
// shown a summary plus the messages kept after it.
//
// The checkpoint must carry its own tail. One that merely pointed at a range
// would make every projection depend on the history it was meant to leave behind,
// so a long conversation would keep paying for entries it had already summarised.
func TestA12CompactionProjectsSummaryAndTail(t *testing.T) {
	store := &session.MemoryStore{}
	live := session.WithStore("You are pi-go.", store)

	old1 := ai.Message{Role: ai.RoleUser, Content: "OLD-ONE"}
	old2 := ai.Message{Role: ai.RoleAssistant, Content: "OLD-TWO"}
	recent1 := ai.Message{Role: ai.RoleUser, Content: "RECENT-ONE"}
	recent2 := ai.Message{Role: ai.RoleAssistant, Content: "RECENT-TWO"}
	for _, m := range []ai.Message{old1, old2, recent1, recent2} {
		if err := live.Append(m); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	if err := live.Compact("SUMMARY-OF-OLD", []ai.Message{recent1, recent2}); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	after := ai.Message{Role: ai.RoleUser, Content: "AFTER"}
	if err := live.Append(after); err != nil {
		t.Fatalf("Append after compaction: %v", err)
	}

	// Rebuilt from the store alone, so the projection cannot be relying on
	// anything the live session happened to keep in memory.
	restored, err := session.Restore(context.Background(), "You are pi-go.", store)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}

	for _, s := range []*session.Session{live, restored} {
		got := contentsOf(s.Project().Messages)
		want := []string{"You are pi-go.", "SUMMARY-OF-OLD", "RECENT-ONE", "RECENT-TWO", "AFTER"}
		if !equal(got, want) {
			t.Errorf("projection = %v, want %v", got, want)
		}
		// The entries the summary replaced are not in the context at all. This
		// is the property the whole checkpoint exists for.
		for _, replaced := range []string{"OLD-ONE", "OLD-TWO"} {
			for _, line := range got {
				if strings.Contains(line, replaced) {
					t.Errorf("projection still carries %q, which the summary replaced", replaced)
				}
			}
		}
	}

	// History is untouched: compaction changed what the model is shown, not what
	// happened. Losing the originals would make the record disagree with the
	// conversation that took place.
	full := contentsOf(live.Truth())
	for _, kept := range []string{"OLD-ONE", "OLD-TWO", "RECENT-ONE", "RECENT-TWO", "AFTER"} {
		found := false
		for _, line := range full {
			if line == kept {
				found = true
			}
		}
		if !found {
			t.Errorf("history lost %q after compaction", kept)
		}
	}
}

func contentsOf(msgs []ai.Message) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.Content)
	}
	return out
}

// TestA12CompactionSurvivesACrashEitherSide pins that publication is all-or-none
// as seen by whoever reopens the conversation.
//
// A crash may land before the checkpoint is durable or after. Both are fine; what
// must never exist is a state between them — a summary whose retained tail is
// missing, or a conversation that lost its old context before the new one was
// safe. Either half alone silently shortens the conversation, and the loss is
// only discoverable by comparing against a record that no longer exists.
func TestA12CompactionSurvivesACrashEitherSide(t *testing.T) {
	t.Run("crash before publication keeps the previous context", func(t *testing.T) {
		store := &crashingStore{failOn: 5} // the checkpoint is the 5th write
		live := session.WithStore("You are pi-go.", store)
		seed(t, live)

		err := live.Compact("SUMMARY-OF-OLD", []ai.Message{
			{Role: ai.RoleUser, Content: "RECENT-ONE"},
			{Role: ai.RoleAssistant, Content: "RECENT-TWO"},
		})
		if err == nil {
			t.Fatal("a checkpoint that was never recorded was reported as published")
		}

		// The LIVE session must not have applied it either. Applying a
		// checkpoint whose write failed makes the running conversation shorter
		// than the recorded one, and the two only get compared after a restart
		// — by which point the context the model was actually given is gone.
		liveAfter := contentsOf(live.Project().Messages)
		wantLive := []string{"You are pi-go.", "OLD-ONE", "OLD-TWO", "RECENT-ONE", "RECENT-TWO"}
		if !equal(liveAfter, wantLive) {
			t.Errorf("live projection after a failed publication = %v, want it "+
				"unchanged %v", liveAfter, wantLive)
		}

		reopened, err := session.Restore(context.Background(), "You are pi-go.", store)
		if err != nil {
			t.Fatalf("Restore: %v", err)
		}
		got := contentsOf(reopened.Project().Messages)
		want := []string{"You are pi-go.", "OLD-ONE", "OLD-TWO", "RECENT-ONE", "RECENT-TWO"}
		if !equal(got, want) {
			t.Errorf("projection after a crash before publication = %v, want the "+
				"complete previous context %v", got, want)
		}
		for _, line := range got {
			if line == "SUMMARY-OF-OLD" {
				t.Error("a summary survived a crash that happened before it was recorded")
			}
		}
	})

	t.Run("crash after publication keeps the whole checkpoint", func(t *testing.T) {
		store := &crashingStore{failOn: 6} // everything through the checkpoint lands
		live := session.WithStore("You are pi-go.", store)
		seed(t, live)

		if err := live.Compact("SUMMARY-OF-OLD", []ai.Message{
			{Role: ai.RoleUser, Content: "RECENT-ONE"},
			{Role: ai.RoleAssistant, Content: "RECENT-TWO"},
		}); err != nil {
			t.Fatalf("Compact: %v", err)
		}
		// The next write is where the process dies.
		if err := live.Append(ai.Message{Role: ai.RoleUser, Content: "AFTER"}); err == nil {
			t.Fatal("the injected crash did not happen")
		}

		reopened, err := session.Restore(context.Background(), "You are pi-go.", store)
		if err != nil {
			t.Fatalf("Restore: %v", err)
		}
		got := contentsOf(reopened.Project().Messages)
		want := []string{"You are pi-go.", "SUMMARY-OF-OLD", "RECENT-ONE", "RECENT-TWO"}
		if !equal(got, want) {
			t.Errorf("projection after a crash following publication = %v, want the "+
				"summary with its complete tail %v", got, want)
		}
		// The message the crash interrupted is absent, and absent from history
		// too: it was never recorded, so claiming it happened would be worse
		// than losing it.
		for _, line := range contentsOf(reopened.Truth()) {
			if line == "AFTER" {
				t.Error("history contains a message whose write never completed")
			}
		}
	})
}

func seed(t *testing.T, s *session.Session) {
	t.Helper()
	for _, m := range []ai.Message{
		{Role: ai.RoleUser, Content: "OLD-ONE"},
		{Role: ai.RoleAssistant, Content: "OLD-TWO"},
		{Role: ai.RoleUser, Content: "RECENT-ONE"},
		{Role: ai.RoleAssistant, Content: "RECENT-TWO"},
	} {
		if err := s.Append(m); err != nil {
			t.Fatalf("seeding: %v", err)
		}
	}
}

// crashingStore stops accepting writes at a chosen point, which is what a process
// death looks like to everything upstream of the disk.
type crashingStore struct {
	entries []session.Entry
	writes  int
	failOn  int
}

func (c *crashingStore) Append(_ context.Context, entries ...session.Entry) error {
	c.writes++
	if c.writes >= c.failOn {
		return errors.New("store: the process died before this write landed")
	}
	c.entries = append(c.entries, entries...)
	return nil
}

func (c *crashingStore) Load(context.Context) ([]session.Entry, error) {
	return append([]session.Entry(nil), c.entries...), nil
}
