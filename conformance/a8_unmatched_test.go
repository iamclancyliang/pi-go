package conformance

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/events"
	"github.com/iamclancyliang/pi-go/internal/runtime"
	"github.com/iamclancyliang/pi-go/internal/session"
	"github.com/iamclancyliang/pi-go/internal/tools"
)

// TestA8AbortLeavesToolCallsWithoutResults pins that unpaired calls are a real
// state, not corruption.
//
// Aborting part-way through a round leaves the assistant's message holding call
// ids that will never get a result. Anything that assumes every call has exactly
// one result — a rebuild of the provider payload, a stored transcript, a replay —
// is wrong about a state the system genuinely reaches, and will either drop the
// turn or refuse to load it.
func TestA8AbortLeavesToolCallsWithoutResults(t *testing.T) {
	gate := newGatedTool("FIRST-RESULT")
	registry := tools.NewRegistry()
	registry.MustRegister(gate)

	sess := session.New("You are pi-go.")
	model := &ai.Scripted{
		Name: "fake-1",
		Replies: []ai.Response{ai.AssistantToolCalls(
			ai.ToolCall{ID: "call-1", Name: "slow_read", Args: `{}`},
			ai.ToolCall{ID: "call-2", Name: "slow_read", Args: `{}`},
		)},
		StopWhenToolsSettled: true,
		Final:                ai.AssistantText("done"),
	}

	rec := runtime.NewRecorder()
	agent, err := runtime.New(runtime.Config{
		Model:     model,
		ModelName: "fake-1",
		Tools:     registry,
		Session:   sess,
		Policy:    runtime.DenyWrites,
		Observers: []events.Observer{rec},
		Now:       fixedClock(),
	})
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	run, err := agent.Start(ctx, "Read twice.")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Abort while a call is in flight.
	gate.waitEntered(t)
	cancel()
	_ = run.Wait()

	// The assistant's request survives with ids that have no result. This is
	// the state the rest of the system has to tolerate.
	unmatched := sess.UnmatchedToolCalls()
	if len(unmatched) == 0 {
		t.Fatalf("no unmatched tool calls after an abort: truth = %d messages", sess.Len())
	}

	// Every unmatched id was really requested, and really has no result.
	requested := map[string]bool{}
	settled := map[string]bool{}
	for _, m := range sess.Truth() {
		for _, tc := range m.ToolCalls {
			requested[tc.ID] = true
		}
		if m.Role == ai.RoleTool {
			settled[m.ToolCallID] = true
		}
	}
	for _, id := range unmatched {
		if !requested[id] {
			t.Errorf("%s is reported unmatched but was never requested", id)
		}
		if settled[id] {
			t.Errorf("%s is reported unmatched but has a result", id)
		}
	}

	// A start with no result is the abort's own signature, and is allowed: the
	// cut falls AFTER a call has been announced, so a call already in flight
	// keeps its start and never produces a result. What must not happen is the
	// reverse — a result for a call that was never announced would be a result
	// invented for work that was not attempted.
	started := map[string]bool{}
	for _, e := range rec.Events() {
		if e.Kind == events.KindToolStart {
			started[e.ToolCallID] = true
		}
	}
	for id := range settled {
		if !started[id] {
			t.Errorf("%s produced a result without ever being announced", id)
		}
	}

	// The projection must still be usable: rebuilding context after an abort is
	// exactly when this state is met.
	if proj := sess.Project(); len(proj.Messages) == 0 {
		t.Error("the projection is empty after an abort")
	}
}

// TestA8ControlCompletedRoundHasNoUnmatched is the paired control.
//
// Without it the assertions above pass against a runtime that never pairs calls
// with results at all, which is a different defect wearing the same shape.
func TestA8ControlCompletedRoundHasNoUnmatched(t *testing.T) {
	registry := tools.NewRegistry()
	registry.MustRegister(&timedTool{name: "quick_tool", delay: time.Millisecond})

	_, sess := runRound(t, registry,
		ai.ToolCall{ID: "call-1", Name: "quick_tool", Args: `{}`},
		ai.ToolCall{ID: "call-2", Name: "quick_tool", Args: `{}`},
	)

	if got := sess.UnmatchedToolCalls(); len(got) != 0 {
		t.Errorf("a completed round left %v unmatched", got)
	}
}

