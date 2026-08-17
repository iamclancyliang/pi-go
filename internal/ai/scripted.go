package ai

import (
	"context"
	"fmt"
	"sync"
)

// Scripted is a deterministic fake model.
//
// Model execution has to be deterministic: the trace must come out identical
// every run, or comparing against a recorded trace proves nothing. It replies
// from a fixed script, in order.
//
// It is NOT a "returns canned answers" stub in the naive sense. The spike work
// was bitten by exactly that: a fresh scripted model on a resumed run re-issued
// its canned tool call and produced a false safety failure. So Scripted can
// consult the conversation before answering — see StopWhenToolsSettled.
type Scripted struct {
	// Replies are returned in order, one per Generate call.
	Replies []Response

	// StopWhenToolsSettled, when true, returns Final instead of the next
	// scripted reply once every tool call in the context already has a
	// matching tool result.
	//
	// This exists because a script indexed purely by call count cannot
	// distinguish "second call of a fresh run" from "first call after a
	// resume" — which is how a harness bug once looked like a product bug.
	StopWhenToolsSettled bool

	// Final is returned when the script is exhausted, or when
	// StopWhenToolsSettled triggers.
	Final Response

	// Name is reported as the serving model when a reply does not name one.
	Name string

	mu       sync.Mutex
	requests []Request
}

// Generate implements Port.
func (s *Scripted) Generate(ctx context.Context, req Request) (Response, error) {
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}

	s.mu.Lock()
	idx := len(s.requests)
	s.requests = append(s.requests, cloneRequest(req))
	s.mu.Unlock()

	if s.StopWhenToolsSettled && toolsSettled(req.Messages) {
		return s.stamp(s.Final), nil
	}
	if idx < len(s.Replies) {
		return s.stamp(s.Replies[idx]), nil
	}
	return s.stamp(s.Final), nil
}

// stamp fills in the serving model name when the scripted reply left it empty.
func (s *Scripted) stamp(r Response) Response {
	if r.Model == "" {
		r.Model = s.Name
	}
	return r
}

// Requests returns the requests this model received, in order.
func (s *Scripted) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Request, len(s.requests))
	copy(out, s.requests)
	return out
}

// toolsSettled reports whether every tool call in msgs has a matching result.
//
// "Settled" is decided by ToolCallID pairing, not by counting or by looking at
// roles: an unmatched call is precisely the state that matters after an
// interrupted run.
func toolsSettled(msgs []Message) bool {
	pending := make(map[string]bool)
	for _, m := range msgs {
		for _, tc := range m.ToolCalls {
			pending[tc.ID] = true
		}
	}
	if len(pending) == 0 {
		return false
	}
	for _, m := range msgs {
		if m.Role == RoleTool && m.ToolCallID != "" {
			delete(pending, m.ToolCallID)
		}
	}
	return len(pending) == 0
}

func cloneRequest(r Request) Request {
	out := r
	out.Messages = make([]Message, len(r.Messages))
	for i, m := range r.Messages {
		cm := m
		if len(m.ToolCalls) > 0 {
			cm.ToolCalls = append([]ToolCall(nil), m.ToolCalls...)
		}
		out.Messages[i] = cm
	}
	out.Tools = append([]ToolSpec(nil), r.Tools...)
	return out
}

// AssistantToolCalls is a helper for building scripts.
func AssistantToolCalls(calls ...ToolCall) Response {
	return Response{ToolCalls: calls}
}

// AssistantText is a helper for building scripts.
func AssistantText(text string) Response {
	return Response{Content: text}
}

// ErrScriptExhausted is returned by Strict when its script runs out.
var ErrScriptExhausted = fmt.Errorf("ai: scripted model exhausted")
