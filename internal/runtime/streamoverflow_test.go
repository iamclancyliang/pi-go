package runtime

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/events"
	"github.com/iamclancyliang/pi-go/internal/session"
)

// overflowingStream refuses the first attempt for size, then answers.
type overflowingStream struct {
	calls   int
	deliver bool
}

func (o *overflowingStream) Generate(context.Context, ai.Request) (ai.Response, error) {
	return ai.Response{}, errors.New("this fake only streams")
}

func (o *overflowingStream) Stream(context.Context, ai.Request) (<-chan ai.StreamEvent, error) {
	o.calls++
	attempt := o.calls
	out := make(chan ai.StreamEvent)
	go func() {
		defer close(out)
		acc := ai.NewAccumulator("fake-1")
		start, err := acc.Begin()
		if err != nil {
			return
		}
		out <- start

		if attempt == 1 {
			// Optionally show something before failing, which is what makes a
			// silent retry unsafe.
			if o.deliver {
				if events, err := acc.Push(ai.Chunk{
					Index: 0, Kind: ai.BlockText, Delta: "already shown",
				}); err == nil {
					for _, e := range events {
						out <- e
					}
				}
			}
			refusal := fmt.Errorf("provider refused: %w", ai.ErrContextOverflow)
			if e, err := acc.Fail(ai.StopError, refusal); err == nil {
				out <- e
			}
			return
		}

		if events, err := acc.Push(ai.Chunk{
			Index: 0, Kind: ai.BlockText, Delta: "shorter answer",
		}); err == nil {
			for _, e := range events {
				out <- e
			}
		}
		if e, err := acc.Close(0); err == nil {
			out <- e
		}
		if e, err := acc.Done(ai.StopEnd, ai.Usage{}); err == nil {
			out <- e
		}
	}()
	return out, nil
}

func overflowPort(t *testing.T, inner ai.Port, sess *session.Session) (*observingPort, *Recorder) {
	t.Helper()
	rec := NewRecorder()
	return &observingPort{
		inner:     inner,
		emitter:   newEmitter(func() time.Time { return time.Unix(0, 0) }, nil, []events.Observer{rec}, nil),
		session:   sess,
		modelName: "fake-1",
		summarize: func(context.Context, []ai.Message) (string, []ai.Message, error) {
			return "summary of what came before", nil, nil
		},
	}, rec
}

// TestAnOverflowBeforeAnythingIsShownIsRetried pins that the budget applies here
// too.
func TestAnOverflowBeforeAnythingIsShownIsRetried(t *testing.T) {
	inner := &overflowingStream{}
	port, _ := overflowPort(t, inner, session.New("You are pi-go."))

	stream, err := port.Stream(context.Background(), ai.Request{})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var final *ai.AssistantMessage
	var kind ai.StreamEventKind
	for event := range stream {
		if event.Terminal() {
			kind, final = event.Kind, event.Final
		}
	}

	if kind != ai.StreamDone {
		t.Fatalf("terminal = %q, want %q: the shortened retry did not replace the "+
			"refusal", kind, ai.StreamDone)
	}
	if inner.calls != 2 {
		t.Errorf("provider calls = %d, want 2: one refused, one after shortening",
			inner.calls)
	}
	if len(final.Blocks) != 1 || final.Blocks[0].Text != "shorter answer" {
		t.Errorf("final = %+v, want only the retry's answer", final.Blocks)
	}
}

// TestAnOverflowAfterSomethingWasShownIsNotRetried pins the boundary the
// whole-answer path never meets.
//
// A retry is a second attempt at the same reply. Once blocks have reached the
// consumer, the retry's blocks would arrive after them as though one reply had
// said both things — so the attempt fails instead, keeping what was shown.
func TestAnOverflowAfterSomethingWasShownIsNotRetried(t *testing.T) {
	inner := &overflowingStream{deliver: true}
	port, _ := overflowPort(t, inner, session.New("You are pi-go."))

	stream, err := port.Stream(context.Background(), ai.Request{})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var final *ai.AssistantMessage
	var kind ai.StreamEventKind
	for event := range stream {
		if event.Terminal() {
			kind, final = event.Kind, event.Final
		}
	}

	if inner.calls != 1 {
		t.Fatalf("provider calls = %d, want 1: a retry spliced a second attempt "+
			"onto a reply the consumer had already started reading", inner.calls)
	}
	if kind != ai.StreamError {
		t.Errorf("terminal = %q, want %q", kind, ai.StreamError)
	}
	if len(final.Blocks) != 1 || final.Blocks[0].Text != "already shown" {
		t.Errorf("final = %+v, want what had already been shown", final.Blocks)
	}
}

