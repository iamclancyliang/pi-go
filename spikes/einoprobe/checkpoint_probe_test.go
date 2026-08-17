package einoprobe

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// memStore is a minimal in-memory CheckPointStore that records every access, so
// the trace shows whether eino actually wrote and re-read a checkpoint.
type memStore struct {
	tr *trace
	mu sync.Mutex
	m  map[string][]byte
}

func newMemStore(tr *trace) *memStore { return &memStore{tr: tr, m: map[string][]byte{}} }

// Get and Set copy the slice: sharing a mutable backing array between eino and
// the store would let a later mutation silently rewrite an earlier checkpoint.
func (s *memStore) Get(ctx context.Context, id string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.m[id]
	s.tr.add(layerControl, "store:Get", fmt.Sprintf("id=%s found=%v bytes=%d", id, ok, len(b)))
	if !ok {
		return nil, false, nil
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out, true, nil
}

func (s *memStore) Set(ctx context.Context, id string, cp []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := make([]byte, len(cp))
	copy(c, cp)
	s.m[id] = c
	s.tr.add(layerControl, "store:Set", fmt.Sprintf("id=%s bytes=%d", id, len(c)))
	return nil
}

// Delete implements the optional CheckPointDeleter interface. Without it, eino
// cannot clean a checkpoint at all — and "the checkpoint was not cleaned up"
// would be a limitation of this test double rather than an observation about
// eino. Implementing it keeps that distinction honest.
func (s *memStore) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, existed := s.m[id]
	delete(s.m, id)
	s.tr.add(layerControl, "store:Delete", fmt.Sprintf("id=%s existed=%v", id, existed))
	return nil
}

func (s *memStore) has(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.m[id]
	return ok
}

// stoppingTool calls Stop(WithGraceful) from inside the tool body — the same
// deterministic barrier used for the steering probes (P2).
type stoppingTool struct {
	tr      *trace
	mu      sync.Mutex
	loop    *adk.TurnLoop[*schema.Message, *schema.Message]
	stopped bool
}

func (p *stoppingTool) setLoop(l *adk.TurnLoop[*schema.Message, *schema.Message]) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.loop = l
}

func (p *stoppingTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "probe_tool", Desc: "stops the loop gracefully mid-turn"}, nil
}

func (p *stoppingTool) InvokableRun(ctx context.Context, args string, opts ...tool.Option) (string, error) {
	p.tr.add(layerTool, "tool:invoke", fmt.Sprintf("probe_tool args=%s", args))
	p.mu.Lock()
	l, already := p.loop, p.stopped
	p.stopped = true
	p.mu.Unlock()
	if l != nil && !already {
		p.tr.add(layerControl, "Stop:graceful", "requesting graceful stop from inside tool")
		l.Stop(adk.WithGraceful())
	}
	return "tool-result", nil
}

var _ tool.InvokableTool = (*stoppingTool)(nil)

