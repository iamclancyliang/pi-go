package einoprobe

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// pushingTool performs the Push from INSIDE the tool body.
//
// This is the deterministic barrier required by P2: when the tool runs, the
// turn is demonstrably in flight, the first model call has completed, and the
// second has not begun. No sleep is involved, so scheduling cannot decide where
// the injection lands.
type pushingTool struct {
	tr   *trace
	mu   sync.Mutex
	loop *adk.TurnLoop[*schema.Message, *schema.Message]
	// preempt selects C1b (Push + WithPreempt) vs C1a (plain Push).
	preempt bool
	pushed  bool
	// resolved is Push's preempt-request resolution channel. It closes either
	// after a cancel was submitted for the target turn or as a no-op, so it says
	// the request finished — not that it did anything.
	resolved <-chan struct{}
}

// requestResolved returns the channel Push handed back for this run's preempt.
func (p *pushingTool) requestResolved() <-chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.resolved
}

func (p *pushingTool) setLoop(l *adk.TurnLoop[*schema.Message, *schema.Message]) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.loop = l
}

func (p *pushingTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "probe_tool", Desc: "pushes a message while the turn is in flight"}, nil
}

func (p *pushingTool) InvokableRun(ctx context.Context, args string, opts ...tool.Option) (string, error) {
	p.tr.add(layerTool, "tool:invoke", fmt.Sprintf("probe_tool args=%s", args))

	p.mu.Lock()
	l, already := p.loop, p.pushed
	p.pushed = true
	prem := p.preempt
	p.mu.Unlock()

	if l != nil && !already {
		msg := schema.UserMessage("INJECTED")
		var ok bool
		var done <-chan struct{}
		if prem {
			ok, done = l.Push(msg, adk.WithPreempt[*schema.Message, *schema.Message](adk.AfterToolCalls))
			p.mu.Lock()
			p.resolved = done
			p.mu.Unlock()
			p.tr.add(layerControl, "Push:withPreempt", fmt.Sprintf("accepted=%v doneNil=%v safePoint=AfterToolCalls", ok, done == nil))
		} else {
			ok, done = l.Push(msg)
			p.tr.add(layerControl, "Push:plain", fmt.Sprintf("accepted=%v doneNil=%v", ok, done == nil))
		}
	}
	return "tool-result", nil
}

var _ tool.InvokableTool = (*pushingTool)(nil)

// sessionTruth is pi-go-owned conversation history.
//
// It is NOT eino's job: `Remaining` only pushes T items back into the TurnLoop
// buffer (that would create duplicate work items, not continuity). Continuity of
// the conversation is ours to maintain and reconstruct.
type sessionTruth struct {
	mu   sync.Mutex
	msgs []*schema.Message
}

func (s *sessionTruth) append(m *schema.Message) {
	if m == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c := *m
	s.msgs = append(s.msgs, &c)
}

func (s *sessionTruth) all() []*schema.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*schema.Message, len(s.msgs))
	copy(out, s.msgs)
	return out
}

