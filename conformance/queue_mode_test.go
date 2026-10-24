package conformance

import (
	"context"
	"testing"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/events"
	"github.com/iamclancyliang/pi-go/internal/runtime"
	"github.com/iamclancyliang/pi-go/internal/session"
	"github.com/iamclancyliang/pi-go/internal/tools"
)

// queueRun starts a run whose first reply calls the gated tool and whose
// later replies are `then`, queues `queue` while the tool is in flight, and
// returns every request the model was sent.
func queueRun(t *testing.T, steering, followUp runtime.QueueMode, steer bool, then []ai.Response, queue ...string) []ai.Request {
	t.Helper()
	gate := newGatedTool("FILE-CONTENTS")
	registry := tools.NewRegistry()
	registry.MustRegister(gate)
	model := &ai.Scripted{
		Name:    "fake-1",
		Replies: append([]ai.Response{ai.AssistantToolCalls(ai.ToolCall{ID: "call-1", Name: "slow_read", Args: `{}`})}, then...),
		Final:   ai.AssistantText("done"),
	}
	agent, err := runtime.New(runtime.Config{
		Model: model, ModelName: "fake-1", Tools: registry, Session: session.New("You are pi-go."),
		Observers: []events.Observer{runtime.NewRecorder()}, Now: fixedClock(),
		SteeringMode: steering, FollowUpMode: followUp,
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
	for _, text := range queue {
		if steer {
			err = run.Steer(text)
		} else {
			err = run.Follow(text)
		}
		if err != nil {
			t.Fatalf("queueing %q: %v", text, err)
		}
	}
	close(gate.release)
	if err := run.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	return model.Requests()
}

// firstCarrying is the index of the first request holding the user text.
func firstCarrying(requests []ai.Request, text string) int {
	for i, r := range requests {
		if containsUserContent(r.Messages, text) {
			return i
		}
	}
	return -1
}

// TestOneAtATimeFollowUpsArriveOneRunEndingEach, Pi's default: the point the
// run would stop takes the oldest follow-up, and the next one waits for the
// next such point.
func TestOneAtATimeFollowUpsArriveOneRunEndingEach(t *testing.T) {
	requests := queueRun(t, "", "", false, nil, "F1", "F2")
	first, second := firstCarrying(requests, "F1"), firstCarrying(requests, "F2")
	if first < 0 || second < 0 {
		t.Fatalf("a follow-up never arrived: F1 at %d, F2 at %d", first, second)
	}
	if second <= first {
		t.Fatalf("F2 arrived with F1 (requests %d and %d) under one-at-a-time", first, second)
	}
}

// TestAllFollowUpsArriveTogether when the mode says so.
func TestAllFollowUpsArriveTogether(t *testing.T) {
	requests := queueRun(t, "", runtime.QueueAll, false, nil, "F1", "F2")
	first := firstCarrying(requests, "F1")
	if first < 0 || !containsUserContent(requests[first].Messages, "F2") {
		t.Fatalf("under all, F1 and F2 did not arrive in one request (F1 at %d)", first)
	}
}

// TestALeftOverSteerIsTakenAfterTheNextToolRound, Pi's next drain point,
// rather than waiting for the run to stop. The reply to S1 calls a tool;
// S2 must be in the request that follows that tool's result, not only in a
// request after the run would have ended.
func TestALeftOverSteerIsTakenAfterTheNextToolRound(t *testing.T) {
	then := []ai.Response{
		ai.AssistantToolCalls(ai.ToolCall{ID: "call-2", Name: "slow_read", Args: `{}`}),
	}
	requests := queueRun(t, "", "", true, then, "S1", "S2")
	first := firstCarrying(requests, "S1")
	if first < 0 {
		t.Fatal("the first steering message never arrived")
	}
	if containsUserContent(requests[first].Messages, "S2") {
		t.Fatal("S2 arrived with S1 under one-at-a-time")
	}
	second := firstCarrying(requests, "S2")
	if second != first+1 {
		t.Fatalf("S2 arrived in request %d, want %d: the one right after the next tool round", second, first+1)
	}
	if !containsToolCall(requests[second].Messages, "call-2") || !containsToolContent(requests[second].Messages, "FILE-CONTENTS") {
		t.Fatalf("S2 was not taken after call-2's round: %s", describeRoles(requests[second].Messages))
	}
}

// TestAllSteeringArrivesTogether when the mode says so.
func TestAllSteeringArrivesTogether(t *testing.T) {
	requests := queueRun(t, runtime.QueueAll, "", true, nil, "S1", "S2")
	first := firstCarrying(requests, "S1")
	if first < 0 || !containsUserContent(requests[first].Messages, "S2") {
		t.Fatalf("under all, S1 and S2 did not arrive in one request (S1 at %d)", first)
	}
}

// TestAFollowUpWaitsEvenWhenASteerInterrupts. A steer preempts the turn and
// starts a new one; a follow-up queued before it still belongs to the point
// the run would stop, not to the steer's.
func TestAFollowUpWaitsEvenWhenASteerInterrupts(t *testing.T) {
	gate := newGatedTool("FILE-CONTENTS")
	registry := tools.NewRegistry()
	registry.MustRegister(gate)
	model := &ai.Scripted{
		Name:    "fake-1",
		Replies: []ai.Response{ai.AssistantToolCalls(ai.ToolCall{ID: "call-1", Name: "slow_read", Args: `{}`})},
		Final:   ai.AssistantText("done"),
	}
	agent, err := runtime.New(runtime.Config{
		Model: model, ModelName: "fake-1", Tools: registry, Session: session.New("You are pi-go."), Now: fixedClock(),
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
	if err := run.Follow("LATER"); err != nil {
		t.Fatal(err)
	}
	if err := run.Steer("NOW"); err != nil {
		t.Fatal(err)
	}
	close(gate.release)
	if err := run.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	requests := model.Requests()
	steered, followed := firstCarrying(requests, "NOW"), firstCarrying(requests, "LATER")
	if steered < 0 || followed < 0 {
		t.Fatalf("NOW at %d, LATER at %d", steered, followed)
	}
	if followed <= steered {
		t.Fatalf("the follow-up arrived with the steer (request %d), before the run would have stopped", followed)
	}
}
