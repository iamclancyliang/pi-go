package conformance

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/events"
	"github.com/iamclancyliang/pi-go/internal/tools"
)

// TestA4TruncatedMessageRunsNoToolCalls pins that truncation fails ALL of them.
//
// Truncation cuts a call's arguments part-way, and cut arguments can still be
// valid on their own: half a path is a path, a shortened command is a command. So
// whether the arguments parse says nothing about whether they are what the model
// meant. Checking each call and running the ones that parse keeps exactly the
// calls whose meaning is most likely to have changed, which is the inverse of the
// rule — and it matters most for the calls that touch a user's files.
func TestA4TruncatedMessageRunsNoToolCalls(t *testing.T) {
	counted := &countingTool{timedTool: timedTool{name: "counted_tool", delay: time.Millisecond}}
	registry := tools.NewRegistry()
	registry.MustRegister(counted)

	truncated := ai.AssistantToolCalls(
		ai.ToolCall{ID: "call-1", Name: "counted_tool", Args: `{"path":"a.txt"}`},
		ai.ToolCall{ID: "call-2", Name: "counted_tool", Args: `{"path":"b.tx`},
		ai.ToolCall{ID: "call-3", Name: "counted_tool", Args: `{}`},
	)
	truncated.Truncated = true

	rec, sess := runRoundWith(t, registry, truncated)

	if got := counted.ran(); got != 0 {
		t.Errorf("a truncated message ran %d tool call(s); it must run none", got)
	}

	// All three are still reported: failing them silently would leave the model
	// with calls it never hears about again, and it would wait for them.
	if got := len(idsOf(rec, events.KindToolResult)); got != 3 {
		t.Errorf("results reported = %d, want 3 (every call failed, none skipped)", got)
	}
	for _, m := range sess.Truth() {
		if m.Role == ai.RoleTool && m.Content == "" {
			t.Errorf("call %s was failed with no explanation", m.ToolCallID)
		}
	}
}

// TestA4UntruncatedMessageStillRuns is the control.
//
// Without it the test above passes against a runtime that never runs any tool at
// all, which is the failure it is supposed to distinguish from correct refusal.
func TestA4UntruncatedMessageStillRuns(t *testing.T) {
	counted := &countingTool{timedTool: timedTool{name: "counted_tool", delay: time.Millisecond}}
	registry := tools.NewRegistry()
	registry.MustRegister(counted)

	// The SAME calls, including the one whose arguments were cut, differing only
	// in that the message was not truncated.
	intact := ai.AssistantToolCalls(
		ai.ToolCall{ID: "call-1", Name: "counted_tool", Args: `{"path":"a.txt"}`},
		ai.ToolCall{ID: "call-2", Name: "counted_tool", Args: `{"path":"b.tx`},
		ai.ToolCall{ID: "call-3", Name: "counted_tool", Args: `{}`},
	)

	rec, _ := runRoundWith(t, registry, intact)

	if got := counted.ran(); got != 3 {
		t.Errorf("an intact message ran %d tool call(s), want 3", got)
	}
	if got := len(idsOf(rec, events.KindToolResult)); got != 3 {
		t.Errorf("results reported = %d, want 3", got)
	}
}

// countingTool records how many times it actually ran.
//
// Counting runs rather than results is the point: a call can be reported as
// failed without ever having run, and that difference is the contract.
type countingTool struct {
	timedTool
	mu    sync.Mutex
	calls int
}

func (c *countingTool) Call(ctx context.Context, args string) (tools.Result, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return c.timedTool.Call(ctx, args)
}

func (c *countingTool) ran() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}
