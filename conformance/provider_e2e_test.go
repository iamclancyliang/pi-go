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
	sent     []string
}

func (s *scriptedTransport) Do(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		body, _ := io.ReadAll(req.Body)
		s.sent = append(s.sent, string(body))
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

// TestReasoningReturnsToTheProviderOnTheNextRound.
//
// This provider requires an assistant's reasoning to be sent back with the next
// request. History that keeps it but never resends it looks complete and still
// breaks the conversation, so the assertion is on the SECOND request's body —
// the only place the difference is visible.
func TestReasoningReturnsToTheProviderOnTheNextRound(t *testing.T) {
	transport := &scriptedTransport{replies: []string{
		sseReply(
			`{"choices":[{"delta":{"reasoning_content":"weighing the options"},"finish_reason":null}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"tc1","type":"function","function":{"name":"list_files","arguments":"{}"}}]},"finish_reason":null}]}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		),
		sseReply(`{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`),
	}}
	_, sess := runWithProvider(t, transport)

	if transport.requests != 2 {
		t.Fatalf("made %d requests, want 2", transport.requests)
	}
	second := transport.sent[1]
	if !strings.Contains(second, "weighing the options") {
		t.Fatalf("the second request did not carry the first round's reasoning:\n%s", second)
	}
	if !strings.Contains(second, `"reasoning_content"`) {
		t.Fatalf("reasoning was resent under the wrong field:\n%s", second)
	}

	// It is kept apart from the answer in history, not merged into it.
	var found bool
	for _, m := range sess.Truth() {
		if m.Role == ai.RoleAssistant && m.Reasoning != "" {
			found = true
			if strings.Contains(m.Content, "weighing the options") {
				t.Fatalf("reasoning was merged into the answer: %q", m.Content)
			}
		}
	}
	if !found {
		t.Fatal("no assistant message in history carried the reasoning")
	}
}

// generateOnly hides the streaming half of a port.
//
// The runtime streams whenever a port can, so without this the collected path
// is never driven through the agent — and a defect that lives only there is
// invisible to every other test.
type generateOnly struct{ inner ai.Port }

func (g generateOnly) Generate(ctx context.Context, req ai.Request) (ai.Response, error) {
	return g.inner.Generate(ctx, req)
}

// TestReasoningReturnsOnTheCollectedPathToo runs the same round trip without
// streaming, because the two paths reach the framework by different code.
func TestReasoningReturnsOnTheCollectedPathToo(t *testing.T) {
	transport := &scriptedTransport{replies: []string{
		sseReply(
			`{"choices":[{"delta":{"reasoning_content":"deliberating"},"finish_reason":null}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"tc1","type":"function","function":{"name":"list_files","arguments":"{}"}}]},"finish_reason":null}]}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		),
		sseReply(`{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`),
	}}
	streaming, err := deepseek.New(deepseek.Config{
		Model: "deepseek-v4-flash", Transport: transport, Environment: fixedEnv{},
		MaxOutputTokens: 32,
	})
	if err != nil {
		t.Fatalf("deepseek.New: %v", err)
	}

	registry, _, _ := tools.NewFixtureRegistry()
	sess := session.New("You are pi-go.")
	agent, err := runtime.New(runtime.Config{
		Model:     generateOnly{inner: streaming},
		ModelName: "deepseek-v4-flash",
		Tools:     registry,
		Session:   sess,
		Now:       fixedClock(),
	})
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	if err := agent.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if transport.requests != 2 {
		t.Fatalf("made %d requests, want 2", transport.requests)
	}
	if !strings.Contains(transport.sent[1], "deliberating") {
		t.Fatalf("the collected path lost the reasoning before the second request:\n%s", transport.sent[1])
	}
}

// TestUsageFromEveryCallReachesTheLedger: a conversation's consumption is the
// sum of its calls, and a two-round exchange spent on both.
func TestUsageFromEveryCallReachesTheLedger(t *testing.T) {
	transport := &scriptedTransport{replies: []string{
		sseReply(
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"tc1","type":"function","function":{"name":"list_files","arguments":"{}"}}]},"finish_reason":null}]}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":100,"completion_tokens":10,"completion_tokens_details":{"reasoning_tokens":4}}}`,
		),
		sseReply(`{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":200,"completion_tokens":20,"completion_tokens_details":{"reasoning_tokens":6}}}`),
	}}
	_, sess := runWithProvider(t, transport)

	attempts := sess.Attempts()
	if len(attempts) != 2 {
		t.Fatalf("recorded %d attempts for a two-call exchange", len(attempts))
	}

	total := sess.Usage()
	if total.InputTokens != 300 || total.OutputTokens != 30 {
		t.Fatalf("total is %d in / %d out; recording only the last call would read 200/20",
			total.InputTokens, total.OutputTokens)
	}
	if total.ReasoningTokens == nil || *total.ReasoningTokens != 10 {
		t.Fatalf("reasoning total is %v, want 10", total.ReasoningTokens)
	}
	if !total.Reported {
		t.Fatal("a total built from reported attempts claims nothing was reported")
	}
}

// TestAnUnreportedFieldStaysUnreportedInTheTotal: summing what was never said
// would invent a number, and a zero would claim the provider said zero.
func TestAnUnreportedFieldStaysUnreportedInTheTotal(t *testing.T) {
	transport := &scriptedTransport{replies: []string{
		sseReply(`{"choices":[{"delta":{"content":"x"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1}}`),
	}}
	_, sess := runWithProvider(t, transport)

	total := sess.Usage()
	if total.ReasoningTokens != nil {
		t.Fatalf("a field no call reported became %d in the total", *total.ReasoningTokens)
	}
	if total.InputTokens != 5 {
		t.Fatalf("input total %d", total.InputTokens)
	}
}
