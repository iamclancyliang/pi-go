package ai

import (
	"errors"
	"fmt"
)

// ErrBlockIdentity reports that a chunk did not say which block it belongs to.
//
// The stream fails rather than guessing. Two adjacent text blocks are
// indistinguishable once their text is concatenated, so a guess is not a
// best-effort reading — it silently merges things the model kept apart.
var ErrBlockIdentity = errors.New("ai: chunk carries no block identity")

// Chunk is one increment as a provider delivered it.
//
// It names its block. That requirement is the whole reason this type exists
// rather than the runtime reading a framework message: nothing downstream can
// recover a boundary the provider did not state.
type Chunk struct {
	// Index identifies the block. Increments for the same block carry the same
	// index, and it is the caller's job to allocate them in the order the blocks
	// were opened.
	Index int

	// Kind is what the block holds. It must not change for a given index.
	Kind BlockKind

	// Delta is the text added to a text or thinking block, or the argument
	// fragment added to a tool call.
	Delta string

	// Call carries a tool call's identity when the block opens. Its arguments
	// arrive as deltas.
	Call ToolCall
}

// Accumulator turns chunks into the event protocol.
//
// It OWNS block identity: it allocates the block list, checks that a chunk's
// index and kind agree with what that block already is, and emits the index a
// consumer uses to attribute an event. Nothing upstream is trusted to have done
// this, because the framework neither allocates these indices nor guarantees they
// are unique across kinds.
type Accumulator struct {
	message AssistantMessage
	open    map[int]bool
	started bool
	ended   bool
}

// NewAccumulator returns an Accumulator for one reply.
func NewAccumulator(model string) *Accumulator {
	return &Accumulator{
		message: AssistantMessage{Model: model},
		open:    map[int]bool{},
	}
}

// Begin emits the start event.
//
// Separate from the first chunk because a reply can fail before any content
// arrives, and such a stream must terminate without ever having started.
func (a *Accumulator) Begin() (StreamEvent, error) {
	if a.started {
		return StreamEvent{}, errors.New("ai: the stream already started")
	}
	a.started = true
	snapshot := a.message.Clone()
	return StreamEvent{Kind: StreamStart, Partial: &snapshot}, nil
}

// Push applies one chunk and returns the events it produced.
//
// A chunk that opens a block produces two events — the block's start and its
// first delta — because a consumer rendering incrementally needs to know the
// block exists before it is told what was added to it.
func (a *Accumulator) Push(c Chunk) ([]StreamEvent, error) {
	switch {
	case a.ended:
		return nil, errors.New("ai: the stream already ended")
	case !a.started:
		return nil, errors.New("ai: chunk before the stream started")
	case c.Index < 0:
		return nil, fmt.Errorf("%w: negative index", ErrBlockIdentity)
	case c.Kind == "":
		return nil, fmt.Errorf("%w: no kind at index %d", ErrBlockIdentity, c.Index)
	}

	// Indices are contiguous from zero. A gap would mean a block nobody
	// announced, and its absence would be invisible in the finished message.
	if c.Index > len(a.message.Blocks) {
		return nil, fmt.Errorf("%w: index %d skips %d",
			ErrBlockIdentity, c.Index, len(a.message.Blocks))
	}

	var events []StreamEvent
	if c.Index == len(a.message.Blocks) {
		a.message.Blocks = append(a.message.Blocks, Block{Kind: c.Kind, Call: c.Call})
		a.open[c.Index] = true
		snapshot := a.message.Clone()
		events = append(events, StreamEvent{
			Kind:         startKind(c.Kind),
			ContentIndex: c.Index,
			Partial:      &snapshot,
		})
	}

	block := &a.message.Blocks[c.Index]
	if block.Kind != c.Kind {
		return nil, fmt.Errorf("%w: index %d is %s, chunk says %s",
			ErrBlockIdentity, c.Index, block.Kind, c.Kind)
	}
	if !a.open[c.Index] {
		return nil, fmt.Errorf("%w: index %d is closed", ErrBlockIdentity, c.Index)
	}

	if c.Delta != "" {
		if block.Kind == BlockToolCall {
			block.Call.Args += c.Delta
		} else {
			block.Text += c.Delta
		}
		snapshot := a.message.Clone()
		events = append(events, StreamEvent{
			Kind:         deltaKind(c.Kind),
			ContentIndex: c.Index,
			Delta:        c.Delta,
			Partial:      &snapshot,
		})
	}
	return events, nil
}

// Close finishes one block.
func (a *Accumulator) Close(index int) (StreamEvent, error) {
	switch {
	case a.ended:
		return StreamEvent{}, errors.New("ai: the stream already ended")
	case index < 0 || index >= len(a.message.Blocks):
		return StreamEvent{}, fmt.Errorf("%w: no block at index %d", ErrBlockIdentity, index)
	case !a.open[index]:
		return StreamEvent{}, fmt.Errorf("%w: index %d is already closed", ErrBlockIdentity, index)
	}

	a.open[index] = false
	block := a.message.Blocks[index]
	snapshot := a.message.Clone()
	event := StreamEvent{
		Kind:         endKind(block.Kind),
		ContentIndex: index,
		Partial:      &snapshot,
	}
	if block.Kind == BlockToolCall {
		event.Call = block.Call
	} else {
		event.Content = block.Text
	}
	return event, nil
}

// Done ends the stream successfully.
//
// Blocks left open are not closed for it: an unclosed block is a fact about what
// arrived, and inventing its end event would tell a consumer the model finished
// something it did not.
func (a *Accumulator) Done(reason StopReason, usage Usage) (StreamEvent, error) {
	if a.ended {
		return StreamEvent{}, errors.New("ai: the stream already ended")
	}
	a.ended = true
	a.message.StopReason = reason
	a.message.Usage = usage
	final := a.message.Clone()
	return StreamEvent{Kind: StreamDone, Final: &final}, nil
}

// Fail ends the stream with what had already arrived.
//
// The accumulated blocks are kept. Pi does not do this everywhere — one of its
// transports replaces the reply with an empty one on abort — but a partial answer
// the caller watched arrive should not vanish because it was stopped.
func (a *Accumulator) Fail(reason StopReason, cause error) (StreamEvent, error) {
	if a.ended {
		return StreamEvent{}, errors.New("ai: the stream already ended")
	}
	a.ended = true
	a.message.StopReason = reason
	a.message.ErrorMessage = "unknown failure"
	if cause != nil {
		a.message.ErrorMessage = cause.Error()
		a.message.Cause = cause
	}
	final := a.message.Clone()
	return StreamEvent{Kind: StreamError, Final: &final}, nil
}

func startKind(k BlockKind) StreamEventKind {
	switch k {
	case BlockThinking:
		return StreamThinkingStart
	case BlockToolCall:
		return StreamToolCallStart
	default:
		return StreamTextStart
	}
}

func deltaKind(k BlockKind) StreamEventKind {
	switch k {
	case BlockThinking:
		return StreamThinkingDelta
	case BlockToolCall:
		return StreamToolCallDelta
	default:
		return StreamTextDelta
	}
}

func endKind(k BlockKind) StreamEventKind {
	switch k {
	case BlockThinking:
		return StreamThinkingEnd
	case BlockToolCall:
		return StreamToolCallEnd
	default:
		return StreamTextEnd
	}
}
