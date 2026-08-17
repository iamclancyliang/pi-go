package runtime

import (
	"sync"
	"time"

	"github.com/iamclancyliang/pi-go/internal/events"
)

// emitter assigns sequence numbers and fans events out to observers.
//
// Sequence assignment is centralised here because Seq is the ordering
// authority for every ordering assertion. If two components numbered events
// independently, "before" would stop meaning anything.
type emitter struct {
	mu        sync.Mutex
	seq       int
	turn      int
	observers []events.Observer
	now       func() time.Time
}

func newEmitter(now func() time.Time, observers ...events.Observer) *emitter {
	if now == nil {
		now = time.Now
	}
	return &emitter{observers: observers, now: now}
}

// emit publishes an event, filling in Seq, Time and the current turn.
//
// The lock is held across observer callbacks so that observers see events in
// sequence order. An observer that blocks therefore blocks the loop — which is
// why events.Observer documents that it must not.
func (e *emitter) emit(kind events.Kind, mutate func(*events.Event)) events.Event {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.seq++
	ev := events.Event{
		Seq:       e.seq,
		Kind:      kind,
		Time:      e.now(),
		TurnIndex: e.turn,
	}
	if mutate != nil {
		mutate(&ev)
	}
	for _, o := range e.observers {
		if o != nil {
			o.OnEvent(ev)
		}
	}
	return ev
}

// beginTurn increments the turn counter and returns the new index.
func (e *emitter) beginTurn() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.turn++
	return e.turn
}

// Recorder collects events in order. It is the golden-trace capture used by
// conformance tests and by the tracer-bullet command.
type Recorder struct {
	mu     sync.Mutex
	events []events.Event
}

// NewRecorder returns an empty Recorder.
func NewRecorder() *Recorder { return &Recorder{} }

// OnEvent implements events.Observer.
func (r *Recorder) OnEvent(e events.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

// Events returns the recorded events in emission order.
func (r *Recorder) Events() []events.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]events.Event(nil), r.events...)
}

// Kinds returns just the event kinds, in order — the shape most ordering
// assertions actually care about.
func (r *Recorder) Kinds() []events.Kind {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]events.Kind, len(r.events))
	for i, e := range r.events {
		out[i] = e.Kind
	}
	return out
}
