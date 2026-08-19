// Package tools defines the tool registration seam and pi-go's tool contract.
//
// This is where tools are registered. It exists already, with no extension
// host to consume it yet, because adding a seam like this later means
// reworking the core rather than extending it.
//
// No framework types appear here. A tool is a pi-go concept, and the runtime
// adapts these to whatever the underlying framework wants — so swapping that
// framework never reaches this package.
package tools

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
)

// Execution describes how a tool may be scheduled relative to others.
//
// This is per-tool metadata, not a global mode: one tool in a multi-call batch
// may need to run alone while the others do not care. A single global flag
// cannot express that.
type Execution struct {
	// Sequential declares that this tool cannot tolerate running
	// concurrently with other calls.
	//
	// Enforced per ROUND of calls: a round that contains such a tool runs one
	// call at a time, in the order the model asked for them, and a round that
	// does not runs them concurrently. A tool that declares this is never run
	// in parallel; rounds that never call it are unaffected.
	Sequential bool

	// ReadOnly declares the tool performs no mutation. v0 ships read-only
	// tools only — no arbitrary write or shell access yet — and the field
	// exists so the pre-execution policy check has something to decide on.
	ReadOnly bool
}

// Tool is a callable capability offered to the model.
type Tool interface {
	// Name is the identifier the model calls. Must be unique in a Registry.
	Name() string

	// Description is shown to the model.
	Description() string

	// Execution reports this tool's scheduling metadata.
	Execution() Execution

	// Call runs the tool. args is the raw argument payload as the model
	// produced it; returning an error is a normal, observable outcome
	// rather than a crash.
	Call(ctx context.Context, args string) (string, error)
}

// ErrDuplicateName is returned when two tools claim the same name.
var ErrDuplicateName = errors.New("tools: duplicate tool name")

// Registry holds the tools available to a run.
//
// Safe for concurrent use: the runtime reads it while tools execute.
type Registry struct {
	mu     sync.RWMutex
	byName map[string]Tool
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{byName: make(map[string]Tool)}
}

// Register adds a tool. It fails on a duplicate name rather than overwriting:
// silently replacing a tool would let an extension shadow a built-in, which is
// a policy decision the seam must not make by accident.
func (r *Registry) Register(t Tool) error {
	if t == nil {
		return errors.New("tools: cannot register a nil tool")
	}
	name := t.Name()
	if name == "" {
		return errors.New("tools: cannot register a tool with an empty name")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.byName[name]; exists {
		return fmt.Errorf("%w: %q", ErrDuplicateName, name)
	}
	r.byName[name] = t
	return nil
}

// MustRegister is Register for composition roots, where a duplicate name is a
// programming error rather than a runtime condition.
func (r *Registry) MustRegister(t Tool) {
	if err := r.Register(t); err != nil {
		panic(err)
	}
}

// Lookup returns the tool with the given name.
func (r *Registry) Lookup(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.byName[name]
	return t, ok
}

// All returns every registered tool, ordered by name.
//
// Ordered because the tool list reaches the model and feeds the golden trace:
// map iteration order would make both nondeterministic, and a golden test that
// fails randomly gets disabled rather than fixed.
func (r *Registry) All() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]Tool, 0, len(r.byName))
	for _, t := range r.byName {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// A per-batch sequencing helper was deliberately removed rather than left
// unused: eino decides sequencing per tools-node at construction, so a
// per-batch API would advertise a granularity the runtime cannot deliver.
// Sequencing is resolved in internal/runtime — see Execution.Sequential.
