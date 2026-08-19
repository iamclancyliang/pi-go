package conformance

import (
	"testing"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/events"
	"github.com/iamclancyliang/pi-go/internal/tools"
)

// TestA5SequentialToolSerialisesItsRound pins that one such tool is enough.
//
// The declaration belongs to a tool, but the consequence belongs to the round:
// a call that cannot tolerate overlap cannot be made safe by running only the
// others concurrently, so the whole round runs one call at a time.
func TestA5SequentialToolSerialisesItsRound(t *testing.T) {
	registry := tools.NewRegistry()
	registry.MustRegister(&timedTool{name: "plain_tool", delay: 1 * time.Millisecond})
	registry.MustRegister(&timedTool{
		name:       "exclusive_tool",
		delay:      1 * time.Millisecond,
		sequential: true,
	})

	rec, _ := runRound(t, registry,
		ai.ToolCall{ID: "call-plain", Name: "plain_tool", Args: `{}`},
		ai.ToolCall{ID: "call-exclusive", Name: "exclusive_tool", Args: `{}`},
	)

	want := []events.Kind{
		events.KindToolStart, events.KindToolEnd, events.KindToolResult,
		events.KindToolStart, events.KindToolEnd, events.KindToolResult,
	}
	if got := toolKinds(rec); !sameKinds(got, want) {
		t.Errorf("shape = %v, want each call finished before the next starts %v", got, want)
	}
	if want := []string{"call-plain", "call-exclusive"}; !equal(idsOf(rec, events.KindToolResult), want) {
		t.Errorf("results = %v, want source order %v", idsOf(rec, events.KindToolResult), want)
	}
}

// TestA5UnusedSequentialToolDoesNotSerialise pins that the consequence is scoped
// to the ROUND and not to the registry.
//
// The exclusive tool is registered and never called. Deciding concurrency from
// the registry — the shape this replaced — serialises every round for the life of
// the process, including rounds like this one that never touch it. That
// regression leaves every per-call assertion intact and changes only the shape,
// so without this test nothing notices.
func TestA5UnusedSequentialToolDoesNotSerialise(t *testing.T) {
	registry := tools.NewRegistry()
	registry.MustRegister(&timedTool{name: "slow_tool", delay: 120 * time.Millisecond})
	registry.MustRegister(&timedTool{name: "fast_tool", delay: 1 * time.Millisecond})
	registry.MustRegister(&timedTool{
		name:       "unused_exclusive",
		delay:      1 * time.Millisecond,
		sequential: true,
	})

	rec, _ := runRound(t, registry,
		ai.ToolCall{ID: "call-slow", Name: "slow_tool", Args: `{}`},
		ai.ToolCall{ID: "call-fast", Name: "fast_tool", Args: `{}`},
	)

	lastStart, firstResult := -1, -1
	for index, e := range rec.Events() {
		switch e.Kind {
		case events.KindToolStart:
			lastStart = index
		case events.KindToolResult:
			if firstResult < 0 {
				firstResult = index
			}
		}
	}
	if lastStart < 0 || firstResult < 0 {
		t.Fatalf("expected starts and results, got %v", rec.Kinds())
	}
	if lastStart > firstResult {
		t.Errorf("an unused exclusive tool serialised the round: %v", rec.Kinds())
	}
}
