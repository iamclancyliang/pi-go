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

// TestOverflowAttemptIsDurableButNotProjected pins both halves at once.
//
// The attempt happened and was paid for, so it must survive for auditing. It must
// also never reach the provider again: sending it back resends the very thing
// that was rejected, and offering it to a summariser lets a provider error be
// written into the conversation as something that was said.
func TestOverflowAttemptIsDurableButNotProjected(t *testing.T) {
	store := &MemoryStore{}
	s := WithStore("You are pi-go.", store)

	must(t, s.Append(ai.Message{Role: ai.RoleUser, Content: "a long question"}))
	if err := s.RecordOverflowAttempt("context window exceeded", ai.Usage{InputTokens: 900, OutputTokens: 10}); err != nil {
		t.Fatalf("RecordOverflowAttempt: %v", err)
	}

	if got := s.OverflowAttempts(); got != 1 {
		t.Errorf("attempts = %d, want 1", got)
	}
	for _, m := range s.Project().Messages {
		if m.Content == "context window exceeded" {
			t.Error("the overflow attempt reached the projection")
		}
	}
	for _, m := range s.Truth() {
		if m.Content == "context window exceeded" {
			t.Error("the overflow attempt was written into the conversation")
		}
	}

	// It survives a restart: a process that died mid-recovery must not come back
	// believing it has a full budget and repeat work the user already paid for.
	reopened, err := Restore(context.Background(), "You are pi-go.", store)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got := reopened.OverflowAttempts(); got != 1 {
		t.Errorf("attempts after restart = %d, want 1: the budget was handed back", got)
	}
	// The refused call was billed, so its cost survives too. Dropping it
	// under-reports what the user actually paid.
	if got := reopened.OverflowUsage(); got.Total() != 910 {
		t.Errorf("usage after restart = %+v, want the refused call's 910 tokens", got)
	}
}

// TestNewUserInputStartsAFreshBudget pins the boundary the budget belongs to.
//
// The spent attempts were trying to answer a different question. Charging them
// against a new one leaves a conversation unable to recover from its first
// overflow, which is the recovery the budget exists to allow.
func TestNewUserInputStartsAFreshBudget(t *testing.T) {
	store := &MemoryStore{}
	s := WithStore("You are pi-go.", store)

	must(t, s.Append(ai.Message{Role: ai.RoleUser, Content: "first question"}))
	if err := s.RecordOverflowAttempt("too large", ai.Usage{InputTokens: 500}); err != nil {
		t.Fatalf("RecordOverflowAttempt: %v", err)
	}
	must(t, s.Append(ai.Message{Role: ai.RoleUser, Content: "second question"}))

	if got := s.OverflowAttempts(); got != 0 {
		t.Errorf("attempts after new input = %d, want 0", got)
	}
	reopened, err := Restore(context.Background(), "You are pi-go.", store)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got := reopened.OverflowAttempts(); got != 0 {
		t.Errorf("attempts after new input, reopened = %d, want 0: the rebuilt "+
			"budget disagrees with the live one", got)
	}
	// The budget resets; the ledger does not. That cost was really incurred.
	if got := s.OverflowUsage(); got.Total() != 500 {
		t.Errorf("usage after new input = %+v, want the earlier 500 tokens kept", got)
	}
}

// TestLoadedEntriesDoNotShareStorageWithTheStore pins that a store hands out
// copies.
//
// A caller that can reach into a returned entry and change what the store holds
// can rewrite what happened, which is the one capability an append-only record
// must not have. The checkpoint is where this is easiest to get wrong: copying the
// struct copies the summary but leaves the retained tail sharing the caller's
// slice, so the copy looks defensive and is not.
func TestLoadedEntriesDoNotShareStorageWithTheStore(t *testing.T) {
	store := &MemoryStore{}
	tail := []ai.Message{{Role: ai.RoleUser, Content: "KEPT"}}
	if err := store.Append(context.Background(), Entry{Checkpoint: &Checkpoint{
		Summary:      "SUMMARY",
		RetainedTail: tail,
	}}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// The caller mutates the slice it passed in, and the copy it was given back.
	tail[0].Content = "MUTATED-VIA-CALLER-SLICE"
	loaded, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	loaded[0].Checkpoint.RetainedTail[0].Content = "MUTATED-VIA-LOADED-ENTRY"
	loaded[0].Checkpoint.Summary = "MUTATED-SUMMARY"

	again, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := again[0].Checkpoint.RetainedTail[0].Content; got != "KEPT" {
		t.Errorf("retained tail in the store = %q, want %q: the record was rewritten "+
			"from outside, so what it reports is no longer what happened", got, "KEPT")
	}
	if got := again[0].Checkpoint.Summary; got != "SUMMARY" {
		t.Errorf("summary in the store = %q, want %q", got, "SUMMARY")
	}
}
