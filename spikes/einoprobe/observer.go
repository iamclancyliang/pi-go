// Package einoprobe holds isolated eino capability spikes (issues #4, #5, #6).
//
// Scope guard: experiment code only. No product module may import from spikes/,
// and spike code must not become the architecture by default — see
// docs/architecture/architecture.md and the Phase 0 readiness gate.
package einoprobe

import (
	"context"
	"fmt"
	"sync"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// call records one invocation of the observing model.
type call struct {
	Method   string   // "Generate" or "Stream"
	NumInput int      // how many messages eino passed
	Roles    []string // role of each input message, in order
	NumOpts  int      // how many options eino attached
}

func (c call) String() string {
	return fmt.Sprintf("%s(inputs=%d roles=%v opts=%d)", c.Method, c.NumInput, c.Roles, c.NumOpts)
}

// observingModel is a fake ChatModel whose only job is to record how eino
// actually drives it.
//
// It deliberately asserts nothing. The point is to discover the real call
// pattern before any spike encodes an assumption about it: a fake built on a
// guessed contract would produce a self-consistent but meaningless result.
// Observe first, assert second.
type observingModel struct {
	mu    sync.Mutex
	calls []call

	// reply is returned verbatim from Generate. Kept trivial on purpose — this
	// model is an instrument, not a simulation.
	reply *schema.Message
}

func newObservingModel(reply *schema.Message) *observingModel {
	return &observingModel{reply: reply}
}

func (m *observingModel) record(method string, input []*schema.Message, opts int) {
	roles := make([]string, 0, len(input))
	for _, msg := range input {
		if msg == nil {
			roles = append(roles, "<nil>")
			continue
		}
		roles = append(roles, string(msg.Role))
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, call{Method: method, NumInput: len(input), Roles: roles, NumOpts: opts})
}

// replyCopy returns a fresh message each call.
//
// Returning the same *schema.Message pointer repeatedly would let any mutation
// by eino — or by a later call — leak backwards into earlier observations, and
// would be a data race once TurnLoop drives this from multiple goroutines. The
// instrument must not be the thing that introduces shared state.
func (m *observingModel) replyCopy() *schema.Message {
	if m.reply == nil {
		return nil
	}
	c := *m.reply
	return &c
}

func (m *observingModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	m.record("Generate", input, len(opts))
	return m.replyCopy(), nil
}

func (m *observingModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.record("Stream", input, len(opts))
	reply := m.replyCopy()
	sr, sw := schema.Pipe[*schema.Message](1)
	go func() {
		defer sw.Close()
		sw.Send(reply, nil)
	}()
	return sr, nil
}

// observed returns a copy of the recorded calls.
func (m *observingModel) observed() []call {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]call, len(m.calls))
	copy(out, m.calls)
	return out
}

// compile-time check that the observing model satisfies the interface the
// spikes will rely on.
var _ model.BaseChatModel = (*observingModel)(nil)
