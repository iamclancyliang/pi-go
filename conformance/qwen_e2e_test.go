package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
// thing that hides that. A message shape is the clearest case: conversion
// accepts what it was told to build, and only sending it can show whether the
// far side speaks that shape.

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
	var starts, ends int
	for _, kind := range rec.Kinds() {
		switch kind {
		case events.KindToolStart:
			starts++
		case events.KindToolEnd:
			ends++
		}
	}
	if starts != 1 || ends != 1 {
		t.Fatalf("a refused call gave a renderer %d starts and %d ends, want one of each; "+
			"a call shown as still running is one the reader is waiting on", starts, ends)
	}
}

// TestAQwenToolResultReachesTheWireInTheProtocolShape.
//
// What this settles is what left the process: a result addressed to the call it
// answers, in the role the protocol names. It does not settle that the provider
// accepted it — nothing offline can, and saying otherwise would describe
// evidence this test does not have.
//
// It is still worth more than a conversion test: a conversion test asserts
// against the value it just built, which is the same value under a different
// name, while this asserts against the bytes that left.
func TestAQwenToolResultReachesTheWireInTheProtocolShape(t *testing.T) {
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
		t.Fatalf("the result did not leave as an answer to the call: %s", second)
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

// TestAQwenBodyReadFailureEndsAsAFailureAndKeepsTheKeyOut.
//
// Four things at once, because each can hold while another does not: the reply
// ends as a failure rather than as a stop the caller never asked for, exactly
// one terminal arrives, what had already been shown survives, and the key this
// call was made with appears nowhere in the error or anything it wraps.
func TestAQwenBodyReadFailureEndsAsAFailureAndKeepsTheKeyOut(t *testing.T) {
	const secret = "7c1e-not-shaped-like-a-key"
	port, err := qwen.New(qwen.Config{
		Model: "qwen-test", MaxOutputTokens: 64,
		Credential: ai.StoredCredential(secret, "a test"),
		Transport: &qwenTransport{responses: []*http.Response{{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: &haltingReader{
				prefix: strings.NewReader("data: " +
					`{"id":"c1","model":"qwen-served","choices":[{"index":0,"delta":{"role":"assistant","content":"already said"}}]}` +
					"\n\n"),
				err: fmt.Errorf("proxy dropped the connection for Authorization=%s", secret),
			},
		}}},
	})
	if err != nil {
		t.Fatalf("qwen.New: %v", err)
	}
	events, err := port.Stream(context.Background(), ai.Request{
		Model: "qwen-test", Messages: []ai.Message{{Role: ai.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var final *ai.AssistantMessage
	terminals := 0
	for ev := range events {
		if ev.Terminal() {
			terminals++
			final = ev.Final
		}
	}
	if terminals != 1 {
		t.Fatalf("a broken read delivered %d terminal events, want exactly 1", terminals)
	}
	if final.StopReason != ai.StopError {
		t.Fatalf("a read nobody stopped ended as %q, which tells a caller it "+
			"cancelled something it did not", final.StopReason)
	}
	if _, classified := ai.FailureOf(final.Cause); !classified {
		t.Fatalf("a caller cannot branch on %v", final.Cause)
	}
	var shown strings.Builder
	for _, b := range final.Blocks {
		shown.WriteString(b.Text)
	}
	if shown.String() != "already said" {
		t.Fatalf("what had already arrived was lost: %q", shown.String())
	}
	// Every layer, not just the top: a wrapper can print a clean message while
	// the cause underneath still carries the key.
	for cause := final.Cause; cause != nil; cause = errors.Unwrap(cause) {
		if strings.Contains(cause.Error(), secret) {
			t.Fatalf("the key this call used reached %T: %v", cause, cause)
		}
	}
}

// haltingReader delivers what it was given and then fails, as a body does when
// what is underneath it breaks mid-reply.
type haltingReader struct {
	prefix *strings.Reader
	err    error
}

func (h *haltingReader) Read(p []byte) (int, error) {
	if h.prefix.Len() > 0 {
		return h.prefix.Read(p)
	}
	return 0, h.err
}

func (h *haltingReader) Close() error { return nil }

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

// TestAQwenReplyBecomesHistoryAndIsLedgered.
//
// A reply that answered but never entered the session is a turn the next
// request will not contain, and tokens that were spent but never recorded are a
// bill with no line for them.
func TestAQwenReplyBecomesHistoryAndIsLedgered(t *testing.T) {
	transport := &qwenTransport{responses: []*http.Response{qwenTextReply("the answer", 11, 4)}}
	sess := session.New("You are pi-go.")
	registry, _, _ := tools.NewFixtureRegistry()
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

	var answered bool
	for _, m := range sess.Truth() {
		if m.Role == ai.RoleAssistant && m.Content == "the answer" {
			answered = true
		}
	}
	if !answered {
		t.Fatal("the reply never became history, so the next request would not contain it")
	}
	used := sess.Usage()
	if used.InputTokens != 11 || used.OutputTokens != 4 {
		t.Fatalf("the ledger holds %d in and %d out, want 11 and 4", used.InputTokens, used.OutputTokens)
	}
}

// TestSeveralQwenToolCallsKeepTheOrderTheModelAsked.
//
// Order is part of what the model said. The package tests show fragments
// reassembling in order; this shows the order surviving the runtime — which
// decides, executes and renders them, and could reorder at any of the three.
func TestSeveralQwenToolCallsKeepTheOrderTheModelAsked(t *testing.T) {
	transport := &qwenTransport{responses: []*http.Response{
		qwenRecorded(
			`{"id":"c1","model":"qwen-served","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":`+
				`[{"index":0,"id":"call_1","type":"function","function":{"name":"list_files","arguments":"{}"}}]}}]}`,
			`{"id":"c1","model":"qwen-served","choices":[{"index":0,"delta":{"tool_calls":`+
				`[{"index":1,"id":"call_2","type":"function","function":{"name":"file_read","arguments":"{\"path\":\"README.md\"}"}}]}}]}`,
			`{"id":"c1","model":"qwen-served","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],`+
				`"usage":{"prompt_tokens":5,"completion_tokens":2}}`,
		),
		qwenTextReply("done", 6, 1),
	}}
	registry, _, _ := tools.NewFixtureRegistry()
	sess := session.New("You are pi-go.")
	rec := runtime.NewRecorder()

	var asked []string
	policy := runtime.PolicyFunc(func(_ context.Context, call runtime.PolicyCall) runtime.Decision {
		asked = append(asked, call.ToolCallID)
		return runtime.Decision{}
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

	want := []string{"call_1", "call_2"}
	if len(asked) != 2 || asked[0] != want[0] || asked[1] != want[1] {
		t.Fatalf("the policy was asked in order %v, want %v", asked, want)
	}
	var results []string
	for _, m := range sess.Truth() {
		if m.Role == ai.RoleTool {
			results = append(results, m.ToolCallID)
		}
	}
	if len(results) != 2 || results[0] != want[0] || results[1] != want[1] {
		t.Fatalf("results returned in order %v, want %v", results, want)
	}

	// What a renderer sees, read from the events rather than from the kinds.
	//
	// Beginnings are in the order the model asked: they are announced in one
	// serial pass before anything runs. Endings are in the order the calls
	// finished, which is a different order on purpose — so this asserts that
	// each call ended exactly once, and says nothing about which ended first.
	// Requiring source order here would demand a guarantee the design
	// deliberately does not make, and would go red on a correct system whenever
	// the second call happened to finish first.
	var started []string
	ended := map[string]int{}
	for _, ev := range rec.Events() {
		switch ev.Kind {
		case events.KindToolStart:
			started = append(started, ev.ToolCallID)
		case events.KindToolEnd:
			ended[ev.ToolCallID]++
		}
	}
	if len(started) != 2 || started[0] != want[0] || started[1] != want[1] {
		t.Fatalf("a renderer saw calls begin in order %v, want %v", started, want)
	}
	for _, id := range want {
		if ended[id] != 1 {
			t.Fatalf("a renderer saw %s finish %d times, want exactly once: %v",
				id, ended[id], ended)
		}
	}
	if len(ended) != 2 {
		t.Fatalf("a renderer saw endings for %v, want exactly the two calls", ended)
	}
}

// TestAQwenPolicyRefusalReachesTheModel.
//
// A refusal recorded only in the session is one the model never reads. The next
// request is where it has to appear, or the model asks again for something that
// will be refused again.
func TestAQwenPolicyRefusalReachesTheModel(t *testing.T) {
	transport := &qwenTransport{responses: []*http.Response{
		qwenToolCallReply("call_1", "list_files"),
		qwenTextReply("understood", 3, 1),
	}}
	registry, _, _ := tools.NewFixtureRegistry()
	agent, err := runtime.New(runtime.Config{
		Model: qwenPort(t, transport), ModelName: "qwen-test", Tools: registry,
		Session: session.New("You are pi-go."), Now: fixedClock(),
		Policy: runtime.PolicyFunc(func(context.Context, runtime.PolicyCall) runtime.Decision {
			return runtime.Decision{Denied: true, Reason: "refused by policy"}
		}),
	})
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	if err := agent.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(transport.sent) != 2 {
		t.Fatalf("a refused call took %d requests, want 2", len(transport.sent))
	}
	// Parsed, not searched. The three facts have to hold together on one
	// message: it answers a call, it answers THIS call, and what it says is the
	// refusal. Finding the words anywhere in the body would also pass on a
	// plain user message that mentions them, which the model reads as a person
	// talking rather than as the outcome of its own call.
	answer, ok := toolResultIn(t, transport.sent[1], "call_1")
	if !ok {
		t.Fatalf("no result addressed to call_1 left the process: %s", transport.sent[1])
	}
	if !strings.Contains(answer, "refused by policy") {
		t.Fatalf("the call was answered with %q, not the refusal", answer)
	}
}

// toolResultIn finds the result addressed to one call in a serialized request.
func toolResultIn(t *testing.T, body, callID string) (string, bool) {
	t.Helper()
	var parsed struct {
		Messages []struct {
			Role       string `json:"role"`
			Content    any    `json:"content"`
			ToolCallID string `json:"tool_call_id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("the request was not the shape this provider speaks: %v", err)
	}
	for _, m := range parsed.Messages {
		if m.Role != "tool" || m.ToolCallID != callID {
			continue
		}
		switch content := m.Content.(type) {
		case string:
			return content, true
		default:
			return fmt.Sprint(content), true
		}
	}
	return "", false
}

// TestAQwenToolCallAnnouncesBothHalvesToARenderer.
//
// A renderer showing a call as still running is showing something that already
// finished. Both halves have to arrive, so asserting that either one did would
// pass on a stream that only ever opens calls.
func TestAQwenToolCallAnnouncesBothHalvesToARenderer(t *testing.T) {
	transport := &qwenTransport{responses: []*http.Response{
		qwenToolCallReply("call_1", "list_files"),
		qwenTextReply("done", 3, 1),
	}}
	registry, _, _ := tools.NewFixtureRegistry()
	rec := runtime.NewRecorder()
	agent, err := runtime.New(runtime.Config{
		Model: qwenPort(t, transport), ModelName: "qwen-test", Tools: registry,
		Session: session.New("You are pi-go."), Observers: []events.Observer{rec},
		Now: fixedClock(),
	})
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	if err := agent.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var starts, ends int
	for _, kind := range rec.Kinds() {
		switch kind {
		case events.KindToolStart:
			starts++
		case events.KindToolEnd:
			ends++
		}
	}
	if starts != 1 || ends != 1 {
		t.Fatalf("a renderer saw %d starts and %d ends, want one of each", starts, ends)
	}
}
