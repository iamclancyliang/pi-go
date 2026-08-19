package session

import (
	"context"

	"github.com/iamclancyliang/pi-go/internal/ai"
)

// Store is where conversation history outlives the process that produced it.
//
// It is append-only by design. A conversation is a sequence of things that
// happened, and editing what happened is not a capability worth having: a store
// that can rewrite history can also silently repair it, and the repair is
// indistinguishable from the original. The one state that most invites repair —
// a tool call with no result, left by an abort — is a real outcome that anything
// reading the history afterwards has to tolerate.
//
// Writes report failure. A history that quietly stops persisting looks exactly
// like a conversation that did not continue, and the difference only becomes
// visible after a restart, when the missing part cannot be recovered.
type Store interface {
	// Append records entries as having happened, ALL OR NONE.
	//
	// A partial write is the one outcome a caller cannot recover from: it leaves
	// the store holding entries the session does not have, so the conversation
	// read back after a restart is not the conversation that ran.
	Append(ctx context.Context, entries ...Entry) error

	// Load returns every recorded entry, in the order they were appended.
	Load(ctx context.Context) ([]Entry, error)
}

// Entry is one thing that happened in a conversation.
//
// Exactly one field is set. A checkpoint is an entry rather than a rewrite of
// earlier ones, because compaction does not change what happened — it changes
// what the model is shown next.
type Entry struct {
	Message    *ai.Message
	Checkpoint *Checkpoint
	Overflow   *OverflowAttempt
}

// OverflowAttempt records that the provider refused a request because the context
// was too large.
//
// It is durable and it is NOT part of the conversation. Both halves matter: the
// attempt happened, was paid for, and has to be auditable — and feeding it back
// would resend the very thing that was rejected, or invite a summariser to
// describe a provider error as something the conversation said.
type OverflowAttempt struct {
	// Detail is what the provider reported, kept for auditing rather than for
	// the model to read.
	Detail string

	// Usage is what the refused call still cost. A rejected request is billed
	// like any other, so dropping it under-reports what the user paid.
	Usage ai.Usage
}

// Checkpoint is a compaction: a summary of what came before it, and the messages
// kept verbatim after it.
//
// It is SELF-CONTAINED. Rebuilding the model's context reads the checkpoint and
// what follows, never the entries the summary replaced. A checkpoint that only
// pointed at a range would make every projection depend on history that is meant
// to be behind it, so the cost of a long conversation would never actually fall.
type Checkpoint struct {
	// Summary stands in for everything before this checkpoint.
	Summary string

	// RetainedTail is kept verbatim, in order. It is stored WITH the summary:
	// a summary whose tail lives elsewhere can be read without it, and that
	// reads as a conversation that lost its recent turns.
	RetainedTail []ai.Message
}

// MemoryStore keeps history in memory. It is durable for the life of the
// process and no longer, which is what a test wants and what a product does not.
type MemoryStore struct {
	entries []Entry
	reads   int
}

// Append implements Store.
func (m *MemoryStore) Append(_ context.Context, entries ...Entry) error {
	staged := make([]Entry, 0, len(entries))
	for _, e := range entries {
		staged = append(staged, cloneEntry(e))
	}
	m.entries = append(m.entries, staged...)
	return nil
}

// Load implements Store.
func (m *MemoryStore) Load(_ context.Context) ([]Entry, error) {
	out := make([]Entry, 0, len(m.entries))
	for _, e := range m.entries {
		out = append(out, cloneEntry(e))
	}
	return out, nil
}

func cloneEntry(e Entry) Entry {
	if e.Message != nil {
		cloned := cloneMessages([]ai.Message{*e.Message})[0]
		return Entry{Message: &cloned}
	}
	if e.Checkpoint != nil {
		cp := *e.Checkpoint
		cp.RetainedTail = cloneMessages(e.Checkpoint.RetainedTail)
		return Entry{Checkpoint: &cp}
	}
	if e.Overflow != nil {
		attempt := *e.Overflow
		return Entry{Overflow: &attempt}
	}
	return Entry{}
}

// Restore rebuilds a session from a store.
//
// The rebuilt session is the history as it was recorded, including any tool call
// that never got a result. Dropping those on load would make the store quietly
// disagree with what happened, and a conversation that reads as consistent while
// having lost a turn is worse than one that shows the gap.
func Restore(ctx context.Context, system string, store Store) (*Session, error) {
	recorded, err := store.Load(ctx)
	if err != nil {
		return nil, err
	}
	s := New(system)
	s.store = store
	for _, e := range recorded {
		switch {
		case e.Message != nil:
			s.messages = append(s.messages, *e.Message)
			if e.Message.Role == ai.RoleUser {
				// A new question starts a new budget. The spent cost is NOT
				// reset: it was really paid, and the ledger is cumulative.
				s.overflowAttempts = 0
			}
		case e.Overflow != nil:
			// Durable, and deliberately not part of the conversation: it is
			// counted against the recovery budget and never projected.
			s.overflowAttempts++
			s.overflowUsage.InputTokens += e.Overflow.Usage.InputTokens
			s.overflowUsage.OutputTokens += e.Overflow.Usage.OutputTokens
			continue
		case e.Checkpoint != nil:
			// A later checkpoint supersedes an earlier one: the newer summary
			// already stands in for everything before it, including the older
			// checkpoint.
			s.checkpoint = e.Checkpoint
			s.sinceCheckpoint = nil
			continue
		}
		if s.checkpoint != nil && e.Message != nil {
			s.sinceCheckpoint = append(s.sinceCheckpoint, *e.Message)
		}
	}
	return s, nil
}

// WithStore returns a session that records every message it accepts.
func WithStore(system string, store Store) *Session {
	s := New(system)
	s.store = store
	return s
}
