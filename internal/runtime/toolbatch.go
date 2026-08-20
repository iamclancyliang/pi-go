package runtime

import (
	"context"
	"sync"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/events"
	"github.com/iamclancyliang/pi-go/internal/session"
	"github.com/iamclancyliang/pi-go/internal/tools"
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

	// afterHandoff runs immediately after a waiting call is released and before
	// the round decides whether it may still run. It is nil in a real run.
	//
	// It exists because the case it guards cannot be reached from outside: the
	// cut has to land in the window between the wait returning and the decision,
	// and a caller has no way to aim at that window. Without this the check below
	// could only be believed, not shown — and a guard nothing can exercise is how
	// a defect of exactly this kind survived in the first place.
	afterHandoff func()

	// declare reports what a tool currently says about repeating a call. Read
	// when the attempt is recorded, so the record carries the terms the tool
	// offered at the time rather than whatever it offers when the record is
	// read back.
	declare func(name string) (tools.ReplayPolicy, string, bool)

	mu         sync.Mutex
	calls      []*batchCall
	byID       map[string]*batchCall
	sequential bool
	truncated  bool
	// storeErr is the first failure to record a result, carried out to the turn.
	storeErr  error
	gates     []chan struct{}
	remaining int
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
	// terminate records that this call asked the loop to stop.
	terminate bool
	// dropped marks a call the abort cut: it produces no result at all.
	dropped bool
	// resultID is the slot the recorded attempt reserved for this call's
	// result. Non-empty means an attempt is on record, so the outcome owes a
	// settlement.
	resultID string
}

func newToolBatch(emitter *emitter, sess *session.Session,
	prepare func(ctx context.Context, name, callID, args string) (string, bool),
	declare func(name string) (tools.ReplayPolicy, string, bool)) *toolBatch {
	return &toolBatch{
		emitter: emitter,
		session: sess,
		prepare: prepare,
		declare: declare,
		byID:    map[string]*batchCall{},
	}
}

// register opens a round of calls in SOURCE order, before any of them runs.
//
// `sequentialFor` is asked about the calls in THIS batch and no others. Deciding
// from the whole registry instead makes one sequential tool serialise every batch
// for the life of the process, including batches that never call it.
func (b *toolBatch) register(ctx context.Context, calls []ai.ToolCall,
	sequentialFor func(name string) bool, truncated bool) {
	b.mu.Lock()

	b.calls = make([]*batchCall, 0, len(calls))
	b.byID = make(map[string]*batchCall, len(calls))
	b.gates = make([]chan struct{}, len(calls))
	b.sequential = false
	b.remaining = len(calls)

	b.truncated = truncated
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
		if ctx.Err() != nil {
			// The round was cut before this call was announced. It gets no
			// start and no result: announcing it would describe a call that
			// was never attempted.
			b.mu.Lock()
			if !call.dropped {
				call.dropped = true
				b.remaining--
			}
			b.mu.Unlock()
			continue
		}
		b.emitStart(call)
		refusal, refused := b.prepareCall(ctx, call)
		if !refused {
			continue
		}
		b.settleWithoutRunning(call, refusal)
	}
}

// prepareCall asks whether a call may run, with no lock held: the decision is
// the caller's and may take arbitrary time.
//
// A TRUNCATED message fails every call it carried, without running any of them.
// Not the ones whose arguments fail to parse — all of them. Truncation cuts the
// arguments, and cut arguments can still be valid: checking each call and running
// the ones that parse keeps exactly the calls whose meaning was most likely
// changed, which inverts the rule.
func (b *toolBatch) prepareCall(ctx context.Context, call *batchCall) (string, bool) {
	b.mu.Lock()
	truncated := b.truncated
	b.mu.Unlock()
	if truncated {
		return "failed: the model's message was cut short, so none of its tool " +
			"calls were run", true
	}
	if b.prepare == nil {
		return "", false
	}
	return b.prepare(ctx, call.name, call.id, call.args)
}

