package conformance

import (
	"testing"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/events"
	"github.com/iamclancyliang/pi-go/internal/tools"
)

// TestA15SameToolsDifferentInterleavings runs the SAME two calls both ways and
// pins that the streams differ in shape.
//
// Everything is held constant except one tool's declaration: same names, same
// arguments, same delays, same order. Only the interleaving changes. That is the
// case a single execution path behind a parallel flag cannot express — such a path
// produces one shape and quietly emits it for both modes, and every per-call
// assertion still holds, because the calls do pair and results do arrive.
func TestA15SameToolsDifferentInterleavings(t *testing.T) {
	calls := []ai.ToolCall{
		{ID: "call-a", Name: "tool_a", Args: `{}`},
		{ID: "call-b", Name: "tool_b", Args: `{}`},
	}

	parallel, _ := runRound(t, registryOf(false), calls...)
	sequential, _ := runRound(t, registryOf(true), calls...)

	parallelShape := toolKinds(parallel)
	sequentialShape := toolKinds(sequential)

	if sameKinds(parallelShape, sequentialShape) {
		t.Fatalf("both modes emitted the same shape: %v", parallelShape)
	}

	// Sequential: each call is finished and recorded before the next begins.
	wantSequential := []events.Kind{
		events.KindToolStart, events.KindToolEnd, events.KindToolResult,
		events.KindToolStart, events.KindToolEnd, events.KindToolResult,
	}
	if !sameKinds(sequentialShape, wantSequential) {
		t.Errorf("sequential shape = %v, want %v", sequentialShape, wantSequential)
	}

	// Parallel: every call announced, then ends, then every result.
	wantParallel := []events.Kind{
		events.KindToolStart, events.KindToolStart,
		events.KindToolEnd, events.KindToolEnd,
		events.KindToolResult, events.KindToolResult,
	}
	if !sameKinds(parallelShape, wantParallel) {
		t.Errorf("parallel shape = %v, want %v", parallelShape, wantParallel)
	}

	// Both modes agree on WHAT was recorded and in which order; only the
	// interleaving differs. Asserting the shapes alone would tolerate a mode
	// that dropped or reordered the results themselves.
	want := []string{"call-a", "call-b"}
	if got := idsOf(parallel, events.KindToolResult); !equal(got, want) {
		t.Errorf("parallel results = %v, want %v", got, want)
	}
	if got := idsOf(sequential, events.KindToolResult); !equal(got, want) {
		t.Errorf("sequential results = %v, want %v", got, want)
	}
}

// registryOf builds the same two tools, differing only in whether the second
// declares that it cannot overlap.
func registryOf(secondIsSequential bool) *tools.Registry {
	registry := tools.NewRegistry()
	registry.MustRegister(&timedTool{name: "tool_a", delay: 60 * time.Millisecond})
	registry.MustRegister(&timedTool{
		name:       "tool_b",
		delay:      1 * time.Millisecond,
		sequential: secondIsSequential,
	})
	return registry
}
