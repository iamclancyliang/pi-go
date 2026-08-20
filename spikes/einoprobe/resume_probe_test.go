package einoprobe

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// buildCPLoop constructs a TurnLoop over a shared store/checkpoint. Step 2 uses
// a FRESH loop instance for the resume, which is what makes it a real resume
// rather than a continuation of the same in-memory loop.
func buildCPLoop(t *testing.T, tr *trace, store *memStore, cpID string, st *stoppingTool, sm model.BaseChatModel, resumeTargetID string, mech injectMech) *adk.TurnLoop[*schema.Message, *schema.Message] {
	t.Helper()

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
				fmt.Sprintf("interrupted=%d unhandled=%d new=%d target=%s", len(interrupted), len(unhandled), len(newItems), resumeTargetID))
			if resumeTargetID == "" {
				return nil, errors.New("no resume target id")
			}
			injected := 0
			modifier := func(ctx context.Context, history []adk.Message) []adk.Message {
				injected++
				tr.add(layerControl, "HistoryModifier:called",
					fmt.Sprintf("invocation=%d historyLen=%d", injected, len(history)))
				return append(history, schema.UserMessage("INJECTED"))
			}
			// The two mechanisms are kept STRICTLY separate. Passing both would make
			// "HistoryModifier called once" unattributable — it could have come from
			// either, and a future fix to the targeted path would silently turn it
			// into two.
			res := &adk.GenResumeResult[*schema.Message, *schema.Message]{Consumed: interrupted}
			switch mech {
			case injectRunOpt:
				// DEPRECATED and NOT target-scoped: applies to the whole resumed run.
				res.RunOpts = []adk.AgentRunOption{adk.WithHistoryModifier(modifier)}
			case injectTargeted:
				// Key is InterruptCtx.ID — matches eino's own cancel_edge_test.go.
				// The "addresses" wording in ResumeParams is stale.
				res.ResumeParams = &adk.ResumeParams{
					Targets: map[string]any{
						resumeTargetID: &adk.ChatModelAgentResumeData{HistoryModifier: modifier},
					},
				}
			}
			return res, nil
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
				if ev != nil && ev.Output != nil && ev.Output.MessageOutput != nil {
					mv := ev.Output.MessageOutput
					if msg, err := mv.GetMessage(); err == nil && msg != nil {
						tr.add(layerControl, "event:message",
							fmt.Sprintf("role=%s toolCallID=%q content=%q", msg.Role, msg.ToolCallID, truncate(msg.Content)))
					}
				}
			}
		},
	})
	st.setLoop(loop)
	return loop
}

func truncate(s string) string {
	if len(s) > 24 {
		return s[:24] + "…"
	}
	return s
}

// historyAwareModel decides from the input it is given rather than from a fixed
// script.
//
// This exists because a fresh scripted model is reset on run 2 and re-issues its
// first canned reply — which looks exactly like a framework tool replay. That
// false positive is precisely what this model removes: if the input already
// contains a settled tool result for tc1, the turn is finished, so answer.
type historyAwareModel struct {
	tr *trace
	mu sync.Mutex
	n  int
}

func (m *historyAwareModel) decide(in []*schema.Message) *schema.Message {
	settled := false
	for _, msg := range in {
		if msg != nil && msg.Role == schema.Tool && msg.ToolCallID == "tc1" {
			settled = true
		}
	}
	m.mu.Lock()
	m.n++
	idx := m.n
	m.mu.Unlock()

	roles := make([]string, 0, len(in))
	var userContents []string
	var pairing []string
	for _, msg := range in {
		if msg == nil {
			continue
		}
		roles = append(roles, string(msg.Role))
		if msg.Role == schema.User {
			userContents = append(userContents, msg.Content)
		}
		for _, tc := range msg.ToolCalls {
			pairing = append(pairing, "assistantToolCallID="+tc.ID)
		}
		if msg.Role == schema.Tool {
			pairing = append(pairing, fmt.Sprintf("toolMsgToolCallID=%s content=%q", msg.ToolCallID, msg.Content))
		}
	}
	m.tr.add(layerModel, "model:Generate", fmt.Sprintf(
		"call#%d inputs=%d roles=%v userContents=%v pairing=%v toolAlreadySettled=%v",
		idx, len(in), roles, userContents, pairing, settled))

	if settled {
		return schema.AssistantMessage("final", nil)
	}
	return schema.AssistantMessage("", []schema.ToolCall{{
		ID: "tc1", Function: schema.FunctionCall{Name: "probe_tool", Arguments: "{}"},
	}})
}