// admit records the attempt before the call can take effect.
//
// The record has to be durable BEFORE the tool runs. Written afterwards it would
// be missing in exactly the case it exists for: a process that dies during the
// call leaves nothing saying an attempt was made, so the restarted process reads
// an untouched conversation and is free to run the call again — against a world
// the first attempt may already have changed.
//
// A call whose attempt cannot be recorded DOES NOT RUN. Running it anyway would
// produce precisely the effect-without-a-record this exists to prevent, and the
// model is told the call failed rather than being left with no answer.
//
// The declared replay terms are captured here rather than looked up during
// recovery, so the decision is judged against what the tool offered when it ran.
func (b *toolBatch) admit(call *batchCall) (string, bool) {
	policy, version, known := tools.ReplayNever, "", false
	if b.declare != nil {
		policy, version, known = b.declare(call.name)
	}
	if !known {
		// An unregistered tool has declared nothing, so nothing about it may be
		// assumed. Never is the reading that cannot be undone by a repeat.
		policy, version = tools.ReplayNever, ""
	}

	operation := b.session.OperationID()
	resultID := operation + "." + call.id
	if err := b.session.RecordIntent(session.ToolIntent{
		OperationID: operation,
		CallID:      call.id,
		ResultID:    resultID,
		Tool:        call.name,
		ToolVersion: version,
		Args:        call.args,
		Replay:      policy,
	}); err != nil {
		b.mu.Lock()
		if b.storeErr == nil {
			b.storeErr = err
		}
		b.mu.Unlock()
		return "failed: this call was not attempted, because the record that " +
			"it was about to run could not be written", false
	}

	b.mu.Lock()
	call.resultID = resultID
	b.mu.Unlock()
	return "", true
}

// beginResult is what the round has already decided about a call.
//
// A tool asks before running, because by then the round may have settled the call
// without it: refused before it was allowed to run, or cut out entirely. Returning
// only a refusal cannot express the second — a cut call is not refused, it is not
// to happen at all — and a tool told merely "not refused" runs.
type beginResult struct {
	// Refusal is what the model is told when the call was refused.
	Refusal string
	// Settled means the round already resolved this call and it must not run.
	Settled bool
	// Dropped means an abort cut this call: it must not run and produces
	// nothing at all, not even a refusal.
	Dropped bool
}

// begin blocks until this call may run, and reports what the round decided.
//
// In a sequential round the announcing and the preparing happen here, because
// each call is announced only when its turn arrives. In a parallel round both
// already happened during registration, and this returns what was decided then.
func (b *toolBatch) begin(ctx context.Context, callID string) beginResult {
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
		return beginResult{}
	}

	// THE ROUND CHECKS THE CUT, not the tool. Marking a call only when it
	// notices cancellation itself leaves every tool that ignores cancellation
	// free to run: nothing would have marked it, and the round reports nothing
	// for a cut call, so the work would happen with no trace of it anywhere.
	if ctx.Err() != nil {
		b.drop(callID)
		return beginResult{Dropped: true}
	}
	if !sequential {
		b.mu.Lock()
		decided := beginResult{
			Refusal: call.result,
			Settled: call.settled,
			Dropped: call.dropped,
		}
		b.mu.Unlock()
		if decided.Settled || decided.Dropped {
			return decided
		}
		return b.admitOrSettle(call)
	}

	// The wait must be cancellable. A sequential round hands the turn from one
	// call to the next, so a call still waiting when the round is cut would wait
	// for a hand-off that is never coming, and the run would never return.
	select {
	case <-gate:
	case <-ctx.Done():
		b.drop(callID)
		return beginResult{Dropped: true}
	}
	if b.afterHandoff != nil {
		b.afterHandoff()
	}

	// THE CUT IS CHECKED AGAIN HERE, and the select above is not enough.
	//
	// When the hand-off and the cut land together both cases are ready, and a
	// select chooses between ready cases at random. Taking the gate branch is
	// therefore normal rather than exceptional, and nothing on that branch has
	// marked this call: the round drops a call only when it observes the cut
	// itself. Without this the call goes on to be announced, prepared and RUN
	// after the round was cut — which a tool that ignores cancellation carries
	// out in full.
	if ctx.Err() != nil {
		b.drop(callID)
		return beginResult{Dropped: true}
	}

	// The round may also have been cut while this call waited its turn, in which
	// case it was already marked.
	b.mu.Lock()
	dropped := call.dropped
	b.mu.Unlock()
	if dropped {
		return beginResult{Dropped: true}
	}

	b.emitStart(call)
	refusal, refused := b.prepareCall(ctx, call)
	if !refused {
		return b.admitOrSettle(call)
	}

	b.settleWithoutRunning(call, refusal)
	return beginResult{Refusal: refusal, Settled: true}
}

