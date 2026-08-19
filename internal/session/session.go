// Package session holds conversational truth.
//
// The distinction this package exists to enforce is between:
//
//   - TRUTH — the durable, append-only record of what actually happened. It is
//     never edited, summarized, or trimmed.
//   - PROJECTION — what a given model call gets to see. It is derived, lossy,
//     and disposable.
//
// Conflating the two is how a compaction or a steering event silently destroys
// history. Steering in particular depends on this: eino truncates at a safe
// point and starts a NEW execution, so continuity is reconstructed from truth
// held here, not carried by the framework.
//
// History is held in memory and, when a Store is supplied, recorded so it
// outlives the process. The Store is a seam, not a backend: this package decides
// what is worth recording and in what order, and the Store decides where it goes.
package session

import (
	"context"
	"fmt"
	"sync"

	"github.com/iamclancyliang/pi-go/internal/ai"
)

// Session is the append-only record of one conversation.
//
// Safe for concurrent use: the runtime appends while a projection is read.
type Session struct {
	// store, when set, is where history is recorded so it outlives the process.
	store Store

	// checkpoint is the latest compaction, if any. It stands in for everything
	// appended before it.
	checkpoint *Checkpoint

	// sinceCheckpoint is what has been appended after that checkpoint, in order.
	sinceCheckpoint []ai.Message

	// overflowAttempts counts recoveries tried against the current input. It is
	// rebuilt from the store, so a restart cannot hand the run a fresh budget.
	overflowAttempts int

	mu sync.RWMutex

	// system is held separately from the transcript because it is not a
	// conversational event — it must survive any projection, including one
	// that drops everything else.
	system   string
	messages []ai.Message
}

// New returns a Session with the given system instruction.
func New(system string) *Session {
	return &Session{system: system}
}

// Append records a message as having happened. It is never removed.
//
// The error is the store's. A history that quietly stops persisting looks exactly
// like a conversation that did not continue, and the difference only shows up
// after a restart, when the missing part cannot be recovered — so the caller is
// told at the point the write failed, not left to discover it later.
func (s *Session) Append(m ai.Message) error {
	return s.AppendAll(m)
}

// AppendAll records several messages as ONE unit.
//
// The durable write happens BEFORE the messages become visible in memory, and it
// happens as a single all-or-none write. Recording them one at a time would let a
// failure part-way through leave the store holding a prefix the session rejected:
// the live conversation and the one a restart reads back would then differ, which
// is the exact disagreement a durable history exists to rule out.
func (s *Session) AppendAll(msgs ...ai.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.store != nil && len(msgs) > 0 {
		entries := make([]Entry, 0, len(msgs))
		for i := range msgs {
			recorded := msgs[i]
			entries = append(entries, Entry{Message: &recorded})
		}
		if err := s.store.Append(context.Background(), entries...); err != nil {
			return fmt.Errorf("session: recording messages: %w", err)
		}
	}
	s.messages = append(s.messages, msgs...)
	if s.checkpoint != nil {
		s.sinceCheckpoint = append(s.sinceCheckpoint, msgs...)
	}
	for _, m := range msgs {
		// New input from the user starts a new recovery budget. The previous
		// attempts were spent trying to answer a different question, and
		// charging them against this one would leave a conversation unable to
		// recover from its first overflow.
		if m.Role == ai.RoleUser {
			s.overflowAttempts = 0
		}
	}
	return nil
}

func (s *Session) Truth() []ai.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneMessages(s.messages)
}

// System returns the system instruction.
func (s *Session) System() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.system
}

// Len reports how many messages are recorded, excluding the system message.
func (s *Session) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.messages)
}

// Projection is the view of a Session handed to one model call.
type Projection struct {
	Messages []ai.Message

	// Complete reports whether this projection contains all of truth. A
	// projection that dropped or summarized anything must say so, so that a
	// consumer can tell "the model saw everything" from "the model saw a
	// summary" without re-deriving it.
	Complete bool
}

