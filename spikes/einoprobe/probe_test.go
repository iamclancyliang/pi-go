// Package einoprobe holds isolated eino capability spikes (issues #4, #5, #6).
//
// Scope guard: this directory is experiment code only. It must not become the
// product architecture — see the design notes and the readiness
// readiness gate. No product module may import anything from spikes/.
package einoprobe

import (
	"testing"

	"github.com/cloudwego/eino/adk"
)

// TestEinoADKSurfaceExists is a smoke check that the pinned eino baseline is
// reachable and that the ADK symbols the spike plan depends on actually exist.
// It asserts nothing about semantics — equivalence to pi is what spikes #4/#5/#6
// are for. See docs/specs/eino-verification-plan.md.
func TestEinoADKSurfaceExists(t *testing.T) {
	// SafePoint constants used by spike #2 (steering).
	var sp adk.SafePoint = adk.AfterToolCalls
	if sp&adk.AfterToolCalls == 0 {
		t.Fatalf("AfterToolCalls not set in SafePoint bitmask")
	}
	if adk.AnySafePoint&adk.AfterChatModel == 0 {
		t.Fatalf("AnySafePoint does not include AfterChatModel")
	}
}