// admitOrSettle records the attempt and reports whether the call may proceed.
//
// One site for both modes. Recording the attempt separately in each would let
// the two drift, and the mode that drifted would run tools with no record of the
// attempt — which looks exactly like a tool that was never called.
func (b *toolBatch) admitOrSettle(call *batchCall) beginResult {
	refusal, admitted := b.admit(call)
	if admitted {
		return beginResult{}
	}
	b.settleWithoutRunning(call, refusal)
	return beginResult{Refusal: refusal, Settled: true}
}

// settleWithoutRunning ends a call that never ran, in the round's order.
func (b *toolBatch) settleWithoutRunning(call *batchCall, result string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	call.settled, call.result = true, result
	b.remaining--
	b.emitEnd(call)
	if b.sequential {
		b.commit(call)
		if index := b.indexOf(call.id); index >= 0 && index+1 < len(b.gates) {
			close(b.gates[index+1])
		}
		return
	}
	if b.remaining == 0 {
		for _, entry := range b.calls {
			if !entry.dropped {
				b.commit(entry)
			}
		}
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
func (b *toolBatch) finish(callID, result string, terminate bool, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	call, known := b.byID[callID]
	if !known {
		return
	}
	call.result, call.err, call.terminate = result, err, terminate
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
		// happened to produce. Calls the abort cut are skipped: they have no
		// result to record.
		for _, entry := range b.calls {
			if !entry.dropped {
				b.commit(entry)
			}
		}
	}
}

// ShouldTerminate reports whether every call in this round asked to stop.
//
// Every, not any: a call cannot know what the others were asked to do, so one of
// them ending the conversation would discard work the model is still waiting on.
// An empty round terminates nothing — there was no request to honour.
//
// A call refused before it ran did not ask for anything, so a round containing one
// does not terminate.
func (b *toolBatch) ShouldTerminate() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.calls) == 0 {
		return false
	}
	for _, call := range b.calls {
		if !call.terminate {
			return false
		}
	}
	return true
}

// drop cuts a call out of the round without a result.
//
// An abort is not a tool failure. A failure is something the model should see and
// can react to; a cut call was never carried out, and inventing a result for it
// would tell the model an attempt was made and reported on. The assistant's
// message keeps the call id with nothing matching it, which is the state anything
// reading the transcript afterwards has to tolerate.
func (b *toolBatch) drop(callID string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	call, known := b.byID[callID]
	if !known || call.dropped || call.settled {
		return
	}
	call.dropped = true
	b.remaining--
	if b.sequential {
		// Hand the round on even though this call produced nothing. The next
		// call is waiting to be let through; without this it waits forever,
		// and a cut round would hang instead of ending.
		if index := b.indexOf(callID); index >= 0 && index+1 < len(b.gates) {
			select {
			case <-b.gates[index+1]:
			default:
				close(b.gates[index+1])
			}
		}
		return
	}
	if b.remaining == 0 {
		for _, entry := range b.calls {
			if !entry.dropped {
				b.commit(entry)
			}
		}
	}
}

// recordingFailure reports the first failure to persist a result, if any.
func (b *toolBatch) recordingFailure() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.storeErr
}

// commit makes the result session truth and emits it.
//
// The result message is what history keeps, so it is appended in the same order
// it is emitted — source order. `tool_end` therefore reports that a call
// finished, not that the session already contains it.
func (b *toolBatch) commit(call *batchCall) {
	// The result the model reads and the settlement of the attempt go down as ONE
	// write, because they are the same transition. Settling first and recording
	// the message second would leave a call that recovery passes over — it is
	// settled — while the conversation the model reads has no result for it.
	//
	// A write that fails is remembered rather than dropped. commit runs while the
	// round holds its lock and cannot return, so the failure is carried out to
	// the turn, which ends rather than continuing with a history that is missing
	// a result the model was told about.
	told := ai.Message{
		Role:       ai.RoleTool,
		Content:    call.result,
		ToolCallID: call.id,
	}
	var err error
	if call.resultID == "" {
		// Nothing was attempted, so there is nothing to settle: this is a call
		// the round refused before it could run.
		err = b.session.Append(told)
	} else {
		err = b.session.Settle(session.ToolSettlement{
			CallID:    call.id,
			ResultID:  call.resultID,
			Result:    call.result,
			Terminate: call.terminate,
		}, told)
	}
	if err != nil && b.storeErr == nil {
		b.storeErr = err
	}
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
