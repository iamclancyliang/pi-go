package einoprobe

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// optObservingModel reports the per-call options it actually received, so we can
// tell tier 1 (options reach the model) from tier 2 (hook fires, options lost).
type optObservingModel struct {
	tr      *trace
	mu      sync.Mutex
	callIdx int
	replies []*schema.Message
}

func (m *optObservingModel) observe(input []*schema.Message, opts []model.Option) *schema.Message {
	m.mu.Lock()
	idx := m.callIdx
	m.callIdx++
	var reply *schema.Message
	if idx < len(m.replies) {
		reply = m.replies[idx]
	} else {
		reply = schema.AssistantMessage("done", nil)
	}
	c := *reply
	m.mu.Unlock()

	common := model.GetCommonOptions(&model.Options{}, opts...)
	temp := "nil"
	if common != nil && common.Temperature != nil {
		temp = fmt.Sprintf("%.1f", *common.Temperature)
	}
	m.tr.add(layerModel, "model:Generate",
		fmt.Sprintf("call#%d inputs=%d opts=%d observedTemperature=%s", idx+1, len(input), len(opts), temp))
	return &c
}

func (m *optObservingModel) Generate(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return m.observe(in, opts), nil
}

func (m *optObservingModel) Stream(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	msg := m.observe(in, opts)
	sr, sw := schema.Pipe[*schema.Message](1)
	go func() { defer sw.Close(); sw.Send(msg, nil) }()
	return sr, nil
}

var _ model.BaseChatModel = (*optObservingModel)(nil)

// perCallOptModel is the wrapper WrapModel returns: it injects a DIFFERENT
// option value on each invocation, so the underlying model can report whether
// per-call configuration actually arrives.
type perCallOptModel struct {
	inner model.BaseModel[*schema.Message]
	temp  float32
}

func (w *perCallOptModel) Generate(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return w.inner.Generate(ctx, in, append(opts, model.WithTemperature(w.temp))...)
}

func (w *perCallOptModel) Stream(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return w.inner.Stream(ctx, in, append(opts, model.WithTemperature(w.temp))...)
}

// wrapModelProbe records every WrapModel invocation and injects a per-call value.
type wrapModelProbe struct {
	adk.BaseChatModelAgentMiddleware
	tr    *trace
	mu    sync.Mutex
	calls int
}

func (p *wrapModelProbe) WrapModel(ctx context.Context, m model.BaseModel[*schema.Message], mc *adk.TypedModelContext[*schema.Message]) (model.BaseModel[*schema.Message], error) {
	p.mu.Lock()
	p.calls++
	n := p.calls
	p.mu.Unlock()
	p.tr.add(layerControl, "WrapModel:fired", fmt.Sprintf("invocation#%d injecting temperature=%.1f", n, float32(n)))
	return &perCallOptModel{inner: m, temp: float32(n)}, nil
}

// TestSpike4RequestParamsViaWrapModel — spike #4, second half.
//
// Pre-registered tiers:
//  1. WrapModel fires per call AND differing options observed at the model
//     -> eino provides a per-call hook; policy still owned by our handler.
//  2. Hook fires per call but options do not reach the provider
//     -> partial; our model port must close the gap.
//  3. Hook does not fire per call -> fall back to the dispatcher approach.
func TestSpike4RequestParamsViaWrapModel(t *testing.T) {
	tr := newTrace()

	om := &optObservingModel{tr: tr, replies: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{{
			ID: "tc1", Function: schema.FunctionCall{Name: "probe_tool", Arguments: "{}"},
		}}),
		schema.AssistantMessage("final", nil),
	}}

	agent, err := adk.NewChatModelAgent(context.Background(), &adk.ChatModelAgentConfig{
		Name: "wrapprobe", Description: "spike4b", Instruction: "probe",
		Model: om,
		ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{
			Tools: []tool.BaseTool{&probeTool{tr: tr}},
		}},
		Handlers: []adk.TypedChatModelAgentMiddleware[*schema.Message]{&wrapModelProbe{tr: tr}},
	})
	if err != nil {
		t.Fatalf("NewChatModelAgent: %v", err)
	}

	loop := adk.NewTurnLoop(adk.TurnLoopConfig[*schema.Message, *schema.Message]{
		GenInput: func(ctx context.Context, l *adk.TurnLoop[*schema.Message, *schema.Message], items []*schema.Message) (*adk.GenInputResult[*schema.Message, *schema.Message], error) {
			tr.beginGenInput(fmt.Sprintf("buffered=%d", len(items)))
			if len(items) == 0 {
				return nil, nil
			}
			return &adk.GenInputResult[*schema.Message, *schema.Message]{
				Input:    &adk.TypedAgentInput[*schema.Message]{Messages: items},
				Consumed: items,
			}, nil
		},
		PrepareAgent: func(ctx context.Context, l *adk.TurnLoop[*schema.Message, *schema.Message], consumed []*schema.Message) (adk.TypedAgent[*schema.Message], error) {
			tr.beginPrepareAgent(fmt.Sprintf("consumed=%d", len(consumed)))
			return agent, nil
		},
		OnAgentEvents: func(ctx context.Context, tc *adk.TurnContext[*schema.Message, *schema.Message], events *adk.AsyncIterator[*adk.TypedAgentEvent[*schema.Message]]) error {
			for {
				if _, ok := events.Next(); !ok {
					return nil
				}
			}
		},
	})

	loop.Push(schema.UserMessage("hello"))
	loop.Run(context.Background())
	loop.Stop(adk.UntilIdleFor(150 * time.Millisecond))
	loop.Wait()

	t.Logf("\n=== SPIKE 4b RAW TIMELINE ===\n%s", tr.render())

	// --- failable assertions ---
	wraps := tr.detailsMatching("WrapModel:fired")
	if len(wraps) != 2 {
		t.Fatalf("WrapModel fired %d times, want 2 (per model call): %v", len(wraps), wraps)
	}
	calls := tr.detailsMatching("model:Generate")
	if len(calls) != 2 {
		t.Fatalf("model calls = %d, want 2: %v", len(calls), calls)
	}
	if !strings.Contains(calls[0], "observedTemperature=1.0") {
		t.Errorf("call#1 = %q, want observedTemperature=1.0", calls[0])
	}
	if !strings.Contains(calls[1], "observedTemperature=2.0") {
		t.Errorf("call#2 = %q, want observedTemperature=2.0 (per-call option must differ)", calls[1])
	}
}

