package ollama_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/provider/ollama"
)

type recordedTransport struct {
	requests  int
	responses []*http.Response
	paths     []string
}

func (r *recordedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.requests++
	r.paths = append(r.paths, req.URL.Path)
	if r.requests > len(r.responses) {
		return nil, io.ErrUnexpectedEOF
	}
	return r.responses[r.requests-1], nil
}

func listed(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func refused(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func newPort(t *testing.T, tr http.RoundTripper) *ollama.Port {
	t.Helper()
	p, err := ollama.New(ollama.Config{Model: "llama-test", Transport: tr, MaxOutputTokens: 64})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

func ask(t *testing.T, p *ollama.Port) (ai.Response, error) {
	t.Helper()
	return p.Generate(context.Background(), ai.Request{
		Model:    "llama-test",
		Messages: []ai.Message{{Role: ai.RoleUser, Content: "hello"}},
	})
}

// TestNoCredentialIsNeeded. Every other provider here refuses to construct
// without one, and that check is right for them; a local server has none, and
// making the requirement optional would lose the check that catches a missing
// key where it matters.
func TestNoCredentialIsNeeded(t *testing.T) {
	if _, err := ollama.New(ollama.Config{
		Model: "llama-test", Transport: &recordedTransport{}, MaxOutputTokens: 8,
	}); err != nil {
		t.Fatalf("a local port refused to build without a credential: %v", err)
	}
}

// TestAModelThatWasNeverPulledSaysHowToFixIt.
//
// The one failure here a user resolves in a single command. Classified as a
// refusal rather than a server fault: retrying pulls nothing, and reading it as
// transient would have a caller wait for a model that will never appear.
func TestAModelThatWasNeverPulledSaysHowToFixIt(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{
		refused(404, `{"error":"model 'llama-test' not found"}`)}}

	_, err := ask(t, newPort(t, tr))
	if err == nil {
		t.Fatal("a missing model reported success")
	}
	got, ok := ai.FailureOf(err)
	if !ok {
		t.Fatalf("the failure carries no classification: %v", err)
	}
	if got != ai.FailureRefused {
		t.Fatalf("a missing model was classified as %q", got)
	}
	if ai.Retryable(err) {
		t.Fatalf("a missing model was reported as worth retrying: %v", err)
	}
	if !strings.Contains(err.Error(), "ollama pull") {
		t.Fatalf("the failure does not say how to fix it: %v", err)
	}
}

// TestALocalServerThatIsNotRunningFailsAsATransport, not as a refusal: nothing
// refused anything, and telling a user their request was rejected sends them to
// look at the request.
func TestALocalServerThatIsNotRunningFailsAsATransport(t *testing.T) {
	tr := &recordedTransport{} // no responses: the connection fails
	_, err := ask(t, newPort(t, tr))
	if err == nil {
		t.Fatal("an unreachable server reported success")
	}
	if got, ok := ai.FailureOf(err); ok && got == ai.FailureRefused {
		t.Fatalf("an unreachable server was reported as a refusal: %v", err)
	}
}

// TestAServerFaultIsWorthRetrying, unlike a missing model.
func TestAServerFaultIsWorthRetrying(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{
		refused(500, `{"error":"internal error"}`)}}
	_, err := ask(t, newPort(t, tr))
	if err == nil {
		t.Fatal("expected a failure")
	}
	got, ok := ai.FailureOf(err)
	if !ok || got != ai.FailureTransient {
		t.Fatalf("a server fault classified as %q (ok=%v)", got, ok)
	}
}

// TestServingOnlyTheConfiguredModel. Loading a model takes seconds to minutes,
// so answering from a different one than the caller configured is expensive as
// well as wrong.
func TestServingOnlyTheConfiguredModel(t *testing.T) {
	tr := &recordedTransport{}
	p := newPort(t, tr)
	_, err := p.Generate(context.Background(), ai.Request{
		Model:    "some-other-model",
		Messages: []ai.Message{{Role: ai.RoleUser, Content: "hello"}},
	})
	if err == nil {
		t.Fatal("a request naming another model was served")
	}
	if tr.requests != 0 {
		t.Fatalf("a refused request still reached the server %d times", tr.requests)
	}
}

// TestAConfigurationThatCouldNotWorkIsRefusedAtConstruction.
func TestAConfigurationThatCouldNotWorkIsRefusedAtConstruction(t *testing.T) {
	tr := &recordedTransport{}
	for name, cfg := range map[string]ollama.Config{
		"no model":        {Transport: tr, MaxOutputTokens: 8},
		"no transport":    {Model: "m", MaxOutputTokens: 8},
		"no output cap":   {Model: "m", Transport: tr},
		"negative window": {Model: "m", Transport: tr, MaxOutputTokens: 8, ContextWindow: -1},
	} {
		if _, err := ollama.New(cfg); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// TestListingReturnsOnlyTheModelsOllamaReports. The provider's tags endpoint
// is the source of identifiers; duplicate and blank records are not useful to
// a selector, and the stable order makes cycling deterministic between calls.
func TestListingReturnsOnlyTheModelsOllamaReports(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{
		listed(`{"models":[{"name":"zeta:latest"},{"name":""},{"name":"alpha"},{"name":"alpha"}]}`),
	}}
	models, err := newPort(t, tr).ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 2 || models[0].Provider != "ollama" || models[0].ID != "alpha" || models[1].ID != "zeta:latest" {
		t.Fatalf("unexpected model listing: %+v", models)
	}
	if tr.requests != 1 || len(tr.paths) != 1 || tr.paths[0] != "/api/tags" {
		t.Fatalf("listing sent %d requests to %v; want one GET /api/tags", tr.requests, tr.paths)
	}
}

// TestAnEmptyListingIsDifferentFromAnUnsupportedProvider. A running Ollama
// server with no pulled models answered the listing successfully; callers must
// preserve that empty result instead of converting it into a provider error.
func TestAnEmptyListingIsDifferentFromAnUnsupportedProvider(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{listed(`{"models":[]}`)}}
	models, err := newPort(t, tr).ListModels(context.Background())
	if err != nil {
		t.Fatalf("an empty listing failed: %v", err)
	}
	if models == nil || len(models) != 0 {
		t.Fatalf("empty Ollama listing became %#v", models)
	}
}

// TestAPromptThatDidNotFitIsReportedAsOverflow, not as an ordinary refusal.
//
// It is the one failure this repository can recover from — by shortening the
// conversation and asking again — so reading it as a generic 400 gives up on
// the only case worth not giving up on. The message is pi's recorded one
// (packages/ai/test/overflow.test.ts:33 at the pin).
func TestAPromptThatDidNotFitIsReportedAsOverflow(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{refused(400,
		`{"error":"prompt too long; exceeded max context length by 100918 tokens"}`)}}

	_, err := ask(t, newPort(t, tr))
	if err == nil {
		t.Fatal("an oversized prompt reported success")
	}
	if !errors.Is(err, ai.ErrContextOverflow) {
		t.Fatalf("an oversized prompt was not reported as an overflow: %v", err)
	}
}

