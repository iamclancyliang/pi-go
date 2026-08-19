package conformance

import (
	"context"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/events"
	"github.com/iamclancyliang/pi-go/internal/runtime"
	"github.com/iamclancyliang/pi-go/internal/session"
	"github.com/iamclancyliang/pi-go/internal/tools"
)

// timedTool finishes after a known delay, and declares its own scheduling.
//
// The delay is what lets a test separate completion order from source order: with
// tools that finish instantly the two coincide, and an implementation that
// confuses them passes.
type timedTool struct {
	name       string
	delay      time.Duration
	sequential bool
}

func (t *timedTool) Name() string        { return t.name }
func (t *timedTool) Description() string { return "test tool" }

func (t *timedTool) Execution() tools.Execution {
	return tools.Execution{Sequential: t.sequential, ReadOnly: true}
}

func (t *timedTool) Call(ctx context.Context, _ string) (string, error) {
	select {
	case <-time.After(t.delay):
		return t.name + " finished", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// runRound runs one model response worth of tool calls and returns the recorder.
func runRound(t testingT, registry *tools.Registry, calls ...ai.ToolCall) (*runtime.Recorder, *session.Session) {
	t.Helper()

	sess := session.New("You are pi-go.")
	model := &ai.Scripted{
		Name:                 "fake-1",
		Replies:              []ai.Response{ai.AssistantToolCalls(calls...)},
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
	if err := agent.Run(ctx, "run the tools"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return rec, sess
}

// testingT is the slice of *testing.T these helpers need.
type testingT interface {
	Helper()
	Fatalf(format string, args ...any)
}

// toolKinds keeps only the tool events, which is where the shapes differ.
func toolKinds(rec *runtime.Recorder) []events.Kind {
	var out []events.Kind
	for _, e := range rec.Events() {
		switch e.Kind {
		case events.KindToolStart, events.KindToolEnd, events.KindToolResult:
			out = append(out, e.Kind)
		}
	}
	return out
}

func idsOf(rec *runtime.Recorder, kind events.Kind) []string {
	var out []string
	for _, e := range rec.Events() {
		if e.Kind == kind {
			out = append(out, e.ToolCallID)
		}
	}
	return out
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

func sameKinds(a, b []events.Kind) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// runRoundWith runs one model response verbatim, so a test can set fields on it
// that a list of calls cannot express.
func runRoundWith(t testingT, registry *tools.Registry, reply ai.Response) (*runtime.Recorder, *session.Session) {
	t.Helper()

	sess := session.New("You are pi-go.")
	model := &ai.Scripted{
		Name:                 "fake-1",
		Replies:              []ai.Response{reply},
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
	if err := agent.Run(ctx, "run the tools"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return rec, sess
}
