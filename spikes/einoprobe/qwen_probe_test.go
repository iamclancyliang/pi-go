package einoprobe

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/cloudwego/eino-ext/components/model/agenticqwen"
	"github.com/cloudwego/eino-ext/components/model/qwen"
	"github.com/cloudwego/eino/schema"
)

// The questions here are the ones that decide whether a provider's adapter can
// be used at all. Each is asked of the real adapter over a recorded exchange,
// because an adapter's documentation describes what it converts, not what it
// preserves.

// countingRoundTripper replays recorded responses and counts requests. The
// count is the point: a bound on requests asserted by counting them holds
// whatever the configuration claims.
type countingRoundTripper struct {
	requests int
	sent     []string
	respond  func(n int) *http.Response
}

func (c *countingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	c.requests++
	if req.Body != nil {
		body, _ := io.ReadAll(req.Body)
		c.sent = append(c.sent, string(body))
	}
	return c.respond(c.requests), nil
}

func chunks(lines ...string) *http.Response {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString("data: " + l + "\n\n")
	}
	b.WriteString("data: [DONE]\n\n")
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(b.String())),
	}
}

// askedHi is the smallest well-formed request this adapter accepts.
func askedHi() []*schema.AgenticMessage {
	return []*schema.AgenticMessage{{
		Role: schema.AgenticRoleType(schema.User),
		ContentBlocks: []*schema.ContentBlock{{
			Type:          schema.ContentBlockTypeUserInputText,
			UserInputText: &schema.UserInputText{Text: "hi"},
		}},
	}}
}

