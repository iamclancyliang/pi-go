package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/events"
	"github.com/iamclancyliang/pi-go/internal/session"
)

// flakyStream fails its first attempt transiently — before it starts, after
// it starts, or after showing text — and answers on the next.
type flakyStream struct {
	calls     int
	preStart  bool
	afterText bool
}

func (f *flakyStream) Generate(context.Context, ai.Request) (ai.Response, error) {
	return ai.Response{}, nil
}

func (f *flakyStream) Stream(context.Context, ai.Request) (<-chan ai.StreamEvent, error) {
	f.calls++
	attempt := f.calls
	transient := &ai.ProviderError{Provider: "fake", Failure: ai.FailureTransient, Status: 503, Detail: "overloaded"}
	if attempt == 1 && f.preStart {
		return nil, transient
	}
	out := make(chan ai.StreamEvent)
	go func() {
		defer close(out)
		acc := ai.NewAccumulator("fake-1")
		start, _ := acc.Begin()
		out <- start
		if attempt == 1 {
			if f.afterText {
				events, _ := acc.Push(ai.Chunk{Index: 0, Kind: ai.BlockText, Delta: "already shown"})
				for _, e := range events {
					out <- e
				}
			}
			e, _ := acc.Fail(ai.StopError, transient)
			out <- e
			return
		}
		events, _ := acc.Push(ai.Chunk{Index: 0, Kind: ai.BlockText, Delta: "answer"})
		for _, e := range events {
			out <- e
		}
		e, _ := acc.Close(0)
		out <- e
		done, _ := acc.Done(ai.StopEnd, ai.Usage{})
		out <- done
	}()
	return out, nil
}

func retryingStreamPort(t *testing.T, inner ai.Port) (*observingPort, *Recorder) {
	t.Helper()
	port, rec := overflowPort(t, inner, session.New("You are pi-go."))
	port.retry = &retrier{policy: RetryPolicy{Enabled: true, MaxRetries: 3, BaseDelay: time.Millisecond,
		Sleep: func(context.Context, time.Duration) error { return nil }}}
	return port, rec
}

func endOf(t *testing.T, port *observingPort) (ai.StreamEventKind, *ai.AssistantMessage, int) {
	t.Helper()
	stream, err := port.Stream(context.Background(), ai.Request{})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var kind ai.StreamEventKind
	var final *ai.AssistantMessage
	starts := 0
	for event := range stream {
		if event.Kind == ai.StreamStart {
			starts++
		}
		if event.Terminal() {
			kind, final = event.Kind, event.Final
		}
	}
	return kind, final, starts
}

// TestAStreamThatFailsBeforeShowingAnythingIsRetried, whether it failed
// before starting or after, and the consumer is told of one reply, not two.
func TestAStreamThatFailsBeforeShowingAnythingIsRetried(t *testing.T) {
	for _, preStart := range []bool{true, false} {
		inner := &flakyStream{preStart: preStart}
		port, rec := retryingStreamPort(t, inner)

		kind, final, starts := endOf(t, port)
		if kind != ai.StreamDone || len(final.Blocks) != 1 || final.Blocks[0].Text != "answer" {
			t.Fatalf("preStart=%v: the retry did not replace the failure: %s %+v", preStart, kind, final)
		}
		if inner.calls != 2 || starts != 1 {
			t.Fatalf("preStart=%v: %d calls and %d starts, want 2 and 1", preStart, inner.calls, starts)
		}
		if got := countStreamKind(rec, events.KindRetryStart); got != 1 {
			t.Fatalf("preStart=%v: %d retry starts announced", preStart, got)
		}
		if got := countStreamKind(rec, events.KindRetryEnd); got != 1 {
			t.Fatalf("preStart=%v: %d retry ends announced", preStart, got)
		}
	}
}

// TestAStreamThatFailsAfterShowingSomethingIsNotRetried. The consumer already
// has those blocks, and a retry's would arrive after them as though one reply
// had said both things.
func TestAStreamThatFailsAfterShowingSomethingIsNotRetried(t *testing.T) {
	inner := &flakyStream{afterText: true}
	port, rec := retryingStreamPort(t, inner)

	kind, _, _ := endOf(t, port)
	if kind != ai.StreamError || inner.calls != 1 {
		t.Fatalf("a reply that had shown text was retried: %s after %d calls", kind, inner.calls)
	}
	if got := countStreamKind(rec, events.KindRetryStart); got != 0 {
		t.Fatalf("%d retries announced", got)
	}
}
