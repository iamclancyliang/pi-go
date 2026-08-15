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

// identifiedModel reports WHICH instance was invoked, so an instance swap is
// directly observable at the model rather than inferred.
type identifiedModel struct {
	id      string
	tr      *trace
	mu      sync.Mutex
	callIdx int
	replies []*schema.Message
}

func (m *identifiedModel) observe(in []*schema.Message) *schema.Message {
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

	roles := make([]string, 0, len(in))
	for _, msg := range in {
		roles = append(roles, string(msg.Role))
	}
	m.tr.add(layerModel, "model:Generate", fmt.Sprintf("instance=%s inputs=%d roles=%v", m.id, len(in), roles))
	return &c
}

func (m *identifiedModel) Generate(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return m.observe(in), nil
}

func (m *identifiedModel) Stream(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	msg := m.observe(in)
	sr, sw := schema.Pipe[*schema.Message](1)
	go func() { defer sw.Close(); sw.Send(msg, nil) }()
	return sr, nil
}

var _ model.BaseChatModel = (*identifiedModel)(nil)

// instanceSwapMiddleware returns a DIFFERENT model instance from WrapModel on
// each invocation. This is the point of the probe: it tests eino's own hook as
// the swap mechanism, rather than a dispatcher hidden behind one Model.
type instanceSwapMiddleware struct {
	adk.BaseChatModelAgentMiddleware
	tr    *trace
	a, b  *identifiedModel
	mu    sync.Mutex
	calls int
}

func (p *instanceSwapMiddleware) WrapModel(ctx context.Context, m model.BaseModel[*schema.Message], mc *adk.TypedModelContext[*schema.Message]) (model.BaseModel[*schema.Message], error) {
	p.mu.Lock()
	p.calls++
	n := p.calls
	p.mu.Unlock()
	pick := p.a
	if n > 1 {
		pick = p.b
	}
	// NOTE: the incoming model m is deliberately discarded — WrapModel is being
	// used to substitute the instance entirely, which is the documented
	// "model failover" use case being tested here.
	p.tr.add(layerControl, "WrapModel:fired", fmt.Sprintf("invocation#%d returning instance=%s", n, pick.id))
	return pick, nil
}

// TestSpike4InstanceSwapViaWrapModel — does eino's own per-call hook support
// substituting the model INSTANCE (not just its options)?
func TestSpike4InstanceSwapViaWrapModel(t *testing.T) {
	tr := newTrace()

	a := &identifiedModel{id: "modelA", tr: tr, replies: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{{
			ID: "tc1", Function: schema.FunctionCall{Name: "probe_tool", Arguments: "{}"},
		}}),
	}}
	b := &identifiedModel{id: "modelB", tr: tr, replies: []*schema.Message{
		schema.AssistantMessage("final from B", nil),
	}}

	agent, err := adk.NewChatModelAgent(context.Background(), &adk.ChatModelAgentConfig{
		Name: "swapprobe", Description: "spike4d", Instruction: "probe",
		Model: a, // baseline; WrapModel substitutes per call
		ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{
			Tools: []tool.BaseTool{&probeTool{tr: tr}},
		}},
		Handlers: []adk.TypedChatModelAgentMiddleware[*schema.Message]{
			&instanceSwapMiddleware{tr: tr, a: a, b: b},
		},
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

	t.Logf("\n=== SPIKE 4d RAW TIMELINE ===\n%s", tr.render())

	// --- failable assertions ---
	wraps := tr.detailsMatching("WrapModel:fired")
	if len(wraps) != 2 {
		t.Fatalf("WrapModel fired %d times, want 2: %v", len(wraps), wraps)
	}
	calls := tr.detailsMatching("model:Generate")
	if len(calls) != 2 {
		t.Fatalf("model calls = %d, want 2: %v", len(calls), calls)
	}
	if !strings.Contains(calls[0], "instance=modelA") {
		t.Errorf("call#1 = %q, want instance=modelA", calls[0])
	}
	if !strings.Contains(calls[1], "instance=modelB") {
		t.Errorf("call#2 = %q, want instance=modelB (WrapModel must substitute the instance)", calls[1])
	}
	for _, role := range []string{"system", "user", "assistant", "tool"} {
		if !strings.Contains(calls[1], role) {
			t.Errorf("call#2 roles missing %q (context continuity): %s", role, calls[1])
		}
	}
	w1, m1 := tr.nthSeq("WrapModel:fired", 1), tr.nthSeq("model:Generate", 1)
	tl := tr.firstSeq("tool:invoke", "")
	w2, m2 := tr.nthSeq("WrapModel:fired", 2), tr.nthSeq("model:Generate", 2)
	for _, s := range []struct {
		name string
		a, b int
	}{{"Wrap#1<A", w1, m1}, {"A<tool", m1, tl}, {"tool<Wrap#2", tl, w2}, {"Wrap#2<B", w2, m2}} {
		if s.a < 0 || s.b < 0 || s.a >= s.b {
			t.Errorf("ordering %s violated: %d vs %d", s.name, s.a, s.b)
		}
	}
	if _, _, genIters, preps := tr.counts(); genIters != 1 || preps != 1 {
		t.Errorf("N2 baseline changed: genIters=%d preps=%d, want 1/1", genIters, preps)
	}
}
