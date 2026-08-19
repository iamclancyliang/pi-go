package render

import (
	"strings"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/events"
)

// TestEveryKindIsRendered fails when a new event kind reaches the renderer
// untaught.
//
// Without it, adding a kind ships a blank detail column: the event appears in the
// trace, carries information, and displays nothing — and every existing test still
// passes, because none of them look at what the tracer prints.
func TestEveryKindIsRendered(t *testing.T) {
	for _, kind := range events.AllKinds() {
		if _, known := Event(events.Event{Kind: kind}); !known {
			t.Errorf("%s reaches the renderer with no case", kind)
		}
	}
}

// TestEndAndResultReadDifferently keeps the two moments distinguishable.
//
// They carry the same text, so rendering them identically would make a trace
// unable to show the difference between a call finishing and its result being
// recorded — which is the distinction the separate kinds exist for.
func TestEndAndResultReadDifferently(t *testing.T) {
	end := events.Event{Kind: events.KindToolEnd, ToolName: "t", ToolCallID: "1"}
	end.Detail.Result = "same text"
	result := events.Event{Kind: events.KindToolResult, ToolName: "t", ToolCallID: "1"}
	result.Detail.Result = "same text"

	endText, _ := Event(end)
	resultText, _ := Event(result)
	if endText == resultText {
		t.Errorf("tool_end and tool_result render identically: %q", endText)
	}
}

func TestTruncateCountsCharacters(t *testing.T) {
	// Bytes would cut a multi-byte character in half and produce invalid text.
	if got := Truncate("排排排排", 2); got != "排排…" {
		t.Errorf("Truncate = %q, want %q", got, "排排…")
	}
	if got := Truncate("short", 40); got != "short" {
		t.Errorf("Truncate shortened a string that fits: %q", got)
	}
	if !strings.HasSuffix(Truncate(strings.Repeat("a", 50), 40), "…") {
		t.Error("a truncated string does not show that it was cut")
	}
}
