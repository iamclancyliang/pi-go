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

// The OpenAI pilot is exercised through the real agent, not only through its
// own package.
//
// A port can satisfy its interface, pass everything in its own tests, and never
// be reached by the runtime — and the tests written against the port are the
// thing that hides that.

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

type openaiEnv struct{}

func (openaiEnv) Lookup(context.Context, string) (string, error) { return "test-key", nil }

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
		Model: "gpt-test", Transport: transport, Environment: openaiEnv{}, MaxOutputTokens: 64,
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
