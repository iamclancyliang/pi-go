package conformance

import (
	"context"
	"testing"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/runtime"
	"github.com/iamclancyliang/pi-go/internal/session"
	"github.com/iamclancyliang/pi-go/internal/tools"
)

// TestAThinkingLevelChangedBetweenRunsReachesTheNextRequest. The level is a
// property of the run that can change during it; a value copied when the
// agent was built would keep asking for the old level while the client was
// told the new one is in effect.
func TestAThinkingLevelChangedBetweenRunsReachesTheNextRequest(t *testing.T) {
	model := &ai.Scripted{Name: "fake-1", Final: ai.AssistantText("ok")}
	agent, err := runtime.New(runtime.Config{
		Model: model, ModelName: "fake-1", Tools: tools.NewRegistry(),
		Session: session.New("You are pi-go."), Thinking: ai.ThinkingHigh,
	})
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := agent.Run(ctx, "first"); err != nil {
		t.Fatalf("first run: %v", err)
	}
	agent.SetThinking(ai.ThinkingLow)
	if got := agent.Thinking(); got != ai.ThinkingLow {
		t.Fatalf("the agent reports %q after being set to low", got)
	}
	if err := agent.Run(ctx, "second"); err != nil {
		t.Fatalf("second run: %v", err)
	}

	requests := model.Requests()
	if len(requests) != 2 {
		t.Fatalf("%d requests, want 2", len(requests))
	}
	if requests[0].Thinking != ai.ThinkingHigh || requests[1].Thinking != ai.ThinkingLow {
		t.Fatalf("the requests asked for %q then %q, want high then low",
			requests[0].Thinking, requests[1].Thinking)
	}
}
