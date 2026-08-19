package conformance

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/events"
	"github.com/iamclancyliang/pi-go/internal/runtime"
	"github.com/iamclancyliang/pi-go/internal/session"
	"github.com/iamclancyliang/pi-go/internal/tools"
)

// gatedTool blocks inside Call until it is released, so a test can act while a
// tool round is genuinely in flight.
//
// A sleep would not do. "While a tool round is active" is a precondition of
// both A2 and A3, and a sleep makes the scheduler decide whether the
// precondition held — so a green result would not mean the contract held.
type gatedTool struct {
	entered chan struct{}
	release chan struct{}
	result  string
}

func newGatedTool(result string) *gatedTool {
	return &gatedTool{
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
		result:  result,
	}
}

func (g *gatedTool) Name() string        { return "slow_read" }
func (g *gatedTool) Description() string { return "A read-only tool that blocks until released." }
func (g *gatedTool) Execution() tools.Execution {
	return tools.Execution{ReadOnly: true}
}

func (g *gatedTool) Call(ctx context.Context, _ string) (tools.Result, error) {
	select {
	case g.entered <- struct{}{}:
	default:
	}
	select {
	case <-g.release:
		return tools.Result{Content: g.result}, nil
	case <-ctx.Done():
		return tools.Result{}, ctx.Err()
	}
}

// waitEntered blocks until the tool has started executing.
func (g *gatedTool) waitEntered(t *testing.T) {
	t.Helper()
	select {
	case <-g.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("tool never started — the precondition 'a tool round is active' did not hold, so this run proves nothing")
	}
}

