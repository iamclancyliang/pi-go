package session_test

import (
	"context"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/session"
)

func count(n int) *int { return &n }

// TestTheLedgerOwnsWhatItRecords: a caller that keeps editing the value it
// handed over, or edits what it reads back, must not be able to rewrite history.
func TestTheLedgerOwnsWhatItRecords(t *testing.T) {
	sess := session.New("system")
	recorded := ai.Usage{InputTokens: 10, CacheReadTokens: count(4), Reported: true}
	sess.RecordUsage(recorded)

	// Editing the value that was handed over.
	*recorded.CacheReadTokens = 999
	if got := sess.Attempts(); *got[0].CacheReadTokens != 4 {
		t.Fatalf("the caller rewrote a recorded entry: %d", *got[0].CacheReadTokens)
	}

	// Editing what was read back.
	view := sess.Attempts()
	*view[0].CacheReadTokens = 123
	if again := sess.Attempts(); *again[0].CacheReadTokens != 4 {
		t.Fatalf("a reader rewrote the ledger: %d", *again[0].CacheReadTokens)
	}
}

// TestOverflowKeepsEveryReportedField: the refused attempt was billed, and the
// ledger has to carry what the provider actually said — including the optional
// counts and the fact that it reported at all. Accumulating only the two
// obvious fields reports less than was used and then claims nothing was said.
func TestOverflowKeepsEveryReportedField(t *testing.T) {
	sess := session.New("system")
	if err := sess.RecordOverflowAttempt("refused", ai.Usage{
		InputTokens: 1000, OutputTokens: 3,
		CacheReadTokens: count(5), ReasoningTokens: count(2), Reported: true,
	}); err != nil {
		t.Fatal(err)
	}

	got := sess.OverflowUsage()
	if got.CacheReadTokens == nil || *got.CacheReadTokens != 5 {
		t.Fatalf("cache reads survived as %v", got.CacheReadTokens)
	}
	if got.ReasoningTokens == nil || *got.ReasoningTokens != 2 {
		t.Fatalf("reasoning survived as %v", got.ReasoningTokens)
	}
	if !got.Reported {
		t.Fatal("a recorded attempt claims the provider reported nothing")
	}
	if got.Total() != 1008 {
		t.Fatalf("total %d, want 1008 (1000 uncached + 5 cached + 3 output)", got.Total())
	}
}

// TestSeveralRefusedAttemptsAreEachRecorded: a call that retried before
// overflowing was billed for every attempt, and one entry loses the boundary.
func TestSeveralRefusedAttemptsAreEachRecorded(t *testing.T) {
	sess := session.New("system")
	for _, used := range []ai.Usage{
		{InputTokens: 70, Reported: true},
		{InputTokens: 30, Reported: true},
	} {
		if err := sess.RecordOverflowAttempt("refused", used); err != nil {
			t.Fatal(err)
		}
	}
	if n := sess.OverflowAttempts(); n != 2 {
		t.Fatalf("recorded %d refused attempts, want 2", n)
	}
	if got := sess.OverflowUsage().InputTokens; got != 100 {
		t.Fatalf("refused attempts total %d input tokens, want 100", got)
	}
	// Neither attempt reported the optional counts, so the sum must not claim
	// they were reported as zero.
	total := sess.OverflowUsage()
	if total.CacheReadTokens != nil || total.ReasoningTokens != nil {
		t.Fatalf("summing silences produced cache=%v reasoning=%v",
			total.CacheReadTokens, total.ReasoningTokens)
	}
}

// TestAStoredOverflowAttemptIsOwnedToo: the store keeps entries, so it has to
// own them. A caller that keeps editing the value it handed over would
// otherwise be editing what was recorded.
func TestAStoredOverflowAttemptIsOwnedToo(t *testing.T) {
	store := &session.MemoryStore{}
	sess := session.WithStore("system", store)

	handed := ai.Usage{InputTokens: 900, CacheReadTokens: count(7), Reported: true}
	if err := sess.RecordOverflowAttempt("refused", handed); err != nil {
		t.Fatal(err)
	}
	*handed.CacheReadTokens = 999

	reopened, err := session.Restore(context.Background(), "system", store)
	if err != nil {
		t.Fatal(err)
	}
	got := reopened.OverflowUsage()
	if got.CacheReadTokens == nil || *got.CacheReadTokens != 7 {
		t.Fatalf("the caller rewrote a persisted entry: %v", got.CacheReadTokens)
	}
}

// TestTheOverflowLedgerIsNotEditableByAReader: a reader that could edit what it
// read would be editing the ledger itself.
func TestTheOverflowLedgerIsNotEditableByAReader(t *testing.T) {
	sess := session.New("system")
	if err := sess.RecordOverflowAttempt("refused", ai.Usage{
		InputTokens: 10, CacheReadTokens: count(5), Reported: true,
	}); err != nil {
		t.Fatal(err)
	}

	view := sess.OverflowUsage()
	*view.CacheReadTokens = 999
	if again := sess.OverflowUsage(); *again.CacheReadTokens != 5 {
		t.Fatalf("a reader rewrote the overflow ledger: %d", *again.CacheReadTokens)
	}
}

// TestRestoringSpendOnlyEntriesDoesNotSpendTheBudget: the cost of a transport
// attempt is durable, but it was never a refused model call, so reopening must
// not charge recovery for it.
func TestRestoringSpendOnlyEntriesDoesNotSpendTheBudget(t *testing.T) {
	store := &session.MemoryStore{}
	sess := session.WithStore("system", store)

	if err := sess.RecordOverflowSpend(ai.Usage{InputTokens: 70, Reported: true}); err != nil {
		t.Fatal(err)
	}
	if err := sess.RecordOverflowAttempt("refused", ai.Usage{InputTokens: 1000, Reported: true}); err != nil {
		t.Fatal(err)
	}

	reopened, err := session.Restore(context.Background(), "system", store)
	if err != nil {
		t.Fatal(err)
	}
	if n := reopened.OverflowAttempts(); n != 1 {
		t.Fatalf("reopening charged %d units of recovery budget, want 1", n)
	}
	if got := reopened.OverflowUsage().InputTokens; got != 1070 {
		t.Fatalf("reopened ledger holds %d input tokens, want 1070", got)
	}
}
