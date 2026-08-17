// Package session holds conversational truth.
//
// The distinction this package exists to enforce (PRD §5.2) is between:
//
//   - TRUTH — the durable, append-only record of what actually happened. It is
//     never edited, summarized, or trimmed.
//   - PROJECTION — what a given model call gets to see. It is derived, lossy,
//     and disposable.
//
// Conflating the two is how a compaction or a steering event silently destroys
// history. Steering in particular depends on this: eino truncates at a safe
// point and starts a NEW execution, so continuity is reconstructed from truth
// held here, not carried by the framework (ADR-0002).
//
// v0 is in-memory only. The storage port is v1 (architecture §1.4); this is the
// "minimal session/context abstraction" v0 requires, and deliberately not a
// durable backend.
package session

import (
	"sync"

	"github.com/iamclancyliang/pi-go/internal/ai"
)

// Session is the append-only record of one conversation.
//
// Safe for concurrent use: the runtime appends while a projection is read.
type Session struct {
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

// Append records a message as truth. It is never removed.
func (s *Session) Append(m ai.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, m)
}

// AppendAll records several messages in order.
func (s *Session) AppendAll(msgs ...ai.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, msgs...)
}

// Truth returns the full recorded history, excluding the system instruction.
//
// The returned slice is a copy: a caller that mutated it would be editing
// history, which is the exact failure this package exists to prevent.
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

	out := make([]ai.Message, 0, len(s.messages)+1)
	if s.system != "" {
		out = append(out, ai.Message{Role: ai.RoleSystem, Content: s.system})
	}
	out = append(out, cloneMessages(s.messages)...)
	return Projection{Messages: out, Complete: true}
}

// UnmatchedToolCalls returns the IDs of tool calls with no recorded result.
//
// This is the state a cancellation can leave behind (A8, C6). It must be
// representable rather than treated as corruption: recovery has to know a call
// was emitted and never settled, and must not blindly replay it.
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
