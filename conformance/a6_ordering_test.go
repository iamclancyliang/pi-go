package conformance

import (
	"testing"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/events"
	"github.com/iamclancyliang/pi-go/internal/tools"
)

// TestA6ParallelOrdering pins the three orders a parallel round emits.
//
// The two calls finish in the OPPOSITE order to the one requested, which is what
// makes the assertions mean anything: with tools that finish in request order,
// completion order and source order coincide and an implementation that confuses
// them passes everything.
func TestA6ParallelOrdering(t *testing.T) {
	registry := tools.NewRegistry()
	registry.MustRegister(&timedTool{name: "slow_tool", delay: 120 * time.Millisecond})
	registry.MustRegister(&timedTool{name: "fast_tool", delay: 1 * time.Millisecond})

	rec, sess := runRound(t, registry,
		ai.ToolCall{ID: "call-slow", Name: "slow_tool", Args: `{}`},
		ai.ToolCall{ID: "call-fast", Name: "fast_tool", Args: `{}`},
	)

	lastStart, lastEnd, firstResult := -1, -1, -1
	for index, e := range rec.Events() {
		switch e.Kind {
		case events.KindToolStart:
			lastStart = index
		case events.KindToolEnd:
			lastEnd = index
		case events.KindToolResult:
			if firstResult < 0 {
				firstResult = index
			}
		}
	}

	// Every start is announced before any result; in the sequential shape this
	// is false by construction, which is what separates the two.
	if lastStart > firstResult {
		t.Errorf("a result was emitted before the last start: %v", rec.Kinds())
	}
	// And every end precedes every result. Recording an outcome and emitting
	// after releasing the lock lets the last call emit its end and then the
	// whole round's results while an earlier end is still unemitted — an order
	// the per-call checks below would not notice.
	if lastEnd > firstResult {
		t.Errorf("a result was emitted before the last end: %v", rec.Kinds())
	}

	if want := []string{"call-slow", "call-fast"}; !equal(idsOf(rec, events.KindToolStart), want) {
		t.Errorf("starts = %v, want source order %v", idsOf(rec, events.KindToolStart), want)
	}
	// Ends follow completion: the slow call was requested first and finishes
	// last, so ends matching source order would mean they report something else.
	if want := []string{"call-fast", "call-slow"}; !equal(idsOf(rec, events.KindToolEnd), want) {
		t.Errorf("ends = %v, want completion order %v", idsOf(rec, events.KindToolEnd), want)
	}
	if want := []string{"call-slow", "call-fast"}; !equal(idsOf(rec, events.KindToolResult), want) {
		t.Errorf("results = %v, want source order %v", idsOf(rec, events.KindToolResult), want)
	}

	// The end says how a call finished, not what it produced: carrying the
	// result there lets a consumer read results in completion order.
	for _, e := range rec.Events() {
		if e.Kind == events.KindToolEnd && e.Detail.Result != "" {
			t.Errorf("tool_end for %s carries a result payload: %q", e.ToolCallID, e.Detail.Result)
		}
	}

	// History carries the same order as the results: a transcript in completion
	// order would replay the round differently from how it was requested.
	var recorded []string
	for _, m := range sess.Truth() {
		if m.Role == ai.RoleTool {
			recorded = append(recorded, m.ToolCallID)
		}
	}
	if want := []string{"call-slow", "call-fast"}; !equal(recorded, want) {
		t.Errorf("tool messages in history = %v, want source order %v", recorded, want)
	}
}
