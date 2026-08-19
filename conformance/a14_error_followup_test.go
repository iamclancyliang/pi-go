package conformance

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/events"
	"github.com/iamclancyliang/pi-go/internal/runtime"
	"github.com/iamclancyliang/pi-go/internal/session"
	"github.com/iamclancyliang/pi-go/internal/tools"
)

// TestA14FollowUpQueuedDuringAnErroredTurnIsNotConsumed pins that a failing turn
// stops the agent and leaves the queue alone.
//
// The tempting assumption is that a queue always drains. It does not: a turn that
// ends in an error stops the agent without looking for a follow-up, so anything
// queued while it was failing stays unconsumed. A caller that assumed otherwise
// would silently lose the message, or worse, act on it a turn later than the user
// believes.
func TestA14FollowUpQueuedDuringAnErroredTurnIsNotConsumed(t *testing.T) {
	model := newFailingPort(errors.New("provider exploded"))
	sess := session.New("You are pi-go.")
	rec := runtime.NewRecorder()

	agent, err := runtime.New(runtime.Config{
		Model:     model,
		ModelName: "fake-1",
		Tools:     tools.NewRegistry(),
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

	run, err := agent.Start(ctx, "Do the thing.")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Queue the follow-up while the turn is still in flight, then let it fail.
	model.waitEntered(t)
	const followUp = "ACTUALLY-DO-THIS-INSTEAD"
	if err := run.Follow(followUp); err != nil {
		t.Fatalf("Follow: %v", err)
	}
	model.release()

	// The run reports the failure rather than swallowing it.
	if err := run.Wait(); err == nil {
		t.Error("an errored turn reported success")
	}

	// The follow-up was never consumed: no request carries it.
	for index, req := range model.Requests() {
		for _, m := range req.Messages {
			if strings.Contains(m.Content, followUp) {
				t.Errorf("request %d consumed the follow-up queued during a failing turn", index)
			}
		}
	}

	// And the agent stopped, rather than continuing to a turn that would.
	kinds := rec.Kinds()
	if got := count(kinds, events.KindTurnStart); got != 1 {
		t.Errorf("turn_start count = %d, want 1: the agent continued past a failing turn", got)
	}
	if got := count(kinds, events.KindAgentEnd); got != 1 {
		t.Errorf("agent_end count = %d, want 1: %v", got, kinds)
	}

	// The turn that failed carries no tool results, because none settled.
	for _, e := range rec.Events() {
		if e.Kind == events.KindTurnEnd && len(e.Detail.ToolCallIDs) > 0 {
			t.Errorf("a failing turn reported tool results: %v", e.Detail.ToolCallIDs)
		}
	}
}

func count(kinds []events.Kind, want events.Kind) int {
	n := 0
	for _, k := range kinds {
		if k == want {
			n++
		}
	}
	return n
}

// failingPort blocks until released, then fails, so a test can act while the turn
// is still in flight.
type failingPort struct {
	err      error
	entered  chan struct{}
	released chan struct{}
	once     sync.Once

	mu       sync.Mutex
	requests []ai.Request
}

func newFailingPort(err error) *failingPort {
	return &failingPort{
		err:      err,
		entered:  make(chan struct{}),
		released: make(chan struct{}),
	}
}

func (p *failingPort) Generate(ctx context.Context, req ai.Request) (ai.Response, error) {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	p.mu.Unlock()

	p.once.Do(func() { close(p.entered) })
	select {
	case <-p.released:
	case <-ctx.Done():
		return ai.Response{}, ctx.Err()
	}
	return ai.Response{}, p.err
}

func (p *failingPort) waitEntered(t *testing.T) {
	t.Helper()
	select {
	case <-p.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the model was never called")
	}
}

func (p *failingPort) release() { close(p.released) }

func (p *failingPort) Requests() []ai.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]ai.Request(nil), p.requests...)
}
