package runtime

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/session"
	"github.com/iamclancyliang/pi-go/internal/tools"
)

// TestBeginDropsACutCallEvenWhenItWinsTheHandoff pins the case a select cannot
// decide for us.
//
// A waiting call is released by either the hand-off from the call before it or
// the cut. When both become ready together a select chooses at random, so the
// hand-off branch is taken about half the time — and on that branch nothing has
// marked the call, because the round marks a call only where it observes the cut
// itself. A cut call must not go on to run, whichever branch happened to win.
//
// Driven directly rather than through a run, because the window between the wait
// returning and the decision cannot be aimed at from outside: driving a whole run
// leaves the two events coinciding by chance, and rarely. Here the call is parked
// in the wait first and only then is the cut applied, so every iteration puts the
// question.
func TestBeginDropsACutCallEvenWhenItWinsTheHandoff(t *testing.T) {
	const attempts = 200

	for attempt := 0; attempt < attempts; attempt++ {
		batch := newToolBatch(
			newEmitter(nil, nil, nil, nil),
			session.New("You are pi-go."),
			func(context.Context, string, string, string) (string, bool) { return "", false },
			func(string) (tools.ReplayPolicy, string, bool) {
				return tools.ReplayNever, "v1", true
			},
		)

		ctx, cancel := context.WithCancel(context.Background())
		batch.register(ctx, []ai.ToolCall{
			{ID: "call-1", Name: "first", Args: `{}`},
			{ID: "call-2", Name: "second", Args: `{}`},
		}, func(string) bool { return true }, false)

		batch.mu.Lock()
		gate := batch.gates[1]
		batch.mu.Unlock()
		batch.afterHandoff = cancel

		// call-2 parks in the wait while the context is still live, so the check
		// before the wait cannot answer for this case.
		var decided beginResult
		var waiting sync.WaitGroup
		waiting.Add(1)
		go func() {
			defer waiting.Done()
			decided = batch.begin(ctx, "call-2")
		}()
		time.Sleep(time.Millisecond)

		// The turn is handed over while the context is still live, so the wait
		// returns through the hand-off. The cut then lands in the window the
		// seam opens — before the round has decided anything about this call.
		//
		// Racing the two instead would ask for something no design can promise:
		// a call released before the cut is visible has already started, and
		// starting it is correct. What must hold is that a call which learns of
		// the cut before it runs does not run.
		close(gate)
		waiting.Wait()

		if !decided.Dropped {
			t.Fatalf("attempt %d: a cut call was allowed to run (result %+v); it won "+
				"the hand-off race, and nothing else marks a call the round did not "+
				"observe being cut", attempt+1, decided)
		}
		cancel()
	}
}
