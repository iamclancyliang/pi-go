package session

import (
	"context"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/tools"
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
	Intent     *ToolIntent
	Settlement *ToolSettlement
	Failure    *OperationFailure
}

// OperationFailure records that the operation ended and cannot be retried as it
// stands.
//
// Durable, because a terminal state that lives only in a returned error is
// forgotten on restart: the process reopens, sees an unanswered question, and
// spends the same money reaching the same conclusion.
type OperationFailure struct {
	// Code is stable enough for a caller to branch on.
	Code string

	// Detail is for a human reading the record afterwards.
	Detail string
}

// OverflowAttempt is one durable record behind a context refusal.
//
// It holds two kinds of thing, told apart by SpendOnly:
//
//   - a refused MODEL CALL, which consumes a unit of the recovery budget; and
//   - the cost of a transport attempt made while trying to complete that call,
//     which consumes none.
//
// Both are durable and neither is ever projected into the conversation:
// resending a refusal would resend what was rejected, and offering it to a
// summariser would write a provider error into the conversation as something
// that was said.
type OverflowAttempt struct {
	// SpendOnly distinguishes the two kinds. True means this record is the cost
	// of a transport attempt, not a refused model call, so it consumes no
	// recovery budget — and restoring it must not consume one either.
	SpendOnly bool

	// Detail explains this record for someone auditing it afterwards, rather
	// than for the model to read. For a refusal it is what the provider
	// reported; for a transport attempt it says that is what the record is.
	Detail string

	// Usage is what THIS record's attempt or call used. A request is billed
	// whether it was answered, refused, or retried at the transport, so
	// dropping any of them under-reports what the user paid.
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
		// Usage holds pointers for its optional counts, so copying the struct
		// alone leaves the stored entry sharing them with the caller's value.
		attempt := *e.Overflow
		attempt.Usage = e.Overflow.Usage.Clone()
		return Entry{Overflow: &attempt}
	}
	if e.Intent != nil {
		intent := *e.Intent
		return Entry{Intent: &intent}
	}
	if e.Settlement != nil {
		settled := *e.Settlement
		return Entry{Settlement: &settled}
	}
	if e.Failure != nil {
		failed := *e.Failure
		return Entry{Failure: &failed}
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
				// A new question starts a new budget, and clears a terminal
				// state that belonged to the previous one. The spent cost is
				// NOT reset: it was really paid, and the ledger is cumulative.
				s.overflowAttempts = 0
				s.failure = nil
				s.operations++
			}
		case e.Overflow != nil:
			// Durable, and deliberately not part of the conversation: it is
			// counted against the recovery budget and never projected.
			// A spend-only entry is the cost of a provider attempt behind a
			// refusal. It is ledgered but consumes no recovery budget, so
			// restoring it must not consume one either.
			s.overflowUsage = s.overflowUsage.Add(e.Overflow.Usage)
			if !e.Overflow.SpendOnly {
				s.overflowAttempts++
			}
			continue
		case e.Failure != nil:
			s.failure = e.Failure
			continue
		case e.Intent != nil:
			s.intents[e.Intent.ResultID] = *e.Intent
			continue
		case e.Settlement != nil:
			s.settled[e.Settlement.ResultID] = *e.Settlement
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

// ToolIntent records that a tool is about to be run, before it is.
//
// It exists because a process can die between the effect and the record of it.
// Without an intent, a transcript cannot distinguish a call that never ran from
// one that ran and lost its answer, and the second is the dangerous reading.
type ToolIntent struct {
	// OperationID names the run this call belongs to, so a settlement can be
	// matched to its operation and not merely to a call id that a later run
	// might reuse.
	OperationID string

	// CallID identifies the model's request.
	CallID string

	// ResultID reserves where the answer will go, and IS this attempt's identity.
	//
	// Reserved before the call so that recovery has somewhere to write a
	// synthetic outcome without inventing an identity the transcript never had.
	// It is what an attempt is filed and paired under, because a call id is not
	// unique: the model chooses them and a later operation may reuse one, so
	// pairing on the call id alone lets an old outcome close a new attempt.
	ResultID string

	// Tool, ToolVersion and Args are exactly what was about to run. The version
	// is recorded because a tool that changed while the process was down is not
	// the tool that agreed to be repeated.
	Tool        string
	ToolVersion string
	Args        string

	// Replay is the policy as declared WHEN THE INTENT WAS WRITTEN. Recovery
	// compares it with what the tool declares now.
	Replay tools.ReplayPolicy
}

// ToolSettlement records what a tool call actually produced.
//
// Its absence is not evidence that nothing happened. It means the outcome is
// unknown, which is a different and more dangerous state than failure.
type ToolSettlement struct {
	// CallID is which request in the conversation this answers.
	CallID string

	// ResultID is the slot the attempt reserved, and is how this settlement is
	// paired with it. Required, because everything filed under an empty name is
	// the same attempt.
	ResultID string

	Result string

	// Interrupted marks a settlement written by recovery rather than by the
	// tool: the effect was never confirmed either way.
	Interrupted bool

	// Terminate is what this call asked the conversation to do next. Recorded
	// with the result because it is part of the outcome: a transcript that keeps
	// the result and forgets the request cannot say why the run stopped.
	Terminate bool
}

// RecoverUnsettled decides what to do about calls whose outcome was lost, and
// settles each one.
//
// A call is repeated only when the policy recorded before the crash AND the
// policy the tool declares now both say it is safe. Either side disagreeing is
// enough to refuse: a tool whose declaration changed while the process was down
// has not agreed to be repeated, and the record written earlier cannot speak for
// the code running now.
//
// Everything else is settled as interrupted — the effect is unknown, not absent.
// Reporting it as "did not happen" is what would repeat a destructive action. The
// synthetic outcome is also appended to the conversation, because a model that is
// never told anything about the call waits for an answer that is not coming.
//
// The intents themselves are returned, not their ids: a caller handed only ids
// has to look the details up again, and the lookup is where the decision made
// here can quietly be bypassed.
func RecoverUnsettled(ctx context.Context, s *Session, declaredNow func(tool string) (tools.ReplayPolicy, string, bool)) ([]ToolIntent, error) {
	var replayable []ToolIntent
	for _, intent := range s.UnsettledIntents() {
		policy, version, known := declaredNow(intent.Tool)
		sameTool := known && version == intent.ToolVersion
		if sameTool && policy == tools.ReplaySafe && intent.Replay == tools.ReplaySafe {
			replayable = append(replayable, intent)
			continue
		}

		if err := SettleAsUnknown(s, intent); err != nil {
			return nil, err
		}
	}
	return replayable, nil
}

// unknownEffect is what the model is told about a call whose outcome was lost.
//
// It says unknown rather than failed on purpose. The tool may have done its work
// and died before reporting it, and a model told the call failed will act as
// though the world was left untouched.
const unknownEffect = "interrupted: the process stopped before this call " +
	"reported its outcome, so whether it took effect is unknown"

// SettleAsUnknown resolves an attempt whose outcome cannot be established.
//
// The model is told in the SAME write. A conversation holding a tool call with no
// result cannot be continued at all, so a settlement recorded without the message
// would close the attempt and leave the conversation unusable.
func SettleAsUnknown(s *Session, intent ToolIntent) error {
	return s.Settle(ToolSettlement{
		CallID:      intent.CallID,
		ResultID:    intent.ResultID,
		Result:      unknownEffect,
		Interrupted: true,
	}, ai.Message{
		Role:       ai.RoleTool,
		Content:    unknownEffect,
		ToolCallID: intent.CallID,
	})
}
