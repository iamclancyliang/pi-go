package conformance

import (
	"context"
	"errors"
	"sync"
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

// blockingConsumer records what a renderer sees, and can hold the reply.
type blockingConsumer struct {
	mu       sync.Mutex
	seen     []ai.StreamEvent
	sawFirst chan struct{}
	release  chan struct{}
	once     sync.Once
}

func newBlockingConsumer() *blockingConsumer {
	return &blockingConsumer{
		sawFirst: make(chan struct{}),
		release:  make(chan struct{}),
	}
}

func (c *blockingConsumer) Reply(event ai.StreamEvent) {
	c.mu.Lock()
	c.seen = append(c.seen, event)
	c.mu.Unlock()

	if event.Kind == ai.StreamTextDelta {
		c.once.Do(func() { close(c.sawFirst) })
		<-c.release
	}
}

func (c *blockingConsumer) events() []ai.StreamEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]ai.StreamEvent(nil), c.seen...)
}

// TestAConsumerSeesContentBeforeTheReplyEnds pins the property that separates
// streaming from a reply delivered whole.
//
// The consumer blocks on the first delta. If the runtime had collected the reply
// and published it afterwards, that block would happen after the run finished; by
// holding it we can show the run is still in flight while the consumer already
// has the first block.
func TestAConsumerSeesContentBeforeTheReplyEnds(t *testing.T) {
	consumer := newBlockingConsumer()
	model := &streamOnlyModel{blocks: []ai.Chunk{
		{Index: 0, Kind: ai.BlockText, Delta: "first"},
		{Index: 1, Kind: ai.BlockText, Delta: "second"},
	}}

	agent, err := runtime.New(runtime.Config{
		Model:          model,
		ModelName:      "fake-1",
		Tools:          tools.NewRegistry(),
		Session:        session.New("You are pi-go."),
		Policy:         runtime.DenyWrites,
		Observers:      []events.Observer{runtime.NewRecorder()},
		ReplyObservers: []runtime.ReplyObserver{consumer},
		Now:            fixedClock(),
	})
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}

	finished := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		finished <- agent.Run(ctx, "ask something")
	}()

	select {
	case <-consumer.sawFirst:
	case err := <-finished:
		t.Fatalf("the run finished (%v) before the consumer saw any content: the "+
			"reply was collected and published afterwards", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the consumer never saw content")
	}

	// The run cannot have completed: the consumer is still holding it.
	select {
	case err := <-finished:
		t.Fatalf("the run completed while the consumer was still holding the first "+
			"block (%v)", err)
	default:
	}

	close(consumer.release)
	if err := <-finished; err != nil {
		t.Fatalf("Run: %v", err)
	}

	var kinds []ai.StreamEventKind
	for _, e := range consumer.events() {
		kinds = append(kinds, e.Kind)
	}
	if len(kinds) == 0 || kinds[0] != ai.StreamStart {
		t.Errorf("consumer saw %v, want it to begin with a start", kinds)
	}
	last := kinds[len(kinds)-1]
	if last != ai.StreamDone {
		t.Errorf("consumer ended on %q, want %q", last, ai.StreamDone)
	}
}

