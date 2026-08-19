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

// TestA7EveryCallMustAgreeToTerminate pins that a round stops only by consensus.
//
// One call cannot end the conversation, because it cannot know what the others
// were asked to do: stopping on the first request would discard work the model is
// still waiting for. Reading the rule as "any call may terminate" is the natural
// assumption and the inverse of what it says.
//
// Both directions are asserted, because either alone is satisfied by a runtime
// that ignores the request entirely, or by one that always stops.
func TestA7EveryCallMustAgreeToTerminate(t *testing.T) {
	t.Run("every call agrees: no further model call", func(t *testing.T) {
		rec, sess, model := runTerminationRound(t, true, true)

		if got := countKind(rec, events.KindModelRequest); got != 1 {
			t.Errorf("model requests = %d, want 1: the round asked to stop, so the "+
				"results must not go back to the model", got)
		}
		// The work still happened and is still recorded: stopping is not
		// discarding.
		if got := len(idsOf(rec, events.KindToolResult)); got != 2 {
			t.Errorf("tool results = %d, want 2", got)
		}
		assertToolTruth(t, sess, "call-a", "call-b")
		_ = model
	})

	t.Run("one call disagrees: the conversation continues", func(t *testing.T) {
		rec, sess, _ := runTerminationRound(t, true, false)

		if got := countKind(rec, events.KindModelRequest); got < 2 {
			t.Errorf("model requests = %d, want at least 2: one call did not ask to "+
				"stop, so the round does not end the conversation", got)
		}
		if got := len(idsOf(rec, events.KindToolResult)); got != 2 {
			t.Errorf("tool results = %d, want 2", got)
		}
		assertToolTruth(t, sess, "call-a", "call-b")
	})
}

// runTerminationRound runs one round of two calls, each asking to stop or not.
func runTerminationRound(t *testing.T, aTerminates, bTerminates bool) (*runtime.Recorder, *session.Session, *ai.Scripted) {
	t.Helper()

	registry := tools.NewRegistry()
	registry.MustRegister(&terminatingTool{name: "tool_a", terminate: aTerminates})
	registry.MustRegister(&terminatingTool{name: "tool_b", terminate: bTerminates})

	sess := session.New("You are pi-go.")
	model := &ai.Scripted{
		Name: "fake-1",
		Replies: []ai.Response{ai.AssistantToolCalls(
			ai.ToolCall{ID: "call-a", Name: "tool_a", Args: `{}`},
			ai.ToolCall{ID: "call-b", Name: "tool_b", Args: `{}`},
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
	if err := agent.Run(ctx, "run both"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return rec, sess, model
}

// assertToolTruth checks the transcript kept every result, in source order.
//
// A stop that loses results would leave a resumed conversation missing work that
// was done, which is worse than not stopping at all.
func assertToolTruth(t *testing.T, sess *session.Session, want ...string) {
	t.Helper()
	var got []string
	for _, m := range sess.Truth() {
		if m.Role == ai.RoleTool {
			got = append(got, m.ToolCallID)
		}
	}
	if !equal(got, want) {
		t.Errorf("tool messages in history = %v, want %v", got, want)
	}
}

// terminatingTool reports whether its result asks the loop to stop.
type terminatingTool struct {
	name      string
	terminate bool
}

func (t *terminatingTool) Name() string        { return t.name }
func (t *terminatingTool) Description() string { return "test tool" }

func (t *terminatingTool) Execution() tools.Execution {
	return tools.Execution{Sequential: false, ReadOnly: true}
}

func (t *terminatingTool) Call(context.Context, string) (tools.Result, error) {
	return tools.Result{Content: t.name + " done", Terminate: t.terminate}, nil
}
