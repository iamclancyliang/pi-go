package runtime

import (
	"context"
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

	// prepare decides whether a call may run at all, before any execution in
	// the round begins. It reports the refusal text when it may not.
	prepare func(ctx context.Context, name, callID, args string) (string, bool)

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
	// settled marks a call refused during preparation: it has already ended
	// and must not run.
	settled bool
}

func newToolBatch(emitter *emitter, sess *session.Session,
	prepare func(ctx context.Context, name, callID, args string) (string, bool)) *toolBatch {
	return &toolBatch{
		emitter: emitter,
		session: sess,
		prepare: prepare,
		byID:    map[string]*batchCall{},
	}
}

// register opens a round of calls in SOURCE order, before any of them runs.
//
// `sequentialFor` is asked about the calls in THIS batch and no others. Deciding
// from the whole registry instead makes one sequential tool serialise every batch
// for the life of the process, including batches that never call it.
func (b *toolBatch) register(ctx context.Context, calls []ai.ToolCall,
	sequentialFor func(name string) bool) {
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
	pending := append([]*batchCall(nil), b.calls...)
	if len(b.gates) > 0 {
		// The first call is free to go in either mode; the rest wait only
		// when the round is sequential.
		close(b.gates[0])
	}
	b.mu.Unlock()

	if sequential {
		// Each call is announced, prepared and finished before the next one
		// starts, so there is nothing to do up front: doing it here would
		// produce the parallel shape.
		return
	}

	// PREPARATION IS A SEPARATE PASS, serial and in source order, before any
	// execution begins. A call refused here ends INLINE -- before the calls
	// after it are even announced -- so a refusal is visible in the stream at
	// the point it was decided rather than alongside results that ran.
	for _, call := range pending {
		b.emitStart(call)
		refusal, refused := b.prepareCall(ctx, call)
		if !refused {
			continue
		}
		b.mu.Lock()
		call.settled, call.result = true, refusal
		b.remaining--
		b.emitEnd(call)
		finished := b.remaining == 0
		ordered := append([]*batchCall(nil), b.calls...)
		b.mu.Unlock()
		if finished {
			b.mu.Lock()
			for _, entry := range ordered {
				b.commit(entry)
			}
			b.mu.Unlock()
		}
	}
}

// prepareCall asks whether a call may run, with no lock held: the decision is
// the caller's and may take arbitrary time.
func (b *toolBatch) prepareCall(ctx context.Context, call *batchCall) (string, bool) {
	if b.prepare == nil {
		return "", false
	}
	return b.prepare(ctx, call.name, call.id, call.args)
}

// begin blocks until this call may run, and reports a refusal decided before it.
//
// In a sequential round the announcing and the preparing happen here, because
// each call is announced only when its turn arrives. In a parallel round both
// already happened during registration, and this returns what was decided then.
func (b *toolBatch) begin(ctx context.Context, callID string) (string, bool) {
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
		// A call the round never saw is not reordered into it: it runs
		// unsequenced rather than being given a position it does not have.
		return "", false
	}
	if !sequential {
		b.mu.Lock()
		defer b.mu.Unlock()
		return call.result, call.settled
	}

	<-gate
	b.emitStart(call)
	refusal, refused := b.prepareCall(ctx, call)
	if !refused {
		return "", false
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	call.settled, call.result = true, refusal
	b.remaining--
	b.emitEnd(call)
	b.commit(call)
	if index >= 0 && index+1 < len(b.gates) {
		close(b.gates[index+1])
	}
	return refusal, true
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