func qwenModel(t *testing.T, tr http.RoundTripper) *agenticqwen.Model {
	t.Helper()
	m, err := agenticqwen.New(context.Background(), &agenticqwen.Config{
		APIKey:     "probe-key",
		BaseURL:    "https://example.invalid/compatible-mode/v1",
		Model:      "qwen-probe",
		HTTPClient: &http.Client{Transport: tr},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return m
}

func collect(t *testing.T, r *schema.StreamReader[*schema.AgenticMessage]) []*schema.AgenticMessage {
	t.Helper()
	defer r.Close()
	var out []*schema.AgenticMessage
	for {
		chunk, err := r.Recv()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
			return out
		}
		out = append(out, chunk)
	}
}

// TestTheAgenticConversionLosesInterleavedToolCallIdentity records a defect,
// not a requirement. Two calls whose fragments alternate arrive as four blocks,
// and the continuation fragments carry neither the id nor the name — so nothing
// downstream can tell which call they belong to.
//
// Written as an assertion of what happens today so it fails if the behaviour
// changes: a fix upstream is worth knowing about, because it would remove the
// reason this repository reads the wire itself.
func TestTheAgenticConversionLosesInterleavedToolCallIdentity(t *testing.T) {
	tr := &countingRoundTripper{respond: func(int) *http.Response {
		return chunks(
			`{"id":"c1","object":"chat.completion.chunk","model":"qwen-served","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"alpha","arguments":"{\"x\""}}]}}]}`,
			`{"id":"c1","object":"chat.completion.chunk","model":"qwen-served","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"beta","arguments":"{\"y\""}}]}}]}`,
			`{"id":"c1","object":"chat.completion.chunk","model":"qwen-served","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":":1}"}}]}}]}`,
			`{"id":"c1","object":"chat.completion.chunk","model":"qwen-served","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":":2}"}}]}}]}`,
			`{"id":"c1","object":"chat.completion.chunk","model":"qwen-served","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":9,"completion_tokens":4,"total_tokens":13}}`,
		)
	}}
	stream, err := qwenModel(t, tr).Stream(context.Background(),
		askedHi())
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	type fragment struct {
		index          int
		id, name, args string
	}
	var seen []fragment
	for _, chunk := range collect(t, stream) {
		for _, b := range chunk.ContentBlocks {
			if b.Type != schema.ContentBlockTypeFunctionToolCall || b.FunctionToolCall == nil {
				continue
			}
			at := -1
			if b.StreamingMeta != nil {
				at = b.StreamingMeta.Index
			}
			seen = append(seen, fragment{
				index: at,
				id:    b.FunctionToolCall.CallID,
				name:  b.FunctionToolCall.Name,
				args:  b.FunctionToolCall.Arguments,
			})
		}
	}
	for _, f := range seen {
		t.Logf("fragment index=%d id=%q name=%q args=%q", f.index, f.id, f.name, f.args)
	}
	if len(seen) == 0 {
		t.Fatal("no tool call fragments arrived at all")
	}

	// Every fragment must be attributable: either it carries the identity, or
	// it carries a position that ties it to a fragment that did.
	if len(seen) != 4 {
		t.Fatalf("two announced calls produced %d fragments; the shape being "+
			"recorded here has changed", len(seen))
	}
	// Each fragment got its own position, so a return to an earlier call reads
	// as a new one.
	for at, f := range seen {
		if f.index != at {
			t.Fatalf("fragment %d reported position %d; the shape being recorded "+
				"here has changed", at, f.index)
		}
	}
	// And the two continuation fragments are anonymous, which is what makes the
	// loss unrecoverable rather than merely awkward.
	for _, f := range seen[2:] {
		if f.id != "" || f.name != "" {
			t.Fatalf("a continuation fragment kept its identity (%+v); the shape "+
				"being recorded here has changed", f)
		}
	}
	if tr.requests != 1 {
		t.Errorf("one call sent %d requests", tr.requests)
	}
}

// TestQwenHiddenRetriesAreOff: a call that fails must not become several billed
// requests underneath, and this adapter exposes no retry setting to turn off.
func TestQwenHiddenRetriesAreOff(t *testing.T) {
	for name, status := range map[string]int{
		"a server error": 500,
		"a throttle":     429,
	} {
		t.Run(name, func(t *testing.T) {
			tr := &countingRoundTripper{respond: func(int) *http.Response {
				return &http.Response{
					StatusCode: status,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"no"}}`)),
				}
			}}
			_, err := qwenModel(t, tr).Stream(context.Background(),
				askedHi())
			if err == nil {
				t.Fatal("a refusal was reported as success")
			}
			t.Logf("%d gave: %v", status, err)
			if tr.requests != 1 {
				t.Errorf("one call made %d requests", tr.requests)
			}
		})
	}
}

// TestQwenUsageAndServedModelAfterConversion records what the adapter's own
// conversion keeps, so the port knows what it has to read from the wire itself.
func TestQwenUsageAndServedModelAfterConversion(t *testing.T) {
	for _, tc := range []struct {
		name  string
		final string
	}{
		{
			name:  "usage reported",
			final: `{"id":"c1","model":"qwen-served-actual","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":4,"total_tokens":13}}`,
		},
		{
			name:  "usage never sent",
			final: `{"id":"c1","model":"qwen-served-actual","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := &countingRoundTripper{respond: func(int) *http.Response {
				return chunks(
					`{"id":"c1","model":"qwen-served-actual","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"}}]}`,
					tc.final,
				)
			}}
			stream, err := qwenModel(t, tr).Stream(context.Background(),
				askedHi())
			if err != nil {
				t.Fatalf("Stream: %v", err)
			}
			for i, chunk := range collect(t, stream) {
				meta := "<nil>"
				if chunk.ResponseMeta != nil {
					usage := "<nil>"
					if u := chunk.ResponseMeta.TokenUsage; u != nil {
						usage = fmt.Sprintf("%+v", *u)
					}
					meta = fmt.Sprintf("usage=%s ext=%+v", usage, chunk.ResponseMeta.Extension)
				}
				t.Logf("chunk %d: blocks=%d meta=%s", i, len(chunk.ContentBlocks), meta)
			}
			t.Logf("request body: %s", tr.sent[0])
		})
	}
}

