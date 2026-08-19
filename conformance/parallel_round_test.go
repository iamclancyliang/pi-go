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

// TestParallelRoundOrdering pins the shape a round of parallel-safe calls emits.
//
// It is the other half of the sequential trace: there, each call finishes before
// the next starts. Here every start precedes any result, ends follow COMPLETION
// and results follow the order the model asked for them. Checking only one of the
// two shapes leaves the other free to be wrong, and they are not the same shape
// with a different amount of concurrency.
//
// The two calls finish in the OPPOSITE order to the one requested, which is what
// makes the last assertion mean anything: results collected as calls complete
// would pass every other check in this test.
func TestParallelRoundOrdering(t *testing.T) {
	slow := &timedTool{name: "slow_tool", delay: 120 * time.Millisecond}
	fast := &timedTool{name: "fast_tool", delay: 1 * time.Millisecond}

	registry := tools.NewRegistry()
	registry.MustRegister(slow)
	registry.MustRegister(fast)

	sess := session.New("You are pi-go.")
	model := &ai.Scripted{
		Name: "fake-1",
		Replies: []ai.Response{
			ai.AssistantToolCalls(
				ai.ToolCall{ID: "call-slow", Name: "slow_tool", Args: `{}`},
				ai.ToolCall{ID: "call-fast", Name: "fast_tool", Args: `{}`},
			),
		},
		StopWhenToolsSettled: true,
		Final:                ai.AssistantText("both done"),
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

	var starts, ends, results []string
	lastStart, firstResult := -1, -1
	for index, e := range rec.Events() {
		switch e.Kind {
		case events.KindToolStart:
			starts = append(starts, e.ToolCallID)
			lastStart = index
		case events.KindToolEnd:
			ends = append(ends, e.ToolCallID)
		case events.KindToolResult:
			results = append(results, e.ToolCallID)
			if firstResult < 0 {
				firstResult = index
			}
		}
	}

	// Every start is announced before any result. In the sequential shape this
	// is false by construction, which is what distinguishes the two.
	if lastStart > firstResult {
		t.Errorf("a result was emitted before the last start: %v", rec.Kinds())
	}
	if want := []string{"call-slow", "call-fast"}; !equal(starts, want) {
		t.Errorf("starts = %v, want source order %v", starts, want)
	}

	// Ends follow completion. The slow call was requested first and finishes
	// last, so an end order matching source order would mean ends are not
	// reporting completion at all.
	if want := []string{"call-fast", "call-slow"}; !equal(ends, want) {
		t.Errorf("ends = %v, want completion order %v", ends, want)
	}

	// Results follow the order the model asked for them, not the order they
	// happened to finish in.
	if want := []string{"call-slow", "call-fast"}; !equal(results, want) {
		t.Errorf("results = %v, want source order %v", results, want)
	}

	// History carries the same order as the result events: a transcript in
	// completion order would replay the round differently from how it was
	// requested.
	var recorded []string
	for _, m := range sess.Truth() {
		if m.Role == ai.RoleTool {
			recorded = append(recorded, m.ToolCallID)
		}
	}
	if want := []string{"call-slow", "call-fast"}; !equal(recorded, want) {
		t.Errorf("tool messages in history = %v, want source order %v", recorded, want)
	}
}

// timedTool is parallel-safe and finishes after a known delay.
type timedTool struct {
	name  string
	delay time.Duration
}

func (t *timedTool) Name() string        { return t.name }
func (t *timedTool) Description() string { return "test tool" }

func (t *timedTool) Execution() tools.Execution {
	return tools.Execution{Sequential: false, ReadOnly: true}
}

func (t *timedTool) Call(ctx context.Context, _ string) (string, error) {
	select {
	case <-time.After(t.delay):
		return t.name + " finished", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func equal(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