// TestSpike3ObserveCheckpointOnGracefulStop — PURE OBSERVATION, arm C step 1.
//
// Question: does Stop(WithGraceful) mid-turn actually produce a checkpoint, and
// what does the exit state contain? Target IDs are NOT pre-guessed — whatever
// InterruptedItems/contexts appear are recorded as found.
func TestSpike3ObserveCheckpointOnGracefulStop(t *testing.T) {
	tr := newTrace()
	store := newMemStore(tr)
	const cpID = "spike3-cp"

	sm := &scriptedModel{tr: tr, replies: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{{
			ID: "tc1", Function: schema.FunctionCall{Name: "probe_tool", Arguments: "{}"},
		}}),
		schema.AssistantMessage("final", nil),
	}}

	st := &stoppingTool{tr: tr}

	agent, err := adk.NewChatModelAgent(context.Background(), &adk.ChatModelAgentConfig{
		Name: "cpprobe", Description: "spike3", Instruction: "probe",
		Model: sm,
		ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{
			Tools: []tool.BaseTool{st},
		}},
	})
	if err != nil {
		t.Fatalf("NewChatModelAgent: %v", err)
	}

	loop := adk.NewTurnLoop(adk.TurnLoopConfig[*schema.Message, *schema.Message]{
		Store:        store,
		CheckpointID: cpID,
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
		GenResume: func(ctx context.Context, l *adk.TurnLoop[*schema.Message, *schema.Message], interrupted, unhandled, newItems []*schema.Message) (*adk.GenResumeResult[*schema.Message, *schema.Message], error) {
			tr.add(layerControl, "GenResume:fired",
				fmt.Sprintf("interrupted=%d unhandled=%d new=%d", len(interrupted), len(unhandled), len(newItems)))
			return nil, nil
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
	st.setLoop(loop)

	loop.Push(schema.UserMessage("hello"))
	loop.Run(context.Background())
	exit := loop.Wait()

	// Record the REAL interrupt contexts. Previously this only logged a boolean,
	// so the claim "targets recorded as found" was not actually backed by the
	// code — step 2 would still have been guessing at a target ID.
	var rootCtxID string
	var ce *adk.CancelError
	if errors.As(exit.ExitReason, &ce) {
		tr.add(layerControl, "cancel:contexts", fmt.Sprintf("count=%d", len(ce.InterruptContexts)))
		for i, ic := range ce.InterruptContexts {
			if ic == nil {
				continue
			}
			tr.add(layerControl, "cancel:context",
				fmt.Sprintf("#%d id=%q address=%v isRootCause=%v infoType=%T",
					i, ic.ID, ic.Address, ic.IsRootCause, ic.Info))
			if ic.IsRootCause && rootCtxID == "" {
				rootCtxID = ic.ID
			}
		}
	}
	tr.add(layerControl, "Wait:returned", fmt.Sprintf(
		"exitReason=%v cancelErr=%v checkpointAttempted=%v checkpointErr=%v interrupted=%d unhandled=%d stopCause=%q",
		exit.ExitReason, errors.As(exit.ExitReason, &ce), exit.CheckpointAttempted, exit.CheckpointErr,
		len(exit.InterruptedItems), len(exit.UnhandledItems), exit.StopCause))
	tr.add(layerControl, "store:finalState", fmt.Sprintf("checkpointPresent=%v", store.has(cpID)))

	// Properties that must hold no matter how the stop lands. These are the
	// real gate: if the tool never ran, or the graceful stop was never
	// requested from inside it, the scenario did not happen at all and any
	// conclusion drawn below would be about nothing.
	if n := tr.countEvents("tool:invoke"); n != 1 {
		t.Fatalf("probe_tool invoked %d times, want exactly 1", n)
	}
	if n := tr.countEvents("Stop:graceful"); n != 1 {
		t.Fatalf("graceful stop requested %d times from inside the tool, want exactly 1", n)
	}

	// TWO OUTCOMES ARE LEGAL HERE, and demanding only one is what made this
	// test fail intermittently.
	//
	// Stop is fire-and-forget: it records a request and returns, with no
	// barrier a caller can wait on. The framework documents that when a cancel
	// cannot be applied to the running agent, cancel options DEGRADE to "exit
	// the loop on entering the next iteration" — that is, the current turn runs
	// to completion instead. Calling Stop from inside the tool therefore races
	// the stop against the turn finishing on its own. Usually the stop wins;
	// occasionally it does not, and the loop exits cleanly with no interrupt.
	//
	// Both are correct framework behaviour, so both are asserted rather than
	// one being treated as a failure. What is NOT acceptable is passing without
	// checking anything, so each branch carries its own assertions.
	if rootCtxID != "" {
		// The stop landed while the turn was still cancellable.
		if !errors.As(exit.ExitReason, &ce) {
			t.Errorf("a root-cause interrupt context exists but the exit reason is not a cancel error: %v", exit.ExitReason)
		}
		if len(exit.InterruptedItems) == 0 {
			t.Error("a root-cause interrupt context exists but no items were recorded as interrupted")
		}
		t.Logf("OUTCOME: cancel applied — root-cause interrupt context ID = %s", rootCtxID)
	} else {
		// The stop degraded: the turn completed and the loop exited at the
		// next iteration. There is then nothing to resume from, which is a
		// legitimate result and not a defect.
		if errors.As(exit.ExitReason, &ce) {
			t.Errorf("no root-cause interrupt context, yet the exit reason is a cancel error — "+
				"the cancel applied but produced no usable target: %v", exit.ExitReason)
		}
		t.Log("OUTCOME: stop degraded to exit-at-next-iteration; the turn ran to completion, " +
			"so there is no interrupt target. Legal framework behaviour, asserted as such.")
	}
	t.Logf("\n=== SPIKE 3 arm C step 1 RAW TIMELINE ===\n%s", tr.render())
	t.Logf("exit: reason=%v attempted=%v err=%v interrupted=%d unhandled=%d",
		exit.ExitReason, exit.CheckpointAttempted, exit.CheckpointErr,
		len(exit.InterruptedItems), len(exit.UnhandledItems))
}
