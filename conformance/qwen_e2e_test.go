package conformance

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/events"
	"github.com/iamclancyliang/pi-go/internal/provider/qwen"
	"github.com/iamclancyliang/pi-go/internal/runtime"
	"github.com/iamclancyliang/pi-go/internal/session"
	"github.com/iamclancyliang/pi-go/internal/tools"
)

// This port is exercised through the real agent, not only through its own
// package.
//
// A port can satisfy its interface, pass everything in its own tests, and never
// be reached by the runtime — and the tests written against the port are the
// thing that hides that. It has already happened twice in this repository: a
// message shape its own conversion accepted and the provider refused, and a
// tool result sent under a role no provider accepts.

// qwenTransport replays a recorded exchange and keeps what it was asked to
// send, so a claim about the request can be checked against the request.
type qwenTransport struct {
	requests  int
	sent      []string
	responses []*http.Response
}

func (q *qwenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		body, _ := io.ReadAll(req.Body)
		q.sent = append(q.sent, string(body))
	}
	q.requests++
	if q.requests > len(q.responses) {
		return nil, io.ErrUnexpectedEOF
	}
	return q.responses[q.requests-1], nil
}

func qwenRecorded(chunks ...string) *http.Response {
	var b strings.Builder
	for _, c := range chunks {
		b.WriteString("data: " + c + "\n\n")
	}
	b.WriteString("data: [DONE]\n\n")
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(b.String())),
	}
}

func qwenTextReply(text string, inputTokens, outputTokens int) *http.Response {
	return qwenRecorded(
		`{"id":"c1","model":"qwen-served","choices":[{"index":0,"delta":{"role":"assistant","content":"`+text+`"}}]}`,
		`{"id":"c1","model":"qwen-served","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":`+itoa(inputTokens)+`,"completion_tokens":`+itoa(outputTokens)+`}}`,
	)
}

func qwenToolCallReply(id, name string) *http.Response {
	return qwenRecorded(
		`{"id":"c1","model":"qwen-served","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":`+
			`[{"index":0,"id":"`+id+`","type":"function","function":{"name":"`+name+`","arguments":"{}"}}]}}]}`,
		`{"id":"c1","model":"qwen-served","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],`+
			`"usage":{"prompt_tokens":5,"completion_tokens":2}}`,
	)
}

func qwenPort(t *testing.T, tr http.RoundTripper) *qwen.Port {
	t.Helper()
	p, err := qwen.New(qwen.Config{
		Model: "qwen-test", Transport: tr, Credential: fixedKey, MaxOutputTokens: 64,
	})
	if err != nil {
		t.Fatalf("qwen.New: %v", err)
	}
	return p
}

// TestQwenToolsReachTheProvider.
//
// The runtime hands the agent a registry; whether those descriptions arrive in
// the request is a separate fact. A model that was never told about a tool
// cannot ask for it, and the reply is indistinguishable from a model that
// chose not to — which is why this is asserted against the bytes that went out
// rather than against the configuration that produced them.
func TestQwenToolsReachTheProvider(t *testing.T) {
	transport := &qwenTransport{responses: []*http.Response{qwenTextReply("nothing to do", 3, 2)}}
	registry, _, _ := tools.NewFixtureRegistry()
	agent, err := runtime.New(runtime.Config{
		Model: qwenPort(t, transport), ModelName: "qwen-test", Tools: registry,
		Session: session.New("You are pi-go."), Now: fixedClock(),
	})
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	if err := agent.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(transport.sent) == 0 {
		t.Fatal("nothing was sent")
	}
	body := transport.sent[0]
	if !strings.Contains(body, `"tools"`) {
		t.Fatalf("the request carried no tools at all: %s", body)
	}
	for _, tool := range registry.All() {
		if !strings.Contains(body, `"`+tool.Name()+`"`) {
			t.Fatalf("tool %q never reached the request: %s", tool.Name(), body)
		}
	}
	// And the cap, for the same reason: requiring it at construction says
	// nothing about what was sent.
	if !strings.Contains(body, `"max_tokens":64`) {
		t.Fatalf("the output cap did not reach the request: %s", body)
	}
}