// TestTheFrameworksOwnReassemblyCannotRepairIt settles whether the information
// is merely awkward to reach or actually gone: if the framework's own
// reassembly cannot rebuild two calls from these chunks, nothing downstream can.
func TestTheFrameworksOwnReassemblyCannotRepairIt(t *testing.T) {
	tr := &countingRoundTripper{respond: func(int) *http.Response {
		return chunks(
			`{"id":"c1","model":"qwen-served","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"alpha","arguments":"{\"x\""}}]}}]}`,
			`{"id":"c1","model":"qwen-served","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"beta","arguments":"{\"y\""}}]}}]}`,
			`{"id":"c1","model":"qwen-served","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":":1}"}}]}}]}`,
			`{"id":"c1","model":"qwen-served","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":":2}"}}]}}]}`,
			`{"id":"c1","model":"qwen-served","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":9,"completion_tokens":4,"total_tokens":13}}`,
		)
	}}
	stream, err := qwenModel(t, tr).Stream(context.Background(), askedHi())
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	joined, err := schema.ConcatAgenticMessages(collect(t, stream))
	if err != nil {
		t.Fatalf("the framework could not reassemble its own chunks: %v", err)
	}
	calls := 0
	for _, b := range joined.ContentBlocks {
		if b.Type != schema.ContentBlockTypeFunctionToolCall || b.FunctionToolCall == nil {
			continue
		}
		calls++
		t.Logf("reassembled call: id=%q name=%q args=%q",
			b.FunctionToolCall.CallID, b.FunctionToolCall.Name, b.FunctionToolCall.Arguments)
	}
	if calls != 4 {
		t.Fatalf("two announced calls reassembled into %d; the shape being "+
			"recorded here has changed", calls)
	}
	// Worse than a miscount: the arguments of one call are split across two of
	// them, so a caller dispatching these would send half a request twice.
	var split []string
	for _, b := range joined.ContentBlocks {
		if b.Type == schema.ContentBlockTypeFunctionToolCall && b.FunctionToolCall != nil {
			split = append(split, b.FunctionToolCall.Arguments)
		}
	}
	if strings.Join(split[:1], "") == `{"x":1}` {
		t.Fatal("the arguments arrived whole; the shape being recorded here has changed")
	}
	t.Logf("arguments as reassembled: %q", split)
}

// TestQwenClassicAdapterKeepsToolCallIndex asks whether the loss above is in
// the provider's wire, in this repository's reading of it, or in one particular
// conversion. The classic adapter reads the same bytes and stops one layer
// earlier, so what it carries is what the wire actually offered.
func TestQwenClassicAdapterKeepsToolCallIndex(t *testing.T) {
	tr := &countingRoundTripper{respond: func(int) *http.Response {
		return chunks(
			`{"id":"c1","model":"qwen-served","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"alpha","arguments":"{\"x\""}}]}}]}`,
			`{"id":"c1","model":"qwen-served","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"beta","arguments":"{\"y\""}}]}}]}`,
			`{"id":"c1","model":"qwen-served","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":":1}"}}]}}]}`,
			`{"id":"c1","model":"qwen-served","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":":2}"}}]}}]}`,
			`{"id":"c1","model":"qwen-served","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":9,"completion_tokens":4,"total_tokens":13}}`,
		)
	}}
	classic, err := qwen.NewChatModel(context.Background(), &qwen.ChatModelConfig{
		APIKey:     "probe-key",
		BaseURL:    "https://example.invalid/compatible-mode/v1",
		Model:      "qwen-probe",
		HTTPClient: &http.Client{Transport: tr},
	})
	if err != nil {
		t.Fatalf("NewChatModel: %v", err)
	}
	stream, err := classic.Stream(context.Background(),
		[]*schema.Message{schema.UserMessage("hi")})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close()

	var chunkList []*schema.Message
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		chunkList = append(chunkList, chunk)
		for _, tc := range chunk.ToolCalls {
			index := "<nil>"
			if tc.Index != nil {
				index = fmt.Sprint(*tc.Index)
			}
			t.Logf("fragment index=%s id=%q name=%q args=%q",
				index, tc.ID, tc.Function.Name, tc.Function.Arguments)
		}
	}
	joined, err := schema.ConcatMessages(chunkList)
	if err != nil {
		t.Fatalf("reassembly: %v", err)
	}
	for _, tc := range joined.ToolCalls {
		t.Logf("reassembled call: id=%q name=%q args=%q",
			tc.ID, tc.Function.Name, tc.Function.Arguments)
	}
	if len(joined.ToolCalls) != 2 {
		t.Errorf("two announced calls reassembled into %d", len(joined.ToolCalls))
	}
	for _, tc := range joined.ToolCalls {
		if tc.ID == "" || tc.Function.Name == "" {
			t.Errorf("a reassembled call lost its identity: %+v", tc)
		}
	}
}

