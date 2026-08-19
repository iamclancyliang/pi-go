package runtime

import (
	"sync"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/events"
	"github.com/iamclancyliang/pi-go/internal/session"
)

// toolBatch owns the ORDER of one round of tool calls.
//
// The event stream is a public surface: clients render from it, so the
// interleaving of starts, ends and results is part of the contract rather than an
// artefact of scheduling. That order cannot be decided inside a tool. A tool knows
// only about itself, so with several running there is no place that can say "every
// start has been emitted" or "this is the last result" — which is exactly what the
// contract is stated in terms of.
//
// The two modes are DIFFERENT SHAPES, not one shape behind a flag:
//
//	sequential: startA endA resultA  startB endB resultB
//	parallel:   startA startB …      ends in COMPLETION order … results in SOURCE order
//
// In sequential mode a result precedes the next start; in parallel mode every
// start precedes any result. Collapsing them into one path with a boolean is how
// the mode nobody designed for ends up silently wrong.
type toolBatch struct {
	emitter *emitter
	session *session.Session

	mu         sync.Mutex
	calls      []*batchCall
	byID       map[string]*batchCall
	sequential bool
	gates      []chan struct{}
	remaining  int
}

type batchCall struct {
	id     string
	name   string
	args   string
	result string
	err    error
}

func newToolBatch(emitter *emitter, sess *session.Session) *toolBatch {
	return &toolBatch{emitter: emitter, session: sess, byID: map[string]*batchCall{}}
}

// register opens a round of calls in SOURCE order, before any of them runs.
//
// `sequentialFor` is asked about the calls in THIS batch and no others. Deciding
// from the whole registry instead makes one sequential tool serialise every batch
// for the life of the process, including batches that never call it.
func (b *toolBatch) register(calls []ai.ToolCall, sequentialFor func(name string) bool) {
	b.mu.Lock()

	b.calls = make([]*batchCall, 0, len(calls))
	b.byID = make(map[string]*batchCall, len(calls))
	b.gates = make([]chan struct{}, len(calls))
	b.sequential = false
	b.remaining = len(calls)

	for index, call := range calls {
		entry := &batchCall{id: call.ID, name: call.Name, args: call.Args}
		b.calls = append(b.calls, entry)
		b.byID[call.ID] = entry
		b.gates[index] = make(chan struct{})
		if sequentialFor(call.Name) {
			b.sequential = true
		}
	}

	sequential := b.sequential
	starts := append([]*batchCall(nil), b.calls...)
	if len(b.gates) > 0 {
		// The first call is free to go in either mode; the rest wait only
		// when the batch is sequential.
		close(b.gates[0])
	}
	b.mu.Unlock()

	if sequential {
		// Starts are emitted per call, interleaved with that call's end and
		// result. Emitting them here would produce the parallel shape.
		return
	}
	// Every start, serially, in source order, BEFORE any execution begins.
	// Emitting a start from inside each tool instead lets the starts race,
	// which still passes a test that only checks results are ordered.
	for _, call := range starts {
		b.emitStart(call)
	}
}

// begin blocks until this call may run, and emits its start if the batch is
// sequential.
func (b *toolBatch) begin(callID string) {
	b.mu.Lock()
	call, known := b.byID[callID]
	sequential := b.sequential
	index := b.indexOf(callID)
	var gate chan struct{}
	if sequential && index >= 0 {
		gate = b.gates[index]
	}
	b.mu.Unlock()

	if !known {
		// A call the batch never saw is not silently reordered into it: it
		// runs unsequenced rather than being attributed a position it does
		// not have.
		return
	}
	if gate != nil {
		<-gate
		b.emitStart(call)
	}
}

// finish records the outcome and emits whatever the mode says comes next.
//
// EMISSION HAPPENS UNDER THE LOCK, and that is the point rather than an oversight.
// Recording the outcome and then emitting outside the lock lets a goroutine be
// descheduled between the two: the last call to finish can emit its own end and
// then the whole batch's results, while an earlier call's end has still not been
// emitted. The stream would then show a result before an end that precedes it,
// which is the one thing the parallel shape promises cannot happen. Ordering by
// lock acquisition makes the shape true by construction instead of by timing.
//
// Observers are notified from inside the critical section, so an observer that
// calls back into this batch would deadlock. They are event sinks; they receive
// the stream and do not drive it.
func (b *toolBatch) finish(callID, result string, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	call, known := b.byID[callID]
	if !known {
		return
	}
	call.result, call.err = result, err
	b.remaining--

	// The end is the execution event: emitted the moment this call finished,
	// which under the lock is completion order.
	b.emitEnd(call)

	if b.sequential {
		b.commit(call)
		if index := b.indexOf(callID); index >= 0 && index+1 < len(b.gates) {
			close(b.gates[index+1])
		}
		return
	}
	if b.remaining == 0 {
		// Source order, once the whole round is in. Committing as each call
		// completes would write history in whatever order the scheduler
		// happened to produce.
		for _, entry := range b.calls {
			b.commit(entry)
		}
	}
}

// commit makes the result session truth and emits it.
//
// The result message is what history keeps, so it is appended in the same order
// it is emitted — source order. `tool_end` therefore reports that a call
// finished, not that the session already contains it.
func (b *toolBatch) commit(call *batchCall) {
	b.session.Append(ai.Message{
		Role:       ai.RoleTool,
		Content:    call.result,
		ToolCallID: call.id,
	})
	b.emitter.emit(events.KindToolResult, func(e *events.Event) {
		e.ToolCallID = call.id
		e.ToolName = call.name
		e.Detail.Result = call.result
	})
}

func (b *toolBatch) emitStart(call *batchCall) {
	b.emitter.emit(events.KindToolStart, func(e *events.Event) {
		e.ToolCallID = call.id
		e.ToolName = call.name
		e.Detail.Args = call.args
	})
}

// emitEnd reports that a call finished, and nothing about what it produced.
//
// Carrying the result here would put it on the completion-ordered event, so a
// consumer could read results in completion order while the source-ordered event
// says otherwise — the two orders the split exists to keep apart. Failure is not
// a result: it belongs to the end, because it is how the call finished.
func (b *toolBatch) emitEnd(call *batchCall) {
	b.emitter.emit(events.KindToolEnd, func(e *events.Event) {
		e.ToolCallID = call.id
		e.ToolName = call.name
		if call.err != nil {
			e.Detail.Err = call.err.Error()
		}
	})
}

// indexOf must be called with the lock held.
func (b *toolBatch) indexOf(callID string) int {
	for index, call := range b.calls {
		if call.id == callID {
			return index
		}
	}
	return -1
}
