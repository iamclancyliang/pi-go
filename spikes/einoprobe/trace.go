package einoprobe

import (
	"fmt"
	"strings"
	"sync"
)

// layer identifies which of the three numbered layers an entry belongs to.
//
// The layers exist because eino's notion of a "turn" is not known to match pi's
// (see INTERPRETATION.md). Recording all three lets the nesting be read off the
// trace rather than assumed from the word "turn".
type layer string

const (
	layerGenInput     layer = "GenInput"     // GenInput iteration
	layerPrepareAgent layer = "PrepareAgent" // PrepareAgent instance
	layerModel        layer = "model"        // model call
	layerTool         layer = "tool"         // tool event
	layerControl      layer = "control"      // Push / Preempt / Stop / safe point
)

// entry is one observed fact. It carries no interpretation.
type entry struct {
	Seq     int
	Layer   layer
	Event   string
	Detail  string
	GenIter int // GenInput iteration this entry falls within (0 = before the first)
	PrepIdx int // PrepareAgent instance this entry falls within (0 = none yet)
}

func (e entry) String() string {
	return fmt.Sprintf("%3d [gen=%d prep=%d] %-12s %-22s %s",
		e.Seq, e.GenIter, e.PrepIdx, e.Layer, e.Event, e.Detail)
}

// trace is the shared, concurrency-safe recorder.
//
// Monotonic Seq is assigned under the same lock that appends, so ordering in
// the slice always matches ordering of Seq — no goroutine-dependent
// interleaving can reorder the record itself.
type trace struct {
	mu      sync.Mutex
	seq     int
	genIter int
	prepIdx int
	entries []entry
}

func newTrace() *trace { return &trace{} }

// add records an event at the current layer counters.
func (t *trace) add(l layer, event, detail string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.seq++
	t.entries = append(t.entries, entry{
		Seq: t.seq, Layer: l, Event: event, Detail: detail,
		GenIter: t.genIter, PrepIdx: t.prepIdx,
	})
}

// beginGenInput increments the GenInput iteration counter and records entry.
func (t *trace) beginGenInput(detail string) {
	t.mu.Lock()
	t.genIter++
	t.mu.Unlock()
	t.add(layerGenInput, "GenInput:enter", detail)
}

// beginPrepareAgent increments the PrepareAgent instance counter.
func (t *trace) beginPrepareAgent(detail string) {
	t.mu.Lock()
	t.prepIdx++
	t.mu.Unlock()
	t.add(layerPrepareAgent, "PrepareAgent:enter", detail)
}

func (t *trace) snapshot() []entry {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]entry, len(t.entries))
	copy(out, t.entries)
	return out
}

// render prints the raw timeline. Raw facts only — interpretation is applied
// separately, against the pre-registered table in INTERPRETATION.md.
func (t *trace) render() string {
	var b strings.Builder
	b.WriteString("seq [gen prep] layer        event                  detail\n")
	b.WriteString("--------------------------------------------------------------------\n")
	for _, e := range t.snapshot() {
		b.WriteString(e.String())
		b.WriteString("\n")
	}
	return b.String()
}

// counts summarises the nesting facts the interpretation table needs:
// model calls per PrepareAgent instance, and whether tool events appeared
// inside each instance.
func (t *trace) counts() (modelCallsPerPrep map[int]int, toolEventsPerPrep map[int]int, genIters, preps int) {
	modelCallsPerPrep = map[int]int{}
	toolEventsPerPrep = map[int]int{}
	for _, e := range t.snapshot() {
		switch e.Layer {
		case layerModel:
			modelCallsPerPrep[e.PrepIdx]++
		case layerTool:
			toolEventsPerPrep[e.PrepIdx]++
		}
		if e.GenIter > genIters {
			genIters = e.GenIter
		}
		if e.PrepIdx > preps {
			preps = e.PrepIdx
		}
	}
	return
}

// --- query helpers for assertions -------------------------------------------
//
// The probes record first and assert second. These helpers turn the raw trace
// into failable claims, so a regression in observed behaviour breaks the build
// instead of silently producing a different-but-green trace.

// eventsMatching returns entries whose Event contains sub, in trace order.
func (t *trace) eventsMatching(sub string) []entry {
	var out []entry
	for _, e := range t.snapshot() {
		if strings.Contains(e.Event, sub) {
			out = append(out, e)
		}
	}
	return out
}

// detailsMatching returns the Detail strings of entries whose Event contains sub.
func (t *trace) detailsMatching(sub string) []string {
	var out []string
	for _, e := range t.eventsMatching(sub) {
		out = append(out, e.Detail)
	}
	return out
}

// firstSeq returns the Seq of the first entry whose Event contains eventSub and
// whose Detail contains detailSub (detailSub may be ""). Returns -1 if absent.
func (t *trace) firstSeq(eventSub, detailSub string) int {
	for _, e := range t.snapshot() {
		if strings.Contains(e.Event, eventSub) && (detailSub == "" || strings.Contains(e.Detail, detailSub)) {
			return e.Seq
		}
	}
	return -1
}

// nthSeq returns the Seq of the n-th (1-based) entry whose Event contains sub.
func (t *trace) nthSeq(sub string, n int) int {
	c := 0
	for _, e := range t.snapshot() {
		if strings.Contains(e.Event, sub) {
			c++
			if c == n {
				return e.Seq
			}
		}
	}
	return -1
}

// countEvents returns how many entries have an Event containing sub.
func (t *trace) countEvents(sub string) int {
	return len(t.eventsMatching(sub))
}

// countInGen returns how many entries with Event containing sub fall in GenInput
// iteration gen.
func (t *trace) countInGen(sub string, gen int) int {
	n := 0
	for _, e := range t.eventsMatching(sub) {
		if e.GenIter == gen {
			n++
		}
	}
	return n
}