func runSteeringProbe(t *testing.T, preempt bool, streaming bool) *trace {
	t.Helper()
	tr := newTrace()
	hist := &sessionTruth{}

	sm := &scriptedModel{tr: tr, replies: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{{
			ID: "tc1", Function: schema.FunctionCall{Name: "probe_tool", Arguments: "{}"},
		}}),
		schema.AssistantMessage("final", nil),
		schema.AssistantMessage("final-2", nil),
	}}

	pt := &pushingTool{tr: tr, preempt: preempt}
	var turnMu sync.Mutex
	turnCount := 0

	agent, err := adk.NewChatModelAgent(context.Background(), &adk.ChatModelAgentConfig{
		Name: "steerprobe", Description: "C1", Instruction: "probe",
		Model: sm,
		ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{
			Tools: []tool.BaseTool{pt},
		}},
	})
	if err != nil {
		t.Fatalf("NewChatModelAgent: %v", err)
	}

	loop := adk.NewTurnLoop(adk.TurnLoopConfig[*schema.Message, *schema.Message]{
		GenInput: func(ctx context.Context, l *adk.TurnLoop[*schema.Message, *schema.Message], items []*schema.Message) (*adk.GenInputResult[*schema.Message, *schema.Message], error) {
			contents := make([]string, 0, len(items))
			for _, it := range items {
				contents = append(contents, it.Content)
			}
			tr.beginGenInput(fmt.Sprintf("buffered=%d contents=%v", len(items), contents))
			if len(items) == 0 {
				return nil, nil
			}
			for _, it := range items {
				hist.append(it)
			}
			full := hist.all()
			roles := make([]string, 0, len(full))
			for _, m := range full {
				roles = append(roles, string(m.Role))
			}
			tr.add(layerControl, "input:reconstructed", fmt.Sprintf("messages=%d roles=%v", len(full), roles))
			return &adk.GenInputResult[*schema.Message, *schema.Message]{
				Input:    &adk.TypedAgentInput[*schema.Message]{Messages: full, EnableStreaming: streaming},
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
					break
				}
				// Materialize real assistant / tool messages into pi-go-owned truth.
				if ev != nil && ev.Output != nil && ev.Output.MessageOutput != nil {
					mv := ev.Output.MessageOutput
					// GetMessage materialises BOTH paths: it returns the message
					// directly when not streaming, and concatenates the stream when
					// it is. pi's real path is streaming, so ignoring IsStreaming=true
					// would verify a path pi does not use.
					msg, err := mv.GetMessage()
					if err != nil {
						tr.add(layerControl, "history:materialize_error", err.Error())
						return fmt.Errorf("materialize failed: %w", err)
					} else if msg != nil {
						hist.append(msg)
						tr.add(layerControl, "history:materialized",
							fmt.Sprintf("streaming=%v role=%s toolCalls=%d toolCallID=%q",
								mv.IsStreaming, msg.Role, len(msg.ToolCalls), msg.ToolCallID))
					}
				}
			}
			// Preempt observation. The channel is non-nil every turn, so non-nil
			// proves nothing — only closure signals a real safe-point cut.
			//
			// Drain completion and the watcher closing the channel are separate
			// goroutines, so a single non-blocking peek is racy. In the preempt
			// scenario we WAIT for closure; in the plain scenario we assert it stays
			// open. Recorded before returning, hence before the next GenInput.
			//
			// Only the turn that actually contained the preempting Push waits. A
			// later turn was never preempted, so waiting on it would block for a
			// closure that is not coming and stall the run instead of testing it.
			turnNo := 0
			turnMu.Lock()
			turnCount++
			turnNo = turnCount
			turnMu.Unlock()

			if preempt && turnNo == 1 {
				// WAIT ON THE REQUEST'S OWN RESOLUTION, then look.
				//
				// Push returns a channel that closes when the preempt request is
				// resolved — either after a cancel was submitted for the target
				// turn, or as a NO-OP when there was no target turn to cancel.
				// Both are resolutions, so waiting on Preempted instead blocks
				// forever whenever the request resolved without contributing.
				//
				// A clock cannot stand in for either: it measures machine load, and
				// it cannot tell "not yet" from "never".
				if resolved := pt.requestResolved(); resolved != nil {
					<-resolved
				}
				select {
				case <-tc.Preempted:
					tr.add(layerControl, "preempt:contributed", "Preempted closed at safe point")
				default:
					tr.add(layerControl, "preempt:noop", "request resolved without contributing")
				}
			} else {
				select {
				case <-tc.Preempted:
					tr.add(layerControl, "preempt:unexpected", "Preempted closed without WithPreempt")
					return fmt.Errorf("Preempted closed without WithPreempt")
				default:
					tr.add(layerControl, "preempt:absent", "Preempted still open (expected for plain Push)")
				}
			}
			return nil
		},
	})
	pt.setLoop(loop)

	loop.Push(schema.UserMessage("hello"))
	loop.Run(context.Background())
	loop.Stop(adk.UntilIdleFor(250 * time.Millisecond))
	exit := loop.Wait()
	// An error returned by OnAgentEvents surfaces here. Without this check the
	// error branches above would record a trace line and still pass.
	if exit != nil && exit.ExitReason != nil {
		t.Fatalf("loop exited with error: %v", exit.ExitReason)
	}
	return tr
}

