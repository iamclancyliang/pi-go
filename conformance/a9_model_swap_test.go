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

// TestA9NoHookNoChange pins the quiet case.
//
// With nothing selecting a model per turn, the model never changes and no change
// is reported. A runtime that announced one every turn, or reported a switch it
// never made, would be indistinguishable from a working one without this.
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

// TestA9AProviderThatServesADifferentModelIsReported pins the case where the
// answer did not come from the model that was asked for.
//
// Nothing else can report it. The framework carries the request and the response
// without comparing them, and the provider is under no obligation to refuse: it
// may answer with whatever it actually ran. A caller billing per model, or
// reading a transcript to see what produced an answer, has only this event to go
// on — so an unreported substitution reads as though the requested model replied.
func TestA9AProviderThatServesADifferentModelIsReported(t *testing.T) {
	substituting := &servingModel{name: "fake-1", serves: "fallback-9"}

	rec := runtime.NewRecorder()
	agent, err := runtime.New(runtime.Config{
		Model:     substituting,
		ModelName: "fake-1",
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
	if err := agent.Run(ctx, "answer"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var changes []events.Event
	for _, e := range rec.Events() {
		if e.Kind == events.KindModelChanged {
			changes = append(changes, e)
		}
	}
	if len(changes) != 1 {
		t.Fatalf("model_changed events = %d, want 1: a substitution nobody reports "+
			"reads as the requested model having answered", len(changes))
	}
	if changes[0].Detail.From != "fake-1" || changes[0].Detail.To != "fallback-9" {
		t.Errorf("model_changed = %q -> %q, want %q -> %q: the event has to name both "+
			"ends or it cannot say what was substituted for what",
			changes[0].Detail.From, changes[0].Detail.To, "fake-1", "fallback-9")
	}
}

// TestA9AProviderThatServesWhatWasAskedIsSilent pins the other direction.
//
// When the model that served the call is the one that was asked for, nothing was
// substituted and no change is reported. A runtime that announces one on every
// call would write substitutions into the event stream that never happened.
func TestA9AProviderThatServesWhatWasAskedIsSilent(t *testing.T) {
	rec := runtime.NewRecorder()
	agent, err := runtime.New(runtime.Config{
		Model:     &servingModel{name: "fake-1", serves: "fake-1"},
		ModelName: "fake-1",
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
	if err := agent.Run(ctx, "answer"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, e := range rec.Events() {
		if e.Kind == events.KindModelChanged {
			t.Errorf("a call served by the model that was asked for reported a "+
				"change %q -> %q", e.Detail.From, e.Detail.To)
		}
	}
}

// servingModel answers, and says which model actually served the call.
type servingModel struct {
	name   string
	serves string
}

func (s *servingModel) Generate(_ context.Context, _ ai.Request) (ai.Response, error) {
	return ai.Response{Content: "answered", Model: s.serves}, nil
}

// TestA9ReselectingTheCurrentModelIsNotAChange pins that a change means a
// difference.
//
// A hook that pins the model every turn is asking for the same thing each time.
// Reporting that as a change on every turn would fill the stream with events that
// describe nothing happening, and a consumer counting them would see a run
// switching models constantly while it never switched at all.
func TestA9ReselectingTheCurrentModelIsNotAChange(t *testing.T) {
	turns := 0
	rec := runtime.NewRecorder()
	agent, err := runtime.New(runtime.Config{
		Model: &ai.Scripted{
			Name: "pinned-model",
			Replies: []ai.Response{ai.AssistantToolCalls(
				ai.ToolCall{ID: "call-1", Name: "quick_tool", Args: `{}`},
			)},
			StopWhenToolsSettled: true,
			Final:                ai.AssistantText("done"),
		},
		ModelName: "pinned-model",
		Tools:     registryWith(&timedTool{name: "quick_tool", delay: time.Millisecond}),
		Session:   session.New("You are pi-go."),
		Policy:    runtime.DenyWrites,
		Observers: []events.Observer{rec},
		Now:       fixedClock(),
		// Asks for the model already in use, every turn.
		PrepareNextTurn: func(context.Context) runtime.NextTurn {
			turns++
			return runtime.NextTurn{ModelName: "pinned-model"}
		},
	})
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := agent.Run(ctx, "answer"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if turns == 0 {
		t.Fatal("the hook was never asked, so this test says nothing about what it " +
			"returned")
	}
	for _, e := range rec.Events() {
		if e.Kind == events.KindModelChanged {
			t.Errorf("re-selecting the model already in use reported a change %q -> %q",
				e.Detail.From, e.Detail.To)
		}
	}
}

// registryWith is a registry holding exactly the given tools.
func registryWith(ts ...tools.Tool) *tools.Registry {
	r := tools.NewRegistry()
	for _, t := range ts {
		r.MustRegister(t)
	}
	return r
}
