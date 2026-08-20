package conformance

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/events"
	"github.com/iamclancyliang/pi-go/internal/runtime"
	"github.com/iamclancyliang/pi-go/internal/session"
	"github.com/iamclancyliang/pi-go/internal/tools"
)

// streamOnlyModel answers only by streaming.
//
// Refusing Generate is the whole point: a runtime that quietly falls back to
// whole answers passes every test written against a provider that can do both,
// and this one makes that fallback visible as a failure.
type streamOnlyModel struct {
	blocks []ai.Chunk
	calls  int
}

func (s *streamOnlyModel) Generate(context.Context, ai.Request) (ai.Response, error) {
	return ai.Response{}, errors.New("this model streams and was asked for a whole answer")
}

func (s *streamOnlyModel) Stream(_ context.Context, _ ai.Request) (<-chan ai.StreamEvent, error) {
	s.calls++
	out := make(chan ai.StreamEvent)
	go func() {
		defer close(out)
		acc := ai.NewAccumulator("fake-1")
		start, err := acc.Begin()
		if err != nil {
			return
		}
		out <- start
		open := -1
		for _, c := range s.blocks {
			if open >= 0 && open != c.Index {
				if e, err := acc.Close(open); err == nil {
					out <- e
				}
			}
			events, err := acc.Push(c)
			if err != nil {
				return
			}
			for _, e := range events {
				out <- e
			}
			open = c.Index
		}
		if open >= 0 {
			if e, err := acc.Close(open); err == nil {
				out <- e
			}
		}
		if e, err := acc.Done(ai.StopEnd, ai.Usage{}); err == nil {
			out <- e
		}
	}()
	return out, nil
}

// TestARunActuallyStreams pins that the feature is reachable from the entry point
// callers use.
//
// Everything below the runtime can be correct while the runtime never asks for
// it. Testing the port and the adapter directly proves they work; only a run
// proves they are used.
func TestARunActuallyStreams(t *testing.T) {
	model := &streamOnlyModel{blocks: []ai.Chunk{
		{Index: 0, Kind: ai.BlockText, Delta: "the answer"},
	}}
	sess := session.New("You are pi-go.")

	agent, err := runtime.New(runtime.Config{
		Model:     model,
		ModelName: "fake-1",
		Tools:     tools.NewRegistry(),
		Session:   sess,
		Policy:    runtime.DenyWrites,
		Observers: []events.Observer{runtime.NewRecorder()},
		Now:       fixedClock(),
	})
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := agent.Run(ctx, "ask something"); err != nil {
		t.Fatalf("Run: %v: the runtime did not stream, so it asked a streaming-only "+
			"model for a whole answer", err)
	}
	if model.calls == 0 {
		t.Fatal("the model was never streamed from: streaming is implemented and " +
			"unreachable from a run")
	}
}

// TestAStreamedReplyBecomesHistory pins that a streamed reply is owned by the
// session exactly as a whole one is.
//
// A reply the model gave and the record does not hold is lost work: the next turn
// is built from history, so anything missing there was never said as far as the
// conversation is concerned.
func TestAStreamedReplyBecomesHistory(t *testing.T) {
	model := &streamOnlyModel{blocks: []ai.Chunk{
		{Index: 0, Kind: ai.BlockText, Delta: "remembered"},
	}}
	sess := session.New("You are pi-go.")

	agent, err := runtime.New(runtime.Config{
		Model:     model,
		ModelName: "fake-1",
		Tools:     tools.NewRegistry(),
		Session:   sess,
		Policy:    runtime.DenyWrites,
		Observers: []events.Observer{runtime.NewRecorder()},
		Now:       fixedClock(),
	})
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := agent.Run(ctx, "ask something"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !carries(sess.Truth(), "remembered") {
		t.Errorf("history = %+v, want the streamed reply: a reply the record does "+
			"not hold is lost to the next turn", sess.Truth())
	}
}