// TestA2SteeringDuringToolRound is acceptance scenario A2: "Steering arrives
// while a tool round is active" -> "Message appears before the next model
// request".
//
// C1b requires all three of pi's properties, judged separately and never
// rounded up:
//  1. the message enters context after a safe point and before the next model
//     request;
//  2. it is not treated as a new session;
//  3. completed tool results are not discarded.
func TestA2SteeringDuringToolRound(t *testing.T) {
	gate := newGatedTool("FILE-CONTENTS")
	registry := tools.NewRegistry()
	registry.MustRegister(gate)

	sess := session.New("You are pi-go.")
	model := &ai.Scripted{
		Name: "fake-1",
		Replies: []ai.Response{
			ai.AssistantToolCalls(ai.ToolCall{ID: "call-1", Name: "slow_read", Args: `{}`}),
		},
		StopWhenToolsSettled: true,
		Final:                ai.AssistantText("acknowledged"),
	}
	rec := runtime.NewRecorder()

	agent, err := runtime.New(runtime.Config{
		Model: model, ModelName: "fake-1", Tools: registry, Session: sess,
		Observers: []events.Observer{rec}, Now: fixedClock(),
	})
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	run, err := agent.Start(ctx, "Read the file.")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Steer while the tool round is provably in flight.
	//
	// Steer returns only once the preempt has been registered, so releasing
	// the tool afterwards cannot race it. The earlier version released the
	// tool as soon as the push was accepted, which left "the preempt is in
	// effect" assumed rather than established — and under load the turn
	// sometimes finished first, turning the steer into a follow-up.
	gate.waitEntered(t)
	if err := run.Steer("INJECTED"); err != nil {
		t.Fatalf("Steer: %v", err)
	}
	close(gate.release)

	if err := run.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	requests := model.Requests()
	if len(requests) < 2 {
		t.Fatalf("model called %d times, want at least 2 (one before the tool round, one after steering)", len(requests))
	}
	t.Logf("trace: %v", rec.Kinds())

	// Find the first model request that carries the steering message.
	// Asserting on CONTENT, not on message count or roles: a role-shape
	// check cannot tell "INJECTED arrived" from "some user message
	// arrived".
	steeredIdx := -1
	for i, req := range requests {
		if containsUserContent(req.Messages, "INJECTED") {
			steeredIdx = i
			break
		}
	}
	if steeredIdx < 0 {
		t.Fatalf("no model request ever contained the steering message; requests=%d", len(requests))
	}

	// (1) It arrived AFTER the tool round, not in the request that started
	// it. The first request must not contain it.
	if containsUserContent(requests[0].Messages, "INJECTED") {
		t.Error("steering appeared in the first model request — it was not injected mid-run at all")
	}
	steered := requests[steeredIdx]

	// (3) Completed tool results survived. Assert the RESULT CONTENT is
	// present, because this is exactly what a naive re-execution loses.
	if !containsToolContent(steered.Messages, "FILE-CONTENTS") {
		t.Errorf("the completed tool result was discarded by the steered execution: %s", describeRoles(steered.Messages))
	}

	// (2) Not a new session: the original prompt and the assistant's tool
	// call are still there.
	if !containsUserContent(steered.Messages, "Read the file.") {
		t.Errorf("original prompt missing — the steered execution started a new session: %s", describeRoles(steered.Messages))
	}
	if !containsToolCall(steered.Messages, "call-1") {
		t.Errorf("assistant tool call call-1 missing from the steered context: %s", describeRoles(steered.Messages))
	}

	// THE DISCRIMINATING ASSERTION. Everything above this point also
	// passes if the message was sent as a plain follow-up — verified by
	// mutation: swapping Steer for Follow left the whole test green. That
	// made it a test of "the message eventually arrived", not of steering.
	//
	// What actually separates them is the shape of the turn that was in
	// flight. Steering preempts at the safe point, so that turn is
	// TRUNCATED: it never makes its closing model call. A follow-up lets it
	// finish, producing a second model_response inside the same turn.
	if got := modelResponsesInTurn(rec, 1); got != 1 {
		t.Errorf("turn 1 produced %d model responses, want exactly 1 — "+
			"the steered turn must be truncated at the safe point, not run to completion. "+
			"A plain follow-up would produce 2 here.", got)
	}

	// The steering must then be consumed by a NEW execution.
	if got := countKind(rec, events.KindModelRequest); got != 2 {
		t.Errorf("model_request events = %d, want exactly 2 "+
			"(one truncated turn, one steered execution)", got)
	}

	// Truth retains everything, including the steering message.
	snap := sess.Snapshot()
	if !containsUserContent(snap.Messages, "INJECTED") {
		t.Error("steering message was never recorded as session truth")
	}
	if len(snap.Unmatched) != 0 {
		t.Errorf("unmatched tool calls after a settled steer: %v", snap.Unmatched)
	}
}

