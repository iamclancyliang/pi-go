package conformance

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/events"
	"github.com/iamclancyliang/pi-go/internal/provider/openai"
	"github.com/iamclancyliang/pi-go/internal/runtime"
	"github.com/iamclancyliang/pi-go/internal/session"
	"github.com/iamclancyliang/pi-go/internal/tools"
)

// This port is exercised through the real agent, not only through its own
// package.
//
// A port can satisfy its interface, pass everything in its own tests, and never
// be reached by the runtime — and the tests written against the port are the
// thing that hides that.

// openaiTransport replays a recorded exchange and counts what it was asked to
// send. The count is what proves a call's request budget, since a configuration
// value proves nothing about what actually went out.
type openaiTransport struct {
	requests  int
	responses []*http.Response
}

func (o *openaiTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		_, _ = io.Copy(io.Discard, req.Body)
	}
	o.requests++
	if o.requests > len(o.responses) {
		return nil, io.ErrUnexpectedEOF
	}
	return o.responses[o.requests-1], nil
}

var fixedKey = ai.StoredCredential("test-key", "a test")

func openaiRecorded(events ...string) *http.Response {
	var b strings.Builder
	for _, e := range events {
		b.WriteString("event: x\ndata: " + e + "\n\n")
	}
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(b.String())),
	}
}

func textReply(text string, inputTokens, outputTokens int) *http.Response {
	return openaiRecorded(
		`{"type":"response.created","response":{"id":"r","model":"gpt-served","status":"in_progress"}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"m","type":"message","role":"assistant","status":"in_progress","content":[]}}`,
		`{"type":"response.content_part.added","item_id":"m","output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}`,
		`{"type":"response.output_text.delta","item_id":"m","output_index":0,"content_index":0,"delta":"`+text+`"}`,
		`{"type":"response.output_text.done","item_id":"m","output_index":0,"content_index":0,"text":"`+text+`"}`,
		`{"type":"response.content_part.done","item_id":"m","output_index":0,"content_index":0,"part":{"type":"output_text","text":"`+text+`"}}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"m","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"`+text+`"}]}}`,
		`{"type":"response.completed","response":{"id":"r","model":"gpt-served","status":"completed","usage":{"input_tokens":`+
			itoa(inputTokens)+`,"output_tokens":`+itoa(outputTokens)+`}}}`,
	)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// TestAnOpenAIReplyReachesTheRuntime proves the port is actually reachable: a
// reply it produced becomes conversational truth, and the ledger holds what the
// call used.
func TestAnOpenAIReplyReachesTheRuntime(t *testing.T) {
	transport := &openaiTransport{responses: []*http.Response{textReply("the answer", 11, 2)}}
	port, err := openai.New(openai.Config{
		Model: "gpt-test", Transport: transport, Credential: fixedKey, MaxOutputTokens: 64,
	})
	if err != nil {
		t.Fatalf("openai.New: %v", err)
	}

	registry, _, _ := tools.NewFixtureRegistry()
	rec := runtime.NewRecorder()
	sess := session.New("You are pi-go.")
	agent, err := runtime.New(runtime.Config{
		Model: port, ModelName: "gpt-test", Tools: registry, Session: sess,
		Observers: []events.Observer{rec}, Now: fixedClock(),
	})
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	if err := agent.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if transport.requests != 1 {
		t.Fatalf("the runtime sent %d requests for one answer", transport.requests)
	}
	msgs := sess.Truth()
	last := msgs[len(msgs)-1]
	if last.Role != ai.RoleAssistant || last.Content != "the answer" {
		t.Fatalf("history ends with %s %q", last.Role, last.Content)
	}
	if total := sess.Usage(); total.InputTokens != 11 || total.OutputTokens != 2 {
		t.Fatalf("ledger holds %d in / %d out", total.InputTokens, total.OutputTokens)
	}
	if len(rec.Kinds()) == 0 {
		t.Fatal("a provider-driven run produced no events")
	}
}

func toolCallReply(callID, name string) *http.Response {
	return openaiRecorded(
		`{"type":"response.created","response":{"id":"r","model":"gpt-served","status":"in_progress"}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"fc","type":"function_call","call_id":"`+callID+`","name":"`+name+`","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","item_id":"fc","output_index":0,"delta":"{}"}`,
		`{"type":"response.function_call_arguments.done","item_id":"fc","output_index":0,"arguments":"{}"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"fc","type":"function_call","call_id":"`+callID+`","name":"`+name+`","arguments":"{}","status":"completed"}}`,
		`{"type":"response.completed","response":{"id":"r","model":"gpt-served","status":"completed","usage":{"input_tokens":4,"output_tokens":2}}}`,
	)
}

// TestAnOpenAIToolCallIsRefusedByPolicyAndRecordedFirst.
//
// A tool call arriving from this provider is subject to the rules that already
// govern any call: a policy may refuse it, the refusal reaches the model as the
// call's result, the tool does not run, and the request is in history before any
// of that happens.
func TestAnOpenAIToolCallIsRefusedByPolicyAndRecordedFirst(t *testing.T) {
	transport := &openaiTransport{responses: []*http.Response{
		toolCallReply("call_1", "list_files"),
		textReply("understood", 3, 1),
	}}
	port, err := openai.New(openai.Config{
		Model: "gpt-test", Transport: transport, Credential: fixedKey, MaxOutputTokens: 64,
	})
	if err != nil {
		t.Fatalf("openai.New: %v", err)
	}

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
		Model: port, ModelName: "gpt-test", Tools: registry, Session: sess,
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

	// The surface a renderer reads: tool events are announced even for a call
	// that was refused, because a silently skipped call is indistinguishable
	// from one that never happened.
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

// TestARefusedOpenAICallLedgersWhatItRead: a request the provider read is a
// request the provider charged for, answered or not.
func TestARefusedOpenAICallLedgersWhatItRead(t *testing.T) {
	transport := &openaiTransport{responses: []*http.Response{{
		StatusCode: 429,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"error":{"code":"rate_limit_exceeded","message":"slow"},` +
				`"usage":{"input_tokens":37,"output_tokens":0}}`)),
	}}}
	port, err := openai.New(openai.Config{
		Model: "gpt-test", Transport: transport, Credential: fixedKey, MaxOutputTokens: 64,
	})
	if err != nil {
		t.Fatalf("openai.New: %v", err)
	}
	registry, _, _ := tools.NewFixtureRegistry()
	sess := session.New("You are pi-go.")
	agent, err := runtime.New(runtime.Config{
		Model: port, ModelName: "gpt-test", Tools: registry, Session: sess, Now: fixedClock(),
	})
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	if err := agent.Run(context.Background(), "hello"); err == nil {
		t.Fatal("a refused call was reported as a successful run")
	}

	if got := sess.Usage().InputTokens; got != 37 {
		t.Fatalf("a refused call ledgered %d input tokens; the provider read 37", got)
	}
}
