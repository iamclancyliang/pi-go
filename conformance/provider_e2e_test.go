package conformance

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/events"
	"github.com/iamclancyliang/pi-go/internal/provider/deepseek"
	"github.com/iamclancyliang/pi-go/internal/runtime"
	"github.com/iamclancyliang/pi-go/internal/session"
	"github.com/iamclancyliang/pi-go/internal/tools"
)

// The provider is exercised HERE through the real Agent.Run, not only through
// its own package's tests.
//
// A port can satisfy its interface, pass every unit test, and still never be
// reached by the runtime — which is a defect no amount of testing against the
// port itself can see, because those tests are the thing that bypasses the gap.

type scriptedTransport struct {
	requests int
	replies  []string
}

func (s *scriptedTransport) Do(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		_, _ = io.Copy(io.Discard, req.Body)
	}
	if s.requests >= len(s.replies) {
		return nil, fmt.Errorf("the runtime asked for reply %d; only %d were scripted",
			s.requests+1, len(s.replies))
	}
	body := s.replies[s.requests]
	s.requests++
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
}

type fixedEnv struct{}

func (fixedEnv) Lookup(context.Context, string) (string, error) { return "test-key", nil }

func sseReply(chunks ...string) string {
	var b strings.Builder
	for _, c := range chunks {
		fmt.Fprintf(&b, "data: %s\n\n", c)
	}
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

func runWithProvider(t *testing.T, transport *scriptedTransport) (*runtime.Recorder, *session.Session) {
	t.Helper()

	port, err := deepseek.New(deepseek.Config{
		Model:           "deepseek-v4-flash",
		Transport:       transport,
		Environment:     fixedEnv{},
		MaxOutputTokens: 32,
	})
	if err != nil {
		t.Fatalf("deepseek.New: %v", err)
	}

	registry, _, _ := tools.NewFixtureRegistry()
	rec := runtime.NewRecorder()
	sess := session.New("You are pi-go.")
	agent, err := runtime.New(runtime.Config{
		Model:     port,
		ModelName: "deepseek-v4-flash",
		Tools:     registry,
		Session:   sess,
		Observers: []events.Observer{rec},
		Now:       fixedClock(),
	})
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	if err := agent.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return rec, sess
}

// TestAProviderReplyReachesTheRuntime proves the port is actually reachable:
// a reply produced by the provider becomes conversational truth.
func TestAProviderReplyReachesTheRuntime(t *testing.T) {
	transport := &scriptedTransport{replies: []string{
		sseReply(
			`{"choices":[{"delta":{"reasoning_content":"thinking"},"finish_reason":null}]}`,
			`{"choices":[{"delta":{"content":"the answer"},"finish_reason":null}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":3,"total_tokens":14}}`,
		),
	}}
	_, sess := runWithProvider(t, transport)

	if transport.requests != 1 {
		t.Fatalf("the runtime sent %d requests for one answer, want 1", transport.requests)
	}

	msgs := sess.Truth()
	last := msgs[len(msgs)-1]
	if last.Role != ai.RoleAssistant || last.Content != "the answer" {
		t.Fatalf("history ends with %s %q, want the assistant's answer", last.Role, last.Content)
	}
	// Reasoning is what the model worked through, not what it said.
	if strings.Contains(last.Content, "thinking") {
		t.Fatalf("reasoning leaked into the answer: %q", last.Content)
	}
}

// TestAProviderToolCallCompletesItsRoundTrip covers the path only: a call from
// a real provider runs and its result reaches history paired to the call.
//
// It does NOT prove the full governance those calls are subject to — policy
// refusal, source ordering and record-before-act are asserted elsewhere against
// the runtime, and a title claiming them here would be describing coverage this
// test does not have.
func TestAProviderToolCallCompletesItsRoundTrip(t *testing.T) {
	transport := &scriptedTransport{replies: []string{
		sseReply(
			`{"choices":[{"delta":{"tool_calls":[{"id":"tc1","type":"function","function":{"name":"list_files","arguments":"{}"}}]},"finish_reason":null}]}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		),
		sseReply(`{"choices":[{"delta":{"content":"two files"},"finish_reason":"stop"}]}`),
	}}
	rec, sess := runWithProvider(t, transport)

	if transport.requests != 2 {
		t.Fatalf("sent %d requests; a tool round trip is two", transport.requests)
	}

	var sawToolResult bool
	for _, m := range sess.Truth() {
		if m.Role == ai.RoleTool && m.ToolCallID == "tc1" {
			sawToolResult = true
			if strings.TrimSpace(m.Content) == "" {
				t.Fatal("an empty tool result reached history")
			}
		}
	}
	if !sawToolResult {
		t.Fatal("no tool result paired to tc1 reached history")
	}
	if kinds := rec.Kinds(); len(kinds) == 0 {
		t.Fatal("a provider-driven run produced no events")
	}
}

// TestAProviderFailureStopsTheRun: a 200 that reports a failure must not be
// handed to the caller as an answer.
func TestAProviderFailureStopsTheRun(t *testing.T) {
	transport := &scriptedTransport{replies: []string{
		sseReply(
			`{"choices":[{"delta":{"content":"partial"},"finish_reason":null}]}`,
			`{"choices":[{"delta":{},"finish_reason":"insufficient_system_resource"}]}`,
		),
	}}
	port, err := deepseek.New(deepseek.Config{
		Model: "deepseek-v4-flash", Transport: transport, Environment: fixedEnv{},
		MaxOutputTokens: 32,
	})
	if err != nil {
		t.Fatalf("deepseek.New: %v", err)
	}
	registry, _, _ := tools.NewFixtureRegistry()
	sess := session.New("You are pi-go.")
	agent, err := runtime.New(runtime.Config{
		Model: port, ModelName: "deepseek-v4-flash", Tools: registry,
		Session: sess, Now: fixedClock(),
	})
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}

	runErr := agent.Run(context.Background(), "hello")
	if runErr == nil {
		t.Fatal("an interrupted reply was reported as a successful run")
	}
	var classified *deepseek.Error
	if !errors.As(runErr, &classified) {
		t.Fatalf("the failure reached the caller as %v, losing its classification", runErr)
	}
	if classified.Failure != deepseek.FailureInterrupted {
		t.Fatalf("classified %s", classified.Failure)
	}
	if transport.requests != 1 {
		t.Fatalf("sent %d requests, want 1: an interruption is not retried", transport.requests)
	}
}