// TestAQwenToolCallIsRefusedByPolicyAndRecordedFirst drives a real provider
// reply through the agent: the call is recorded before the policy is asked, the
// tool never runs, the refusal returns to the model as that call's result, and
// a renderer still sees the call happen.
func TestAQwenToolCallIsRefusedByPolicyAndRecordedFirst(t *testing.T) {
	transport := &qwenTransport{responses: []*http.Response{
		qwenToolCallReply("call_1", "list_files"),
		qwenTextReply("understood", 3, 1),
	}}
	registry, _, listFiles := tools.NewFixtureRegistry()
	sess := session.New("You are pi-go.")
	rec := runtime.NewRecorder()

	// Asserted at the moment of the decision: record-before-act only means
	// anything while the act is still pending.
	recordedFirst := false
	policy := runtime.PolicyFunc(func(_ context.Context, call runtime.PolicyCall) runtime.Decision {
		for _, m := range sess.Truth() {
			for _, c := range m.ToolCalls {
				if c.ID == call.ToolCallID {
					recordedFirst = true
				}
			}
		}
		return runtime.Decision{Denied: true, Reason: "refused by policy"}
	})

	agent, err := runtime.New(runtime.Config{
		Model: qwenPort(t, transport), ModelName: "qwen-test", Tools: registry, Session: sess,
		Policy: policy, Observers: []events.Observer{rec}, Now: fixedClock(),
	})
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	if err := agent.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !recordedFirst {
		t.Fatal("the call was not in history when the policy was asked about it")
	}
	if ran := listFiles.Calls(); ran != 0 {
		t.Fatalf("a refused tool ran %d times", ran)
	}
	var sawRefusal bool
	for _, m := range sess.Truth() {
		if m.Role == ai.RoleTool && m.ToolCallID == "call_1" &&
			strings.Contains(m.Content, "refused by policy") {
			sawRefusal = true
		}
	}
	if !sawRefusal {
		t.Fatal("the refusal never reached the model as the call's result, so the model " +
			"cannot tell a refusal from a call that vanished")
	}
	var sawToolEvents bool
	for _, kind := range rec.Kinds() {
		if kind == events.KindToolStart || kind == events.KindToolEnd {
			sawToolEvents = true
		}
	}
	if !sawToolEvents {
		t.Fatal("a refused tool call produced no tool events for a renderer to show")
	}
}

// TestAQwenToolResultTravelsInAShapeTheProviderAccepts.
//
// The shape a result is sent in is not something this repository's own
// conversion test can settle: it passed for a role the provider refuses, twice,
// and only a run that actually sent the messages showed it.
func TestAQwenToolResultTravelsInAShapeTheProviderAccepts(t *testing.T) {
	transport := &qwenTransport{responses: []*http.Response{
		qwenToolCallReply("call_1", "list_files"),
		qwenTextReply("done", 4, 1),
	}}
	registry, _, listFiles := tools.NewFixtureRegistry()
	sess := session.New("You are pi-go.")
	agent, err := runtime.New(runtime.Config{
		Model: qwenPort(t, transport), ModelName: "qwen-test", Tools: registry,
		Session: sess, Now: fixedClock(),
	})
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	if err := agent.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if ran := listFiles.Calls(); ran != 1 {
		t.Fatalf("the tool ran %d times, want 1", ran)
	}
	if transport.requests != 2 {
		t.Fatalf("a tool round trip took %d requests, want 2", transport.requests)
	}
	// The second request is the one carrying the result back.
	second := transport.sent[1]
	if !strings.Contains(second, `"role":"tool"`) || !strings.Contains(second, `"call_1"`) {
		t.Fatalf("the result did not travel as an answer to the call: %s", second)
	}
}

