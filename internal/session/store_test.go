package session

import (
	"context"
	"errors"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/ai"
)

// TestRestoreKeepsAnUnpairedToolCall is the property the store exists to hold.
//
// An abort leaves a tool call with no result, and that is a real outcome rather
// than damage. A store that dropped or repaired it on load would disagree with
// what happened, and a conversation that reads as consistent while having lost a
// turn is worse than one that shows the gap.
func TestRestoreKeepsAnUnpairedToolCall(t *testing.T) {
	store := &MemoryStore{}
	live := WithStore("You are pi-go.", store)

	must(t, live.Append(ai.Message{Role: ai.RoleUser, Content: "do two things"}))
	must(t, live.Append(ai.Message{
		Role: ai.RoleAssistant,
		ToolCalls: []ai.ToolCall{
			{ID: "call-1", Name: "read", Args: `{}`},
			{ID: "call-2", Name: "read", Args: `{}`},
		},
	}))
	// Only the first settles; the round was cut before the second.
	must(t, live.Append(ai.Message{Role: ai.RoleTool, ToolCallID: "call-1", Content: "done"}))

	before := live.UnmatchedToolCalls()
	if len(before) != 1 || before[0] != "call-2" {
		t.Fatalf("unmatched before restore = %v, want [call-2]", before)
	}

	restored, err := Restore(context.Background(), "You are pi-go.", store)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got := restored.UnmatchedToolCalls(); len(got) != 1 || got[0] != "call-2" {
		t.Errorf("unmatched after restore = %v, want [call-2]: the gap was repaired away", got)
	}
	if got, want := len(restored.Truth()), len(live.Truth()); got != want {
		t.Errorf("restored %d messages, want %d", got, want)
	}
	for i, m := range restored.Truth() {
		if m.ToolCallID != live.Truth()[i].ToolCallID || m.Content != live.Truth()[i].Content {
			t.Errorf("message %d differs after restore: %+v vs %+v", i, m, live.Truth()[i])
		}
	}
}

// TestAppendReportsAFailedWrite pins that a lost write is not silent.
//
// A history that quietly stops persisting looks exactly like a conversation that
// did not continue, and the difference only appears after a restart, when the
// missing part cannot be recovered.
func TestAppendReportsAFailedWrite(t *testing.T) {
	boom := errors.New("disk full")
	s := WithStore("You are pi-go.", &failingStore{err: boom})

	err := s.Append(ai.Message{Role: ai.RoleUser, Content: "hello"})
	if err == nil {
		t.Fatal("a failed durable write was reported as success")
	}
	if !errors.Is(err, boom) {
		t.Errorf("error = %v, want it to wrap the store's own failure", err)
	}
	// And the message did not enter memory: reporting it there while the store
	// rejected it would let a reader see something that will not survive.
	if got := s.Len(); got != 0 {
		t.Errorf("session holds %d messages after a failed write, want 0", got)
	}
}

// TestNoStoreStillWorks keeps the seam optional.
func TestNoStoreStillWorks(t *testing.T) {
	s := New("You are pi-go.")
	if err := s.Append(ai.Message{Role: ai.RoleUser, Content: "hello"}); err != nil {
		t.Fatalf("Append with no store: %v", err)
	}
	if got := s.Len(); got != 1 {
		t.Errorf("Len = %d, want 1", got)
	}
}

type failingStore struct{ err error }

func (f *failingStore) Append(context.Context, ...Entry) error { return f.err }
func (f *failingStore) Load(context.Context) ([]Entry, error) {
	return nil, f.err
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("append: %v", err)
	}
}

// TestAppendAllLeavesNoPartialWrite pins the unit the method promises.
//
// Writing the messages one at a time lets a failure part-way through leave the
// store holding a prefix the session rejected. Nothing looks wrong until a
// restart, when the conversation read back contains messages the live one never
// accepted — a divergence that a durable history exists to rule out.
func TestAppendAllLeavesNoPartialWrite(t *testing.T) {
	store := &failAfterFirst{}
	s := WithStore("You are pi-go.", store)

	err := s.AppendAll(
		ai.Message{Role: ai.RoleUser, Content: "first"},
		ai.Message{Role: ai.RoleAssistant, Content: "second"},
	)
	if err == nil {
		t.Fatal("a rejected batch was reported as success")
	}
	if got := s.Len(); got != 0 {
		t.Errorf("session holds %d messages after a rejected batch, want 0", got)
	}
	if got := len(store.entries); got != 0 {
		t.Errorf("store kept %d entries from a rejected batch, want 0: the live "+
			"session and a restart would read different conversations", got)
	}
}

// failAfterFirst accepts a single entry and rejects anything larger, which is how
// a store that cannot write a batch atomically behaves.
type failAfterFirst struct{ entries []Entry }

func (f *failAfterFirst) Append(_ context.Context, entries ...Entry) error {
	if len(entries) > 1 {
		return errors.New("store: cannot write this batch atomically")
	}
	f.entries = append(f.entries, entries...)
	return nil
}

func (f *failAfterFirst) Load(context.Context) ([]Entry, error) {
	return append([]Entry(nil), f.entries...), nil
}
