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
	// Append records one message as having happened.
	Append(ctx context.Context, m ai.Message) error

	// Load returns every recorded message, in the order they were appended.
	Load(ctx context.Context) ([]ai.Message, error)
}

// MemoryStore keeps history in memory. It is durable for the life of the
// process and no longer, which is what a test wants and what a product does not.
type MemoryStore struct {
	messages []ai.Message
}

// Append implements Store.
func (m *MemoryStore) Append(_ context.Context, msg ai.Message) error {
	m.messages = append(m.messages, cloneMessages([]ai.Message{msg})...)
	return nil
}

// Load implements Store.
func (m *MemoryStore) Load(_ context.Context) ([]ai.Message, error) {
	return cloneMessages(m.messages), nil
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
	s.messages = recorded
	return s, nil
}

// WithStore returns a session that records every message it accepts.
func WithStore(system string, store Store) *Session {
	s := New(system)
	s.store = store
	return s
}
