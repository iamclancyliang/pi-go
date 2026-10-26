package conformance

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/events"
	"github.com/iamclancyliang/pi-go/internal/tools"
)

// detailedTool reports structured details alongside its result.
type detailedTool struct{ timedTool }

func (t *detailedTool) Call(context.Context, string) (tools.Result, error) {
	return tools.Result{Content: "done", Details: map[string]int{"lines": 3}}, nil
}

// TestToolDetailsReachTheResultEventButNotTheTrace: an interface drawing a
// call in-process gets the tool's details on tool_result, while the
// serialised event — the published trace — keeps its shape.
func TestToolDetailsReachTheResultEventButNotTheTrace(t *testing.T) {
	registry := tools.NewRegistry()
	registry.MustRegister(&detailedTool{timedTool{name: "detailed"}})
	rec, _ := runRound(t, registry, ai.ToolCall{ID: "call-1", Name: "detailed", Args: `{}`})

	var result *events.Event
	for _, e := range rec.Events() {
		if e.Kind == events.KindToolResult {
			e := e
			result = &e
		}
	}
	if result == nil {
		t.Fatal("no tool_result event")
	}
	details, ok := result.Detail.ToolDetails.(map[string]int)
	if !ok || details["lines"] != 3 {
		t.Fatalf("details %#v", result.Detail.ToolDetails)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "lines") {
		t.Fatalf("details leaked into the trace: %s", encoded)
	}
}
