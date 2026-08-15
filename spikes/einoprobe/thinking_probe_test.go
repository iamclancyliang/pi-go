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

// thinkingOpts is a provider-specific option type. It is deliberately NOT a
// common option: proving a common option (temperature) travels says nothing
// about whether provider-specific reasoning level travels the same path.
type thinkingOpts struct{ Level string }

// thinkingObservingModel reads ONLY the impl-specific path, so a value arriving
// via the common-option path cannot produce a false pass.
type thinkingObservingModel struct {
	tr      *trace
	mu      sync.Mutex
	callIdx int
	replies []*schema.Message
}

func (m *thinkingObservingModel) observe(in []*schema.Message, opts []model.Option) *schema.Message {
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

	impl := model.GetImplSpecificOptions(&thinkingOpts{}, opts...)
	level := "<empty>"
	if impl != nil && impl.Level != "" {
		level = impl.Level
	}
	roles := make([]string, 0, len(in))
	for _, msg := range in {
		roles = append(roles, string(msg.Role))
	}
	m.tr.add(layerModel, "model:Generate",
		fmt.Sprintf("call#%d thinkingLevel=%s inputs=%d roles=%v", idx+1, level, len(in), roles))
	return &c
}

func (m *thinkingObservingModel) Generate(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return m.observe(in, opts), nil
}

func (m *thinkingObservingModel) Stream(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	msg := m.observe(in, opts)
	sr, sw := schema.Pipe[*schema.Message](1)
	go func() { defer sw.Close(); sw.Send(msg, nil) }()
	return sr, nil
}

var _ model.BaseChatModel = (*thinkingObservingModel)(nil)

type thinkingWrapper struct {
	inner model.BaseModel[*schema.Message]
	level string
}

func (w *thinkingWrapper) opt() model.Option {
	lvl := w.level
	return model.WrapImplSpecificOptFn(func(o *thinkingOpts) { o.Level = lvl })
}

func (w *thinkingWrapper) Generate(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return w.inner.Generate(ctx, in, append(opts, w.opt())...)
}

func (w *thinkingWrapper) Stream(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return w.inner.Stream(ctx, in, append(opts, w.opt())...)
}

type thinkingMiddleware struct {
	adk.BaseChatModelAgentMiddleware
	tr    *trace
	mu    sync.Mutex
	calls int
}

func (p *thinkingMiddleware) WrapModel(ctx context.Context, m model.BaseModel[*schema.Message], mc *adk.TypedModelContext[*schema.Message]) (model.BaseModel[*schema.Message], error) {
	p.mu.Lock()
	p.calls++
	n := p.calls
	p.mu.Unlock()
	level := "low"
	if n > 1 {
		level = "high"
	}
	p.tr.add(layerControl, "WrapModel:fired", fmt.Sprintf("invocation#%d injecting thinkingLevel=%s", n, level))
	return &thinkingWrapper{inner: m, level: level}, nil
}

// TestSpike4ThinkingLevelPerCall — provider-specific reasoning level, read only
// via GetImplSpecificOptions so the common-option path cannot cause a false pass.
func TestSpike4ThinkingLevelPerCall(t *testing.T) {
	tr := newTrace()

	tm := &thinkingObservingModel{tr: tr, replies: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{{
			ID: "tc1", Function: schema.FunctionCall{Name: "probe_tool", Arguments: "{}"},
		}}),
		schema.AssistantMessage("final", nil),
	}}

	agent, err := adk.NewChatModelAgent(context.Background(), &adk.ChatModelAgentConfig{
		Name: "thinkprobe", Description: "spike4c", Instruction: "probe",
		Model: tm,
		ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{
			Tools: []tool.BaseTool{&probeTool{tr: tr}},
		}},
		Handlers: []adk.TypedChatModelAgentMiddleware[*schema.Message]{&thinkingMiddleware{tr: tr}},
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
				tr.add(layerControl, "agent:event", "")
			}
		},
	})

	loop.Push(schema.UserMessage("hello"))
	loop.Run(context.Background())
	loop.Stop(adk.UntilIdleFor(150 * time.Millisecond))
	loop.Wait()

	t.Logf("\n=== SPIKE 4c RAW TIMELINE ===\n%s", tr.render())

	// --- failable assertions (the trace alone cannot catch a regression) ---

	wraps := tr.detailsMatching("WrapModel:fired")
	if len(wraps) != 2 {
		t.Fatalf("WrapModel fired %d times, want 2: %v", len(wraps), wraps)
	}
	if !strings.Contains(wraps[0], "thinkingLevel=low") {
		t.Errorf("WrapModel #1 detail = %q, want thinkingLevel=low", wraps[0])
	}
	if !strings.Contains(wraps[1], "thinkingLevel=high") {
		t.Errorf("WrapModel #2 detail = %q, want thinkingLevel=high", wraps[1])
	}

	calls := tr.detailsMatching("model:Generate")
	if len(calls) != 2 {
		t.Fatalf("model calls = %d, want 2: %v", len(calls), calls)
	}
	if !strings.Contains(calls[0], "thinkingLevel=low") {
		t.Errorf("model call#1 = %q, want thinkingLevel=low reaching the model", calls[0])
	}
	if !strings.Contains(calls[1], "thinkingLevel=high") {
		t.Errorf("model call#2 = %q, want thinkingLevel=high reaching the model", calls[1])
	}
	// context continuity: the second call must carry the assistant + tool turn
	for _, role := range []string{"system", "user", "assistant", "tool"} {
		if !strings.Contains(calls[1], role) {
			t.Errorf("model call#2 roles missing %q: %s", role, calls[1])
		}
	}

	// strict partial order: Wrap#1 < model#1 < tool < Wrap#2 < model#2
	w1, m1 := tr.nthSeq("WrapModel:fired", 1), tr.nthSeq("model:Generate", 1)
	tl := tr.firstSeq("tool:invoke", "")
	w2, m2 := tr.nthSeq("WrapModel:fired", 2), tr.nthSeq("model:Generate", 2)
	for _, step := range []struct {
		name string
		a, b int
	}{{"Wrap#1<model#1", w1, m1}, {"model#1<tool", m1, tl}, {"tool<Wrap#2", tl, w2}, {"Wrap#2<model#2", w2, m2}} {
		if step.a < 0 || step.b < 0 || step.a >= step.b {
			t.Errorf("ordering %s violated: %d vs %d", step.name, step.a, step.b)
		}
	}

	// N2 baseline must be unchanged: one GenInput iteration, one PrepareAgent instance
	_, _, genIters, preps := tr.counts()
	if genIters != 1 || preps != 1 {
		t.Errorf("N2 baseline changed: genIters=%d preps=%d, want 1/1", genIters, preps)
	}
}
