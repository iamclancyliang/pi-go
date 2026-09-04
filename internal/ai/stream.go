package ai

// StopReason is why an assistant reply ended.
//
// A reply that is still arriving has none: `pending` in Pi's vocabulary is a
// state of the message, never a terminal reason, so it is absent here rather
// than represented and then excluded.
type StopReason string

const (
	StopEnd      StopReason = "stop"
	StopLength   StopReason = "length"
	StopToolUse  StopReason = "toolUse"
	StopDeferred StopReason = "deferred"

	// StopError and StopAborted are the two terminal failures. Cancellation is
	// not a separate outcome: a cancelled reply ends as StopAborted carrying
	// whatever had already arrived.
	StopError   StopReason = "error"
	StopAborted StopReason = "aborted"
)

// BlockKind is what one content block holds.
type BlockKind string

const (
	BlockText     BlockKind = "text"
	BlockThinking BlockKind = "thinking"
	BlockToolCall BlockKind = "tool_call"
)

// Block is one piece of an assistant reply.
//
// Blocks are ORDERED and heterogeneous, and a block's position in that order is
// its identity for the whole stream. Text and thinking are not two fields of one
// message here, because a reply can hold several of each and their relative order
// is part of what was said.
type Block struct {
	Kind BlockKind

	// Text is the content of a text or thinking block.
	Text string

	// Call is the tool call of a tool-call block. Its arguments accumulate as
	// deltas arrive, so it is only complete once the block closes.
	Call ToolCall
}

// AssistantMessage is a reply, complete or in progress.
//
// Distinct from Message, which is what history keeps: history has no use for
// block boundaries, and a reply under construction has no place in it.
type AssistantMessage struct {
	Blocks []Block

	// EarlierAttempts is what attempts before this one reported using.
	EarlierAttempts []Usage

	// StopReason is empty while the reply is still arriving.
	StopReason StopReason

	// ErrorMessage explains a terminal failure. Pi leaves this optional; pi-go
	// always fills it on a failure, because an error a caller cannot read is an
	// error it cannot act on.
	ErrorMessage string

	// Cause is the failure itself, kept so a caller can classify it.
	//
	// Text is for a reader; a decision needs the value. Matching on the message
	// would misread an unrelated failure that happens to quote the same words,
	// and would depend on wording nothing promised to keep.
	//
	// It does not survive leaving the process: a caller reading a stream back
	// from a record has ErrorMessage and nothing else.
	Cause error

	Model string
	Usage Usage
}

// Clone returns a copy that shares nothing with the original.
//
// Deep, and that is the point rather than caution: events are delivered across
// goroutines, so handing out the accumulator would be a data race, and a consumer
// that kept an event would watch it change. Every mutable descendant is copied —
// the block slice, each block, and the tool call's arguments.
func (m AssistantMessage) Clone() AssistantMessage {
	// Cause is carried by reference on purpose: an error value is immutable in
	// practice, and copying it would mean losing the wrapping a caller needs to
	// classify it.
	cloned := m
	if m.Blocks != nil {
		cloned.Blocks = make([]Block, len(m.Blocks))
		copy(cloned.Blocks, m.Blocks)
	}
	// Usage holds pointers for its optional counts, so copying the struct alone
	// leaves the snapshot sharing them with the message it came from.
	cloned.Usage = m.Usage.Clone()
	cloned.EarlierAttempts = CloneUsages(m.EarlierAttempts)
	return cloned
}

// StreamEventKind is one of twelve, and the set is closed.
type StreamEventKind string

const (
	// StreamStart precedes every content event. It is NOT guaranteed to be the
	// first event of a stream: a reply that fails before it begins terminates
	// without one.
	StreamStart StreamEventKind = "start"

	StreamTextStart StreamEventKind = "text_start"
	StreamTextDelta StreamEventKind = "text_delta"
	StreamTextEnd   StreamEventKind = "text_end"

	StreamThinkingStart StreamEventKind = "thinking_start"
	StreamThinkingDelta StreamEventKind = "thinking_delta"
	StreamThinkingEnd   StreamEventKind = "thinking_end"

	StreamToolCallStart StreamEventKind = "toolcall_start"
	StreamToolCallDelta StreamEventKind = "toolcall_delta"
	StreamToolCallEnd   StreamEventKind = "toolcall_end"

	StreamDone  StreamEventKind = "done"
	StreamError StreamEventKind = "error"
)

// StreamEvent is one observation of a reply arriving.
type StreamEvent struct {
	Kind StreamEventKind

	// Seq is this event's position in the run's single event order, assigned
	// by the runtime when the event is delivered to observers — zero at the
	// port boundary, where no such order exists yet.
	//
	// It shares one counter with the lifecycle events (ADR-0009): a consumer
	// holding both families can interleave them correctly instead of guessing
	// that a delta belongs to whichever turn happens to be open.
	Seq int

	// ContentIndex identifies the block this event concerns. Set on the nine
	// block events; meaningless on start, done and error.
	//
	// It is the position of the block in Partial.Blocks, which is what lets a
	// consumer attribute an event without assuming blocks close in order.
	ContentIndex int

	// Delta is what this event added, on the three delta events.
	Delta string

	// Content is the finished text of a text or thinking block, on its end event.
	Content string

	// Call is the finished tool call, on toolcall_end.
	Call ToolCall

	// Partial is the reply AS OF THIS EVENT, on the ten non-terminal events. It
	// is a snapshot: a consumer never has to accumulate deltas itself, and
	// nothing it does to the snapshot reaches the stream.
	//
	// Nil on done and error, which carry Final instead.
	Partial *AssistantMessage

	// Final is the complete reply, on done and error. On error it carries
	// whatever had already arrived rather than an empty message.
	Final *AssistantMessage
}

// Terminal reports whether this event ends the stream.
func (e StreamEvent) Terminal() bool {
	return e.Kind == StreamDone || e.Kind == StreamError
}
