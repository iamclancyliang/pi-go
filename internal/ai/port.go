// Package ai owns the model port.
//
// This package defines AND implements the boundary the rest of pi-go uses to
// reach a model. Framework and provider types stay hidden behind it.
//
// Nothing here may expose a third-party type. That is what keeps the framework
// choice reversible: if pi-go ever stops using its current one, this boundary
// does not change, and neither does any caller of it.
package ai

import "context"

// Role identifies who produced a message.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ToolCall is a model's request to invoke a tool.
type ToolCall struct {
	// ID pairs this call with its result. Every downstream contract that
	// says "the result reached the model" is asserted on this ID, never on
	// message shape.
	ID   string
	Name string
	Args string
}

// Message is one entry of conversational context.
type Message struct {
	Role    Role
	Content string

	// ToolCalls is set on assistant messages requesting tools.
	ToolCalls []ToolCall

	// ToolCallID is set on tool messages, pairing back to the ToolCall.
	ToolCallID string
}

// ToolSpec describes a tool to the model. It is deliberately a copy of the
// tool's public shape rather than a reference to tools.Tool: the model layer
// must not depend on the tool registry, only on its description.
type ToolSpec struct {
	Name        string
	Description string
}

// Request is one model invocation.
type Request struct {
	Messages []Message
	Tools    []ToolSpec

	// Model names the model to serve this request. Empty means the
	// implementation's default.
	//
	// It is per-request because the model must be changeable between turns
	// without rebuilding the agent.
	Model string

	// ReasoningLevel is a provider-specific reasoning/thinking setting
	// ("", "low", "high"). Empty means unset.
	ReasoningLevel string
}

// Response is a model's reply.
type Response struct {
	Content   string
	ToolCalls []ToolCall

	// Model is the model that actually served the request, which is not
	// necessarily Request.Model — a middleware may substitute it. Reporting
	// what served the call is what makes model_changed provable rather than
	// assumed.
	Model string
}

// Port is the model boundary.
//
// Generate is non-streaming. v0 needs a deterministic vertical slice; pi's real
// path is streaming and a Stream method lands with the contracts that require
// it, so that streaming is verified rather than assumed to work.
type Port interface {
	Generate(ctx context.Context, req Request) (Response, error)
}