// TestAnOrdinaryFaultIsNotMistakenForOverflow. A detector that fires on any
// failure would have a caller shorten a conversation that was never too long,
// paying for a second request to fix a problem it does not have. pi's own
// negative case: packages/ai/test/overflow.test.ts:78.
func TestAnOrdinaryFaultIsNotMistakenForOverflow(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{refused(500,
		`{"error":"model runner crashed unexpectedly"}`)}}

	_, err := ask(t, newPort(t, tr))
	if err == nil {
		t.Fatal("expected a failure")
	}
	if errors.Is(err, ai.ErrContextOverflow) {
		t.Fatalf("a crashed runner was reported as an overflow: %v", err)
	}
}

// ndjson is a streamed reply as Ollama sends it: one JSON object per line, with
// no event framing around it.
func ndjson(lines ...string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/x-ndjson"}},
		Body:       io.NopCloser(strings.NewReader(strings.Join(lines, "\n") + "\n")),
	}
}

const finished = `{"message":{"role":"assistant","content":""},"done":true,"done_reason":"stop","prompt_eval_count":7,"eval_count":3}`

func toolCallLine(name, args string) string {
	return `{"message":{"role":"assistant","content":"","tool_calls":[{"function":{"index":0,"name":"` +
		name + `","arguments":` + args + `}}]},"done":false}`
}