// TestASecondOverflowEndsTheOperation pins that the budget is one attempt.
func TestASecondOverflowEndsTheOperation(t *testing.T) {
	sess := session.New("You are pi-go.")
	port, rec := overflowPort(t, &alwaysOverflowing{}, sess)

	stream, err := port.Stream(context.Background(), ai.Request{})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for event := range stream {
		_ = event
	}

	if sess.Failure() == nil {
		t.Fatal("a second refusal did not end the operation, so reopening would " +
			"start the same losing attempt again")
	}
	if got := sess.Failure().Code; got != CodeContextOverflow {
		t.Errorf("code = %q, want %q", got, CodeContextOverflow)
	}
	if got := countStreamKind(rec, events.KindModelRequest); got != 2 {
		t.Errorf("model_request = %d, want 2: one original and one after shortening", got)
	}
}

// alwaysOverflowing refuses every attempt.
type alwaysOverflowing struct{}

func (alwaysOverflowing) Generate(context.Context, ai.Request) (ai.Response, error) {
	return ai.Response{}, errors.New("this fake only streams")
}

func (alwaysOverflowing) Stream(context.Context, ai.Request) (<-chan ai.StreamEvent, error) {
	out := make(chan ai.StreamEvent)
	go func() {
		defer close(out)
		acc := ai.NewAccumulator("fake-1")
		start, err := acc.Begin()
		if err != nil {
			return
		}
		out <- start
		refusal := fmt.Errorf("provider refused: %w", ai.ErrContextOverflow)
		if e, err := acc.Fail(ai.StopError, refusal); err == nil {
			out <- e
		}
	}()
	return out, nil
}

// misleadingError fails with a message that quotes the overflow wording without
// being an overflow.
//
// The wording is what a text match would key on, so this is the case that
// separates classifying a failure from reading about one.
type misleadingError struct{}

func (misleadingError) Generate(context.Context, ai.Request) (ai.Response, error) {
	return ai.Response{}, errors.New("this fake only streams")
}

func (misleadingError) Stream(context.Context, ai.Request) (<-chan ai.StreamEvent, error) {
	out := make(chan ai.StreamEvent)
	go func() {
		defer close(out)
		acc := ai.NewAccumulator("fake-1")
		start, err := acc.Begin()
		if err != nil {
			return
		}
		out <- start
		// Not wrapped: a plain failure whose text happens to quote the phrase.
		refusal := errors.New("upstream said: " + ai.ErrContextOverflow.Error())
		if e, err := acc.Fail(ai.StopError, refusal); err == nil {
			out <- e
		}
	}()
	return out, nil
}

// TestAFailureThatOnlyReadsLikeAnOverflowIsNotRecovered pins that recovery keys
// on the failure, not on its wording.
//
// Spending the recovery budget on an unrelated failure shortens the context for
// nothing and leaves a real overflow with no allowance left.
func TestAFailureThatOnlyReadsLikeAnOverflowIsNotRecovered(t *testing.T) {
	sess := session.New("You are pi-go.")
	port, rec := overflowPort(t, misleadingError{}, sess)

	stream, err := port.Stream(context.Background(), ai.Request{})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range stream {
	}

	if got := sess.OverflowAttempts(); got != 0 {
		t.Errorf("recovery attempts = %d, want 0: an unrelated failure spent the "+
			"budget because its text quoted the overflow wording", got)
	}
	if got := countStreamKind(rec, events.KindModelRequest); got != 1 {
		t.Errorf("model_request = %d, want 1: the context was shortened and the "+
			"model asked again for a failure that had nothing to do with size", got)
	}
}

// TestARetriedReplyBeginsOnce pins that recovery does not describe two replies.
//
// The consumer was told this reply began before the first attempt was refused.
// Announcing it again would have it render one answer as two, and the second
// start carries a partial that contradicts the first.
func TestARetriedReplyBeginsOnce(t *testing.T) {
	port, _ := overflowPort(t, &overflowingStream{}, session.New("You are pi-go."))

	stream, err := port.Stream(context.Background(), ai.Request{})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	starts, terminals := 0, 0
	for event := range stream {
		switch {
		case event.Kind == ai.StreamStart:
			starts++
		case event.Terminal():
			terminals++
		}
	}
	if starts != 1 {
		t.Errorf("observable starts = %d, want 1: one reply was described as two", starts)
	}
	if terminals != 1 {
		t.Errorf("terminals = %d, want 1", terminals)
	}
}
