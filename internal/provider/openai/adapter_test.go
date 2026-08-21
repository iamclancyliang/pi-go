package openai_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/provider/openai"
)

// recordedTransport replays a recorded exchange and counts requests.
type recordedTransport struct {
	requests  int
	responses []*http.Response
	sent      []string
}

func (r *recordedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		body, _ := io.ReadAll(req.Body)
		r.sent = append(r.sent, string(body))
	}
	r.requests++
	if r.requests > len(r.responses) {
		return nil, io.ErrUnexpectedEOF
	}
	return r.responses[r.requests-1], nil
}

var fixedKey = ai.StoredCredential("test-key", "a test")

func recorded(events ...string) *http.Response {
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

func newPort(t *testing.T, tr http.RoundTripper) *openai.Port {
	t.Helper()
	p, err := openai.New(openai.Config{
		Model: "gpt-test", Transport: tr, Credential: fixedKey, MaxOutputTokens: 64,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

// TestAReplyArrivesThroughTheAdapter drives the real eino-ext adapter over a
// recorded exchange: no network, and the request count proves the transport was
// the one used.
func TestAReplyArrivesThroughTheAdapter(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{recorded(
		`{"type":"response.created","response":{"id":"resp_1","model":"gpt-test-served","status":"in_progress"}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","status":"in_progress","content":[]}}`,
		`{"type":"response.content_part.added","item_id":"msg_1","output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}`,
		`{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"the answer"}`,
		`{"type":"response.output_text.done","item_id":"msg_1","output_index":0,"content_index":0,"text":"the answer"}`,
		`{"type":"response.content_part.done","item_id":"msg_1","output_index":0,"content_index":0,"part":{"type":"output_text","text":"the answer"}}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"the answer"}]}}`,
		`{"type":"response.completed","response":{"id":"resp_1","model":"gpt-test-served","status":"completed","usage":{"input_tokens":11,"output_tokens":2,"input_tokens_details":{"cached_tokens":4}}}}`,
	)}}
	p := newPort(t, tr)

	resp, err := p.Generate(context.Background(), ai.Request{
		Model:    "gpt-test",
		Messages: []ai.Message{{Role: ai.RoleUser, Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if tr.requests != 1 {
		t.Fatalf("sent %d requests, want exactly 1", tr.requests)
	}
	if resp.Content != "the answer" {
		t.Fatalf("content %q", resp.Content)
	}
	// From the capture, not the adapter's flattened metadata.
	if resp.Model != "gpt-test-served" {
		t.Fatalf("served model %q; a substitution would be invisible", resp.Model)
	}
	if resp.Usage.InputTokens != 7 {
		t.Fatalf("uncached input %d, want 7 (11 reported, 4 cached)", resp.Usage.InputTokens)
	}
	if resp.Usage.CacheReadTokens == nil || *resp.Usage.CacheReadTokens != 4 {
		t.Fatalf("cache read %v", resp.Usage.CacheReadTokens)
	}
	if resp.Usage.ReasoningTokens != nil {
		t.Fatalf("a count the provider never sent became %d", *resp.Usage.ReasoningTokens)
	}
	if resp.Usage.Total() != 13 {
		t.Fatalf("total %d, want 13", resp.Usage.Total())
	}
}

// TestThisPortRefusesAConfigurationItCannotHonour: each seam is required, so a
// test cannot reach the network or a real credential by omission, and a request
// cannot be built without an output cap.
func TestThisPortRefusesAConfigurationItCannotHonour(t *testing.T) {
	base := openai.Config{
		Model: "m", Transport: &recordedTransport{}, Credential: fixedKey, MaxOutputTokens: 8,
	}
	for _, tc := range []struct {
		name   string
		mutate func(*openai.Config)
	}{
		{"no model", func(c *openai.Config) { c.Model = "" }},
		{"no transport", func(c *openai.Config) { c.Transport = nil }},
		{"no credential", func(c *openai.Config) { c.Credential = ai.Credential{} }},
		{"no output cap", func(c *openai.Config) { c.MaxOutputTokens = 0 }},
		{"negative output cap", func(c *openai.Config) { c.MaxOutputTokens = -1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			tc.mutate(&cfg)
			if _, err := openai.New(cfg); err == nil {
				t.Fatalf("a port was built with %s", tc.name)
			}
		})
	}
}

// TestAnUnsupportedBlockIsReportedNotDropped.
//
// This package handles text, reasoning and function tool calls. A block of any
// other kind is content the rest of this repository has no contract for, and
// dropping it silently would hand back a reply that is missing something nobody
// mentioned.
func TestAnUnsupportedBlockIsReportedNotDropped(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{recorded(
		`{"type":"response.created","response":{"id":"resp_1","model":"gpt-test","status":"in_progress"}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"img_1","type":"image_generation_call","status":"in_progress"}}`,
		`{"type":"response.image_generation_call.partial_image","item_id":"img_1","output_index":0,"partial_image_index":0,"partial_image_b64":"iVBORw0KGgo="}`,
		`{"type":"response.completed","response":{"id":"resp_1","model":"gpt-test","status":"completed","usage":{"input_tokens":3,"output_tokens":1}}}`,
	)}}
	p := newPort(t, tr)

	_, err := p.Generate(context.Background(), ai.Request{
		Model:    "gpt-test",
		Messages: []ai.Message{{Role: ai.RoleUser, Content: "draw"}},
	})
	if err == nil {
		t.Fatal("a reply carrying an unsupported block was returned as though complete")
	}
	if !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("the failure did not say what it could not handle: %v", err)
	}
}

// TestTwoInterleavedToolCallsKeepTheirIdentity.
//
// This is the control that disqualified the DeepSeek adapter: it turned a valid
// interleaved stream into four calls whose continuations had lost their ID and
// name. A provider may stream several calls at once, so an adapter that tracks
// only the most recent index cannot carry them.
func TestTwoInterleavedToolCallsKeepTheirIdentity(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{recorded(
		`{"type":"response.created","response":{"id":"resp_1","model":"gpt-test","status":"in_progress"}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"fc_1","type":"function_call","call_id":"call_a","name":"first","arguments":""}}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"id":"fc_2","type":"function_call","call_id":"call_b","name":"second","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"delta":"{\"a\""}`,
		`{"type":"response.function_call_arguments.delta","item_id":"fc_2","output_index":1,"delta":"{\"b\""}`,
		`{"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"delta":":1}"}`,
		`{"type":"response.function_call_arguments.delta","item_id":"fc_2","output_index":1,"delta":":2}"}`,
		`{"type":"response.function_call_arguments.done","item_id":"fc_1","output_index":0,"arguments":"{\"a\":1}"}`,
		`{"type":"response.function_call_arguments.done","item_id":"fc_2","output_index":1,"arguments":"{\"b\":2}"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"fc_1","type":"function_call","call_id":"call_a","name":"first","arguments":"{\"a\":1}","status":"completed"}}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"id":"fc_2","type":"function_call","call_id":"call_b","name":"second","arguments":"{\"b\":2}","status":"completed"}}`,
		`{"type":"response.completed","response":{"id":"resp_1","model":"gpt-test","status":"completed","usage":{"input_tokens":5,"output_tokens":4}}}`,
	)}}
	p := newPort(t, tr)

	resp, err := p.Generate(context.Background(), ai.Request{
		Model:    "gpt-test",
		Messages: []ai.Message{{Role: ai.RoleUser, Content: "call two things"}},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(resp.ToolCalls) != 2 {
		t.Fatalf("two interleaved calls became %d: %+v", len(resp.ToolCalls), resp.ToolCalls)
	}
	// Source order, and identity on both.
	if resp.ToolCalls[0].ID != "call_a" || resp.ToolCalls[0].Name != "first" {
		t.Fatalf("first call arrived as %+v", resp.ToolCalls[0])
	}
	if resp.ToolCalls[1].ID != "call_b" || resp.ToolCalls[1].Name != "second" {
		t.Fatalf("second call arrived as %+v", resp.ToolCalls[1])
	}
	for i, want := range []string{`{"a":1}`, `{"b":2}`} {
		if resp.ToolCalls[i].Args != want {
			t.Fatalf("call %d reassembled as %q, want %q", i, resp.ToolCalls[i].Args, want)
		}
	}
}

// TestStreamAndGenerateAgreeOnTheSameReply: the single reply is the stream
// collected, so the two must not be able to disagree. Driving both from the
// same recording is what makes that checkable rather than assumed.
func TestStreamAndGenerateAgreeOnTheSameReply(t *testing.T) {
	events := []string{
		`{"type":"response.created","response":{"id":"r","model":"gpt-served","status":"in_progress"}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"m","type":"message","role":"assistant","status":"in_progress","content":[]}}`,
		`{"type":"response.content_part.added","item_id":"m","output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}`,
		`{"type":"response.output_text.delta","item_id":"m","output_index":0,"content_index":0,"delta":"one "}`,
		`{"type":"response.output_text.delta","item_id":"m","output_index":0,"content_index":0,"delta":"two"}`,
		`{"type":"response.output_text.done","item_id":"m","output_index":0,"content_index":0,"text":"one two"}`,
		`{"type":"response.content_part.done","item_id":"m","output_index":0,"content_index":0,"part":{"type":"output_text","text":"one two"}}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"m","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"one two"}]}}`,
		`{"type":"response.completed","response":{"id":"r","model":"gpt-served","status":"completed","usage":{"input_tokens":9,"output_tokens":3,"output_tokens_details":{"reasoning_tokens":0}}}}`,
	}
	req := ai.Request{Model: "gpt-test", Messages: []ai.Message{{Role: ai.RoleUser, Content: "hi"}}}

	collected, err := newPort(t, &recordedTransport{responses: []*http.Response{recorded(events...)}}).
		Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	stream, err := newPort(t, &recordedTransport{responses: []*http.Response{recorded(events...)}}).
		Stream(context.Background(), req)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var final *ai.AssistantMessage
	terminals := 0
	for ev := range stream {
		if ev.Final != nil {
			final = ev.Final
			terminals++
		}
	}
	if terminals != 1 {
		t.Fatalf("the stream produced %d terminal events, want exactly 1", terminals)
	}

	var text string
	for _, b := range final.Blocks {
		if b.Kind == ai.BlockText {
			text += b.Text
		}
	}
	if text != collected.Content {
		t.Fatalf("streamed %q, collected %q", text, collected.Content)
	}
	if final.Model != collected.Model {
		t.Fatalf("streamed model %q, collected %q", final.Model, collected.Model)
	}
	if final.Usage.Total() != collected.Usage.Total() {
		t.Fatalf("streamed total %d, collected %d", final.Usage.Total(), collected.Usage.Total())
	}
	// A reported zero must survive both paths as a reported zero.
	if final.Usage.ReasoningTokens == nil || *final.Usage.ReasoningTokens != 0 {
		t.Fatalf("a reported zero became %v on the streamed path", final.Usage.ReasoningTokens)
	}
	if collected.Usage.ReasoningTokens == nil || *collected.Usage.ReasoningTokens != 0 {
		t.Fatalf("a reported zero became %v on the collected path", collected.Usage.ReasoningTokens)
	}
}

// TestAServedModelIsAbsentWhenTheProviderDidNotSayWhich: reporting the model
// that was asked for would make a substitution invisible, and inventing one is
// worse than admitting the reply did not say.
func TestAServedModelIsAbsentWhenTheProviderDidNotSayWhich(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{recorded(
		`{"type":"response.created","response":{"id":"r","status":"in_progress"}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"m","type":"message","role":"assistant","status":"in_progress","content":[]}}`,
		`{"type":"response.content_part.added","item_id":"m","output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}`,
		`{"type":"response.output_text.delta","item_id":"m","output_index":0,"content_index":0,"delta":"x"}`,
		`{"type":"response.output_text.done","item_id":"m","output_index":0,"content_index":0,"text":"x"}`,
		`{"type":"response.content_part.done","item_id":"m","output_index":0,"content_index":0,"part":{"type":"output_text","text":"x"}}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"m","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"x"}]}}`,
		// Completed, with no model named.
		`{"type":"response.completed","response":{"id":"r","status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}`,
	)}}

	resp, err := newPort(t, tr).Generate(context.Background(), ai.Request{
		Model: "gpt-test", Messages: []ai.Message{{Role: ai.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	// Empty, not "some other name": the only honest answer when the provider
	// did not say is that it did not say. Asserting merely that it differs from
	// the requested model would pass while the CONFIGURED model was reported —
	// which is just as much a name nobody confirmed. Both are "gpt-test" here,
	// so only emptiness distinguishes a captured truth from an echo.
	if resp.Model != "" {
		t.Fatalf("the reply named no model, but %q was reported as having served it", resp.Model)
	}
}

// TestAFailedReplyStillReportsWhatItRead: a reply the provider abandoned had
// its request read, and a ledger that dropped those counts would be optimistic
// about exactly the calls that went wrong.
func TestAFailedReplyStillReportsWhatItRead(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{recorded(
		`{"type":"response.created","response":{"id":"r","model":"gpt-served","status":"in_progress"}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"m","type":"message","role":"assistant","status":"in_progress","content":[]}}`,
		`{"type":"response.content_part.added","item_id":"m","output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}`,
		`{"type":"response.output_text.delta","item_id":"m","output_index":0,"content_index":0,"delta":"partial"}`,
		`{"type":"response.failed","response":{"id":"r","model":"gpt-served","status":"failed","usage":{"input_tokens":42,"output_tokens":3}}}`,
	)}}
	p := newPort(t, tr)

	_, err := p.Generate(context.Background(), ai.Request{
		Model: "gpt-test", Messages: []ai.Message{{Role: ai.RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("a failed reply was returned as an answer")
	}
	var reporter ai.UsageReporter
	if !errors.As(err, &reporter) {
		t.Fatalf("the failure carries no usage: %v", err)
	}
	var input int
	for _, u := range reporter.Consumed() {
		input += u.InputTokens
	}
	if input != 42 {
		t.Fatalf("a failed call reported %d input tokens, want 42", input)
	}
	if tr.requests != 1 {
		t.Fatalf("sent %d requests", tr.requests)
	}
}

// TestBlocksArriveInOrderAndTheStreamEndsOnce.
//
// A consumer rendering as it goes needs a block finished before another begins,
// and exactly one ending. Tool-call blocks are the one exception and stay open
// for each other, because a provider may interleave their fragments.
func TestBlocksArriveInOrderAndTheStreamEndsOnce(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{recorded(
		`{"type":"response.created","response":{"id":"r","model":"gpt-served","status":"in_progress"}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"rs","type":"reasoning","summary":[]}}`,
		`{"type":"response.reasoning_summary_part.added","item_id":"rs","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":""}}`,
		`{"type":"response.reasoning_summary_text.delta","item_id":"rs","output_index":0,"summary_index":0,"delta":"thinking"}`,
		`{"type":"response.reasoning_summary_text.done","item_id":"rs","output_index":0,"summary_index":0,"text":"thinking"}`,
		`{"type":"response.reasoning_summary_part.done","item_id":"rs","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":"thinking"}}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"rs","type":"reasoning","summary":[{"type":"summary_text","text":"thinking"}]}}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"id":"m","type":"message","role":"assistant","status":"in_progress","content":[]}}`,
		`{"type":"response.content_part.added","item_id":"m","output_index":1,"content_index":0,"part":{"type":"output_text","text":""}}`,
		`{"type":"response.output_text.delta","item_id":"m","output_index":1,"content_index":0,"delta":"answer"}`,
		`{"type":"response.output_text.done","item_id":"m","output_index":1,"content_index":0,"text":"answer"}`,
		`{"type":"response.content_part.done","item_id":"m","output_index":1,"content_index":0,"part":{"type":"output_text","text":"answer"}}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"id":"m","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"answer"}]}}`,
		`{"type":"response.completed","response":{"id":"r","model":"gpt-served","status":"completed","usage":{"input_tokens":4,"output_tokens":2}}}`,
	)}}
	p := newPort(t, tr)

	stream, err := p.Stream(context.Background(), ai.Request{
		Model: "gpt-test", Messages: []ai.Message{{Role: ai.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	open := map[int]bool{}
	terminals := 0
	for ev := range stream {
		switch ev.Kind {
		case ai.StreamTextStart, ai.StreamThinkingStart, ai.StreamToolCallStart:
			for at, still := range open {
				if still {
					t.Fatalf("block %d began while block %d was still open", ev.ContentIndex, at)
				}
			}
			open[ev.ContentIndex] = true
		case ai.StreamTextEnd, ai.StreamThinkingEnd, ai.StreamToolCallEnd:
			open[ev.ContentIndex] = false
		case ai.StreamDone, ai.StreamError:
			terminals++
		}
	}
	if terminals != 1 {
		t.Fatalf("the stream produced %d terminal events, want exactly 1", terminals)
	}
}

// TestACancelledStreamStillEnds: a consumer that watched a reply arrive keeps
// what arrived and is told it was aborted, rather than left with a channel that
// simply closed.
func TestACancelledStreamStillEnds(t *testing.T) {
	events := []string{
		`{"type":"response.created","response":{"id":"r","model":"gpt-served","status":"in_progress"}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"m","type":"message","role":"assistant","status":"in_progress","content":[]}}`,
		`{"type":"response.content_part.added","item_id":"m","output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}`,
	}
	for i := 0; i < 400; i++ {
		events = append(events,
			`{"type":"response.output_text.delta","item_id":"m","output_index":0,"content_index":0,"delta":"x"}`)
	}
	tr := &recordedTransport{responses: []*http.Response{recorded(events...)}}
	p := newPort(t, tr)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := p.Stream(ctx, ai.Request{
		Model: "gpt-test", Messages: []ai.Message{{Role: ai.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	var final *ai.AssistantMessage
	read := 0
	for ev := range stream {
		read++
		if read == 3 {
			cancel()
		}
		if ev.Final != nil {
			final = ev.Final
		}
	}
	if final == nil {
		t.Fatal("a cancelled stream closed with no terminal, so a consumer cannot tell " +
			"an abort from a completed reply")
	}
	if final.StopReason != ai.StopAborted {
		t.Fatalf("terminal reason %v, want %v", final.StopReason, ai.StopAborted)
	}
}

// TestTheRequestActuallyCarriesTheCapAndTheTools.
//
// Requiring an output cap at construction says nothing about what was sent, and
// a reply with no cap is a bill nobody chose. Tools omitted from the request
// leave the model unable to ask for anything, which is indistinguishable from a
// model that chose not to. Both are asserted on the bytes that went out.
func TestTheRequestActuallyCarriesTheCapAndTheTools(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{recorded(
		`{"type":"response.created","response":{"id":"r","model":"gpt-served","status":"in_progress"}}`,
		`{"type":"response.completed","response":{"id":"r","model":"gpt-served","status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}`,
	)}}
	p := newPort(t, tr)

	if _, err := p.Generate(context.Background(), ai.Request{
		Model:    "gpt-test",
		Messages: []ai.Message{{Role: ai.RoleUser, Content: "hi"}},
		Tools:    []ai.ToolSpec{{Name: "list_files", Description: "list the files"}},
	}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(tr.sent) == 0 {
		t.Fatal("nothing was sent")
	}
	body := tr.sent[0]
	for _, want := range []string{`"max_output_tokens":64`, `"list_files"`, `"list the files"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the request body is missing %s\n%s", want, body)
		}
	}
}

// TestABlockIndexThatSkipsIsRefused: renumbering a stream to look contiguous
// hides a malformed one instead of reporting it, and the reply would then claim
// an order the provider never sent.
func TestABlockIndexThatSkipsIsRefused(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{recorded(
		`{"type":"response.created","response":{"id":"r","model":"gpt-served","status":"in_progress"}}`,
		// The provider's first item is at index 3, with nothing before it.
		`{"type":"response.output_item.added","output_index":3,"item":{"id":"m","type":"message","role":"assistant","status":"in_progress","content":[]}}`,
		`{"type":"response.content_part.added","item_id":"m","output_index":3,"content_index":0,"part":{"type":"output_text","text":""}}`,
		`{"type":"response.output_text.delta","item_id":"m","output_index":3,"content_index":0,"delta":"x"}`,
		`{"type":"response.completed","response":{"id":"r","model":"gpt-served","status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}`,
	)}}
	p := newPort(t, tr)

	_, err := p.Generate(context.Background(), ai.Request{
		Model: "gpt-test", Messages: []ai.Message{{Role: ai.RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("a stream whose first block was index 3 was renumbered and accepted")
	}
	if !strings.Contains(err.Error(), "renumber") {
		t.Fatalf("the failure did not explain itself: %v", err)
	}
}

// TestTextDoesNotBeginWhileAToolCallIsOpen: tool-call blocks stay open only for
// each other, since a provider may interleave their fragments. Any other kind
// beginning means the earlier blocks are finished.
func TestTextDoesNotBeginWhileAToolCallIsOpen(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{recorded(
		`{"type":"response.created","response":{"id":"r","model":"gpt-served","status":"in_progress"}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"fc","type":"function_call","call_id":"c1","name":"list_files","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","item_id":"fc","output_index":0,"delta":"{}"}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"id":"m","type":"message","role":"assistant","status":"in_progress","content":[]}}`,
		`{"type":"response.content_part.added","item_id":"m","output_index":1,"content_index":0,"part":{"type":"output_text","text":""}}`,
		`{"type":"response.output_text.delta","item_id":"m","output_index":1,"content_index":0,"delta":"after"}`,
		`{"type":"response.completed","response":{"id":"r","model":"gpt-served","status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}`,
	)}}
	p := newPort(t, tr)

	stream, err := p.Stream(context.Background(), ai.Request{
		Model: "gpt-test", Messages: []ai.Message{{Role: ai.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	open := map[int]bool{}
	for ev := range stream {
		switch ev.Kind {
		case ai.StreamTextStart, ai.StreamThinkingStart, ai.StreamToolCallStart:
			for at, still := range open {
				if still {
					t.Fatalf("block %d began while block %d was still open", ev.ContentIndex, at)
				}
			}
			open[ev.ContentIndex] = true
		case ai.StreamTextEnd, ai.StreamThinkingEnd, ai.StreamToolCallEnd:
			open[ev.ContentIndex] = false
		}
	}
}

func refusal(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// TestFailuresAreClassifiedByValue: nothing downstream reads an error's text, so
// a provider that rewords a message cannot change what this repository does —
// and for a billing failure that difference is money.
func TestFailuresAreClassifiedByValue(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		want      openai.Failure
		retryable bool
	}{
		{
			name: "exhausted quota", status: 429,
			body: `{"error":{"type":"insufficient_quota","code":"insufficient_quota","message":"you are out"}}`,
			want: openai.FailureQuota,
		},
		{
			name: "ordinary throttle", status: 429,
			body:      `{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"slow down"}}`,
			want:      openai.FailureThrottled,
			retryable: true,
		},
		{
			// The code alone, with no matching type: the provider does not
			// always send both, and reading only the type would let an
			// exhausted balance through as something retryable.
			name: "exhausted quota reported by code alone", status: 429,
			body: `{"error":{"type":"rate_limit_error","code":"billing_hard_limit_reached","message":"limit"}}`,
			want: openai.FailureQuota,
		},
		{
			name: "rejected credential", status: 401,
			body: `{"error":{"type":"invalid_request_error","code":"invalid_api_key","message":"bad key"}}`,
			want: openai.FailureAuth,
		},
		{
			name: "refused request", status: 400,
			body: `{"error":{"type":"invalid_request_error","code":"unknown_parameter","message":"no"}}`,
			want: openai.FailureRefused,
		},
		{
			name: "server failure", status: 503,
			body:      `{"error":{"type":"server_error","message":"busy"}}`,
			want:      openai.FailureTransient,
			retryable: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := &recordedTransport{responses: []*http.Response{refusal(tc.status, tc.body)}}
			_, err := newPort(t, tr).Generate(context.Background(), ai.Request{
				Model: "gpt-test", Messages: []ai.Message{{Role: ai.RoleUser, Content: "hi"}},
			})
			var classified *openai.Error
			if !errors.As(err, &classified) {
				t.Fatalf("failure %v is not classified", err)
			}
			if classified.Failure != tc.want {
				t.Fatalf("classified %s, want %s", classified.Failure, tc.want)
			}
			if classified.Failure.Retryable() != tc.retryable {
				t.Fatalf("retryable %v, want %v", classified.Failure.Retryable(), tc.retryable)
			}
		})
	}
}

// TestQuotaAndThrottleShareAStatusAndDiffer: both are 429, and only one is worth
// retrying. Reading the status alone cannot tell them apart.
func TestQuotaAndThrottleShareAStatusAndDiffer(t *testing.T) {
	quota := `{"error":{"type":"insufficient_quota","code":"insufficient_quota","message":"gone"}}`
	throttle := `{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"slow"}}`

	var got []openai.Failure
	for _, body := range []string{quota, throttle} {
		tr := &recordedTransport{responses: []*http.Response{refusal(429, body)}}
		_, err := newPort(t, tr).Generate(context.Background(), ai.Request{
			Model: "gpt-test", Messages: []ai.Message{{Role: ai.RoleUser, Content: "hi"}},
		})
		var classified *openai.Error
		if !errors.As(err, &classified) {
			t.Fatalf("failure %v is not classified", err)
		}
		got = append(got, classified.Failure)
	}
	if got[0] == got[1] {
		t.Fatalf("both 429s classified as %s; the status cannot be what separates them", got[0])
	}
	if got[0].Retryable() {
		t.Fatal("an exhausted quota was marked retryable")
	}
	if !got[1].Retryable() {
		t.Fatal("an ordinary throttle was marked terminal")
	}
}

// TestATooLargeRequestReachesTheRecoveryPath: an overflow is the shared sentinel
// the runtime shortens and retries on, not a provider-specific refusal.
func TestATooLargeRequestReachesTheRecoveryPath(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{refusal(400,
		`{"error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"too long"}}`)}}
	_, err := newPort(t, tr).Generate(context.Background(), ai.Request{
		Model: "gpt-test", Messages: []ai.Message{{Role: ai.RoleUser, Content: "hi"}},
	})
	if !errors.Is(err, ai.ErrContextOverflow) {
		t.Fatalf("a too-large request produced %v, so the shortening path never runs", err)
	}
}

// TestATerminalSplitAcrossDataLinesIsStillRead: an event's data may arrive as
// several `data:` lines meant to be joined. Parsing them separately drops the
// terminal of any event large enough to be split.
func TestATerminalSplitAcrossDataLinesIsStillRead(t *testing.T) {
	body := "event: x\ndata: {\"type\":\"response.completed\",\"response\":\n" +
		"data: {\"id\":\"r\",\"model\":\"gpt-split\",\"status\":\"completed\",\n" +
		"data: \"usage\":{\"input_tokens\":8,\"output_tokens\":1}}}\n\n"
	tr := &recordedTransport{responses: []*http.Response{{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}}}

	resp, err := newPort(t, tr).Generate(context.Background(), ai.Request{
		Model: "gpt-test", Messages: []ai.Message{{Role: ai.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.Model != "gpt-split" {
		t.Fatalf("a terminal split across data lines was missed: model %q", resp.Model)
	}
	if resp.Usage.InputTokens != 8 {
		t.Fatalf("usage from a split terminal: %d input tokens", resp.Usage.InputTokens)
	}
}

// blockingBody never returns data. A stream waiting on a provider that has gone
// quiet is the case where cancellation has to be noticed while WAITING, not
// only while handing an event to a consumer.
type blockingBody struct{ ctx context.Context }

// Read blocks until the request's context ends, as a real transport's body
// does: net/http aborts an in-flight read when the request is cancelled.
func (b *blockingBody) Read([]byte) (int, error) {
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}
func (b *blockingBody) Close() error { return nil }

// TestCancellingWhileWaitingForDataIsAnAbort: the caller stopped, so the reply
// was cut short by them. Reporting it as a provider failure would invite a
// retry of the request they just cancelled.
func TestCancellingWhileWaitingForDataIsAnAbort(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tr := &recordedTransport{responses: []*http.Response{{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       &blockingBody{ctx: ctx},
	}}}
	p := newPort(t, tr)
	stream, err := p.Stream(ctx, ai.Request{
		Model: "gpt-test", Messages: []ai.Message{{Role: ai.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()

	var final *ai.AssistantMessage
	for ev := range stream {
		if ev.Final != nil {
			final = ev.Final
		}
	}
	if final == nil {
		t.Fatal("a stream cancelled while waiting closed with no terminal")
	}
	if final.StopReason != ai.StopAborted {
		t.Fatalf("terminal reason %v, want %v: the caller stopped, the provider did not fail",
			final.StopReason, ai.StopAborted)
	}
}

// TestAFailureInsideA200NamesItsOwnReason.
//
// A reply can fail inside a successful HTTP response for a reason the status
// cannot express. Calling every such ending an interruption loses the
// difference between "try later" and "this cannot succeed", and the second one
// costs money to retry.
func TestAFailureInsideA200NamesItsOwnReason(t *testing.T) {
	for _, tc := range []struct {
		name string
		code string
		want openai.Failure
	}{
		{"exhausted quota", "insufficient_quota", openai.FailureQuota},
		{"rejected credential", "invalid_api_key", openai.FailureAuth},
		{"filtered content", "content_filter", openai.FailureRefused},
		{"no code at all", "", openai.FailureInterrupted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			errPart := ""
			if tc.code != "" {
				errPart = `,"error":{"code":"` + tc.code + `","message":"reported inside a 200"}`
			}
			tr := &recordedTransport{responses: []*http.Response{recorded(
				`{"type":"response.created","response":{"id":"r","model":"gpt-served","status":"in_progress"}}`,
				`{"type":"response.failed","response":{"id":"r","model":"gpt-served","status":"failed"`+errPart+
					`,"usage":{"input_tokens":5,"output_tokens":0}}}`,
			)}}

			_, err := newPort(t, tr).Generate(context.Background(), ai.Request{
				Model: "gpt-test", Messages: []ai.Message{{Role: ai.RoleUser, Content: "hi"}},
			})
			var classified *openai.Error
			if !errors.As(err, &classified) {
				t.Fatalf("failure %v is not classified", err)
			}
			if classified.Failure != tc.want {
				t.Fatalf("classified %s, want %s", classified.Failure, tc.want)
			}
			if classified.Failure.Retryable() {
				t.Fatalf("%s was marked retryable", classified.Failure)
			}
		})
	}
}

// TestAContentIndexThatSkipsIsRefused: the adapter renumbers content indices
// inside an item just as it renumbers the items, so a gap is invisible after
// conversion and accepting it reports an order the provider never sent.
func TestAContentIndexThatSkipsIsRefused(t *testing.T) {
	skipping := func() *recordedTransport {
		return &recordedTransport{responses: []*http.Response{recorded(
			`{"type":"response.created","response":{"id":"r","model":"gpt-served","status":"in_progress"}}`,
			`{"type":"response.output_item.added","output_index":0,"item":{"id":"m","type":"message","role":"assistant","status":"in_progress","content":[]}}`,
			// The first content part of this item is announced at index 3.
			`{"type":"response.content_part.added","item_id":"m","output_index":0,"content_index":3,"part":{"type":"output_text","text":""}}`,
			`{"type":"response.output_text.delta","item_id":"m","output_index":0,"content_index":3,"delta":"x"}`,
			`{"type":"response.completed","response":{"id":"r","model":"gpt-served","status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}`,
		)}}
	}
	tr := skipping()
	tr2 := skipping

	// Nothing renumbered may reach the consumer first: a consumer cannot unsee
	// what it has already been given, so the stream must fail before any block
	// event carrying the wrong order is delivered.
	stream, streamErr := newPort(t, tr).Stream(context.Background(), ai.Request{
		Model: "gpt-test", Messages: []ai.Message{{Role: ai.RoleUser, Content: "hi"}},
	})
	if streamErr != nil {
		t.Fatal(streamErr)
	}
	for ev := range stream {
		switch ev.Kind {
		case ai.StreamTextStart, ai.StreamTextDelta, ai.StreamThinkingStart,
			ai.StreamThinkingDelta, ai.StreamToolCallStart, ai.StreamToolCallDelta:
			t.Fatalf("a %s event was delivered before the skipped index was caught", ev.Kind)
		}
	}

	_, err := newPort(t, tr2()).Generate(context.Background(), ai.Request{
		Model: "gpt-test", Messages: []ai.Message{{Role: ai.RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("a content index of 3 with nothing before it was renumbered and accepted")
	}
	if !strings.Contains(err.Error(), "renumber") {
		t.Fatalf("the failure did not explain itself: %v", err)
	}
}

// TestAConfigDoesNotPrintACallersSecret: a config holds a resolved key, and
// anything that formats a struct reaches every field it has.
func TestAConfigDoesNotPrintACallersSecret(t *testing.T) {
	const secret = "sk-a-callers-secret-value"
	cfg := openai.Config{
		Model: "m", Transport: &recordedTransport{}, MaxOutputTokens: 8,
		Credential: ai.StoredCredential(secret, "a test"),
	}
	for name, rendered := range map[string]string{
		"%v":  fmt.Sprintf("%v", cfg),
		"%+v": fmt.Sprintf("%+v", cfg),
		"%#v": fmt.Sprintf("%#v", cfg),
	} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("formatting a config with %s printed the key: %s", name, rendered)
		}
	}
}

// TestAMissingCredentialIsATypedAbsence: a caller must be able to tell "nothing
// configured" from "the provider rejected what we sent", and must learn it
// before a request is billed rather than from the reply to one.
func TestAMissingCredentialIsATypedAbsence(t *testing.T) {
	tr := &recordedTransport{}
	_, err := openai.New(openai.Config{
		Model: "m", Transport: tr, MaxOutputTokens: 8,
	})
	var classified *openai.Error
	if !errors.As(err, &classified) {
		t.Fatalf("a missing credential produced %v, which a caller cannot branch on", err)
	}
	if classified.Failure != openai.FailureAuth {
		t.Fatalf("classified %s", classified.Failure)
	}
	if tr.requests != 0 {
		t.Fatalf("made %d requests without a credential", tr.requests)
	}
}

// TestARedirectIsNotFollowed: a redirect is another request, and the default
// client follows them. A call budgeted for one request would then quietly make
// several — each billable, none counted.
func TestARedirectIsNotFollowed(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{
		{
			StatusCode: 307,
			Header:     http.Header{"Location": []string{"https://elsewhere.example/v1/responses"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"moved"}}`)),
		},
		recorded(
			`{"type":"response.created","response":{"id":"r","model":"gpt-served","status":"in_progress"}}`,
			`{"type":"response.completed","response":{"id":"r","model":"gpt-served","status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}`,
		),
	}}

	_, err := newPort(t, tr).Generate(context.Background(), ai.Request{
		Model: "gpt-test", Messages: []ai.Message{{Role: ai.RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("a redirect was followed and the reply treated as an answer")
	}
	if tr.requests != 1 {
		t.Fatalf("made %d requests for one call; a redirect must not add another", tr.requests)
	}
}

// TestATerminalAtEndOfStreamIsStillRead: a final event needs no blank line
// after it to be valid, so a reply that ends at EOF must not lose its status,
// model and usage.
func TestATerminalAtEndOfStreamIsStillRead(t *testing.T) {
	// No trailing blank line after the terminal event.
	body := "event: x\ndata: " +
		`{"type":"response.created","response":{"id":"r","model":"gpt-eof","status":"in_progress"}}` +
		"\n\nevent: x\ndata: " +
		`{"type":"response.completed","response":{"id":"r","model":"gpt-eof","status":"completed","usage":{"input_tokens":6,"output_tokens":1}}}`
	tr := &recordedTransport{responses: []*http.Response{{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}}}

	resp, err := newPort(t, tr).Generate(context.Background(), ai.Request{
		Model: "gpt-test", Messages: []ai.Message{{Role: ai.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.Model != "gpt-eof" {
		t.Fatalf("a terminal at end of stream was missed: model %q", resp.Model)
	}
	if resp.Usage.InputTokens != 6 {
		t.Fatalf("usage from a terminal at EOF: %d", resp.Usage.InputTokens)
	}
}

// TestAPortServesOnlyTheModelItWasBuiltFor: a configured model that is only
// validated and printed is a second source of truth about which model is in
// play, and the wrong one to believe when reading a reply.
func TestAPortServesOnlyTheModelItWasBuiltFor(t *testing.T) {
	tr := &recordedTransport{responses: []*http.Response{recorded(
		`{"type":"response.completed","response":{"id":"r","model":"gpt-served","status":"completed"}}`,
	)}}
	p := newPort(t, tr) // built for "gpt-test"

	_, err := p.Generate(context.Background(), ai.Request{
		Model: "some-other-model", Messages: []ai.Message{{Role: ai.RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("a request naming a different model was served anyway")
	}
	if tr.requests != 0 {
		t.Fatalf("sent %d requests for a model this port does not serve", tr.requests)
	}
}

// TestCountBasedOverflowDetection covers the two checks that infer an overflow
// from reported counts. Both read typed numbers; neither reads any text.
func TestCountBasedOverflowDetection(t *testing.T) {
	windowed := func(t *testing.T, tr http.RoundTripper, window int) *openai.Port {
		t.Helper()
		p, err := openai.New(openai.Config{
			Model: "gpt-test", Transport: tr, Credential: fixedKey,
			MaxOutputTokens: 16, ContextWindow: window,
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		return p
	}
	completed := func(usage string) *recordedTransport {
		return &recordedTransport{responses: []*http.Response{recorded(
			`{"type":"response.created","response":{"id":"r","model":"gpt-served","status":"in_progress"}}`,
			`{"type":"response.completed","response":{"id":"r","model":"gpt-served","status":"completed"`+
				usage+`}}`,
		)}}
	}
	ask := func(p *openai.Port) error {
		_, err := p.Generate(context.Background(), ai.Request{
			Model: "gpt-test", Messages: []ai.Message{{Role: ai.RoleUser, Content: "hi"}},
		})
		return err
	}

	t.Run("accepted input beyond the window is an overflow", func(t *testing.T) {
		tr := completed(`,"usage":{"input_tokens":1100001,"output_tokens":1}`)
		if err := ask(windowed(t, tr, 1_100_000)); !errors.Is(err, ai.ErrContextOverflow) {
			t.Fatalf("input past the window produced %v, so the shortening path never runs", err)
		}
	})

	t.Run("cached input still occupies the window", func(t *testing.T) {
		// Cheaper, not smaller: counting only the uncached part would miss an
		// overflow on exactly the requests a cache makes common.
		tr := completed(`,"usage":{"input_tokens":1200000,"output_tokens":1,"input_tokens_details":{"cached_tokens":600000}}`)
		if err := ask(windowed(t, tr, 1_100_000)); !errors.Is(err, ai.ErrContextOverflow) {
			t.Fatalf("a 1.2M prompt (600k of it cached) against a 1.1M window produced %v", err)
		}
	})

	t.Run("an ordinary reply is not an overflow", func(t *testing.T) {
		tr := completed(`,"usage":{"input_tokens":10,"output_tokens":1}`)
		if err := ask(windowed(t, tr, 1_100_000)); err != nil {
			t.Fatalf("an ordinary reply was rejected: %v", err)
		}
	})

	t.Run("unreported usage disables the checks", func(t *testing.T) {
		if err := ask(windowed(t, completed(``), 1_100_000)); err != nil {
			t.Fatalf("silence about usage was read as zero and became an overflow: %v", err)
		}
	})

	t.Run("no window leaves them off", func(t *testing.T) {
		tr := completed(`,"usage":{"input_tokens":9999999,"output_tokens":1}`)
		if err := ask(newPort(t, tr)); err != nil {
			t.Fatalf("a port with no measured window invented an overflow: %v", err)
		}
	})
}

// stream returns a recorded reply whose events can be edited first.
func streamOf(events ...string) *recordedTransport {
	return &recordedTransport{responses: []*http.Response{recorded(events...)}}
}

// wellFormed is one text block announced exactly as the provider documents it.
func wellFormed() []string {
	return []string{
		`{"type":"response.created","response":{"id":"r","model":"gpt-test","status":"in_progress"}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"m","type":"message","role":"assistant","status":"in_progress","content":[]}}`,
		`{"type":"response.content_part.added","item_id":"m","output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}`,
		`{"type":"response.output_text.delta","item_id":"m","output_index":0,"content_index":0,"delta":"visible"}`,
		`{"type":"response.output_text.done","item_id":"m","output_index":0,"content_index":0,"text":"visible"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"m","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"visible"}]}}`,
		`{"type":"response.completed","response":{"id":"r","model":"gpt-test","status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}`,
	}
}

// TestAnAnnouncementWithNoIdentityFailsBeforeItsContent: a block's position is
// what the provider said it was. An announcement that omits it leaves nothing
// to check, and a check that only walks recorded positions passes exactly then
// — so absence has to be recorded as the thing it is.
//
// The consumer must not see the content first: a renderer cannot unshow what it
// has already been given.
func TestAnAnnouncementWithNoIdentityFailsBeforeItsContent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		edit    func([]string) []string
		wantErr string
	}{
		{
			name: "an item with no output_index",
			edit: func(events []string) []string {
				events[1] = `{"type":"response.output_item.added","item":{"id":"m","type":"message","role":"assistant","status":"in_progress","content":[]}}`
				return events
			},
			wantErr: "an output item with no output_index",
		},
		{
			name: "a content part with no content_index",
			edit: func(events []string) []string {
				events[2] = `{"type":"response.content_part.added","item_id":"m","output_index":0,"part":{"type":"output_text","text":""}}`
				return events
			},
			wantErr: "a content part with no content_index",
		},
		{
			name: "a content part with no output_index",
			edit: func(events []string) []string {
				events[2] = `{"type":"response.content_part.added","item_id":"m","content_index":0,"part":{"type":"output_text","text":""}}`
				return events
			},
			wantErr: "a content part with no output_index",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newPort(t, streamOf(tc.edit(wellFormed())...))
			events, err := p.Stream(context.Background(), ai.Request{
				Model: "gpt-test", Messages: []ai.Message{{Role: ai.RoleUser, Content: "hi"}},
			})
			if err != nil {
				t.Fatalf("Stream: %v", err)
			}
			var failure error
			for ev := range events {
				switch ev.Kind {
				case ai.StreamStart, ai.StreamError:
				case ai.StreamDone:
					t.Fatal("a stream with an unidentified block completed")
				default:
					t.Fatalf("content reached the consumer before the stream failed: %s", ev.Kind)
				}
				if ev.Kind == ai.StreamError && ev.Final != nil {
					failure = ev.Final.Cause
				}
			}
			if failure == nil {
				t.Fatal("a block with no announced position was accepted")
			}
			if !strings.Contains(failure.Error(), tc.wantErr) {
				t.Fatalf("the failure did not say what was missing: %v", failure)
			}
			if _, ok := ai.FailureOf(failure); !ok {
				t.Fatalf("a caller cannot branch on %v", failure)
			}
		})
	}
}

// TestAWellFormedStreamStillPasses guards the check above from being satisfied
// by refusing everything.
func TestAWellFormedStreamStillPasses(t *testing.T) {
	resp, err := newPort(t, streamOf(wellFormed()...)).Generate(context.Background(), ai.Request{
		Model: "gpt-test", Messages: []ai.Message{{Role: ai.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.Content != "visible" {
		t.Fatalf("content %q", resp.Content)
	}
}

// failingTransport fails the way a network does, without a response.
type failingTransport struct {
	err      error
	requests int
}

func (f *failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	f.requests++
	return nil, f.err
}

// TestATransportFailureLeavesTyped: the outcome set is what a caller branches
// on. A failure that arrives as prose forces it to read text, and reading text
// is what the typed set exists to remove.
func TestATransportFailureLeavesTyped(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want ai.Failure
	}{
		{"a truncated body", io.ErrUnexpectedEOF, ai.FailureTransient},
		{"a refused connection", &net.OpError{Op: "dial", Err: errors.New("refused")}, ai.FailureTransient},
		{"something unrecognised", errors.New("the adapter disagreed"), ai.FailureUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := newPort(t, &failingTransport{err: tc.err}).Generate(
				context.Background(), ai.Request{
					Model: "gpt-test", Messages: []ai.Message{{Role: ai.RoleUser, Content: "hi"}},
				})
			failure, ok := ai.FailureOf(err)
			if !ok {
				t.Fatalf("a caller cannot branch on %v", err)
			}
			if failure != tc.want {
				t.Fatalf("classified %s, want %s", failure, tc.want)
			}
			// An unrecognised failure is not retried: this repository does not
			// guess that a repeat would cost less than it did.
			if got := ai.Retryable(err); got != (tc.want == ai.FailureTransient) {
				t.Fatalf("Retryable %v for %s", got, tc.want)
			}
		})
	}
}

// TestAnOverflowInsideA200IsRecoverable: a reply can fail inside a 200 for a
// reason the status cannot express. Read by the ending alone this is an
// interruption, which is never retried — so the one failure that recovers by
// shortening would be the one given up on.
func TestAnOverflowInsideA200IsRecoverable(t *testing.T) {
	tr := streamOf(
		`{"type":"response.created","response":{"id":"r","model":"gpt-test","status":"in_progress"}}`,
		`{"type":"response.failed","response":{"id":"r","model":"gpt-test","status":"failed","error":{"code":"context_length_exceeded","message":"too long"},"usage":{"input_tokens":900,"output_tokens":0}}}`,
	)
	_, err := newPort(t, tr).Generate(context.Background(), ai.Request{
		Model: "gpt-test", Messages: []ai.Message{{Role: ai.RoleUser, Content: "hi"}},
	})
	if !errors.Is(err, ai.ErrContextOverflow) {
		t.Fatalf("an overflow inside a 200 arrived as %v, which is not recovered from", err)
	}
}

// TestTheProvidersOwnRetryInstructionSurvives: nothing inside this package
// retries, so an instruction that is not carried out of it is one the caller
// who does decide never learns of.
func TestTheProvidersOwnRetryInstructionSurvives(t *testing.T) {
	refusal := func(status int, header, body string) *http.Response {
		h := http.Header{"Content-Type": []string{"application/json"}}
		if header != "" {
			h.Set("x-should-retry", header)
		}
		return &http.Response{StatusCode: status, Header: h,
			Body: io.NopCloser(strings.NewReader(body))}
	}
	for _, tc := range []struct {
		name string
		resp *http.Response
		want bool
	}{
		{
			name: "a transient status the provider says not to repeat",
			resp: refusal(503, "false", `{"error":{"message":"down"}}`),
			want: false,
		},
		{
			name: "a terminal status the provider asks to repeat",
			resp: refusal(400, "true", `{"error":{"message":"odd"}}`),
			want: true,
		},
		{
			name: "an exhausted balance the provider asks to repeat",
			resp: refusal(402, "true", `{"error":{"code":"insufficient_quota","message":"gone"}}`),
			want: false,
		},
		{
			name: "a transient status with no instruction",
			resp: refusal(503, "", `{"error":{"message":"down"}}`),
			want: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := &recordedTransport{responses: []*http.Response{tc.resp}}
			_, err := newPort(t, tr).Generate(context.Background(), ai.Request{
				Model: "gpt-test", Messages: []ai.Message{{Role: ai.RoleUser, Content: "hi"}},
			})
			if err == nil {
				t.Fatal("expected a refusal")
			}
			if got := ai.Retryable(err); got != tc.want {
				t.Fatalf("Retryable %v, want %v, for %v", got, tc.want, err)
			}
			if tr.requests != 1 {
				t.Fatalf("made %d requests; this port does not retry", tr.requests)
			}
		})
	}
}