// TestAQwenFailureInsideA200StopsTheRun: a 200 that reports a failure must not
// reach the caller as an answer.
func TestAQwenFailureInsideA200StopsTheRun(t *testing.T) {
	transport := &qwenTransport{responses: []*http.Response{qwenRecorded(
		`{"id":"c1","model":"qwen-served","choices":[{"index":0,"delta":{"role":"assistant","content":"partial"}}]}`,
		`{"id":"c1","model":"qwen-served","error":{"code":"Arrearage","message":"gone"}}`,
	)}}
	sess := session.New("You are pi-go.")
	registry, _, _ := tools.NewFixtureRegistry()
	agent, err := runtime.New(runtime.Config{
		Model: qwenPort(t, transport), ModelName: "qwen-test", Tools: registry,
		Session: sess, Now: fixedClock(),
	})
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	runErr := agent.Run(context.Background(), "hello")
	if runErr == nil {
		t.Fatal("a failure inside a 200 was answered as a reply")
	}
	failure, ok := ai.FailureOf(runErr)
	if !ok {
		t.Fatalf("a caller cannot branch on %v", runErr)
	}
	if failure != ai.FailureQuota {
		t.Fatalf("classified %s, want an exhausted balance", failure)
	}
}

// TestARefusedQwenCallLedgersWhatItRead: a request the provider read is a
// request the provider charged for, answered or not.
func TestARefusedQwenCallLedgersWhatItRead(t *testing.T) {
	transport := &qwenTransport{responses: []*http.Response{{
		StatusCode: 429,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"error":{"code":"Throttling","message":"slow"},` +
				`"usage":{"prompt_tokens":37,"completion_tokens":0}}`)),
	}}}
	sess := session.New("You are pi-go.")
	registry, _, _ := tools.NewFixtureRegistry()
	agent, err := runtime.New(runtime.Config{
		Model: qwenPort(t, transport), ModelName: "qwen-test", Tools: registry,
		Session: sess, Now: fixedClock(),
	})
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	if err := agent.Run(context.Background(), "hello"); err == nil {
		t.Fatal("a refusal was answered as a reply")
	}
	if got := sess.Usage().InputTokens; got != 37 {
		t.Fatalf("the ledger holds %d input tokens for a refused call, want 37", got)
	}
}

// TestQwenReasoningReturnsToTheProviderOnTheNextRound.
//
// A model's own reasoning is part of what it said. Dropping it from the history
// sent back leaves the model reading a version of the conversation in which it
// reached a conclusion with no working — and the round that follows a tool call
// is exactly where that matters.
func TestQwenReasoningReturnsToTheProviderOnTheNextRound(t *testing.T) {
	transport := &qwenTransport{responses: []*http.Response{
		qwenRecorded(
			`{"id":"c1","model":"qwen-served","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"weighing it up"}}]}`,
			`{"id":"c1","model":"qwen-served","choices":[{"index":0,"delta":{"tool_calls":`+
				`[{"index":0,"id":"call_1","type":"function","function":{"name":"list_files","arguments":"{}"}}]}}]}`,
			`{"id":"c1","model":"qwen-served","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],`+
				`"usage":{"prompt_tokens":5,"completion_tokens":2}}`,
		),
		qwenTextReply("done", 4, 1),
	}}
	registry, _, _ := tools.NewFixtureRegistry()
	agent, err := runtime.New(runtime.Config{
		Model: qwenPort(t, transport), ModelName: "qwen-test", Tools: registry,
		Session: session.New("You are pi-go."), Now: fixedClock(),
	})
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	if err := agent.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(transport.sent) != 2 {
		t.Fatalf("a tool round trip sent %d requests, want 2", len(transport.sent))
	}
	if !strings.Contains(transport.sent[1], "weighing it up") {
		t.Fatalf("the model's own reasoning did not return with the history: %s", transport.sent[1])
	}
	// And it returns as reasoning, not folded into the answer: the two are
	// different things to a model reading its own history.
	if !strings.Contains(transport.sent[1], `"reasoning_content"`) {
		t.Fatalf("reasoning came back as something else: %s", transport.sent[1])
	}
}