// TestAStreamedToolCallIsGovernedLikeAnyOther pins that arriving in pieces buys a
// tool call no exemptions.
//
// The policy check, the recorded attempt and the paired events are what make a
// tool call safe to run at all. A call that reached the tools node without them
// would be the one case where none of that applied — and nothing in the stream
// would say so.
func TestAStreamedToolCallIsGovernedLikeAnyOther(t *testing.T) {
	store := &session.MemoryStore{}
	sess := session.WithStore("You are pi-go.", store)

	registry := tools.NewRegistry()
	registry.MustRegister(&timedTool{name: "read_files", delay: time.Millisecond})

	rec := runtime.NewRecorder()
	model := &streamOnlyToolModel{}
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
	if err := agent.Run(ctx, "read it"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The call was announced and reported, in the pairing the event contract
	// requires.
	starts := idsOf(rec, events.KindToolStart)
	results := idsOf(rec, events.KindToolResult)
	if len(starts) != 1 || starts[0] != "call-1" {
		t.Errorf("tool_start = %v, want one call-1: a streamed call reached the "+
			"tools node without the round opening", starts)
	}
	if len(results) != 1 || results[0] != "call-1" {
		t.Errorf("tool_result = %v, want one call-1", results)
	}

	// The attempt is durable, which is what makes a repeat decision possible
	// after a crash.
	reopened, err := session.Restore(context.Background(), "You are pi-go.", store)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if _, settled := reopened.Settlement("op-1.call-1"); !settled {
		t.Error("no durable record of the streamed call's outcome, so a restart " +
			"could not tell whether it had run")
	}
}

// streamOnlyToolModel streams a tool call, then an answer once it is settled.
type streamOnlyToolModel struct{ calls int }

func (m *streamOnlyToolModel) Generate(context.Context, ai.Request) (ai.Response, error) {
	return ai.Response{}, errors.New("this model streams and was asked for a whole answer")
}

func (m *streamOnlyToolModel) Stream(_ context.Context, req ai.Request) (<-chan ai.StreamEvent, error) {
	m.calls++
	settled := false
	for _, msg := range req.Messages {
		if msg.Role == ai.RoleTool {
			settled = true
		}
	}
	out := make(chan ai.StreamEvent)
	go func() {
		defer close(out)
		acc := ai.NewAccumulator("fake-1")
		start, err := acc.Begin()
		if err != nil {
			return
		}
		out <- start

		chunk := ai.Chunk{Index: 0, Kind: ai.BlockText, Delta: "all done"}
		if !settled {
			chunk = ai.Chunk{
				Index: 0, Kind: ai.BlockToolCall,
				Call:  ai.ToolCall{ID: "call-1", Name: "read_files"},
				Delta: `{}`,
			}
		}
		events, err := acc.Push(chunk)
		if err != nil {
			return
		}
		for _, e := range events {
			out <- e
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

// recordingConsumer keeps every event a renderer would see.
type recordingConsumer struct {
	mu   sync.Mutex
	seen []ai.StreamEvent
}

func (c *recordingConsumer) Reply(event ai.StreamEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seen = append(c.seen, event)
}

func (c *recordingConsumer) events() []ai.StreamEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]ai.StreamEvent(nil), c.seen...)
}

// TestAConsumerSeesBlocksTheFrameworkCannotKeep pins that the reply surface
// carries what pi-go produced, not what the framework could represent.
//
// Two adjacent text blocks are the case that separates them: the framework joins
// all text into one string with no boundary, so a reply routed back from its view
// arrives as one block. Anything that reconstructs the consumer's events from the
// framework fails here, and only here — every other assertion looks identical.
func TestAConsumerSeesBlocksTheFrameworkCannotKeep(t *testing.T) {
	consumer := &recordingConsumer{}
	model := &streamOnlyModel{blocks: []ai.Chunk{
		{Index: 0, Kind: ai.BlockText, Delta: "first"},
		{Index: 1, Kind: ai.BlockText, Delta: "second"},
	}}

	agent, err := runtime.New(runtime.Config{
		Model:          model,
		ModelName:      "fake-1",
		Tools:          tools.NewRegistry(),
		Session:        session.New("You are pi-go."),
		Policy:         runtime.DenyWrites,
		Observers:      []events.Observer{runtime.NewRecorder()},
		ReplyObservers: []runtime.ReplyObserver{consumer},
		Now:            fixedClock(),
	})
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := agent.Run(ctx, "ask something"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var final *ai.AssistantMessage
	terminals := 0
	for _, event := range consumer.events() {
		if event.Terminal() {
			terminals++
			final = event.Final
		}
	}

	if terminals != 1 {
		t.Fatalf("terminals seen = %d, want exactly 1", terminals)
	}
	if final == nil || len(final.Blocks) != 2 {
		t.Fatalf("consumer saw %d blocks, want 2: the reply reached it through the "+
			"framework's flattening, where adjacent text has no boundary", len(final.Blocks))
	}
	if final.Blocks[0].Text != "first" || final.Blocks[1].Text != "second" {
		t.Errorf("blocks = %q / %q, want %q / %q", final.Blocks[0].Text,
			final.Blocks[1].Text, "first", "second")
	}
}

// TestAStreamedCallTheePolicyRefusesDoesNotRun pins that a streamed call is
// checked before it can act.
//
// A refused call that runs anyway looks identical in the event stream to one that
// was allowed: the difference is only visible in whether the tool did its work.
func TestAStreamedCallThePolicyRefusesDoesNotRun(t *testing.T) {
	registry := tools.NewRegistry()
	writer := &countingWriteTool{}
	registry.MustRegister(writer)

	sess := session.New("You are pi-go.")
	agent, err := runtime.New(runtime.Config{
		Model:     &streamOnlyWriteModel{},
		ModelName: "fake-1",
		Tools:     registry,
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
	if err := agent.Run(ctx, "delete it"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if writer.runs != 0 {
		t.Errorf("the tool ran %d times despite the policy refusing it: arriving in "+
			"pieces exempted it from the check", writer.runs)
	}
	if !carries(sess.Truth(), "denied") {
		t.Error("the model was not told the call was refused, so it will ask again")
	}
}

type countingWriteTool struct{ runs int }

func (c *countingWriteTool) Name() string               { return "delete_files" }
func (c *countingWriteTool) Description() string        { return "test tool" }
func (c *countingWriteTool) Execution() tools.Execution { return tools.Execution{} }
func (c *countingWriteTool) Call(context.Context, string) (tools.Result, error) {
	c.runs++
	return tools.Result{Content: "deleted"}, nil
}

// streamOnlyWriteModel streams a call the policy will refuse.
type streamOnlyWriteModel struct{}

func (streamOnlyWriteModel) Generate(context.Context, ai.Request) (ai.Response, error) {
	return ai.Response{}, errors.New("this model streams and was asked for a whole answer")
}

func (streamOnlyWriteModel) Stream(_ context.Context, req ai.Request) (<-chan ai.StreamEvent, error) {
	settled := false
	for _, msg := range req.Messages {
		if msg.Role == ai.RoleTool {
			settled = true
		}
	}
	out := make(chan ai.StreamEvent)
	go func() {
		defer close(out)
		acc := ai.NewAccumulator("fake-1")
		start, err := acc.Begin()
		if err != nil {
			return
		}
		out <- start
		chunk := ai.Chunk{Index: 0, Kind: ai.BlockText, Delta: "understood"}
		if !settled {
			chunk = ai.Chunk{
				Index: 0, Kind: ai.BlockToolCall,
				Call:  ai.ToolCall{ID: "call-1", Name: "delete_files"},
				Delta: `{}`,
			}
		}
		events, err := acc.Push(chunk)
		if err != nil {
			return
		}
		for _, e := range events {
			out <- e
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

// TestStreamedCallsKeepTheOrderTheModelAskedFor pins source order through the
// streaming path.
//
// Source order is not recoverable later: once the round starts, the only order
// anything can observe is the order the calls happened to finish. A streamed
// reply carries its calls in the order the blocks arrived, and that is the order
// the round must open in.
func TestStreamedCallsKeepTheOrderTheModelAskedFor(t *testing.T) {
	registry := tools.NewRegistry()
	registry.MustRegister(&timedTool{name: "slow_tool", delay: 40 * time.Millisecond})
	registry.MustRegister(&timedTool{name: "fast_tool", delay: time.Millisecond})

	rec := runtime.NewRecorder()
	agent, err := runtime.New(runtime.Config{
		Model:     &streamOnlyTwoCallModel{},
		ModelName: "fake-1",
		Tools:     registry,
		Session:   session.New("You are pi-go."),
		Policy:    runtime.DenyWrites,
		Observers: []events.Observer{rec},
		Now:       fixedClock(),
	})
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := agent.Run(ctx, "do both"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The slow call was asked for first, so its result comes first even though it
	// finished last.
	results := idsOf(rec, events.KindToolResult)
	if !equal(results, []string{"call-slow", "call-fast"}) {
		t.Errorf("results = %v, want [call-slow call-fast]: the round opened in "+
			"completion order rather than the order the model asked", results)
	}
}

// streamOnlyTwoCallModel streams two calls, slow first.
type streamOnlyTwoCallModel struct{}

func (streamOnlyTwoCallModel) Generate(context.Context, ai.Request) (ai.Response, error) {
	return ai.Response{}, errors.New("this model streams and was asked for a whole answer")
}

func (streamOnlyTwoCallModel) Stream(_ context.Context, req ai.Request) (<-chan ai.StreamEvent, error) {
	settled := false
	for _, msg := range req.Messages {
		if msg.Role == ai.RoleTool {
			settled = true
		}
	}
	out := make(chan ai.StreamEvent)
	go func() {
		defer close(out)
		acc := ai.NewAccumulator("fake-1")
		start, err := acc.Begin()
		if err != nil {
			return
		}
		out <- start

		if settled {
			if evs, err := acc.Push(ai.Chunk{Index: 0, Kind: ai.BlockText, Delta: "both done"}); err == nil {
				for _, e := range evs {
					out <- e
				}
			}
			if e, err := acc.Close(0); err == nil {
				out <- e
			}
			if e, err := acc.Done(ai.StopEnd, ai.Usage{}); err == nil {
				out <- e
			}
			return
		}

		calls := []ai.Chunk{
			{Index: 0, Kind: ai.BlockToolCall, Call: ai.ToolCall{ID: "call-slow", Name: "slow_tool"}, Delta: `{}`},
			{Index: 1, Kind: ai.BlockToolCall, Call: ai.ToolCall{ID: "call-fast", Name: "fast_tool"}, Delta: `{}`},
		}
		for i, c := range calls {
			if i > 0 {
				if e, err := acc.Close(i - 1); err == nil {
					out <- e
				}
			}
			evs, err := acc.Push(c)
			if err != nil {
				return
			}
			for _, e := range evs {
				out <- e
			}
		}
		if e, err := acc.Close(len(calls) - 1); err == nil {
			out <- e
		}
		if e, err := acc.Done(ai.StopEnd, ai.Usage{}); err == nil {
			out <- e
		}
	}()
	return out, nil
}