func (m *historyAwareModel) Generate(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return m.decide(in), nil
}

func (m *historyAwareModel) Stream(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	msg := m.decide(in)
	sr, sw := schema.Pipe[*schema.Message](1)
	go func() { defer sw.Close(); sw.Send(msg, nil) }()
	return sr, nil
}

var _ model.BaseChatModel = (*historyAwareModel)(nil)

// injectMech selects which resume-injection mechanism a run exercises.
type injectMech int

const (
	injectRunOpt injectMech = iota
	injectTargeted
)

// TestSpike3ResumeAfterCheckpoint — arm C step 2.
//
// Hard safety gate: a destructive tool must NOT be replayed, and an
// already-settled tool result must NOT be re-published.
func runArmC(t *testing.T, mech injectMech, cpID string, skipFinalCheckpoint bool) (*trace, *memStore) {
	t.Helper()
	tr := newTrace()
	store := newMemStore(tr)

	// --- run 1: stop gracefully from inside the tool, producing a checkpoint ---
	st1 := &stoppingTool{tr: tr}
	loop1 := buildCPLoop(t, tr, store, cpID, st1, &historyAwareModel{tr: tr}, "", mech)
	loop1.Push(schema.UserMessage("hello"))
	loop1.Run(context.Background())
	exit1 := loop1.Wait()

	var ce *adk.CancelError
	rootID := ""
	if errors.As(exit1.ExitReason, &ce) {
		for _, ic := range ce.InterruptContexts {
			if ic != nil && ic.IsRootCause {
				rootID = ic.ID
				break
			}
		}
	}
	if rootID == "" {
		// Name what actually came back. Without it this failure says only that
		// the expected shape was absent, which cannot distinguish "the framework
		// reported a different exit" from "the interrupt contexts were empty" —
		// and the two want opposite investigations.
		reason := "nil"
		if exit1 != nil && exit1.ExitReason != nil {
			reason = fmt.Sprintf("%T: %v", exit1.ExitReason, exit1.ExitReason)
		}
		contexts := "not a *adk.CancelError"
		if errors.As(exit1.ExitReason, &ce) {
			contexts = fmt.Sprintf("%d interrupt context(s), none root-cause",
				len(ce.InterruptContexts))
		}
		t.Fatalf("run 1 produced no root-cause interrupt context; ExitReason = %s; %s\n%s",
			reason, contexts, tr.render())
	}
	// run1 facts promoted from log lines to assertions.
	if ce == nil {
		t.Fatal("run 1 ExitReason is not a *CancelError")
	}
	if !exit1.CheckpointAttempted {
		t.Error("run 1 CheckpointAttempted = false, want true")
	}
	if exit1.CheckpointErr != nil {
		t.Errorf("run 1 CheckpointErr = %v, want nil", exit1.CheckpointErr)
	}
	if !store.has(cpID) {
		t.Error("run 1 wrote no checkpoint to the store")
	}
	tr.add(layerControl, "run1:done", fmt.Sprintf("rootTarget=%s checkpointPresent=%v", rootID, store.has(cpID)))

	// --- run 2: FRESH loop, same store + checkpoint id -> must resume ---
	tr.add(layerControl, "run2:start", "fresh TurnLoop over the same checkpoint")
	st2 := &stoppingTool{tr: tr}
	st2.stopped = true // do not stop again; let the resumed run complete
	loop2 := buildCPLoop(t, tr, store, cpID, st2, &historyAwareModel{tr: tr}, rootID, mech)
	loop2.Run(context.Background())
	// WithSkipCheckpoint declares "no further resume", which is what allows the
	// checkpoint to be cleaned up. Cleanup is REQUIRED WIRING, not automatic.
	stopOpts := []adk.StopOption{adk.UntilIdleFor(200_000_000)}
	if skipFinalCheckpoint {
		stopOpts = append(stopOpts, adk.WithSkipCheckpoint())
	}
	loop2.Stop(stopOpts...)
	exit2 := loop2.Wait()
	tr.add(layerControl, "run2:done", fmt.Sprintf("exitReason=%v checkpointPresent=%v", exit2.ExitReason, store.has(cpID)))

	t.Logf("\n=== SPIKE 3 arm C RAW TIMELINE (mech=%v) ===\n%s", mech, tr.render())
	return tr, store
}

