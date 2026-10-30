package gemini_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/provider/gemini"
)

// recordedTransport replays a recorded exchange and counts what was sent.
type recordedTransport struct {
	requests  int
	urls      []string
	responses []*http.Response
}

func (r *recordedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.requests++
	r.urls = append(r.urls, req.URL.String())
	if r.requests > len(r.responses) {
		return nil, io.ErrUnexpectedEOF
	}
	return r.responses[r.requests-1], nil
}

var fixedKey = ai.StoredCredential("gemini-test-key", "a test")

func refused(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func newPort(t *testing.T, tr http.RoundTripper) *gemini.Port {
	t.Helper()
	p, err := gemini.New(gemini.Config{
		Model: "gemini-test", Transport: tr,
		Credential: fixedKey, MaxOutputTokens: 64,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

func ask(t *testing.T, p *gemini.Port) (ai.Response, error) {
	t.Helper()
	return p.Generate(context.Background(), ai.Request{
		Model:    "gemini-test",
		Messages: []ai.Message{{Role: ai.RoleUser, Content: "hello"}},
	})
}

func failureOf(t *testing.T, err error) ai.Failure {
	t.Helper()
	got, ok := ai.FailureOf(err)
	if !ok {
		t.Fatalf("the failure carries no classification: %v", err)
	}
	return got
}

// body is this provider's error envelope: the status code repeated as a field,
// a message, and Google's own canonical status name.
func body(code int, status, message string) string {
	return fmt.Sprintf(`{"error":{"code":%d,"message":%q,"status":%q}}`, code, message, status)
}

// TestTheCanonicalStatusNamesThisProviderSends.
//
// These are google.rpc.Code names, shared across every Google API rather than
// invented for this one. They are read before the HTTP status because they are
// more precise, and because a gateway can rewrite a status while leaving the
// body alone.
func TestTheCanonicalStatusNamesThisProviderSends(t *testing.T) {
	for _, c := range []struct {
		name   string
		status int
		code   string
		want   ai.Failure
	}{
		{"unauthenticated", 401, "UNAUTHENTICATED", ai.FailureAuth},
		{"permission denied", 403, "PERMISSION_DENIED", ai.FailureAuth},
		{"resource exhausted", 429, "RESOURCE_EXHAUSTED", ai.FailureThrottled},
		{"unavailable", 503, "UNAVAILABLE", ai.FailureTransient},
		{"internal", 500, "INTERNAL", ai.FailureTransient},
		{"invalid argument", 400, "INVALID_ARGUMENT", ai.FailureRefused},
		{"not found", 404, "NOT_FOUND", ai.FailureRefused},

		// The reason the name is read first. A proxy in front of the provider
		// can rewrite the status; it does not rewrite the body. Classified by
		// status alone, a throttle arriving as a 400 would be reported as a
		// request to go and fix.
		{"throttle through a rewritten status", 400, "RESOURCE_EXHAUSTED", ai.FailureThrottled},
	} {
		t.Run(c.name, func(t *testing.T) {
			tr := &recordedTransport{responses: []*http.Response{
				refused(c.status, body(c.status, c.code, "nope"))}}
			_, err := ask(t, newPort(t, tr))
			if err == nil {
				t.Fatal("expected a failure")
			}
			if got := failureOf(t, err); got != c.want {
				t.Fatalf("%s classified as %q, want %q", c.code, got, c.want)
			}
		})
	}
}

// TestAPromptThatDidNotFitIsReportedAsOverflow.
//
// The wording is pi's recorded one (packages/ai/src/utils/overflow.ts:16 at the
// pin). The two numbers are compared rather than the phrase matched.
func TestAPromptThatDidNotFitIsReportedAsOverflow(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{refused(400,
		body(400, "INVALID_ARGUMENT",
			"The input token count (1196265) exceeds the maximum number of tokens allowed (1048575)"))}}

	_, err := ask(t, newPort(t, tr))
	if err == nil {
		t.Fatal("an oversized prompt reported success")
	}
	if !errors.Is(err, ai.ErrContextOverflow) {
		t.Fatalf("an oversized prompt was not reported as an overflow: %v", err)
	}
}

// TestAMessageMentioningTokensIsNotAnOverflow. A false positive spends a second
// billed request shortening a conversation that was never too long.
func TestAMessageMentioningTokensIsNotAnOverflow(t *testing.T) {
	for name, message := range map[string]string{
		"a count within the maximum": "The input token count (10) exceeds the maximum number of tokens allowed (1048575)",
		"an unrelated complaint":     "maxOutputTokens must be a positive number of tokens",
	} {
		t.Run(name, func(t *testing.T) {
			tr := &recordedTransport{responses: []*http.Response{
				refused(400, body(400, "INVALID_ARGUMENT", message))}}
			_, err := ask(t, newPort(t, tr))
			if err == nil {
				t.Fatal("expected a failure")
			}
			if errors.Is(err, ai.ErrContextOverflow) {
				t.Fatalf("%s was reported as an overflow: %v", name, err)
			}
		})
	}
}

// TestOneCallSendsOneRequest, counted at the transport.
func TestOneCallSendsOneRequest(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{
		refused(500, body(500, "INTERNAL", "boom"))}}
	if _, err := ask(t, newPort(t, tr)); err == nil {
		t.Fatal("expected a failure")
	}
	if tr.requests != 1 {
		t.Fatalf("one call sent %d requests", tr.requests)
	}
}

// TestServingOnlyTheConfiguredModel.
func TestServingOnlyTheConfiguredModel(t *testing.T) {
	tr := &recordedTransport{}
	p := newPort(t, tr)
	_, err := p.Generate(context.Background(), ai.Request{
		Model:    "gemini-something-else",
		Messages: []ai.Message{{Role: ai.RoleUser, Content: "hello"}},
	})
	if err == nil {
		t.Fatal("a request naming another model was served")
	}
	if tr.requests != 0 {
		t.Fatalf("a refused request still reached the provider %d times", tr.requests)
	}
}

// TestAConfigurationThatCouldNotWorkIsRefusedAtConstruction.
func TestAConfigurationThatCouldNotWorkIsRefusedAtConstruction(t *testing.T) {
	tr := &recordedTransport{}
	for name, cfg := range map[string]gemini.Config{
		"no model":        {Transport: tr, Credential: fixedKey, MaxOutputTokens: 8},
		"no transport":    {Model: "m", Credential: fixedKey, MaxOutputTokens: 8},
		"no credential":   {Model: "m", Transport: tr, MaxOutputTokens: 8},
		"no output cap":   {Model: "m", Transport: tr, Credential: fixedKey},
		"negative window": {Model: "m", Transport: tr, Credential: fixedKey, MaxOutputTokens: 8, ContextWindow: -1},
	} {
		if _, err := gemini.New(cfg); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// TestTheCredentialNeverReachesAFailure.
func TestTheCredentialNeverReachesAFailure(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{refused(400,
		body(400, "INVALID_ARGUMENT", "we received gemini-test-key and disliked it"))}}

	_, err := ask(t, newPort(t, tr))
	if err == nil {
		t.Fatal("expected a failure")
	}
	if strings.Contains(err.Error(), "gemini-test-key") {
		t.Fatalf("the credential survived into a failure: %v", err)
	}
}

// TestAnUnreadableRefusalStillClassifies.
func TestAnUnreadableRefusalStillClassifies(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{refused(503, `<html>gateway</html>`)}}
	_, err := ask(t, newPort(t, tr))
	if err == nil {
		t.Fatal("expected a failure")
	}
	if got := failureOf(t, err); got != ai.FailureTransient {
		t.Fatalf("an unreadable 503 classified as %q", got)
	}
}

// TestTheBackendIsNotChosenByTheEnvironment.
//
// Left to itself the SDK reads GOOGLE_GENAI_USE_VERTEXAI and switches to Vertex
// AI — a different service, billed to a Google Cloud project rather than to the
// key this port resolved — because of a variable this port never saw.
//
// Asserted at the URL, because that is where the difference actually shows.
// Measured with the pin removed, the same call goes to
// aiplatform.googleapis.com instead: it does not fail, it silently reaches
// somewhere else. A provider is chosen by asking for it.
func TestTheBackendIsNotChosenByTheEnvironment(t *testing.T) {
	t.Setenv("GOOGLE_GENAI_USE_VERTEXAI", "1")

	tr := &recordedTransport{responses: []*http.Response{
		refused(400, body(400, "INVALID_ARGUMENT", "nope"))}}
	if _, err := ask(t, newPort(t, tr)); err == nil {
		t.Fatal("expected a failure")
	}
	if len(tr.urls) != 1 {
		t.Fatalf("one call sent %d requests: %v", len(tr.urls), tr.urls)
	}
	if got := tr.urls[0]; !strings.Contains(got, "generativelanguage.googleapis.com") {
		t.Fatalf("an environment variable redirected the call to %s", got)
	}
}

// eventStream is a streamed reply as this provider frames it: one complete
// response per event, with the ending and final counts on the last.
func eventStream(responses ...string) *http.Response {
	var body strings.Builder
	for _, response := range responses {
		body.WriteString("data: " + response + "\r\n\r\n")
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body.String())),
	}
}