// TestC1aFollowUpContract — regression gate. Plain Push from inside the tool:
// the item must be buffered to the NEXT GenInput iteration (follow-up), never
// consumed by the in-flight turn. Also the negative control for the preempt
// signal: no safe-point closure may occur without WithPreempt.
func TestC1aFollowUpContract(t *testing.T) {
	tr := runSteeringProbe(t, false, false)
	t.Logf("\n=== C1a RAW TIMELINE (plain Push) ===\n%s", tr.render())

	// --- C1a assertions (also the negative control for the preempt signal) ---
	if n := tr.countEvents("Push:plain"); n != 1 {
		t.Fatalf("Push:plain recorded %d times, want 1", n)
	}
	if !strings.Contains(tr.detailsMatching("Push:plain")[0], "accepted=true") {
		t.Errorf("plain Push not accepted: %s", tr.detailsMatching("Push:plain")[0])
	}
	for _, ev := range []string{"preempt:contributed", "preempt:unexpected"} {
		if n := tr.countEvents(ev); n != 0 {
			t.Errorf("%s occurred %d times without WithPreempt, want 0", ev, n)
		}
	}
	calls := tr.detailsMatching("model:Generate")
	if len(calls) < 2 {
		t.Fatalf("expected at least 2 model calls, got %d", len(calls))
	}
	// The in-flight turn must complete WITHOUT the injected message. Assert on
	// the delivered CONTENT — a role-shape check cannot tell "INJECTED arrived"
	// apart from "a user message arrived".
	if strings.Contains(calls[1], "INJECTED") {
		t.Errorf("call#2 must not contain INJECTED: %s", calls[1])
	}
	if len(calls) < 3 {
		t.Fatalf("expected a 3rd model call after the injected GenInput, got %d", len(calls))
	}
	if !strings.Contains(calls[2], "INJECTED") {
		t.Errorf("call#3 must contain INJECTED once consumed: %s", calls[2])
	}
	if tr.countInGen("model:Generate", 1) != 2 {
		t.Errorf("gen1 model calls = %d, want 2 (turn completes normally)", tr.countInGen("model:Generate", 1))
	}
	// Injected item is consumed only in the NEXT GenInput iteration.
	inj := tr.firstSeq("GenInput:enter", "INJECTED")
	if inj < 0 {
		t.Fatal("INJECTED never reached a GenInput iteration")
	}
	if m2 := tr.nthSeq("model:Generate", 2); m2 > inj {
		t.Errorf("call#2 (seq %d) must precede the INJECTED GenInput (seq %d)", m2, inj)
	}
}

// TestC1bSteeringContract — regression gate. Push + WithPreempt(AfterToolCalls).
// Asserts safe-point truncation, ordering, ID pairing, and that the steered
// content is actually delivered exactly once.
func TestC1bSteeringContract(t *testing.T) {
	tr := runSteeringProbe(t, true, false)
	t.Logf("\n=== C1b RAW TIMELINE (Push + WithPreempt) ===\n%s", tr.render())
	assertC1b(t, tr, "model:Generate")
}

