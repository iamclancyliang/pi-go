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

// TestA9NextTurnMayChangeTheModel pins a mid-run model change and its record.
//
// The framework reports no such event, so without one of pi-go's own a run that
// switched models halfway is indistinguishable from one that did not — and
// nothing afterwards can explain why the later answers differ in style, cost or
// capability. The change is therefore announced as well as applied.
//
// It applies from the NEXT turn: the turn that just ran was executed with the
// model it had, and rewriting that would describe a conversation that did not
// happen.
func TestA9NextTurnMayChangeTheModel(t *testing.T) {
	// The first turn holds inside a tool, so the follow-up is queued while that
	// turn is still running and a second turn actually happens.
	gate := newGatedTool("held")
	registry := tools.NewRegistry()
	registry.MustRegister(gate)

	model := &ai.Scripted{
		Name: "fast-model",
		Replies: []ai.Response{
			ai.AssistantToolCalls(ai.ToolCall{ID: "call-1", Name: "slow_read", Args: `{}`}),
			ai.AssistantText("first answer"),
		},
		Final: ai.AssistantText("second answer"),
	}

	swapped := false
	rec := runtime.NewRecorder()
	sess := session.New("You are pi-go.")
	agent, err := runtime.New(runtime.Config{
		Model:     model,
		ModelName: "fast-model",
		Tools:     registry,
		Session:   sess,
		Policy:    runtime.DenyWrites,
		Observers: []events.Observer{rec},
		Now:       fixedClock(),
		PrepareNextTurn: func(context.Context) runtime.NextTurn {
			if swapped {
				return runtime.NextTurn{}
			}
			swapped = true
			return runtime.NextTurn{ModelName: "careful-model"}
		},
	})
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	run, err := agent.Start(ctx, "First question.")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	gate.waitEntered(t)
	if err := run.Follow("Second question."); err != nil {
		t.Fatalf("Follow: %v", err)
	}
	close(gate.release)
	if err := run.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	requests := model.Requests()
	if len(requests) < 3 {
		t.Fatalf("expected a second turn, got %d model requests", len(requests))
	}

	// Every request in the FIRST turn keeps the model that turn started with,
	// including the one after its tool call: the change belongs to later turns.
	if got := requests[0].Model; got != "fast-model" {
		t.Errorf("first request used %q, want the model that turn started with", got)
	}
	if got := requests[1].Model; got != "fast-model" {
		t.Errorf("the post-tool request of the first turn used %q, want fast-model: a "+
			"change must not reach back into the turn that chose it", got)
	}
	// The change takes effect from the next turn.
	if got := requests[2].Model; got != "careful-model" {
		t.Errorf("second turn used %q, want the model chosen after the first turn", got)
	}

	// And it is on the record.
	var changes [][2]string
	for _, e := range rec.Events() {
		if e.Kind == events.KindModelChanged {
			changes = append(changes, [2]string{e.Detail.From, e.Detail.To})
		}
	}
	if len(changes) != 1 {
		t.Fatalf("model_changed events = %d, want exactly 1: %v", len(changes), changes)
	}
	if changes[0] != [2]string{"fast-model", "careful-model"} {
		t.Errorf("model_changed = %v, want fast-model -> careful-model", changes[0])
	}
}

// TestA9NoHookNoChange is the control.
//
// Without it the assertions above pass against a runtime that announces a change
// on every turn, or that reports one it never made.
func TestA9NoHookNoChange(t *testing.T) {
	model := &ai.Scripted{
		Name:    "fast-model",
		Replies: []ai.Response{ai.AssistantText("first answer")},
		Final:   ai.AssistantText("second answer"),
	}

	rec := runtime.NewRecorder()
	agent, err := runtime.New(runtime.Config{
		Model:     model,
		ModelName: "fast-model",
		Tools:     tools.NewRegistry(),
		Session:   session.New("You are pi-go."),
		Policy:    runtime.DenyWrites,
		Observers: []events.Observer{rec},
		Now:       fixedClock(),
	})
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	run, err := agent.Start(ctx, "First question.")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := run.Follow("Second question."); err != nil {
		t.Fatalf("Follow: %v", err)
	}
	if err := run.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	if got := countKind(rec, events.KindModelChanged); got != 0 {
		t.Errorf("model_changed events = %d with no hook, want 0", got)
	}
	for index, req := range model.Requests() {
		if req.Model != "fast-model" {
			t.Errorf("request %d used %q with no hook, want fast-model", index, req.Model)
		}
	}
}
