package openai_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/provider/openai"
)

// recordedTransport replays a recorded exchange and counts requests.
type recordedTransport struct {
	requests  int
	responses []*http.Response
}

func (r *recordedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		_, _ = io.Copy(io.Discard, req.Body)
	}
	r.requests++
	if r.requests > len(r.responses) {
		return nil, io.ErrUnexpectedEOF
	}
	return r.responses[r.requests-1], nil
}

type fixedEnv struct{}

func (fixedEnv) Lookup(context.Context, string) (string, error) { return "test-key", nil }

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
		Model: "gpt-test", Transport: tr, Environment: fixedEnv{}, MaxOutputTokens: 64,
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
		Model: "m", Transport: &recordedTransport{}, Environment: fixedEnv{}, MaxOutputTokens: 8,
	}
	for _, tc := range []struct {
		name   string
		mutate func(*openai.Config)
	}{
		{"no model", func(c *openai.Config) { c.Model = "" }},
		{"no transport", func(c *openai.Config) { c.Transport = nil }},
		{"no environment", func(c *openai.Config) { c.Environment = nil }},
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
// This tranche supports text, reasoning and function tool calls. A block of any
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
		Model: "gpt-asked-for", Messages: []ai.Message{{Role: ai.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.Model == "gpt-asked-for" {
		t.Fatal("the requested model was reported as the one that served the reply")
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

// TestBlocksArriveInOrderAndTheStreamEndsOnce: a consumer rendering as it goes
// needs each block finished before the next begins, and exactly one ending.
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