// assertC1b holds the C1b regression claims, shared by the non-streaming and
// streaming variants so neither can drift from the other.
func assertC1b(t *testing.T, tr *trace, modelEvent string) {
	t.Helper()

	if n := tr.countEvents("Push:withPreempt"); n != 1 {
		t.Fatalf("Push:withPreempt recorded %d times, want 1", n)
	}
	if !strings.Contains(tr.detailsMatching("Push:withPreempt")[0], "accepted=true") {
		t.Errorf("preempting Push not accepted: %s", tr.detailsMatching("Push:withPreempt")[0])
	}
	if n := tr.countEvents("preempt:contributed"); n != 1 {
		t.Errorf("preempt:contributed = %d, want exactly 1", n)
	}
	for _, bad := range []string{"preempt:unexpected", "history:materialize_error"} {
		if n := tr.countEvents(bad); n != 0 {
			t.Errorf("%s occurred %d times, want 0", bad, n)
		}
	}
	// Safe-point truncation: the preempted turn makes exactly one model call.
	if n := tr.countInGen(modelEvent, 1); n != 1 {
		t.Errorf("gen1 model calls = %d, want 1 (turn truncated at safe point)", n)
	}
	// Strict ordering through the safe point.
	push := tr.firstSeq("Push:withPreempt", "")
	toolMat := tr.firstSeq("history:materialized", "role=tool")
	pre := tr.firstSeq("preempt:contributed", "")
	rebuild := tr.firstSeq("input:reconstructed", "assistant tool user")
	call2 := tr.nthSeq(modelEvent, 2)
	for _, s := range []struct {
		name string
		a, b int
	}{
		{"Push<toolResultMaterialized", push, toolMat},
		{"toolResult<preempt", toolMat, pre},
		{"preempt<gen2Reconstructed", pre, rebuild},
		{"gen2Reconstructed<call#2", rebuild, call2},
	} {
		if s.a < 0 || s.b < 0 || s.a >= s.b {
			t.Errorf("ordering %s violated: %d vs %d", s.name, s.a, s.b)
		}
	}
	// call#2 must carry the reconstructed context AND the intact ID pairing.
	calls := tr.detailsMatching(modelEvent)
	if len(calls) < 2 {
		t.Fatalf("expected 2 model calls, got %d", len(calls))
	}
	if !strings.Contains(calls[1], "roles=[system user assistant tool user]") {
		t.Errorf("call#2 roles = %s, want [system user assistant tool user]", calls[1])
	}
	for _, want := range []string{"assistantToolCallID=tc1", "toolMsgToolCallID=tc1", `content="tool-result"`} {
		if !strings.Contains(calls[1], want) {
			t.Errorf("call#2 missing %s: %s", want, calls[1])
		}
	}
	// The steered message must actually be delivered, exactly once — proven by
	// content, not by counting an extra user role.
	if !strings.Contains(calls[1], "INJECTED") {
		t.Errorf("call#2 must contain the steered message INJECTED: %s", calls[1])
	}
	if n := strings.Count(calls[1], "INJECTED"); n != 1 {
		t.Errorf("INJECTED appears %d times in call#2 input, want exactly 1: %s", n, calls[1])
	}
	if !strings.Contains(calls[1], "userContents=[hello INJECTED]") {
		t.Errorf("call#2 userContents = %s, want [hello INJECTED]", calls[1])
	}
}

// TestC1bSteeringContractStreaming runs the same C1b scenario with
// EnableStreaming=true. pi's real path is streaming, so verifying only the
// non-streaming path would leave the production path unverified.
func TestC1bSteeringContractStreaming(t *testing.T) {
	tr := runSteeringProbe(t, true, true)
	t.Logf("\n=== C1b RAW TIMELINE (streaming) ===\n%s", tr.render())

	// The streaming path must actually be exercised — otherwise a silent
	// regression to Generate would keep this test green while leaving pi's real
	// path unverified.
	if n := tr.countEvents("model:Stream"); n < 2 {
		t.Errorf("model:Stream calls = %d, want >= 2 (streaming path must be used)", n)
	}
	if n := tr.countEvents("model:Generate"); n != 0 {
		t.Errorf("model:Generate used %d times in the streaming variant, want 0", n)
	}
	foundStreamingAssistant := false
	for _, d := range tr.detailsMatching("history:materialized") {
		if strings.Contains(d, "streaming=true") && strings.Contains(d, "role=assistant") {
			foundStreamingAssistant = true
		}
	}
	if !foundStreamingAssistant {
		t.Error("no assistant message materialized from a stream (streaming=true)")
	}
	assertC1b(t, tr, "model:Stream")
}
