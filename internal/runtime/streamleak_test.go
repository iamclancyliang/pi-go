package runtime

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/events"
	"github.com/iamclancyliang/pi-go/internal/session"
)

// endlessStream keeps producing until its context is cancelled.
type endlessStream struct{}

func (endlessStream) Generate(context.Context, ai.Request) (ai.Response, error) {
	return ai.Response{}, nil
}

func (endlessStream) Stream(ctx context.Context, _ ai.Request) (<-chan ai.StreamEvent, error) {
	out := make(chan ai.StreamEvent)
	go func() {
		defer close(out)
		acc := ai.NewAccumulator("fake-1")
		start, err := acc.Begin()
		if err != nil {
			return
		}
		select {
		case out <- start:
		case <-ctx.Done():
			return
		}
		for i := 0; ; i++ {
			events, err := acc.Push(ai.Chunk{Index: 0, Kind: ai.BlockText, Delta: "x"})
			if err != nil {
				return
			}
			for _, e := range events {
				select {
				case out <- e:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, nil
}

// TestAbandoningAStreamDoesNotLeak pins that nobody is left holding a reply the
// caller walked away from.
//
// The forwarding goroutine sends into a channel. A reader that stops reading —
// which is what an abandoned run looks like — would leave that send blocked
// forever, and with it the provider's own goroutine, for the life of the process.
func TestAbandoningAStreamDoesNotLeak(t *testing.T) {
	before := runtime.NumGoroutine()

	for i := 0; i < 20; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		port := &observingPort{
			inner:     endlessStream{},
			emitter:   newEmitter(func() time.Time { return time.Unix(0, 0) }, []events.Observer{NewRecorder()}, nil),
			session:   session.New("You are pi-go."),
			modelName: "fake-1",
		}
		stream, err := port.Stream(ctx, ai.Request{})
		if err != nil {
			t.Fatalf("Stream: %v", err)
		}
		// Read one event, then walk away without draining.
		<-stream
		cancel()
	}

	// Give the abandoned goroutines a chance to notice and exit.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= before+2 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("goroutines went from %d to %d and stayed: abandoned streams are still "+
		"holding their sends", before, runtime.NumGoroutine())
}