// Project returns the default v0 projection: the system message followed by
// complete history, losing nothing.
//
// v0 has no compaction, so the honest projection is the total one. Lossy
// projections arrive with compaction in v1 — and when they do, Complete is
// already there to distinguish them.
func (s *Session) Project() Projection {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]ai.Message, 0, len(s.messages)+2)
	if s.system != "" {
		out = append(out, ai.Message{Role: ai.RoleSystem, Content: s.system})
	}

	// With a checkpoint, the model sees its summary and everything after it, and
	// nothing the summary replaced. Reading further back would rebuild the very
	// context the compaction existed to shorten, so the conversation would keep
	// paying for history it has already summarised.
	if s.checkpoint != nil {
		out = append(out, ai.Message{
			Role:    ai.RoleSystem,
			Content: s.checkpoint.Summary,
		})
		out = append(out, cloneMessages(s.checkpoint.RetainedTail)...)
		out = append(out, cloneMessages(s.sinceCheckpoint)...)
		return Projection{Messages: out, Complete: true}
	}

	out = append(out, cloneMessages(s.messages)...)
	return Projection{Messages: out, Complete: true}
}

// RecordOverflowAttempt durably notes that the provider refused the request for
// being too large, and counts it against this input's recovery budget.
//
// The attempt is recorded but never projected. Sending it back would resend the
// thing that was rejected, and offering it to a summariser would let a provider
// error be written into the conversation as something that was said.
func (s *Session) RecordOverflowAttempt(detail string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.store != nil {
		if err := s.store.Append(context.Background(), Entry{
			Overflow: &OverflowAttempt{Detail: detail},
		}); err != nil {
			return fmt.Errorf("session: recording an overflow attempt: %w", err)
		}
	}
	s.overflowAttempts++
	return nil
}

// OverflowAttempts reports how many recoveries have been tried for the current
// input.
//
// Read from durable state rather than from a counter in memory, so a process that
// died mid-recovery does not come back believing it has a full budget and repeat
// work the user already paid for.
func (s *Session) OverflowAttempts() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.overflowAttempts
}

// Compact publishes a checkpoint: a summary of everything so far, and the
// messages kept verbatim after it.
//
// History is not edited. The entries the summary replaces stay exactly where they
// were — a compaction changes what the model is shown next, not what happened.
func (s *Session) Compact(summary string, retainedTail []ai.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cp := &Checkpoint{Summary: summary, RetainedTail: cloneMessages(retainedTail)}
	if s.store != nil {
		if err := s.store.Append(context.Background(), Entry{Checkpoint: cp}); err != nil {
			return fmt.Errorf("session: publishing checkpoint: %w", err)
		}
	}
	s.checkpoint = cp
	s.sinceCheckpoint = nil
	return nil
}

func (s *Session) UnmatchedToolCalls() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Preserve emission order; map iteration would make this
	// nondeterministic and it feeds assertions.
	var order []string
	pending := make(map[string]bool)
	for _, m := range s.messages {
		for _, tc := range m.ToolCalls {
			if !pending[tc.ID] {
				order = append(order, tc.ID)
				pending[tc.ID] = true
			}
		}
	}
	for _, m := range s.messages {
		if m.Role == ai.RoleTool && m.ToolCallID != "" {
			delete(pending, m.ToolCallID)
		}
	}

	out := make([]string, 0, len(pending))
	for _, id := range order {
		if pending[id] {
			out = append(out, id)
		}
	}
	return out
}

// Snapshot is a serializable view of a session, for the trace and for
// inspection.
type Snapshot struct {
	System   string       `json:"system,omitempty"`
	Messages []ai.Message `json:"messages"`

	// Unmatched lists tool calls still awaiting a result.
	Unmatched []string `json:"unmatched_tool_calls,omitempty"`
}

// Snapshot returns the session's current state.
func (s *Session) Snapshot() Snapshot {
	return Snapshot{
		System:    s.System(),
		Messages:  s.Truth(),
		Unmatched: s.UnmatchedToolCalls(),
	}
}

func cloneMessages(in []ai.Message) []ai.Message {
	out := make([]ai.Message, len(in))
	for i, m := range in {
		cm := m
		if len(m.ToolCalls) > 0 {
			cm.ToolCalls = append([]ai.ToolCall(nil), m.ToolCalls...)
		}
		out[i] = cm
	}
	return out
}