// TestSpike3ArmCRunOpt — C-runopt: injection via the DEPRECATED, non-target-scoped
// WithHistoryModifier run option. This is the only arm-C path that currently works.
func TestSpike3ArmCRunOpt(t *testing.T) {
	tr, store := runArmC(t, injectRunOpt, "spike3-runopt", true)

	if n := tr.countEvents("GenResume:fired"); n != 1 {
		t.Errorf("GenResume fired %d times, want exactly 1", n)
	}
	if n := tr.countEvents("GenInput:enter"); n != 1 {
		t.Errorf("GenInput:enter fired %d times, want 1 (run 1 only)", n)
	}
	if n := tr.countEvents("tool:invoke"); n != 1 {
		t.Errorf("SAFETY: tool:invoke %d times across the lifecycle, want exactly 1", n)
	}
	settled := 0
	for _, d := range tr.detailsMatching("event:message") {
		if strings.Contains(d, `toolCallID="tc1"`) {
			settled++
		}
	}
	// Exactly one: "<= 1" would also pass at zero, which would mean the tool
	// result never reached the event stream at all.
	if settled != 1 {
		t.Errorf("SAFETY: settled tool result tc1 published %d times, want exactly 1", settled)
	}
	if n := tr.countEvents("HistoryModifier:called"); n != 1 {
		t.Errorf("HistoryModifier called %d times, want exactly 1", n)
	}
	calls := tr.detailsMatching("model:Generate")
	last := calls[len(calls)-1]
	if !strings.Contains(last, "INJECTED") || strings.Count(last, "INJECTED") != 1 {
		t.Errorf("resumed input must contain INJECTED exactly once: %s", last)
	}
	// Cleanup IS achievable — but only because WithSkipCheckpoint was passed.
	if n := tr.countEvents("store:Delete"); n != 1 {
		t.Errorf("store:Delete happened %d times, want 1 (cleanup requires WithSkipCheckpoint)", n)
	}
	// Delete being CALLED is not the same as the checkpoint being GONE — a no-op
	// delete would satisfy the count above and still leave state behind.
	if store.has("spike3-runopt") {
		t.Error("checkpoint still present after cleanup; Delete was called but did not remove it")
	}
}

// TestSpike3ArmCTargetedGap — C-targeted: ResumeParams.Targets +
// ChatModelAgentResumeData.
//
// WHAT THIS REPOSITORY PROVES: on the **TurnLoop cancel-resume path** in eino
// v0.9.14, targeted resume data is not delivered — GenResume runs, but the
// HistoryModifier never fires and INJECTED never reaches the model.
//
// WHAT IT DOES NOT PROVE HERE: that the gap is independent of TurnLoop. @gpt-codex
// reproduced the same zero-delivery directly against Runner.Run + WithCancel ->
// ResumeWithParams (and with both InterruptCtx.ID and Address keys), which points
// at the cancel-resume path itself rather than the TurnLoop bridge — but that
// experiment lives outside this repository and is therefore cited as EXTERNAL
// independent verification, not as something this test establishes.
//
// This test asserts the CURRENT behaviour. If eino fixes it, this test fails —
// which is the intent: the gap should be re-evaluated, not silently outlived.
func TestSpike3ArmCTargetedGap(t *testing.T) {
	tr, _ := runArmC(t, injectTargeted, "spike3-targeted", true)

	if n := tr.countEvents("GenResume:fired"); n != 1 {
		t.Errorf("GenResume fired %d times, want exactly 1 (resume itself works)", n)
	}
	if n := tr.countEvents("HistoryModifier:called"); n != 0 {
		t.Errorf("HistoryModifier called %d times — targeted resume data now DELIVERS; "+
			"the v0.9.14 capability gap may be fixed, re-evaluate arm C", n)
	}
	calls := tr.detailsMatching("model:Generate")
	last := calls[len(calls)-1]
	if strings.Contains(last, "INJECTED") {
		t.Errorf("targeted path now injects — re-evaluate: %s", last)
	}
}