// TestA3FollowUpQueuedDuringRun is acceptance scenario A3: "Follow-up is queued
// during a run" -> "It is consumed only after the current run would stop".
//
// This is also the negative control for A2's preempt: identical timing, plain
// Push instead of a steering Push. If the in-flight turn saw this message too,
// steering and follow-up would be the same mechanism.
func TestA3FollowUpQueuedDuringRun(t *testing.T) {
	gate := newGatedTool("FILE-CONTENTS")
	registry := tools.NewRegistry()
	registry.MustRegister(gate)

	sess := session.New("You are pi-go.")
	model := &ai.Scripted{
		Name: "fake-1",
		Replies: []ai.Response{
			ai.AssistantToolCalls(ai.ToolCall{ID: "call-1", Name: "slow_read", Args: `{}`}),
		},
		StopWhenToolsSettled: true,
		Final:                ai.AssistantText("done"),
	}
	rec := runtime.NewRecorder()

	agent, err := runtime.New(runtime.Config{
		Model: model, ModelName: "fake-1", Tools: registry, Session: sess,
		Observers: []events.Observer{rec}, Now: fixedClock(),
	})
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	run, err := agent.Start(ctx, "Read the file.")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	gate.waitEntered(t)
	if err := run.Follow("FOLLOWUP"); err != nil {
		t.Fatalf("Follow: %v", err)
	}
	close(gate.release)

	if err := run.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	requests := model.Requests()
	t.Logf("trace: %v", rec.Kinds())
	if len(requests) < 2 {
		t.Fatalf("model called %d times, want at least 2", len(requests))
	}

	// The turn that was in flight must complete WITHOUT the follow-up. That
	// turn's closing model call is the one right after the tool settles —
	// request index 1.
	if containsUserContent(requests[1].Messages, "FOLLOWUP") {
		t.Errorf("the in-flight turn consumed the follow-up; it must wait for the next turn: %s",
			describeRoles(requests[1].Messages))
	}

	// And it must be consumed eventually, in a LATER request.
	consumedAt := -1
	for i, req := range requests {
		if containsUserContent(req.Messages, "FOLLOWUP") {
			consumedAt = i
			break
		}
	}
	if consumedAt < 0 {
		t.Fatalf("follow-up was never consumed across %d model requests", len(requests))
	}
	if consumedAt < 2 {
		t.Errorf("follow-up consumed at request %d, want >= 2 (after the in-flight turn closed)", consumedAt)
	}

	// A follow-up starts a NEW turn; steering does not. That difference is
	// what makes them distinct inputs.
	if turns := countKind(rec, events.KindTurnStart); turns < 2 {
		t.Errorf("turn_start events = %d, want >= 2 — a follow-up must open a new turn", turns)
	}

	// Truth keeps it, and keeps it in conversational order: recorded when
	// consumed, so it lands after the tool result rather than between the
	// tool call and its result.
	snap := sess.Snapshot()
	followIdx, toolIdx := -1, -1
	for i, m := range snap.Messages {
		if m.Role == ai.RoleUser && strings.Contains(m.Content, "FOLLOWUP") {
			followIdx = i
		}
		if m.Role == ai.RoleTool && strings.Contains(m.Content, "FILE-CONTENTS") {
			toolIdx = i
		}
	}
	if followIdx < 0 {
		t.Fatal("follow-up missing from session truth")
	}
	if toolIdx < 0 {
		t.Fatal("tool result missing from session truth")
	}
	if followIdx < toolIdx {
		t.Errorf("follow-up recorded at %d, before the tool result at %d — truth is not in conversational order",
			followIdx, toolIdx)
	}
}

func containsUserContent(msgs []ai.Message, want string) bool {
	for _, m := range msgs {
		if m.Role == ai.RoleUser && strings.Contains(m.Content, want) {
			return true
		}
	}
	return false
}

func containsToolContent(msgs []ai.Message, want string) bool {
	for _, m := range msgs {
		if m.Role == ai.RoleTool && strings.Contains(m.Content, want) {
			return true
		}
	}
	return false
}

func containsToolCall(msgs []ai.Message, id string) bool {
	for _, m := range msgs {
		for _, tc := range m.ToolCalls {
			if tc.ID == id {
				return true
			}
		}
	}
	return false
}

func describeRoles(msgs []ai.Message) string {
	var b strings.Builder
	b.WriteString("[")
	for i, m := range msgs {
		if i > 0 {
			b.WriteString(" ")
		}
		b.WriteString(string(m.Role))
		if m.Content != "" {
			b.WriteString(":" + truncateForLog(m.Content))
		}
	}
	b.WriteString("]")
	return b.String()
}

func truncateForLog(s string) string {
	r := []rune(s)
	if len(r) <= 24 {
		return string(r)
	}
	return string(r[:24]) + "…"
}

// modelResponsesInTurn counts model responses emitted inside one turn.
//
// This is the observable that separates a truncated turn from a completed one,
// and therefore steering from follow-up.
func modelResponsesInTurn(rec *runtime.Recorder, turn int) int {
	n := 0
	for _, e := range rec.Events() {
		if e.Kind == events.KindModelResponse && e.TurnIndex == turn {
			n++
		}
	}
	return n
}

func countKind(rec *runtime.Recorder, k events.Kind) int {
	n := 0
	for _, got := range rec.Kinds() {
		if got == k {
			n++
		}
	}
	return n
}
