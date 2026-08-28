package runtime

import (
	"context"
	"fmt"
	"sync"

	"github.com/iamclancyliang/pi-go/internal/tools"
)

// The v0 seams.
//
// These exist now even though the extension host that will consume them ships
// much later, because adding a seam after the fact means reworking the core.
// They are deliberately small: real call sites with real behaviour, not
// speculative extension APIs.

// Decision is the outcome of a pre-execution policy check.
type Decision struct {
	// Denied stops the tool from running.
	Denied bool

	// Reason explains a denial. It reaches the model as the tool result, so
	// the model can react rather than silently losing a call.
	Reason string
}

// Policy is the pre-execution policy / denial seam.
//
// Scope is policy and denial ONLY. Rewriting a tool's arguments is deliberately
// not offered: an earlier draft borrowed that idea from another project and it
// was removed, because silently altering what the model asked for makes the
// trace stop describing what actually happened. Do not add an argument-rewrite
// return value here without an explicit decision to allow it.
type Policy interface {
	// Before runs before a tool executes. Returning a denial prevents
	// execution; the call still produces observable tool_start/tool_end
	// events, because a silently skipped call is indistinguishable from one
	// that never happened.
	Before(ctx context.Context, call PolicyCall) Decision
}

// PolicyCall describes a tool invocation awaiting a decision.
type PolicyCall struct {
	ToolCallID string
	ToolName   string
	Args       string
	Execution  tools.Execution
}

// PolicyFunc adapts a function to Policy.
type PolicyFunc func(context.Context, PolicyCall) Decision

// Before implements Policy.
func (f PolicyFunc) Before(ctx context.Context, c PolicyCall) Decision { return f(ctx, c) }

// AllowAll is the default policy.
var AllowAll Policy = PolicyFunc(func(context.Context, PolicyCall) Decision {
	return Decision{}
})

// DenyWrites denies any tool that does not declare itself read-only.
//
// It has real work to do now that the built-in set includes tools that change
// files: read, ls, find and grep pass, and write, edit and bash do not. The
// declaration is the tool's, so a policy cannot be fooled by a name — a tool
// that mutates and says otherwise is the tool's own defect, and one test per
// mutating tool pins what it declares.
var DenyWrites Policy = PolicyFunc(func(_ context.Context, c PolicyCall) Decision {
	if c.Execution.ReadOnly {
		return Decision{}
	}
	return Decision{Denied: true, Reason: fmt.Sprintf("tool %q is not read-only; v0 denies mutation", c.ToolName)}
})

// Capabilities is the host capability discovery seam.
//
// Degradation is DECLARED here rather than discovered by an extension calling
// something and failing. An extension that asks "can I stream?" gets an answer;
// it does not get to find out by crashing.
type Capabilities struct {
	// Streaming reports whether the model boundary delivers incremental
	// output. False at v0: the eino adapter delivers a single chunk, which
	// is not streaming and is not claimed to be.
	Streaming bool

	// DurableStorage reports whether sessions survive process exit. False
	// at v0 — the storage port is v1.
	DurableStorage bool

	// ToolDenial reports whether the pre-execution policy seam is wired.
	ToolDenial bool

	// ExtensionTransport names the transport for out-of-process
	// extensions. Empty for now: the transport has not been chosen, and
	// naming one here would quietly decide it.
	ExtensionTransport string
}

// V0Capabilities reports what the v0 runtime actually provides.
func V0Capabilities() Capabilities {
	return Capabilities{
		Streaming:          false,
		DurableStorage:     false,
		ToolDenial:         true,
		ExtensionTransport: "",
	}
}

// StateNamespace is the per-extension state isolation seam.
//
// Keys are namespaced so two extensions cannot collide or read each other's
// state. In-memory at v0; durability arrives with the storage port at v1.
type StateNamespace struct {
	mu     sync.RWMutex
	values map[string]map[string]string
}

// NewStateNamespace returns an empty namespace store.
func NewStateNamespace() *StateNamespace {
	return &StateNamespace{values: make(map[string]map[string]string)}
}

// Set stores a value in the given namespace.
func (s *StateNamespace) Set(namespace, key, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ns, ok := s.values[namespace]
	if !ok {
		ns = make(map[string]string)
		s.values[namespace] = ns
	}
	ns[key] = value
}

// Get reads a value from the given namespace.
func (s *StateNamespace) Get(namespace, key string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.values[namespace][key]
	return v, ok
}