// classicQwen builds the classic adapter over an injected transport.
func classicQwen(t *testing.T, tr http.RoundTripper) *qwen.ChatModel {
	t.Helper()
	m, err := qwen.NewChatModel(context.Background(), &qwen.ChatModelConfig{
		APIKey:     "probe-key",
		BaseURL:    "https://example.invalid/compatible-mode/v1",
		Model:      "qwen-probe",
		HTTPClient: &http.Client{Transport: tr},
	})
	if err != nil {
		t.Fatalf("NewChatModel: %v", err)
	}
	return m
}

// TestQwenClassicCarriesWhatThePortNeeds records what survives this adapter's
// own conversion, which decides what the port has to read from the wire itself.
func TestQwenClassicCarriesWhatThePortNeeds(t *testing.T) {
	for _, tc := range []struct {
		name  string
		final string
	}{
		{"usage reported", `{"id":"c1","model":"qwen-served-actual","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":4,"total_tokens":13,"prompt_tokens_details":{"cached_tokens":3}}}`},
		{"usage never sent", `{"id":"c1","model":"qwen-served-actual","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`},
		{"usage reported as zero", `{"id":"c1","model":"qwen-served-actual","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := &countingRoundTripper{respond: func(int) *http.Response {
				return chunks(
					`{"id":"c1","model":"qwen-served-actual","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"}}]}`,
					tc.final,
				)
			}}
			stream, err := classicQwen(t, tr).Stream(context.Background(),
				[]*schema.Message{schema.UserMessage("hi")})
			if err != nil {
				t.Fatalf("Stream: %v", err)
			}
			defer stream.Close()
			for i := 0; ; i++ {
				chunk, err := stream.Recv()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatalf("Recv: %v", err)
				}
				meta := "<nil>"
				if chunk.ResponseMeta != nil {
					usage := "<nil>"
					if u := chunk.ResponseMeta.Usage; u != nil {
						usage = fmt.Sprintf("%+v", *u)
					}
					meta = fmt.Sprintf("finish=%q usage=%s", chunk.ResponseMeta.FinishReason, usage)
				}
				t.Logf("chunk %d: content=%q meta=%s extra=%v", i, chunk.Content, meta, chunk.Extra)
			}
			if tr.requests != 1 {
				t.Errorf("one call made %d requests", tr.requests)
			}
		})
	}
}

// TestQwenClassicMakesNoHiddenRetries: a refusal must not become several billed
// requests underneath, and this adapter exposes no retry setting to turn off.
func TestQwenClassicMakesNoHiddenRetries(t *testing.T) {
	for name, status := range map[string]int{"a server error": 500, "a throttle": 429} {
		t.Run(name, func(t *testing.T) {
			tr := &countingRoundTripper{respond: func(int) *http.Response {
				return &http.Response{
					StatusCode: status,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"no"}}`)),
				}
			}}
			if _, err := classicQwen(t, tr).Stream(context.Background(),
				[]*schema.Message{schema.UserMessage("hi")}); err == nil {
				t.Fatal("a refusal was reported as success")
			}
			if tr.requests != 1 {
				t.Errorf("one call made %d requests", tr.requests)
			}
		})
	}
}
