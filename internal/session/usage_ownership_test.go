package session_test

import (
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
