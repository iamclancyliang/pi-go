package conformance

import (
	"context"
	"strings"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/compaction"
	"github.com/iamclancyliang/pi-go/internal/session"
)

// TestA12ASplitTurnLeavesAContextAProviderAccepts. One question followed by
// many rounds of tool work used to be impossible to compact, because the only
// place a cut could land was the question itself. Split, the context the
// model sees next must still be well formed: something from the user comes
// before the kept reply, and every tool result follows the call it answers.
func TestA12ASplitTurnLeavesAContextAProviderAccepts(t *testing.T) {
	long := strings.Repeat("x", 4000)
	sess := session.New("You are pi-go.")
	turn := []ai.Message{{Role: ai.RoleUser, Content: "refactor the parser"}}
	for _, id := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		turn = append(turn,
			ai.Message{Role: ai.RoleAssistant, ToolCalls: []ai.ToolCall{{ID: id, Name: "read", Args: `{"path":"p.go"}`}}},
			ai.Message{Role: ai.RoleTool, ToolCallID: id, Content: long})
	}
	for _, m := range turn {
		if err := sess.Append(m); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	c := &compaction.Compactor{Model: &ai.Scripted{Name: "m", Final: ai.AssistantText("PREFIX")}, ModelName: "m", KeepRecentTokens: 2500}
	summary, kept, err := c.Summarize(context.Background(), sess.Truth())
	if err != nil {
		t.Fatalf("a single long turn could not be compacted: %v", err)
	}
	if err := sess.Compact(summary, kept); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	projected := sess.Project().Messages
	var firstOther ai.Message
	for _, m := range projected {
		if m.Role != ai.RoleSystem {
			firstOther = m
			break
		}
	}
	if firstOther.Role != ai.RoleUser || !strings.Contains(firstOther.Content, "PREFIX") {
		t.Fatalf("the context does not open with the summary as a user message: %+v", firstOther)
	}
	called := map[string]bool{}
	for _, m := range projected {
		for _, call := range m.ToolCalls {
			called[call.ID] = true
		}
		if m.Role == ai.RoleTool && !called[m.ToolCallID] {
			t.Fatalf("a tool result for %q arrives before its call: %v", m.ToolCallID, contentsOf(projected))
		}
	}
	if len(projected) >= len(turn)+1 {
		t.Fatalf("nothing was dropped: %d messages projected from a %d-message turn", len(projected), len(turn))
	}
}
