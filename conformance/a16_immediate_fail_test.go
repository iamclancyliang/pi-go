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

// TestA16RefusedCallEndsBeforeTheNextStarts pins the shape of a call that never
// runs.
//
// Deciding a refusal is not executing: it happens while the round is still being
// announced, so the refused call ends there — before the calls after it are
// announced at all. A legal trace is startA, endA, startB. Deciding it inside the
// tool instead can only end the call after every start has been emitted, which
// tells a reader the refusal happened later than it did.
//
// The results still follow the order the model asked for them: only the END moves
// inline. That distinction is the whole point, so both are asserted.
func TestA16RefusedCallEndsBeforeTheNextStarts(t *testing.T) {
	registry := tools.NewRegistry()
	registry.MustRegister(deniedTool{})
	registry.MustRegister(&timedTool{name: "slow_tool", delay: 80 * time.Millisecond})

	sess := session.New("You are pi-go.")
	model := &ai.Scripted{
		Name: "fake-1",
		Replies: []ai.Response{ai.AssistantToolCalls(
			ai.ToolCall{ID: "call-denied", Name: "write_tool", Args: `{}`},
			ai.ToolCall{ID: "call-slow", Name: "slow_tool", Args: `{}`},
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
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := agent.Run(ctx, "go"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	deniedStart, deniedEnd, otherStart := -1, -1, -1
	var results []string
	for index, e := range rec.Events() {
		switch {
		case e.Kind == events.KindToolStart && e.ToolCallID == "call-denied":
			deniedStart = index
		case e.Kind == events.KindToolEnd && e.ToolCallID == "call-denied":
			deniedEnd = index
		case e.Kind == events.KindToolStart && e.ToolCallID == "call-slow":
			otherStart = index
		case e.Kind == events.KindToolResult:
			results = append(results, e.ToolCallID)
		}
	}
	if deniedStart < 0 || deniedEnd < 0 || otherStart < 0 {
		t.Fatalf("expected a start and end for both calls, got %v", rec.Kinds())
	}
	// The whole shape, in order. Asserting only that the refusal ends before the
	// next call starts leaves out the announcement: a refused call that never
	// emitted a start at all satisfies that half while telling a reader the call
	// was never requested.
	if !(deniedStart < deniedEnd && deniedEnd < otherStart) {
		t.Errorf("want startA < endA < startB, got start=%d end=%d nextStart=%d in %v",
			deniedStart, deniedEnd, otherStart, rec.Kinds())
	}
	if want := []string{"call-denied", "call-slow"}; !equal(results, want) {
		t.Errorf("results = %v, want source order %v", results, want)
	}

	// The refusal is a result the model can read, not a silent drop: without it
	// the model cannot learn the call will never succeed and will retry.
	var refused string
	for _, m := range sess.Truth() {
		if m.ToolCallID == "call-denied" {
			refused = m.Content
		}
	}
	if refused == "" {
		t.Error("the refusal never reached session truth")
	}

	// And the tool really did not run.
	if got := deniedRuns; got != 0 {
		t.Errorf("a refused tool ran %d times", got)
	}
}

// deniedTool is refused by policy because it declares it can mutate.
type deniedTool struct{}

var deniedRuns int

func (deniedTool) Name() string        { return "write_tool" }
func (deniedTool) Description() string { return "declares mutation, so policy refuses it" }

func (deniedTool) Execution() tools.Execution {
	return tools.Execution{Sequential: false, ReadOnly: false}
}

func (deniedTool) Call(context.Context, string) (tools.Result, error) {
	deniedRuns++
	return tools.Result{Content: "wrote"}, nil
}

// These doubles exist to exercise scheduling, settlement and failure paths
// rather than argument handling, so they declare no arguments. Nil says that;
// an empty schema would instead tell a model there is a shape to fill in.

func (deniedTool) Parameters() *tools.Schema { return nil }
