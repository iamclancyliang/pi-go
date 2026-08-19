package conformance

import (
	"context"
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
