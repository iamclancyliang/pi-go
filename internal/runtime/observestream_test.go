package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/events"
	"github.com/iamclancyliang/pi-go/internal/session"
)

func observedStreamingPort(t *testing.T, inner ai.Port, sess *session.Session) (*observingPort, *Recorder) {
	t.Helper()
	rec := NewRecorder()
	return &observingPort{
		inner:     inner,
		emitter:   newEmitter(func() time.Time { return time.Unix(0, 0) }, nil, []events.Observer{rec}, nil),
		session:   sess,
		modelName: "fake-1",
	}, rec
}

// countStreamKind counts events of one kind on the recorder.
func countStreamKind(rec *Recorder, kind events.Kind) int {
	n := 0
	for _, e := range rec.Events() {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

// TestTheObservingPortStreams pins that the guards are not bypassed and, more
// basically, that streaming is reachable at all.
//
// The model adapter asks whether its port streams. The observing port wraps the
// provider, so if it does not answer yes, every reply falls back to arriving in
// one piece and the streaming implementation is never reached by anything.
func TestTheObservingPortStreams(t *testing.T) {
	port, rec := observedStreamingPort(t, &streamingFake{chunks: []ai.Chunk{
		{Index: 0, Kind: ai.BlockText, Delta: "hi"},
	}}, session.New("You are pi-go."))

	if _, ok := any(port).(ai.StreamingPort); !ok {
		t.Fatal("the observing port does not stream, so the adapter will silently " +
			"fall back and nothing will ever reach the streaming path")
	}

	stream, err := port.Stream(context.Background(), ai.Request{})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var terminal ai.StreamEvent
	for event := range stream {
		if event.Terminal() {
			terminal = event
		}
	}
	if terminal.Kind != ai.StreamDone {
		t.Fatalf("terminal = %q, want %q", terminal.Kind, ai.StreamDone)
	}

	// The run-level surface still describes the call, once each.
	if got := countStreamKind(rec, events.KindModelRequest); got != 1 {
		t.Errorf("model_request = %d, want 1", got)
	}
	if got := countStreamKind(rec, events.KindModelResponse); got != 1 {
		t.Errorf("model_response = %d, want 1", got)
	}
	// And it is not flooded with the reply's own protocol.
	if got := len(rec.Events()); got > 4 {
		t.Errorf("run events = %d: the reply's deltas are being republished onto the "+
			"stream a client watches for lifecycle", got)
	}
}

// TestAStandingFailureIsNotStreamedAround pins that the terminal-state guard
// covers this path too.
//
// A guard that only the whole-answer path applies is not a guard: the same
// question would be asked again simply by asking it a different way.
func TestAStandingFailureIsNotStreamedAround(t *testing.T) {
	sess := session.New("You are pi-go.")
	if err := sess.Fail(CodeContextOverflow, "recovery already spent"); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	port, _ := observedStreamingPort(t, &streamingFake{chunks: []ai.Chunk{
		{Index: 0, Kind: ai.BlockText, Delta: "should never run"},
	}}, sess)

	if _, err := port.Stream(context.Background(), ai.Request{}); err == nil {
		t.Fatal("a session that already failed terminally was asked again by " +
			"streaming instead of generating")
	}
}

// TestASubstitutionIsReportedWhenStreaming pins that the model-change contract
// holds on this path as well.
func TestASubstitutionIsReportedWhenStreaming(t *testing.T) {
	port, rec := observedStreamingPort(t, &substitutingStream{serves: "fallback-9"},
		session.New("You are pi-go."))

	stream, err := port.Stream(context.Background(), ai.Request{})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range stream {
	}

	var changes []events.Event
	for _, e := range rec.Events() {
		if e.Kind == events.KindModelChanged {
			changes = append(changes, e)
		}
	}
	if len(changes) != 1 {
		t.Fatalf("model_changed = %d, want 1: a reply served by another model was "+
			"not reported", len(changes))
	}
	if changes[0].Detail.From != "fake-1" || changes[0].Detail.To != "fallback-9" {
		t.Errorf("model_changed = %q -> %q, want %q -> %q",
			changes[0].Detail.From, changes[0].Detail.To, "fake-1", "fallback-9")
	}
}

// substitutingStream answers by streaming, from a model other than the one asked
// for.
type substitutingStream struct{ serves string }

func (s *substitutingStream) Generate(context.Context, ai.Request) (ai.Response, error) {
	return ai.Response{}, errors.New("this fake only streams")
}

func (s *substitutingStream) Stream(context.Context, ai.Request) (<-chan ai.StreamEvent, error) {
	out := make(chan ai.StreamEvent)
	acc := ai.NewAccumulator(s.serves)
	// Written from a goroutine: a chunk produces more than one event, so any
	// fixed buffer is a guess and a wrong guess deadlocks the producer.
	go func() {
		defer close(out)
		start, err := acc.Begin()
		if err != nil {
			return
		}
		out <- start
		events, err := acc.Push(ai.Chunk{Index: 0, Kind: ai.BlockText, Delta: "answered"})
		if err != nil {
			return
		}
		for _, e := range events {
			out <- e
		}
		if closed, err := acc.Close(0); err == nil {
			out <- closed
		}
		if done, err := acc.Done(ai.StopEnd, ai.Usage{}); err == nil {
			out <- done
		}
	}()
	return out, nil
}
