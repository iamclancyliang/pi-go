package openai_test

import (
	"context"
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