// final drains a stream down to the message it finished with.
func final(t *testing.T, p *ollama.Port) ai.AssistantMessage {
	t.Helper()
	events, err := p.Stream(context.Background(), ai.Request{
		Model:    "llama-test",
		Messages: []ai.Message{{Role: ai.RoleUser, Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var last ai.StreamEvent
	for ev := range events {
		last = ev
	}
	if last.Kind != ai.StreamDone || last.Final == nil {
		t.Fatalf("the stream ended with %q: %+v", last.Kind, last.Final)
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

// TestAToolCallArrivesAsACompleteBlock. Ollama's reply gives a call no position
// and no id, and says "stop" even when the model asked for a tool; all three
// are what the rest of this repository needs in order to run the call and pair
// its result.
func TestAToolCallArrivesAsACompleteBlock(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{ndjson(
		toolCallLine("read", `{"path":"README.md"}`), finished)}}

	got := final(t, newPort(t, tr))

	calls := toolCalls(got)
	if len(calls) != 1 {
		t.Fatalf("got %d tool calls: %+v", len(calls), got.Blocks)
	}
	if calls[0].Name != "read" || calls[0].Args != `{"path":"README.md"}` {
		t.Fatalf("the call did not survive: %+v", calls[0])
	}
	if calls[0].ID == "" {
		t.Fatal("the call has no id for its result to pair with")
	}
	if got.StopReason != ai.StopToolUse {
		t.Fatalf("a reply calling a tool stopped with %q", got.StopReason)
	}
	if !got.Usage.Reported || got.Usage.InputTokens != 7 || got.Usage.OutputTokens != 3 {
		t.Fatalf("the reply's counts were lost: %+v", got.Usage)
	}
}

// TestSeparateCallsStaySeparate, whether they share a chunk or not. A shared
// position would append one call's arguments to another's, and a shared id
// would pair a result with the wrong call — including a call from an earlier
// reply, since the ids live on in the conversation.
func TestSeparateCallsStaySeparate(t *testing.T) {
	sameChunk := `{"message":{"role":"assistant","content":"","tool_calls":[` +
		`{"function":{"index":0,"name":"read","arguments":{"path":"a"}}},` +
		`{"function":{"index":1,"name":"read","arguments":{"path":"b"}}}]},"done":false}`
	reply := func() *http.Response {
		return ndjson(sameChunk, toolCallLine("ls", `{"path":"c"}`), finished)
	}
	tr := &recordedTransport{responses: []*http.Response{reply(), reply()}}
	p := newPort(t, tr)

	first, second := toolCalls(final(t, p)), toolCalls(final(t, p))

	if len(first) != 3 {
		t.Fatalf("got %d tool calls: %+v", len(first), first)
	}
	for at, want := range []string{`{"path":"a"}`, `{"path":"b"}`, `{"path":"c"}`} {
		if first[at].Args != want {
			t.Fatalf("call %d carries %q, want %q", at, first[at].Args, want)
		}
	}
	seen := map[string]bool{}
	for _, call := range append(first, second...) {
		if seen[call.ID] {
			t.Fatalf("id %q was used twice", call.ID)
		}
		seen[call.ID] = true
	}
}

// TestACallWithNoArgumentsIsAnEmptyObject, not the JSON null the conversion
// produces: a tool's arguments are an object, and null fails its schema.
func TestACallWithNoArgumentsIsAnEmptyObject(t *testing.T) {
	line := `{"message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"ls"}}]},"done":false}`
	tr := &recordedTransport{responses: []*http.Response{ndjson(line, finished)}}

	calls := toolCalls(final(t, newPort(t, tr)))
	if len(calls) != 1 || calls[0].Args != "{}" {
		t.Fatalf("got %+v", calls)
	}
}

// TestAPlainReplyEndsAsTheServerSaid. The finish reason is only rewritten for a
// reply that called a tool; a reply that did not must still read as finished.
func TestAPlainReplyEndsAsTheServerSaid(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{ndjson(
		`{"message":{"role":"assistant","content":"hi"},"done":false}`, finished)}}

	got := final(t, newPort(t, tr))
	if got.StopReason != ai.StopEnd {
		t.Fatalf("a plain reply stopped with %q", got.StopReason)
	}
	if len(toolCalls(got)) != 0 {
		t.Fatalf("a plain reply gained tool calls: %+v", got.Blocks)
	}
}

// TestALengthCutoffIsNotReportedAsToolUse. A reply cut short by the output cap
// has to say so, even with a complete call in it.
func TestALengthCutoffIsNotReportedAsToolUse(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{ndjson(
		toolCallLine("read", `{"path":"a"}`),
		`{"message":{"role":"assistant","content":""},"done":true,"done_reason":"length"}`)}}

	if got := final(t, newPort(t, tr)); got.StopReason != ai.StopLength {
		t.Fatalf("a cut-off reply stopped with %q", got.StopReason)
	}
}

// TestAStreamThatNeverFinishesIsNotReportedAsComplete. Taking the ending from
// the framework must not turn a dropped connection into a finished reply.
func TestAStreamThatNeverFinishesIsNotReportedAsComplete(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{ndjson(
		`{"message":{"role":"assistant","content":"partial"},"done":false}`)}}

	events, err := newPort(t, tr).Stream(context.Background(), ai.Request{
		Model:    "llama-test",
		Messages: []ai.Message{{Role: ai.RoleUser, Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var last ai.StreamEvent
	for ev := range events {
		last = ev
	}
	if last.Kind == ai.StreamDone {
		t.Fatalf("a truncated reply was reported as complete: %+v", last.Final)
	}
	if last.Final != nil && last.Final.Usage.Reported {
		t.Fatalf("placeholder zeroes were reported as counts: %+v", last.Final.Usage)
	}
}