// TestSpike4NoWrapModelControl is the paired negative control for C8: with NO
// WrapModel handler registered, the model must observe no injected option.
//
// Without this, "WrapModel delivers per-call options" rests only on the positive
// case — and a value arriving by some other route would look identical.
func TestSpike4NoWrapModelControl(t *testing.T) {
	tr := newTrace()

	om := &optObservingModel{tr: tr, replies: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{{
			ID: "tc1", Function: schema.FunctionCall{Name: "probe_tool", Arguments: "{}"},
		}}),
		schema.AssistantMessage("final", nil),
	}}

	agent, err := adk.NewChatModelAgent(context.Background(), &adk.ChatModelAgentConfig{
		Name: "nowrap", Description: "spike4 control", Instruction: "probe",
		Model: om,
		ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{
			Tools: []tool.BaseTool{&probeTool{tr: tr}},
		}},
		// Handlers deliberately omitted.
	})
	if err != nil {
		t.Fatalf("NewChatModelAgent: %v", err)
	}

	loop := adk.NewTurnLoop(adk.TurnLoopConfig[*schema.Message, *schema.Message]{
		GenInput: func(ctx context.Context, l *adk.TurnLoop[*schema.Message, *schema.Message], items []*schema.Message) (*adk.GenInputResult[*schema.Message, *schema.Message], error) {
			tr.beginGenInput(fmt.Sprintf("buffered=%d", len(items)))
			if len(items) == 0 {
				return nil, nil
			}
			return &adk.GenInputResult[*schema.Message, *schema.Message]{
				Input:    &adk.TypedAgentInput[*schema.Message]{Messages: items},
				Consumed: items,
			}, nil
		},
		PrepareAgent: func(ctx context.Context, l *adk.TurnLoop[*schema.Message, *schema.Message], consumed []*schema.Message) (adk.TypedAgent[*schema.Message], error) {
			tr.beginPrepareAgent(fmt.Sprintf("consumed=%d", len(consumed)))
			return agent, nil
		},
		OnAgentEvents: func(ctx context.Context, tc *adk.TurnContext[*schema.Message, *schema.Message], events *adk.AsyncIterator[*adk.TypedAgentEvent[*schema.Message]]) error {
			for {
				if _, ok := events.Next(); !ok {
					return nil
				}
			}
		},
	})

	loop.Push(schema.UserMessage("hello"))
	loop.Run(context.Background())
	loop.Stop(adk.UntilIdleFor(150 * time.Millisecond))
	loop.Wait()

	t.Logf("\n=== SPIKE 4 CONTROL (no WrapModel) ===\n%s", tr.render())

	if n := tr.countEvents("WrapModel:fired"); n != 0 {
		t.Errorf("WrapModel fired %d times with no handler registered, want 0", n)
	}
	for _, d := range tr.detailsMatching("model:Generate") {
		if !strings.Contains(d, "observedTemperature=nil") {
			t.Errorf("model observed an injected option with no WrapModel handler: %s", d)
		}
	}
}