func textChunk(text string) string {
	return `{"candidates":[{"content":{"role":"model","parts":[{"text":"` + text + `"}]}}],` +
		`"usageMetadata":{"promptTokenCount":12,"totalTokenCount":12}}`
}

func callPart(name, args string) string {
	return `{"functionCall":{"name":"` + name + `","args":` + args + `}}`
}

func callChunk(parts ...string) string {
	return `{"candidates":[{"content":{"role":"model","parts":[` + strings.Join(parts, ",") + `]}}]}`
}

func endChunk(reason string) string {
	return `{"candidates":[{"content":{"role":"model","parts":[{"text":""}]},"finishReason":"` + reason + `"}],` +
		`"usageMetadata":{"promptTokenCount":12,"cachedContentTokenCount":4,` +
		`"candidatesTokenCount":7,"thoughtsTokenCount":2,"totalTokenCount":21}}`
}

// streamed drains a reply down to its last event. Tools are bound, because the
// calls these tests are about only happen on a model that has them.
func streamed(t *testing.T, p *gemini.Port) ai.StreamEvent {
	t.Helper()
	events, err := p.Stream(context.Background(), ai.Request{
		Model:    "gemini-test",
		Messages: []ai.Message{{Role: ai.RoleUser, Content: "hello"}},
		Tools:    []ai.ToolSpec{{Name: "read", Description: "reads a file"}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var last ai.StreamEvent
	for ev := range events {
		last = ev
	}
	if last.Final == nil {
		t.Fatalf("the stream ended with %q and no message", last.Kind)
	}
	return last
}

func replay(t *testing.T, replies ...*http.Response) *gemini.Port {
	t.Helper()
	return newPort(t, &recordedTransport{responses: replies})
}

func completed(t *testing.T, reply *http.Response) ai.AssistantMessage {
	t.Helper()
	last := streamed(t, replay(t, reply))
	if last.Kind != ai.StreamDone {
		t.Fatalf("the stream ended with %q: %s", last.Kind, last.Final.ErrorMessage)
	}
	return *last.Final
}

func toolCalls(m ai.AssistantMessage) []ai.ToolCall {
	var calls []ai.ToolCall
	for _, b := range m.Blocks {
		if b.Kind == ai.BlockToolCall {
			calls = append(calls, b.Call)
		}
	}
	return calls
}

// TestAStreamedTextReplyCompletes with the counts of its last response.
func TestAStreamedTextReplyCompletes(t *testing.T) {
	got := completed(t, eventStream(textChunk("hello "), textChunk("there"), endChunk("STOP")))

	if got.StopReason != ai.StopEnd {
		t.Fatalf("stopped with %q", got.StopReason)
	}
	if len(got.Blocks) != 1 || got.Blocks[0].Kind != ai.BlockText || got.Blocks[0].Text != "hello there" {
		t.Fatalf("blocks: %+v", got.Blocks)
	}
	// The prompt count includes the cached part and the output count the thoughts.
	if !got.Usage.Reported || got.Usage.InputTokens != 8 || got.Usage.OutputTokens != 9 {
		t.Fatalf("usage: %+v", got.Usage)
	}
	if got.Usage.CacheReadTokens == nil || *got.Usage.CacheReadTokens != 4 {
		t.Fatalf("the cached count was lost: %+v", got.Usage)
	}
	if got.Usage.ReasoningTokens == nil || *got.Usage.ReasoningTokens != 2 {
		t.Fatalf("the thoughts count was lost: %+v", got.Usage)
	}
}

// TestAStreamedToolCallCompletes. This provider gives a call no position and says
// STOP even when the model asked for a tool, and the loop needs both.
func TestAStreamedToolCallCompletes(t *testing.T) {
	got := completed(t, eventStream(
		textChunk("reading"),
		callChunk(callPart("read", `{"path":"a"}`), callPart("read", `{"path":"b"}`)),
		callChunk(callPart("ls", `{"path":"c"}`)),
		endChunk("STOP")))

	if got.StopReason != ai.StopToolUse {
		t.Fatalf("stopped with %q", got.StopReason)
	}
	calls := toolCalls(got)
	if len(calls) != 3 {
		t.Fatalf("got %d tool calls: %+v", len(calls), got.Blocks)
	}
	seen := map[string]bool{}
	for at, want := range []ai.ToolCall{
		{Name: "read", Args: `{"path":"a"}`},
		{Name: "read", Args: `{"path":"b"}`},
		{Name: "ls", Args: `{"path":"c"}`},
	} {
		if calls[at].Name != want.Name || calls[at].Args != want.Args {
			t.Fatalf("call %d: got %+v, want %+v", at, calls[at], want)
		}
		if calls[at].ID == "" || seen[calls[at].ID] {
			t.Fatalf("call %d has no id of its own: %+v", at, calls)
		}
		seen[calls[at].ID] = true
	}
	if !got.Usage.Reported || got.Usage.OutputTokens != 9 {
		t.Fatalf("usage: %+v", got.Usage)
	}
}

// TestAToolCallSharingTheLastResponseCounts. The call and the ending often arrive
// together, and the ending must be read with that call already counted.
func TestAToolCallSharingTheLastResponseCounts(t *testing.T) {
	got := completed(t, eventStream(`{"candidates":[{"content":{"role":"model","parts":[`+
		callPart("read", `{"path":"a"}`)+`]},"finishReason":"STOP"}]}`))

	if got.StopReason != ai.StopToolUse || len(toolCalls(got)) != 1 {
		t.Fatalf("stopped with %q: %+v", got.StopReason, got.Blocks)
	}
}

// TestACallWithNoArgumentsIsAnEmptyObject, not the JSON null the conversion
// produces: a tool's arguments are an object, and null fails its schema.
func TestACallWithNoArgumentsIsAnEmptyObject(t *testing.T) {
	calls := toolCalls(completed(t, eventStream(
		callChunk(`{"functionCall":{"name":"ls"}}`), endChunk("STOP"))))

	if len(calls) != 1 || calls[0].Args != "{}" {
		t.Fatalf("got %+v", calls)
	}
}

// TestALengthCutoffIsNotReportedAsToolUse. A reply cut short by the output cap
// has to say so, even with a complete call in it.
func TestALengthCutoffIsNotReportedAsToolUse(t *testing.T) {
	got := completed(t, eventStream(callChunk(callPart("read", `{"path":"a"}`)), endChunk("MAX_TOKENS")))

	if got.StopReason != ai.StopLength {
		t.Fatalf("stopped with %q", got.StopReason)
	}
}

// TestEveryFilterEndingIsARefusal, not a finished reply, and keeps what arrived.
func TestEveryFilterEndingIsARefusal(t *testing.T) {
	for _, reason := range []string{
		"SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII",
		"IMAGE_SAFETY", "IMAGE_PROHIBITED_CONTENT", "IMAGE_RECITATION",
	} {
		last := streamed(t, replay(t, eventStream(textChunk("partial"), endChunk(reason))))
		if last.Kind != ai.StreamError {
			t.Fatalf("%s ended with %q", reason, last.Kind)
		}
		got, ok := ai.FailureOf(last.Final.Cause)
		if !ok || got != ai.FailureRefused {
			t.Errorf("%s classified as %q", reason, got)
		}
		if len(last.Final.Blocks) != 1 || last.Final.Blocks[0].Text != "partial" {
			t.Errorf("%s lost what had arrived: %+v", reason, last.Final.Blocks)
		}
	}
}

// TestAnEndingThatIsNotAnAnswerFails, naming itself. These are the provider's
// failures rather than its filters, or endings nobody here has seen, and none of
// them says the reply is whole. A malformed call stays a failure even after a call
// that was fine, since the ending must not be rewritten just because one arrived.
func TestAnEndingThatIsNotAnAnswerFails(t *testing.T) {
	for _, reason := range []string{
		"FINISH_REASON_UNSPECIFIED", "OTHER", "LANGUAGE", "MALFORMED_FUNCTION_CALL",
		"UNEXPECTED_TOOL_CALL", "NO_IMAGE", "AN_ENDING_FROM_THE_FUTURE",
	} {
		last := streamed(t, replay(t, eventStream(
			callChunk(callPart("read", `{"path":"a"}`)), endChunk(reason))))
		if last.Kind != ai.StreamError {
			t.Fatalf("%s ended with %q", reason, last.Kind)
		}
		if !strings.Contains(last.Final.ErrorMessage, reason) {
			t.Errorf("%s was not named: %s", reason, last.Final.ErrorMessage)
		}
	}
}

// TestAStreamThatNeverFinishesIsNotComplete. Taking the ending from the framework
// must not turn a dropped connection into a finished reply.
func TestAStreamThatNeverFinishesIsNotComplete(t *testing.T) {
	last := streamed(t, replay(t, eventStream(textChunk("partial"))))
	if last.Kind != ai.StreamError {
		t.Fatalf("a truncated reply ended with %q", last.Kind)
	}
}

// TestABlockedPromptIsNotComplete. A blocked prompt has no candidates and so no
// ending at all, and the adapter reports it as an error.
func TestABlockedPromptIsNotComplete(t *testing.T) {
	last := streamed(t, replay(t, eventStream(
		`{"promptFeedback":{"blockReason":"SAFETY"},"usageMetadata":{"promptTokenCount":12,"totalTokenCount":12}}`)))
	if last.Kind != ai.StreamError {
		t.Fatalf("a blocked prompt ended with %q", last.Kind)
	}
}