// TestA8SequentialAbortDoesNotHang covers the other execution shape.
//
// A sequential round hands the turn from one call to the next. A call still
// waiting when the round is cut is waiting for a hand-off that will never come,
// so the run does not fail — it never returns at all. A hang is worse than a
// wrong answer: nothing reports it, and the caller has nothing to act on.
//
// The parallel case cannot find this: there is no hand-off there, so every call
// is free to observe the cut on its own.
func TestA8SequentialAbortDoesNotHang(t *testing.T) {
	gate := newGatedTool("FIRST-RESULT")
	registry := tools.NewRegistry()
	registry.MustRegister(gate)
	// Registered AND called, so this round is sequential.
	registry.MustRegister(&timedTool{name: "exclusive_tool", delay: time.Millisecond, sequential: true})

	sess := session.New("You are pi-go.")
	model := &ai.Scripted{
		Name: "fake-1",
		Replies: []ai.Response{ai.AssistantToolCalls(
			ai.ToolCall{ID: "call-1", Name: "slow_read", Args: `{}`},
			ai.ToolCall{ID: "call-2", Name: "exclusive_tool", Args: `{}`},
		)},
		StopWhenToolsSettled: true,
		Final:                ai.AssistantText("done"),
	}

	rec := runtime.NewRecorder()
	agent, err := runtime.New(runtime.Config{
		Model:     model,
		ModelName: "fake-1",
		Tools:     registry,
		Session:   sess,
		Policy:    runtime.DenyWrites,
		Observers: []events.Observer{rec},
		Now:       fixedClock(),
	})
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	run, err := agent.Start(ctx, "Read, then list.")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	gate.waitEntered(t)
	cancel()

	// The run must RETURN. Asserting on what it returns comes second: a run that
	// never returns cannot be asserted on at all.
	done := make(chan struct{})
	go func() {
		_ = run.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a cancelled sequential round never returned: the call waiting its " +
			"turn is waiting for a hand-off that will not come")
	}

	// And the cut call left no result behind, same as the parallel shape.
	if got := sess.UnmatchedToolCalls(); len(got) == 0 {
		t.Error("a cancelled sequential round produced results for every call")
	}

	// The call that never got its turn is not announced either. Opening the
	// successor's gate to unblock it must not also start it: a start says the
	// call was attempted, and this one never was.
	for _, e := range rec.Events() {
		if e.Kind == events.KindToolStart && e.ToolCallID == "call-2" {
			t.Errorf("a call the abort never reached was announced: %v", rec.Kinds())
		}
	}
	// The one that did run keeps its start, and nothing follows it.
	if got := idsOf(rec, events.KindToolStart); !equal(got, []string{"call-1"}) {
		t.Errorf("starts = %v, want only the call that was in flight", got)
	}
}

// TestA8CutCallIsNotExecuted pins that a call the abort never reached does not run.
//
// "Produces no result" and "does not run" are different claims, and the event
// stream cannot tell them apart: a round reports nothing for a call it cut, so a
// tool that ran anyway looks identical to one that did not. The difference is
// visible only in what the tool did — which for a real tool means files written
// or commands executed.
//
// The round is SEQUENTIAL, which is what makes the claim decidable. There, calls
// after the current one have genuinely not been reached when the cut arrives, so
// preventing them is a guarantee. In a parallel round every call is dispatched at
// once: a call already inside its tool cannot be un-run, and asserting otherwise
// tests a race rather than a rule.
//
// The tool IGNORES cancellation, deliberately. One that honours it would stop by
// itself and prove nothing about whether the round prevented the call.
func TestA8CutCallIsNotExecuted(t *testing.T) {
	gate := newGatedTool("FIRST-RESULT")
	stubborn := &stubbornTool{}
	registry := tools.NewRegistry()
	registry.MustRegister(gate)
	registry.MustRegister(stubborn)
	// Makes this round sequential, so the second call waits its turn instead of
	// being dispatched alongside the first.
	registry.MustRegister(&timedTool{name: "pacer", delay: time.Millisecond, sequential: true})

	sess := session.New("You are pi-go.")
	model := &ai.Scripted{
		Name: "fake-1",
		Replies: []ai.Response{ai.AssistantToolCalls(
			ai.ToolCall{ID: "call-1", Name: "slow_read", Args: `{}`},
			ai.ToolCall{ID: "call-2", Name: "stubborn_tool", Args: `{}`},
		)},
		StopWhenToolsSettled: true,
		Final:                ai.AssistantText("done"),
	}

	rec := runtime.NewRecorder()
	agent, err := runtime.New(runtime.Config{
		Model:     model,
		ModelName: "fake-1",
		Tools:     registry,
		Session:   sess,
		Policy:    runtime.DenyWrites,
		Observers: []events.Observer{rec},
		Now:       fixedClock(),
	})
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	run, err := agent.Start(ctx, "Read, then be stubborn.")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	gate.waitEntered(t)
	cancel()
	_ = run.Wait()

	// Give a tool that ignores cancellation every chance to run before asserting
	// that it did not.
	time.Sleep(100 * time.Millisecond)
	if got := stubborn.ran(); got != 0 {
		t.Errorf("a call cut by the abort still executed %d time(s); the round must "+
			"prevent it, not merely decline to report it", got)
	}
}

// stubbornTool ignores cancellation, so only "did it run" distinguishes a call
// the round prevented from one it merely stayed quiet about.
type stubbornTool struct {
	mu    sync.Mutex
	calls int
}

func (s *stubbornTool) Name() string        { return "stubborn_tool" }
func (s *stubbornTool) Description() string { return "ignores cancellation" }

func (s *stubbornTool) Execution() tools.Execution {
	return tools.Execution{Sequential: true, ReadOnly: true}
}

func (s *stubbornTool) Call(context.Context, string) (tools.Result, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	return tools.Result{Content: "ran anyway"}, nil
}

func (s *stubbornTool) ran() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}
