// Package events defines pi-go's observable event contract.
//
// The event stream is a published contract: the trace a client sees is the
// product, not a debugging aid. This package therefore has zero dependencies —
// not on the framework, not on the runtime, not on anything.
//
// This package only names the events and carries their payloads. The rules
// about what order they may be emitted in live with the code that emits them.
package events

import "time"

// Kind identifies an event type. Kinds are part of the published contract, so
// the string values are stable and must not be renamed to suit a refactor.
type Kind string

const (
	KindAgentStart Kind = "agent_start"
	KindTurnStart  Kind = "turn_start"

	KindModelRequest  Kind = "model_request"
	KindModelResponse Kind = "model_response"

	// KindModelChanged is pi-go's own event. eino executes a per-call model
	// swap but does not interpret it and emits nothing, so if we do not
	// publish this, a mid-turn model change is invisible to clients and to
	// the session record.
	KindModelChanged Kind = "model_changed"

	KindToolStart Kind = "tool_start"
	KindToolEnd   Kind = "tool_end"

	KindTurnEnd  Kind = "turn_end"
	KindAgentEnd Kind = "agent_end"
)

// Event is one observable occurrence.
//
// Seq is assigned by the emitter and is the ordering authority: consumers must
// not infer order from Time. Wall-clock timestamps can tie or go backwards, and
// ordering assertions have to survive that.
type Event struct {
	Seq  int       `json:"seq"`
	Kind Kind      `json:"kind"`
	Time time.Time `json:"time"`

	// TurnIndex is 1-based; 0 means "not scoped to a turn" (agent_start).
	TurnIndex int `json:"turn_index,omitempty"`

	// ToolCallID pairs tool_start with tool_end, and pairs both with the
	// model's originating call. Proving a tool result reached the model by
	// role shape alone is what the spike work kept getting wrong; the ID is
	// the thing that actually pairs.
	ToolCallID string `json:"tool_call_id,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`

	// Detail carries kind-specific payload. Kept as a typed struct rather
	// than map[string]any so the golden trace cannot silently change shape.
	Detail Detail `json:"detail,omitzero"`
}

// Detail is the union of per-kind payloads. Only the fields relevant to a
// Kind are populated.
type Detail struct {
	// MessageCount is how many messages were sent to the model on a
	// model_request. It is the observable that distinguishes "context was
	// preserved" from "context was rebuilt empty".
	MessageCount int `json:"message_count,omitempty"`

	// Model names the model serving this request.
	Model string `json:"model,omitempty"`

	// From and To describe a model_changed transition.
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`

	// Text is the assistant's content on model_response.
	Text string `json:"text,omitempty"`

	// ToolCallIDs lists the calls a model_response requested, in the order
	// the model emitted them.
	ToolCallIDs []string `json:"tool_call_ids,omitempty"`

	// Args and Result carry tool input/output.
	Args   string `json:"args,omitempty"`
	Result string `json:"result,omitempty"`

	// Err is set when a step failed. A tool_end with Err is still a
	// tool_end: failures are observable, not silent.
	Err string `json:"err,omitempty"`

	// Reason explains a turn_end or agent_end ("stop", "error", "aborted").
	Reason string `json:"reason,omitempty"`
}

// Observer receives events as they are emitted.
//
// This is how anything outside the runtime watches it work — extensions,
// telemetry and protocol adapters all consume this. It exists already even
// though those consumers ship later, because adding it afterwards would mean
// reworking the runtime.
//
// Implementations must not block: the emitter holds the loop while calling.
type Observer interface {
	OnEvent(Event)
}

// ObserverFunc adapts a function to Observer.
type ObserverFunc func(Event)

// OnEvent implements Observer.
func (f ObserverFunc) OnEvent(e Event) { f(e) }
