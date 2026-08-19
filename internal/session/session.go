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
	"sort"
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

	// intents are tool calls that were about to run, by call id.
	intents map[string]ToolIntent

	// settled holds the recorded outcome of each call, by call id.
	settled map[string]ToolSettlement

	// failure is the terminal state of the current input, if it has one.
	failure *OperationFailure

	// operations counts the inputs this conversation has been asked to answer.
	// It is rebuilt from the store, so the operation a record names is the same
	// one before and after a restart.
	operations int

	// overflowUsage accumulates what the refused attempts cost.
	overflowUsage ai.Usage

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
	return &Session{
		intents: map[string]ToolIntent{},
		settled: map[string]ToolSettlement{}, system: system}
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
	s.rememberLocked(msgs...)
	return nil
}

// rememberLocked makes messages visible in memory. The caller holds the lock and
// has already recorded them.
func (s *Session) rememberLocked(msgs ...ai.Message) {
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
			s.failure = nil
			s.operations++
		}
	}
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

// Fail durably records that the operation ended and cannot be retried as it
// stands.
func (s *Session) Fail(code, detail string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	failure := &OperationFailure{Code: code, Detail: detail}
	if s.store != nil {
		recorded := *failure
		if err := s.store.Append(context.Background(), Entry{Failure: &recorded}); err != nil {
			return fmt.Errorf("session: recording a terminal failure: %w", err)
		}
	}
	s.failure = failure
	return nil
}

// Failure reports the terminal state of the current input, or nil.
//
// Read before asking the model anything: reopening a conversation that already
// failed and asking again spends the same money to reach the same conclusion.
func (s *Session) Failure() *OperationFailure {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.failure == nil {
		return nil
	}
	copied := *s.failure
	return &copied
}

// OperationID names the input currently being answered.
//
// Derived from the count of inputs rather than generated, because a generated id
// is lost with the process that generated it: a record written before a crash
// would then name an operation the restarted process cannot recognise. Counting
// inputs reaches the same name again because the inputs themselves are durable.
func (s *Session) OperationID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return fmt.Sprintf("op-%d", s.operations)
}

// RecordIntent durably notes that a tool is about to run.
//
// Written BEFORE the tool is invoked. Written after, it would be missing in
// exactly the case it exists for: a crash between the effect and its record.
func (s *Session) RecordIntent(intent ToolIntent) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.store != nil {
		recorded := intent
		if err := s.store.Append(context.Background(), Entry{Intent: &recorded}); err != nil {
			return fmt.Errorf("session: recording a tool intent: %w", err)
		}
	}
	s.intents[intent.CallID] = intent
	return nil
}

// Settle durably records what a call produced, together with what the model is
// shown about it, as ONE all-or-none write.
//
// One write, because the two halves are the same transition. Recording the
// settlement first and the message second lets a failure in between leave a call
// that nothing will ever revisit: it is settled, so recovery passes over it,
// while the conversation the model reads has no result for it at all. That is a
// worse state than either write failing outright, because nothing afterwards can
// tell that anything is missing.
//
// told may be empty, for a call whose result was already recorded on its own.
func (s *Session) Settle(settlement ToolSettlement, told ...ai.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.store != nil {
		entries := make([]Entry, 0, len(told)+1)
		for i := range told {
			recorded := told[i]
			entries = append(entries, Entry{Message: &recorded})
		}
		recorded := settlement
		entries = append(entries, Entry{Settlement: &recorded})
		if err := s.store.Append(context.Background(), entries...); err != nil {
			return fmt.Errorf("session: settling a tool call: %w", err)
		}
	}
	s.settled[settlement.CallID] = settlement
	s.rememberLocked(told...)
	return nil
}

// UnsettledIntents lists calls that were started and whose outcome is unknown.
//
// Unknown, not failed: the tool may have done its work and died before saying
// so. Treating these as "did not happen" is the reading that repeats a
// destructive action.
func (s *Session) UnsettledIntents() []ToolIntent {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []ToolIntent
	for id, intent := range s.intents {
		if _, done := s.settled[id]; !done {
			out = append(out, intent)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CallID < out[j].CallID })
	return out
}

// Settlement reports the recorded outcome of a call, if it has one.
func (s *Session) Settlement(callID string) (ToolSettlement, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	settled, ok := s.settled[callID]
	return settled, ok
}

// RecordOverflowAttempt durably notes that the provider refused the request for
// being too large, and counts it against this input's recovery budget.
//
// The attempt is recorded but never projected. Sending it back would resend the
// thing that was rejected, and offering it to a summariser would let a provider
// error be written into the conversation as something that was said.
func (s *Session) RecordOverflowAttempt(detail string, usage ai.Usage) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.store != nil {
		if err := s.store.Append(context.Background(), Entry{
			Overflow: &OverflowAttempt{Detail: detail, Usage: usage},
		}); err != nil {
			return fmt.Errorf("session: recording an overflow attempt: %w", err)
		}
	}
	s.overflowAttempts++
	s.overflowUsage.InputTokens += usage.InputTokens
	s.overflowUsage.OutputTokens += usage.OutputTokens
	return nil
}

// OverflowUsage is what the refused attempts cost.
//
// Kept separate from the conversation: the calls were paid for and must be
// auditable, while nothing they returned belongs in what the model is shown.
func (s *Session) OverflowUsage() ai.Usage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.overflowUsage
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
