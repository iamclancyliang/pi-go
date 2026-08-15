package einoprobe

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// probeTool is a fake tool that records its invocation into the shared trace.
type probeTool struct{ tr *trace }

func (p *probeTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "probe_tool", Desc: "records that it ran"}, nil
}

func (p *probeTool) InvokableRun(ctx context.Context, args string, opts ...tool.Option) (string, error) {
	p.tr.add(layerTool, "tool:invoke", fmt.Sprintf("probe_tool args=%s", args))
	return "tool-result", nil
}

var _ tool.InvokableTool = (*probeTool)(nil)

// TestObserveNestingWithToolCall satisfies precondition P1: the first model
// reply deterministically produces a tool call, so the tool's completion must be
// followed by a second model call. Without that, N1 and N2 cannot be told apart.
//
// PURE OBSERVATION — no nesting claim is asserted here.
func TestObserveNestingWithToolCall(t *testing.T) {
	tr := newTrace()

	sm := &scriptedModel{
		tr: tr,
		replies: []*schema.Message{
			// call #1 -> a tool call (P1)
			schema.AssistantMessage("", []schema.ToolCall{{
				ID:       "tc1",
				Function: schema.FunctionCall{Name: "probe_tool", Arguments: "{}"},
			}}),
			// call #2 -> plain text, ends the turn
			schema.AssistantMessage("final answer", nil),
		},
	}

	agent, err := adk.NewChatModelAgent(context.Background(), &adk.ChatModelAgentConfig{
		Name:        "probe",
		Description: "observation-only probe agent",
		Instruction: "probe",
		Model:       sm,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{&probeTool{tr: tr}},
			},
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
				_, ok := events.Next()
				if !ok {
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

	t.Logf("\n=== RAW TIMELINE (no interpretation) ===\n%s", tr.render())
	mpp, tpp, genIters, preps := tr.counts()
	t.Logf("GenInput iterations=%d  PrepareAgent instances=%d", genIters, preps)
	t.Logf("model calls per PrepareAgent instance: %v", mpp)
	t.Logf("tool events per PrepareAgent instance: %v", tpp)

	// --- failable assertions: the N2 verdict must not silently drift ---
	if genIters != 1 || preps != 1 {
		t.Fatalf("expected 1 GenInput iteration and 1 PrepareAgent instance, got %d/%d", genIters, preps)
	}
	if mpp[1] != 2 {
		t.Errorf("model calls inside PrepareAgent#1 = %d, want 2 (P1 requires a second call)", mpp[1])
	}
	if tpp[1] != 1 {
		t.Errorf("tool events inside PrepareAgent#1 = %d, want 1", tpp[1])
	}
	// N2 specifically: the tool must fall BETWEEN the two model calls.
	m1, tl, m2 := tr.nthSeq("model:Generate", 1), tr.firstSeq("tool:invoke", ""), tr.nthSeq("model:Generate", 2)
	if !(m1 > 0 && tl > m1 && m2 > tl) {
		t.Errorf("N2 ordering violated: model#1=%d tool=%d model#2=%d", m1, tl, m2)
	}
}
