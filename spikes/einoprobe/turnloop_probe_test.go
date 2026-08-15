package einoprobe

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// scriptedModel returns a fixed sequence of replies and records every call into
// the shared trace.
//
// P1 requires the first reply to deterministically produce a tool call and the
// tool's completion to be followed by a second model call — otherwise N1 and N2
// cannot be told apart, because there is nothing to count between PrepareAgent
// instances.
type scriptedModel struct {
	tr *trace

	mu      sync.Mutex
	callIdx int
	replies []*schema.Message
}

func (m *scriptedModel) next(method string, input []*schema.Message) *schema.Message {
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

	roles := make([]string, 0, len(input))
	for _, msg := range input {
		if msg == nil {
			roles = append(roles, "<nil>")
			continue
		}
		roles = append(roles, string(msg.Role))
	}
	// P3 requires proving the tool message DELIVERED to this call still pairs
	// with the original tool call. Recording roles alone would force us to infer
	// that from two separate facts; record the identifiers directly instead.
	var pairing []string
	for _, msg := range input {
		if msg == nil {
			continue
		}
		for _, tc := range msg.ToolCalls {
			pairing = append(pairing, fmt.Sprintf("assistantToolCallID=%s", tc.ID))
		}
		if msg.Role == schema.Tool {
			pairing = append(pairing, fmt.Sprintf("toolMsgToolCallID=%s content=%q", msg.ToolCallID, msg.Content))
		}
	}
	// Record the actual user CONTENT delivered to the model. Roles alone cannot
	// distinguish "the injected message arrived" from "some user message
	// arrived" — proving it by proxy is what P3 already caught once.
	var userContents []string
	for _, msg := range input {
		if msg != nil && msg.Role == schema.User {
			userContents = append(userContents, msg.Content)
		}
	}
	m.tr.add(layerModel, method, fmt.Sprintf("call#%d inputs=%d roles=%v userContents=%v pairing=%v",
		idx+1, len(input), roles, userContents, pairing))
	return &c
}

func (m *scriptedModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return m.next("model:Generate", input), nil
}

func (m *scriptedModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	reply := m.next("model:Stream", input)
	sr, sw := schema.Pipe[*schema.Message](1)
	go func() {
		defer sw.Close()
		sw.Send(reply, nil)
	}()
	return sr, nil
}

var _ model.BaseChatModel = (*scriptedModel)(nil)

// TestObserveTurnLoopNesting is a PURE OBSERVATION run. It asserts only that
// the loop terminated and produced a trace; it deliberately makes no claim
// about the nesting. The verdict is applied separately against the
// pre-registered table in INTERPRETATION.md.
func TestObserveTurnLoopNesting(t *testing.T) {
	tr := newTrace()

	sm := &scriptedModel{
		tr: tr,
		replies: []*schema.Message{
			schema.AssistantMessage("first reply", nil),
			schema.AssistantMessage("second reply", nil),
		},
	}

	agent, err := adk.NewChatModelAgent(context.Background(), &adk.ChatModelAgentConfig{
		Name:        "probe",
		Description: "observation-only probe agent",
		Instruction: "probe",
		Model:       sm,
	})
	if err != nil {
		t.Fatalf("NewChatModelAgent: %v", err)
	}

	loop := adk.NewTurnLoop(adk.TurnLoopConfig[*schema.Message, *schema.Message]{
		GenInput: func(ctx context.Context, l *adk.TurnLoop[*schema.Message, *schema.Message], items []*schema.Message) (*adk.GenInputResult[*schema.Message, *schema.Message], error) {
			tr.beginGenInput(fmt.Sprintf("buffered=%d", len(items)))
			if len(items) == 0 {
				tr.add(layerControl, "Stop", "no buffered items")
				l.Stop()
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
				ev, ok := events.Next()
				if !ok {
					return nil
				}
				name := "agent:event"
				if ev != nil && ev.Output != nil {
					name = "agent:output"
				}
				tr.add(layerControl, name, fmt.Sprintf("preempted=%v stopped=%v", tc.Preempted, tc.Stopped))
			}
		},
	})

	ok, done := loop.Push(schema.UserMessage("hello"))
	tr.add(layerControl, "Push:returned", fmt.Sprintf("accepted=%v doneChanNil=%v", ok, done == nil))

	loop.Run(context.Background())

	// The loop is long-running: it waits for further items rather than exiting
	// after one turn. Stop with UntilIdleFor gives a deterministic exit once the
	// turn has settled — not a sleep in our code (P2).
	loop.Stop(adk.UntilIdleFor(100 * time.Millisecond))

	// Run is documented non-blocking; Wait is the deterministic barrier for
	// loop completion. Using a sleep here would violate precondition P2 and let
	// scheduling decide what the trace contains.
	exit := loop.Wait()
	tr.add(layerControl, "Wait:returned", fmt.Sprintf("exitState=%T", exit))
	t.Logf("\n=== RAW TIMELINE (no interpretation) ===\n%s", tr.render())
	mpp, tpp, genIters, preps := tr.counts()
	t.Logf("GenInput iterations=%d  PrepareAgent instances=%d", genIters, preps)
	t.Logf("model calls per PrepareAgent: %v", mpp)
	t.Logf("tool events per PrepareAgent: %v", tpp)

	if len(tr.snapshot()) == 0 {
		t.Fatal("no trace recorded — harness did not run")
	}
}
