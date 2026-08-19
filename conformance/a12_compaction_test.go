package conformance

import (
	"context"
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
