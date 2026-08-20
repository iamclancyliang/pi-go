package ai

import (
	"context"
	"errors"
	"testing"
	"time"
)

// gatedProvider releases each chunk only when the test says so.
//
// The gate is what makes the timing observable. Without it a test sees the same
// events whether they were delivered as they arrived or buffered to the end and
// replayed, which is the difference the whole contract is about.
type gatedProvider struct {
	chunks   []Chunk
	release  []chan struct{}
	finished chan struct{}
}

func newGatedProvider(chunks ...Chunk) *gatedProvider {
	p := &gatedProvider{chunks: chunks, finished: make(chan struct{})}
	for range chunks {
		p.release = append(p.release, make(chan struct{}))
	}
	return p
}

func (p *gatedProvider) Generate(context.Context, Request) (Response, error) {
	return Response{}, errors.New("this provider only streams")
}

func (p *gatedProvider) Stream(ctx context.Context, _ Request) (<-chan StreamEvent, error) {
	out := make(chan StreamEvent)
	acc := NewAccumulator("fake-1")

	go func() {
		defer close(out)
		defer close(p.finished)

		start, err := acc.Begin()
		if err != nil {
			return
		}
		out <- start

		for i, chunk := range p.chunks {
			select {
			case <-p.release[i]:
			case <-ctx.Done():
				if event, err := acc.Fail(StopAborted, ctx.Err()); err == nil {
					out <- event
				}
				return
			}
			events, err := acc.Push(chunk)
			if err != nil {
				if event, failErr := acc.Fail(StopError, err); failErr == nil {
					out <- event
				}
				return
			}
			for _, event := range events {
				out <- event
			}
		}
		// Every block is closed before the reply ends: a normal ending with an
		// unfinished block is not something the protocol allows.
		for i := range p.chunks {
			if event, err := acc.Close(i); err == nil {
				out <- event
			}
		}
		if event, err := acc.Done(StopEnd, Usage{InputTokens: 1, OutputTokens: 2}); err == nil {
			out <- event
		}
	}()
	return out, nil
}

// TestDeliveryHappensBeforeTheStreamEnds is the only test here that a buffered
// implementation cannot pass.
//
// Event shape proves nothing about timing: a provider that collected the whole
// reply and then emitted a correct-looking sequence would satisfy every ordering
// and index rule. What it cannot do is show the first block while the second is
// still unsent — so that is what is asserted, with the later chunks held back.
func TestDeliveryHappensBeforeTheStreamEnds(t *testing.T) {
	provider := newGatedProvider(
		Chunk{Index: 0, Kind: BlockText, Delta: "first"},
		Chunk{Index: 1, Kind: BlockText, Delta: "second"},
	)

	events, err := provider.Stream(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	if got := (<-events).Kind; got != StreamStart {
		t.Fatalf("first event = %q, want %q", got, StreamStart)
	}

	// Release ONLY the first chunk. The second and the terminal stay withheld.
	close(provider.release[0])

	var sawFirst *AssistantMessage
	for event := range gather(t, events, 2) {
		if event.Kind == StreamTextDelta {
			sawFirst = event.Partial
		}
	}
	if sawFirst == nil {
		t.Fatal("the first block was not delivered while the rest was withheld: the " +
			"reply is being collected and replayed, not streamed")
	}
	if got := sawFirst.Blocks[0].Text; got != "first" {
		t.Errorf("first block = %q, want %q", got, "first")
	}
	if len(sawFirst.Blocks) != 1 {
		t.Errorf("blocks visible = %d, want 1: the second arrived before it was sent",
			len(sawFirst.Blocks))
	}

	// The stream is still open, which is the other half: it had not finished.
	select {
	case <-provider.finished:
		t.Fatal("the stream ended before the second chunk was released")
	default:
	}

	close(provider.release[1])
	var final *AssistantMessage
	for event := range events {
		if event.Terminal() {
			final = event.Final
		}
	}
	if final == nil || len(final.Blocks) != 2 {
		t.Fatalf("final = %+v, want two blocks", final)
	}
}

// gather reads n events, failing rather than hanging if they do not arrive.
func gather(t *testing.T, events <-chan StreamEvent, n int) chan StreamEvent {
	t.Helper()
	out := make(chan StreamEvent, n)
	for i := 0; i < n; i++ {
		select {
		case event, ok := <-events:
			if !ok {
				t.Fatal("the stream closed early")
			}
			out <- event
		case <-time.After(5 * time.Second):
			t.Fatalf("event %d never arrived", i)
		}
	}
	close(out)
	return out
}

// TestCancelEndsWithWhatArrived pins the stronger-than-Pi guarantee end to end.
func TestCancelEndsWithWhatArrived(t *testing.T) {
	provider := newGatedProvider(
		Chunk{Index: 0, Kind: BlockText, Delta: "half"},
		Chunk{Index: 1, Kind: BlockText, Delta: "never sent"},
	)
	ctx, cancel := context.WithCancel(context.Background())

	events, err := provider.Stream(ctx, Request{})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	<-events // start
	close(provider.release[0])
	for event := range gather(t, events, 2) {
		_ = event
	}

	cancel()

	var final *AssistantMessage
	var kind StreamEventKind
	for event := range events {
		if event.Terminal() {
			kind, final = event.Kind, event.Final
		}
	}
	if kind != StreamError {
		t.Fatalf("terminal = %q, want %q: cancelling is a failure, not a short success",
			kind, StreamError)
	}
	if final.StopReason != StopAborted {
		t.Errorf("reason = %q, want %q", final.StopReason, StopAborted)
	}
	if len(final.Blocks) != 1 || final.Blocks[0].Text != "half" {
		t.Errorf("final blocks = %+v, want the half that arrived", final.Blocks)
	}
}
