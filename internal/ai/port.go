// Package ai owns the model port.
//
// This package defines AND implements the boundary the rest of pi-go uses to
// reach a model. Framework and provider types stay hidden behind it.
//
// Nothing here may expose a third-party type. That is what keeps the framework
// choice reversible: if pi-go ever stops using its current one, this boundary
// does not change, and neither does any caller of it.
package ai

import (
	"context"
	"errors"
)

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

	// Reasoning is what an assistant worked through before answering, when the
	// provider reports it separately from the answer.
	//
	// Kept apart from Content because they are different things: Content is
	// what was said, and a renderer showing reasoning as the answer would be
	// quoting the model's notes. It is kept AT ALL because some providers
	// require the reasoning of earlier turns to be sent back with the next
	// request, and a history that dropped it cannot continue that conversation.
	Reasoning string
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

	// Reasoning is what the model worked through, when the provider reports it
	// apart from the answer. It is not part of Content: a caller showing it as
	// the answer would be quoting the model's notes rather than its reply.
	Reasoning string

	// Model is the model that actually served the request, which is not
	// necessarily Request.Model — a middleware may substitute it. Reporting
	// what served the call is what makes model_changed provable rather than
	// assumed.
	Model string

	// EarlierAttempts is what attempts before this one reported using.
	//
	// A call that retried spent on every attempt, and a ledger holding only the
	// one that succeeded undercounts exactly the spend the retry created.
	EarlierAttempts []Usage

	// Usage is the token counts the provider reported for this call. It is not
	// a money figure: no provider here reports one, and any currency attached
	// downstream is computed from published prices rather than stated.
	Usage Usage

	// Truncated reports that the model stopped because it ran out of room
	// rather than because it had finished.
	//
	// It matters because truncation can cut a tool call's arguments mid-way,
	// and the cut arguments may still be valid on their own: half a path is a
	// path, and a shortened command is a command. Whether the arguments parse
	// therefore says nothing about whether they are what the model meant.
	Truncated bool
}

// Port is the model boundary.
//
// Generate answers in one piece. It remains the whole contract for a caller that
// only wants the answer, and it is what the deterministic tests are written
// against.
type Port interface {
	Generate(ctx context.Context, req Request) (Response, error)
}

// StreamingPort is a Port that can also deliver a reply as it arrives.
//
// Separate from Port because not every provider streams, and a provider that
// does not should say so by not implementing this rather than by emitting one
// chunk and calling it a stream.
//
// The returned channel carries the event protocol and is closed after a terminal
// event. Cancelling ctx does not abandon it: the stream ends with an error event
// carrying what had already arrived, because a partial answer the caller watched
// arrive should not vanish because they stopped it.
type StreamingPort interface {
	Port

	Stream(ctx context.Context, req Request) (<-chan StreamEvent, error)
}

// Usage is what one model call consumed.
//
// Reported by the provider rather than counted here: only the provider knows how
// it tokenised the request, and a local estimate that disagrees with the bill is
// worse than no estimate at all.
type Usage struct {
	InputTokens  int
	OutputTokens int

	// CacheReadTokens and ReasoningTokens are absent when the provider did not
	// report them, which is not the same as reporting zero.
	//
	// Zero is a real answer: a model that did no reasoning reports no reasoning
	// tokens, and a request that missed the cache reports no cache reads.
	// Collapsing "did none" into "did not say" leaves a ledger unable to tell a
	// provider that reasoned without billing tokens from one that never said how
	// many it used. A pointer is the smallest thing that can hold that difference.
	//
	// ReasoningTokens is a SUBSET of OutputTokens, not an addition to it.
	// Adding them double-counts.
	CacheReadTokens *int
	ReasoningTokens *int

	// Reported distinguishes a provider that said nothing about usage from one
	// that reported a call using no tokens. Callers that record consumption must
	// not treat silence as nothing used.
	Reported bool
}

// Total is every token the call reported using.
func (u Usage) Total() int { return u.InputTokens + u.OutputTokens }

// ErrContextOverflow reports that a request was refused for exceeding the
// model's context, rather than for any transient reason.
//
// It is distinguished from an ordinary failure because the two need opposite
// responses: a transient failure is retried unchanged, and retrying an overflow
// unchanged sends back exactly what was just refused. Recovering from one means
// shortening the context first.
var ErrContextOverflow = errors.New("ai: the request exceeded the model's context")
