package deepseek_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/provider/deepseek"
)

// countingTransport answers from a script and counts what it was asked to send.
//
// The count is the point: a bound on requests asserted by counting them holds
// whatever the configuration says, and a configuration value proves nothing
// about what was actually sent.
type countingTransport struct {
	requests int
	bodies   []string
	respond  func(n int) *http.Response
}

func (t *countingTransport) Do(req *http.Request) (*http.Response, error) {
	t.requests++
	if req.Body != nil {
		body, _ := io.ReadAll(req.Body)
		t.bodies = append(t.bodies, string(body))
	}
	if t.respond == nil {
		return nil, errors.New("no scripted response")
	}
	return t.respond(t.requests), nil
}

type env map[string]string

func (e env) Lookup(_ context.Context, name string) (string, error) { return e[name], nil }

func sse(lines ...string) *http.Response {
	var b strings.Builder
	for _, l := range lines {
		fmt.Fprintf(&b, "data: %s\n\n", l)
	}
	b.WriteString("data: [DONE]\n\n")
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(b.String()))}
}

func status(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body))}
}

func newPort(t *testing.T, tr deepseek.Transport, e deepseek.Environment) *deepseek.Port {
	t.Helper()
	p, err := deepseek.New(deepseek.Config{
		Model: "deepseek-v4-flash", Transport: tr, Environment: e, MaxOutputTokens: 16,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

// TestOneCallSendsOneRequest is the cost bound, asserted by counting rather than
// by reading configuration.
func TestOneCallSendsOneRequest(t *testing.T) {
	tr := &countingTransport{respond: func(int) *http.Response {
		return sse(`{"choices":[{"delta":{"content":"hi"},"finish_reason":null}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6}}`)
	}}
	p := newPort(t, tr, env{"DEEPSEEK_API_KEY": "k"})

	resp, err := p.Generate(context.Background(), ai.Request{Model: "m"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if tr.requests != 1 {
		t.Fatalf("sent %d requests, want exactly 1", tr.requests)
	}
	if resp.Content != "hi" {
		t.Fatalf("content %q", resp.Content)
	}
}

// TestTheRequestCarriesTheFieldsThisProviderReads guards the cap that costs
// money when it is named wrongly: max_completion_tokens is silently not this
// provider's field, so a reply capped under that name may have no cap at all.
func TestTheRequestCarriesTheFieldsThisProviderReads(t *testing.T) {
	tr := &countingTransport{respond: func(int) *http.Response {
		return sse(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`)
	}}
	p := newPort(t, tr, env{"DEEPSEEK_API_KEY": "k"})
	if _, err := p.Generate(context.Background(), ai.Request{Model: "m"}); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	body := tr.bodies[0]
	for _, want := range []string{`"max_tokens":16`, `"include_usage":true`} {
		if !strings.Contains(body, want) {
			t.Errorf("request body missing %s\n%s", want, body)
		}
	}
	for _, unwanted := range []string{"max_completion_tokens", `"store"`} {
		if strings.Contains(body, unwanted) {
			t.Errorf("request body contains %s, which this provider does not read\n%s", unwanted, body)
		}
	}
}

// TestQuotaAndThrottleReachOppositeOutcomes is the pair that cannot be passed by
// reading one field: both are limit failures, and only one is worth retrying.
func TestQuotaAndThrottleReachOppositeOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		want      deepseek.Failure
		retryable bool
	}{
		{"exhausted balance", 402, deepseek.FailureQuota, false},
		{"ordinary throttle", 429, deepseek.FailureThrottled, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := &countingTransport{respond: func(int) *http.Response {
				return status(tc.status, `{"error":{"message":"limit"}}`)
			}}
			p := newPort(t, tr, env{"DEEPSEEK_API_KEY": "k"})

			_, err := p.Generate(context.Background(), ai.Request{Model: "m"})
			var got *deepseek.Error
			if !errors.As(err, &got) {
				t.Fatalf("error %v is not classified", err)
			}
			if got.Failure != tc.want {
				t.Fatalf("classified %s, want %s", got.Failure, tc.want)
			}
			if got.Failure.Retryable() != tc.retryable {
				t.Fatalf("retryable %v, want %v", got.Failure.Retryable(), tc.retryable)
			}
			if tr.requests != 1 {
				t.Fatalf("sent %d requests, want 1", tr.requests)
			}
		})
	}
}

// TestA200ThatReportsFailureIsNotASuccess covers the two stop reasons that carry
// a failure inside a successful HTTP response. A check that read only the status
// would pass here while the defect went straight through.
func TestA200ThatReportsFailureIsNotASuccess(t *testing.T) {
	for _, tc := range []struct {
		reason string
		want   deepseek.Failure
	}{
		{"insufficient_system_resource", deepseek.FailureInterrupted},
		{"content_filter", deepseek.FailureRefused},
		{"a_reason_from_the_future", deepseek.FailureUnknown},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			tr := &countingTransport{respond: func(int) *http.Response {
				return sse(`{"choices":[{"delta":{"content":"partial"},"finish_reason":null}]}`,
					fmt.Sprintf(`{"choices":[{"delta":{},"finish_reason":%q}]}`, tc.reason))
			}}
			p := newPort(t, tr, env{"DEEPSEEK_API_KEY": "k"})

			_, err := p.Generate(context.Background(), ai.Request{Model: "m"})
			var got *deepseek.Error
			if !errors.As(err, &got) {
				t.Fatalf("a 200 reporting %s produced %v, which is not a classified failure", tc.reason, err)
			}
			if got.Failure != tc.want {
				t.Fatalf("classified %s, want %s", got.Failure, tc.want)
			}
			if got.Failure.Retryable() {
				t.Fatalf("%s must not be retried", got.Failure)
			}
			if tr.requests != 1 {
				t.Fatalf("sent %d requests, want 1", tr.requests)
			}
		})
	}
}

// TestCredentialPrecedence covers the four distinct cases, and that the key
// never appears in what a failure reports.
func TestCredentialPrecedence(t *testing.T) {
	ctx := context.Background()

	t.Run("a stored key wins", func(t *testing.T) {
		c, err := deepseek.Resolve(ctx, env{"DEEPSEEK_API_KEY": "from-env"}, "stored")
		if err != nil {
			t.Fatal(err)
		}
		if c.Key() != "stored" || c.Source != "stored credential" {
			t.Fatalf("resolved %s from %q", c.Key(), c.Source)
		}
	})

	t.Run("otherwise the environment", func(t *testing.T) {
		c, err := deepseek.Resolve(ctx, env{"DEEPSEEK_API_KEY": "from-env"}, "")
		if err != nil {
			t.Fatal(err)
		}
		if c.Key() != "from-env" || c.Source != "DEEPSEEK_API_KEY" {
			t.Fatalf("resolved %s from %q", c.Key(), c.Source)
		}
	})

	t.Run("a blank value counts as unset", func(t *testing.T) {
		_, err := deepseek.Resolve(ctx, env{"DEEPSEEK_API_KEY": "   "}, "")
		if !errors.Is(err, deepseek.ErrNoCredential) {
			t.Fatalf("a blank variable resolved to %v; an empty key sent to a provider "+
				"fails as a bad credential rather than a missing one", err)
		}
	})

	t.Run("nothing set is a typed failure", func(t *testing.T) {
		_, err := deepseek.Resolve(ctx, env{}, "")
		if !errors.Is(err, deepseek.ErrNoCredential) {
			t.Fatalf("absence produced %v, which a caller cannot branch on", err)
		}
	})

	t.Run("the key is not in what a report can print", func(t *testing.T) {
		c, err := deepseek.Resolve(ctx, env{"DEEPSEEK_API_KEY": "sk-secret-value"}, "")
		if err != nil {
			t.Fatal(err)
		}
		for _, rendered := range []string{
			fmt.Sprintf("%v", c), fmt.Sprintf("%+v", c), fmt.Sprintf("%#v", c), c.String(),
		} {
			if strings.Contains(rendered, "sk-secret-value") {
				t.Fatalf("the key reached a formatted value: %s", rendered)
			}
		}
	})
}

// TestConfigurationRefusesAWindowItWouldMisuse: a window at or below a size this
// provider is recorded accepting would report accepted replies as overflows and
// buy a shortened retry of each.
func TestConfigurationRefusesAWindowItWouldMisuse(t *testing.T) {
	_, err := deepseek.New(deepseek.Config{
		Model: "m", Transport: &countingTransport{}, Environment: env{}, ContextWindow: 1_000_000,
	})
	if err == nil {
		t.Fatal("a window of 1,000,000 was accepted, though a request of 1,015,083 tokens is recorded being served")
	}
}

// TestAPortWithoutASuppliedTransportIsRefused: omitting the seam must fail, or
// "no test reaches the network" holds only by convention.
func TestAPortWithoutASuppliedTransportIsRefused(t *testing.T) {
	if _, err := deepseek.New(deepseek.Config{Model: "m", Environment: env{}}); err == nil {
		t.Fatal("a port was built with no transport")
	}
	if _, err := deepseek.New(deepseek.Config{Model: "m", Transport: &countingTransport{}}); err == nil {
		t.Fatal("a port was built with no environment")
	}
}

// TestUsageKeepsUnreportedApartFromZero: zero is a real answer, and a ledger
// that cannot tell it from silence cannot tell free reasoning from unexplained
// spend.
func TestUsageKeepsUnreportedApartFromZero(t *testing.T) {
	t.Run("reported zero stays zero", func(t *testing.T) {
		tr := &countingTransport{respond: func(int) *http.Response {
			return sse(`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":0,"completion_tokens_details":{"reasoning_tokens":0}}}`)
		}}
		p := newPort(t, tr, env{"DEEPSEEK_API_KEY": "k"})
		resp, err := p.Generate(context.Background(), ai.Request{Model: "m"})
		if err != nil {
			t.Fatal(err)
		}
		if resp.Usage.ReasoningTokens == nil || *resp.Usage.ReasoningTokens != 0 {
			t.Fatalf("a reported zero became %v", resp.Usage.ReasoningTokens)
		}
	})

	t.Run("unreported stays absent", func(t *testing.T) {
		tr := &countingTransport{respond: func(int) *http.Response {
			return sse(`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2}}`)
		}}
		p := newPort(t, tr, env{"DEEPSEEK_API_KEY": "k"})
		resp, err := p.Generate(context.Background(), ai.Request{Model: "m"})
		if err != nil {
			t.Fatal(err)
		}
		if resp.Usage.ReasoningTokens != nil {
			t.Fatalf("an unreported field became %d", *resp.Usage.ReasoningTokens)
		}
		if !resp.Usage.Reported {
			t.Fatal("usage arrived but was not marked reported")
		}
	})

	t.Run("no usage at all is not a free call", func(t *testing.T) {
		tr := &countingTransport{respond: func(int) *http.Response {
			return sse(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`)
		}}
		p := newPort(t, tr, env{"DEEPSEEK_API_KEY": "k"})
		resp, err := p.Generate(context.Background(), ai.Request{Model: "m"})
		if err != nil {
			t.Fatal(err)
		}
		if resp.Usage.Reported {
			t.Fatal("a reply that reported no usage claims to have reported some")
		}
	})
}

// TestAToolCallSplitAcrossChunksStaysOneCall reproduces how a provider actually
// streams a call: identity first, arguments in fragments afterwards. Treating
// each fragment as its own block would yield several calls, all but the first
// without a name.
func TestAToolCallSplitAcrossChunksStaysOneCall(t *testing.T) {
	tr := &countingTransport{respond: func(int) *http.Response {
		return sse(
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"tc1","type":"function","function":{"name":"list_files","arguments":"{\"pa"}}]},"finish_reason":null}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"th\":\"."}}]},"finish_reason":null}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"}"}}]},"finish_reason":null}]}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		)
	}}
	p := newPort(t, tr, env{"DEEPSEEK_API_KEY": "k"})

	resp, err := p.Generate(context.Background(), ai.Request{Model: "m"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("one call streamed in three chunks became %d calls: %+v", len(resp.ToolCalls), resp.ToolCalls)
	}
	call := resp.ToolCalls[0]
	if call.ID != "tc1" || call.Name != "list_files" {
		t.Fatalf("call identity lost: %+v", call)
	}
	if call.Args != `{"path":"."}` {
		t.Fatalf("arguments reassembled as %q", call.Args)
	}
}

// TestAReplyAskingForToolsSaysSo: a reply that requested tools has not finished
// answering, and reporting it as ended would drop the request.
func TestAReplyAskingForToolsSaysSo(t *testing.T) {
	tr := &countingTransport{respond: func(int) *http.Response {
		return sse(
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"tc1","type":"function","function":{"name":"list_files","arguments":"{}"}}]},"finish_reason":null}]}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`)
	}}
	p := newPort(t, tr, env{"DEEPSEEK_API_KEY": "k"})

	events, err := p.Stream(context.Background(), ai.Request{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	var final *ai.AssistantMessage
	for ev := range events {
		if ev.Final != nil {
			final = ev.Final
		}
	}
	if final == nil || final.StopReason != ai.StopToolUse {
		t.Fatalf("a tool request ended as %v, want %v", final.StopReason, ai.StopToolUse)
	}
}

// TestTheServedModelIsReported: a substitution the provider made must be
// visible, so what served the call is read from the reply.
func TestTheServedModelIsReported(t *testing.T) {
	tr := &countingTransport{respond: func(int) *http.Response {
		return sse(`{"model":"deepseek-something-else","choices":[{"delta":{"content":"x"},"finish_reason":"stop"}]}`)
	}}
	p := newPort(t, tr, env{"DEEPSEEK_API_KEY": "k"})
	resp, err := p.Generate(context.Background(), ai.Request{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Model != "deepseek-something-else" {
		t.Fatalf("reported %q as the serving model; a substitution would be invisible", resp.Model)
	}
}

// TestAnUncappedRequestCannotBeBuilt: a cap that can be omitted is not a cap.
func TestAnUncappedRequestCannotBeBuilt(t *testing.T) {
	for _, cap := range []int{0, -1} {
		if _, err := deepseek.New(deepseek.Config{
			Model: "m", Transport: &countingTransport{}, Environment: env{}, MaxOutputTokens: cap,
		}); err == nil {
			t.Fatalf("a port with MaxOutputTokens=%d was built; its requests would carry no cap", cap)
		}
	}
}

// TestNoModelIsRefusedBeforeAnythingIsSent: a request naming no model must fail
// as a typed error and send nothing, rather than quietly reaching whichever
// model this port happened to be configured with.
func TestNoModelIsRefusedBeforeAnythingIsSent(t *testing.T) {
	tr := &countingTransport{respond: func(int) *http.Response {
		return sse(`{"choices":[{"delta":{"content":"should not happen"},"finish_reason":"stop"}]}`)
	}}
	p, err := deepseek.New(deepseek.Config{
		Model: "configured", Transport: tr, Environment: env{"DEEPSEEK_API_KEY": "k"},
		MaxOutputTokens: 8,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, genErr := p.Generate(context.Background(), ai.Request{})
	var classified *deepseek.Error
	if !errors.As(genErr, &classified) {
		t.Fatalf("an unnamed model produced %v, which a caller cannot branch on", genErr)
	}
	if tr.requests != 0 {
		t.Fatalf("sent %d requests for a call that named no model", tr.requests)
	}
}

// TestTheKeyIsNotInAnyErrorOrFormattedPort covers the two paths a credential
// could take out of this package: a formatted config, and a provider that
// echoes the request back inside an error body.
func TestTheKeyIsNotInAnyErrorOrFormattedPort(t *testing.T) {
	const secret = "sk-super-secret-value"
	tr := &countingTransport{respond: func(int) *http.Response {
		return status(400, `{"error":{"message":"bad request: Bearer `+secret+`","code":"invalid"}}`)
	}}
	store := deepseek.NewMemoryStore()
	if _, err := store.Modify(context.Background(), "deepseek",
		func(deepseek.Stored, bool) (deepseek.Stored, bool, error) {
			return deepseek.NewAPIKey(secret), true, nil
		}); err != nil {
		t.Fatal(err)
	}
	cfg := deepseek.Config{
		Model: "m", Transport: tr, Environment: env{}, Store: store, MaxOutputTokens: 8,
	}
	p, err := deepseek.New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	_, genErr := p.Generate(context.Background(), ai.Request{Model: "m"})
	if genErr == nil {
		t.Fatal("expected a failure")
	}
	for name, rendered := range map[string]string{
		"error text": genErr.Error(),
		"port %v":    fmt.Sprintf("%v", p),
		"port %+v":   fmt.Sprintf("%+v", p),
		"port %#v":   fmt.Sprintf("%#v", p),
		"config %v":  fmt.Sprintf("%v", cfg),
		"config %+v": fmt.Sprintf("%+v", cfg),
		"config %#v": fmt.Sprintf("%#v", cfg),
	} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("the key reached %s: %s", name, rendered)
		}
	}
}

// TestCountBasedOverflowDetection covers the two checks that infer an overflow
// from reported counts. Both read typed numbers; neither reads any text.
func TestCountBasedOverflowDetection(t *testing.T) {
	newWindowed := func(t *testing.T, tr deepseek.Transport, window int) *deepseek.Port {
		t.Helper()
		p, err := deepseek.New(deepseek.Config{
			Model: "m", Transport: tr, Environment: env{"DEEPSEEK_API_KEY": "k"},
			MaxOutputTokens: 8, ContextWindow: window,
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		return p
	}

	t.Run("accepted input beyond the window is an overflow", func(t *testing.T) {
		tr := &countingTransport{respond: func(int) *http.Response {
			return sse(`{"choices":[{"delta":{"content":"x"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1100001,"completion_tokens":1}}`)
		}}
		_, err := newWindowed(t, tr, 1_100_000).Generate(context.Background(), ai.Request{Model: "m"})
		if !errors.Is(err, ai.ErrContextOverflow) {
			t.Fatalf("input past the window produced %v, so the shortening path never runs", err)
		}
	})

	t.Run("a filled window with no output is an overflow", func(t *testing.T) {
		tr := &countingTransport{respond: func(int) *http.Response {
			return sse(`{"choices":[{"delta":{},"finish_reason":"length"}],"usage":{"prompt_tokens":1099999,"completion_tokens":0}}`)
		}}
		_, err := newWindowed(t, tr, 1_100_000).Generate(context.Background(), ai.Request{Model: "m"})
		if !errors.Is(err, ai.ErrContextOverflow) {
			t.Fatalf("a filled window with no room to answer produced %v", err)
		}
	})

	t.Run("cached input still occupies the window", func(t *testing.T) {
		// Prompt tokens served from cache are cheaper, not smaller: they occupy
		// the same room. prompt_tokens is the whole prompt including them, so
		// the window comparison must use the whole thing — while the reported
		// input is the uncached remainder, and adding the two would count the
		// cached part twice.
		tr := &countingTransport{respond: func(int) *http.Response {
			return sse(`{"choices":[{"delta":{"content":"x"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1200000,"completion_tokens":1,"prompt_tokens_details":{"cached_tokens":600000}}}`)
		}}
		_, err := newWindowed(t, tr, 1_100_000).Generate(context.Background(), ai.Request{Model: "m"})
		if !errors.Is(err, ai.ErrContextOverflow) {
			t.Fatalf("a 1.2M prompt (600k of it cached) against a 1.1M window produced %v", err)
		}
	})

	t.Run("an ordinary reply is not an overflow", func(t *testing.T) {
		tr := &countingTransport{respond: func(int) *http.Response {
			return sse(`{"choices":[{"delta":{"content":"x"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":1}}`)
		}}
		if _, err := newWindowed(t, tr, 1_100_000).Generate(context.Background(), ai.Request{Model: "m"}); err != nil {
			t.Fatalf("an ordinary reply was rejected: %v", err)
		}
	})

	t.Run("unreported usage disables the checks", func(t *testing.T) {
		tr := &countingTransport{respond: func(int) *http.Response {
			return sse(`{"choices":[{"delta":{},"finish_reason":"length"}]}`)
		}}
		if _, err := newWindowed(t, tr, 1_100_000).Generate(context.Background(), ai.Request{Model: "m"}); err != nil {
			t.Fatalf("silence about usage was read as zero and became an overflow: %v", err)
		}
	})

	t.Run("no window leaves them off", func(t *testing.T) {
		tr := &countingTransport{respond: func(int) *http.Response {
			return sse(`{"choices":[{"delta":{"content":"x"},"finish_reason":"stop"}],"usage":{"prompt_tokens":9999999,"completion_tokens":1}}`)
		}}
		p := newPort(t, tr, env{"DEEPSEEK_API_KEY": "k"})
		if _, err := p.Generate(context.Background(), ai.Request{Model: "m"}); err != nil {
			t.Fatalf("a port with no measured window invented an overflow: %v", err)
		}
	})
}

// TestCancellationStaysCancellation: a caller that stopped the request must not
// be told the provider had a transient problem and to try again.
func TestCancellationStaysCancellation(t *testing.T) {
	tr := &countingTransport{respond: func(int) *http.Response { return nil }}
	tr.respond = nil // Do returns an error
	p := newPort(t, tr, env{"DEEPSEEK_API_KEY": "k"})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := p.Generate(ctx, ai.Request{Model: "m"})
	if err == nil {
		t.Fatal("a cancelled call succeeded")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation arrived as %v, which a caller cannot recognise", err)
	}
}

// TestTheStoreSerializesWritesAgainstDeletes: an unserialized pair lets a write
// land after the delete that was meant to remove it, leaving a credential the
// user believes they removed.
func TestTheStoreSerializesWritesAgainstDeletes(t *testing.T) {
	store := deepseek.NewMemoryStore()
	ctx := context.Background()

	set := func(v string) {
		if _, err := store.Modify(ctx, "deepseek",
			func(deepseek.Stored, bool) (deepseek.Stored, bool, error) {
				return deepseek.NewAPIKey(v), true, nil
			}); err != nil {
			t.Fatal(err)
		}
	}
	set("first")

	// A slow write racing a delete. The write starts first and holds the
	// provider's lock while it works.
	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = store.Modify(ctx, "deepseek",
			func(cur deepseek.Stored, _ bool) (deepseek.Stored, bool, error) {
				close(started)
				time.Sleep(30 * time.Millisecond)
				return deepseek.NewAPIKey("second"), true, nil
			})
	}()
	<-started
	if err := store.Delete(ctx, "deepseek"); err != nil {
		t.Fatal(err)
	}
	<-done

	// The delete waited for the write, so the delete is the last word.
	if _, err := store.Read(ctx, "deepseek"); !errors.Is(err, deepseek.ErrNoStoredCredential) {
		t.Fatal("a credential survived the delete that followed the write it raced")
	}
}

// TestConcurrentModifiesEachSeeTheLastValue: every mutation is a serialized
// read-modify-write, so no update is silently lost.
func TestConcurrentModifiesEachSeeTheLastValue(t *testing.T) {
	store := deepseek.NewMemoryStore()
	ctx := context.Background()
	const writers = 20

	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = store.Modify(ctx, "deepseek",
				func(cur deepseek.Stored, exists bool) (deepseek.Stored, bool, error) {
					n := 0
					if exists {
						n = len(cur.Key())
					}
					return deepseek.NewAPIKey(strings.Repeat("x", n+1)), true, nil
				})
		}()
	}
	wg.Wait()

	got, err := store.Read(ctx, "deepseek")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Key()) != writers {
		t.Fatalf("%d serialized writes produced a key of length %d; updates were lost",
			writers, len(got.Key()))
	}
}

// TestListIsNonSecretAndSideEffectFree.
func TestListIsNonSecretAndSideEffectFree(t *testing.T) {
	store := deepseek.NewMemoryStore()
	ctx := context.Background()
	if _, err := store.Modify(ctx, "deepseek",
		func(deepseek.Stored, bool) (deepseek.Stored, bool, error) {
			return deepseek.NewAPIKey("sk-listing-secret"), true, nil
		}); err != nil {
		t.Fatal(err)
	}

	infos, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].ProviderID != "deepseek" || infos[0].Type != deepseek.TypeAPIKey {
		t.Fatalf("listed %+v", infos)
	}
	rendered := fmt.Sprintf("%v %+v %#v", infos, infos, infos)
	if strings.Contains(rendered, "sk-listing-secret") {
		t.Fatalf("listing disclosed the key: %s", rendered)
	}

	// The CONTAINER must redact too. fmt reaches unexported fields by
	// reflection and cannot call a method on what it finds there, so a store
	// without its own String prints the map structurally — secret included.
	for _, r := range []string{
		fmt.Sprintf("%v", store), fmt.Sprintf("%+v", store), fmt.Sprintf("%#v", store),
	} {
		if strings.Contains(r, "sk-listing-secret") {
			t.Fatalf("the store disclosed a held key when formatted: %s", r)
		}
	}

	// A stored credential formats without its secret too.
	held, _ := store.Read(ctx, "deepseek")
	for _, r := range []string{fmt.Sprintf("%v", held), fmt.Sprintf("%+v", held), fmt.Sprintf("%#v", held)} {
		if strings.Contains(r, "sk-listing-secret") {
			t.Fatalf("a stored credential formatted its secret: %s", r)
		}
	}
}

// TestOneCredentialPerProvider: storing again replaces rather than accumulates.
func TestOneCredentialPerProvider(t *testing.T) {
	store := deepseek.NewMemoryStore()
	ctx := context.Background()
	for _, v := range []string{"one", "two"} {
		if _, err := store.Modify(ctx, "deepseek",
			func(deepseek.Stored, bool) (deepseek.Stored, bool, error) {
				return deepseek.NewAPIKey(v), true, nil
			}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.Read(ctx, "deepseek")
	if err != nil {
		t.Fatal(err)
	}
	if got.Key() != "two" {
		t.Fatalf("provider holds %q", got.Key())
	}
	if infos, _ := store.List(ctx); len(infos) != 1 {
		t.Fatalf("provider accumulated %d credentials", len(infos))
	}
}

// retryTransport answers from a scripted sequence and records what it was asked.
type retryTransport struct {
	requests  int
	responses []*http.Response
}

func (r *retryTransport) Do(*http.Request) (*http.Response, error) {
	r.requests++
	if r.requests > len(r.responses) {
		return nil, errors.New("more requests than scripted")
	}
	return r.responses[r.requests-1], nil
}

func withHeader(resp *http.Response, k, v string) *http.Response {
	if resp.Header == nil {
		resp.Header = http.Header{}
	}
	resp.Header.Set(k, v)
	return resp
}

func retryingPort(t *testing.T, tr deepseek.Transport, policy deepseek.RetryPolicy) *deepseek.Port {
	t.Helper()
	p, err := deepseek.New(deepseek.Config{
		Model: "m", Transport: tr, Environment: env{"DEEPSEEK_API_KEY": "k"},
		MaxOutputTokens: 8, Retry: policy,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

// TestTheShippedBudgetSendsOneRequest: the default is no retry, so an ordinary
// call is one billable request whatever the failure.
func TestTheShippedBudgetSendsOneRequest(t *testing.T) {
	tr := &retryTransport{responses: []*http.Response{status(503, `{"error":{"message":"busy"}}`)}}
	p := newPort(t, tr, env{"DEEPSEEK_API_KEY": "k"})
	if _, err := p.Generate(context.Background(), ai.Request{Model: "m"}); err == nil {
		t.Fatal("expected a failure")
	}
	if tr.requests != 1 {
		t.Fatalf("the shipped budget sent %d requests", tr.requests)
	}
}

// TestARetryableFailureIsRetriedUnderAPositiveBudget: observed by counting, not
// by reading the policy.
func TestARetryableFailureIsRetriedUnderAPositiveBudget(t *testing.T) {
	tr := &retryTransport{responses: []*http.Response{
		status(503, `{"error":{"message":"busy"}}`),
		sse(`{"choices":[{"delta":{"content":"recovered"},"finish_reason":"stop"}]}`),
	}}
	p := retryingPort(t, tr, deepseek.RetryPolicy{MaxRetries: 2, BaseDelay: time.Millisecond})

	resp, err := p.Generate(context.Background(), ai.Request{Model: "m"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if tr.requests != 2 {
		t.Fatalf("made %d requests, want 2", tr.requests)
	}
	if resp.Content != "recovered" {
		t.Fatalf("content %q", resp.Content)
	}
}

// TestAnExhaustedBalanceIsTerminalBeforeAnyRetry: the ordering that costs money
// when it is the other way round.
func TestAnExhaustedBalanceIsTerminalBeforeAnyRetry(t *testing.T) {
	tr := &retryTransport{responses: []*http.Response{
		status(402, `{"error":{"message":"Insufficient Balance"}}`),
		sse(`{"choices":[{"delta":{"content":"never"},"finish_reason":"stop"}]}`),
	}}
	p := retryingPort(t, tr, deepseek.RetryPolicy{MaxRetries: 5, BaseDelay: time.Millisecond})

	_, err := p.Generate(context.Background(), ai.Request{Model: "m"})
	var classified *deepseek.Error
	if !errors.As(err, &classified) || classified.Failure != deepseek.FailureQuota {
		t.Fatalf("classified %v", err)
	}
	if tr.requests != 1 {
		t.Fatalf("an exhausted balance was retried %d times; each attempt spends balance it cannot have",
			tr.requests-1)
	}
}

// TestTheProvidersOwnInstructionOutranksTheStatus, in both directions.
func TestTheProvidersOwnInstructionOutranksTheStatus(t *testing.T) {
	t.Run("false stops a status that would be retried", func(t *testing.T) {
		tr := &retryTransport{responses: []*http.Response{
			withHeader(status(503, `{"error":{"message":"no"}}`), "x-should-retry", "false"),
			sse(`{"choices":[{"delta":{"content":"never"},"finish_reason":"stop"}]}`),
		}}
		p := retryingPort(t, tr, deepseek.RetryPolicy{MaxRetries: 3, BaseDelay: time.Millisecond})
		if _, err := p.Generate(context.Background(), ai.Request{Model: "m"}); err == nil {
			t.Fatal("expected a failure")
		}
		if tr.requests != 1 {
			t.Fatalf("made %d requests despite x-should-retry: false", tr.requests)
		}
	})

	t.Run("true retries a status that would not be", func(t *testing.T) {
		tr := &retryTransport{responses: []*http.Response{
			withHeader(status(400, `{"error":{"message":"odd"}}`), "x-should-retry", "true"),
			sse(`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`),
		}}
		p := retryingPort(t, tr, deepseek.RetryPolicy{MaxRetries: 3, BaseDelay: time.Millisecond})
		if _, err := p.Generate(context.Background(), ai.Request{Model: "m"}); err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if tr.requests != 2 {
			t.Fatalf("made %d requests despite x-should-retry: true", tr.requests)
		}
	})

	t.Run("it never overrides an exhausted balance", func(t *testing.T) {
		tr := &retryTransport{responses: []*http.Response{
			withHeader(status(402, `{"error":{"message":"Insufficient Balance"}}`), "x-should-retry", "true"),
			sse(`{"choices":[{"delta":{"content":"never"},"finish_reason":"stop"}]}`),
		}}
		p := retryingPort(t, tr, deepseek.RetryPolicy{MaxRetries: 3, BaseDelay: time.Millisecond})
		if _, err := p.Generate(context.Background(), ai.Request{Model: "m"}); err == nil {
			t.Fatal("expected a failure")
		}
		if tr.requests != 1 {
			t.Fatalf("a header talked this into retrying an exhausted balance %d times", tr.requests-1)
		}
	})
}

// TestAServerRequestedWaitBeyondTheCapIsRefused rather than slept: a process
// that appears to hang is worse than a failure that explains itself.
func TestAServerRequestedWaitBeyondTheCapIsRefused(t *testing.T) {
	tr := &retryTransport{responses: []*http.Response{
		withHeader(status(429, `{"error":{"message":"slow down"}}`), "retry-after", "600"),
	}}
	p := retryingPort(t, tr, deepseek.RetryPolicy{
		MaxRetries: 3, BaseDelay: time.Millisecond, MaxDelay: time.Second,
	})

	start := time.Now()
	_, err := p.Generate(context.Background(), ai.Request{Model: "m"})
	if err == nil {
		t.Fatal("expected a failure")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("slept %v for a wait beyond the cap", elapsed)
	}
	if !strings.Contains(err.Error(), "cap") {
		t.Fatalf("the refusal did not explain itself: %v", err)
	}
	if tr.requests != 1 {
		t.Fatalf("made %d requests", tr.requests)
	}
}

// TestTheServerRequestedWaitIsHonoured, in milliseconds when offered.
func TestTheServerRequestedWaitIsHonoured(t *testing.T) {
	tr := &retryTransport{responses: []*http.Response{
		withHeader(status(429, `{"error":{"message":"slow"}}`), "retry-after-ms", "120"),
		sse(`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`),
	}}
	p := retryingPort(t, tr, deepseek.RetryPolicy{
		MaxRetries: 2, BaseDelay: time.Hour, MaxDelay: time.Minute,
	})

	start := time.Now()
	if _, err := p.Generate(context.Background(), ai.Request{Model: "m"}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 100*time.Millisecond {
		t.Fatalf("retried after %v, ignoring the requested wait", elapsed)
	}
	if elapsed > 30*time.Second {
		t.Fatalf("used the backoff instead of the requested wait: %v", elapsed)
	}
}

// TestCancellingABackoffStaysCancellation.
func TestCancellingABackoffStaysCancellation(t *testing.T) {
	tr := &retryTransport{responses: []*http.Response{
		status(503, `{"error":{"message":"busy"}}`),
		sse(`{"choices":[{"delta":{"content":"never"},"finish_reason":"stop"}]}`),
	}}
	// The credential comes from the store, so resolution does not consult the
	// context: the backoff is then the only thing that can notice cancellation,
	// which is what this test is for.
	store := deepseek.NewMemoryStore()
	if _, err := store.Modify(context.Background(), "deepseek",
		func(deepseek.Stored, bool) (deepseek.Stored, bool, error) {
			return deepseek.NewAPIKey("k"), true, nil
		}); err != nil {
		t.Fatal(err)
	}
	p, err := deepseek.New(deepseek.Config{
		Model: "m", Transport: tr, Environment: env{}, Store: store, MaxOutputTokens: 8,
		Retry: deepseek.RetryPolicy{MaxRetries: 3, BaseDelay: time.Hour},
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()

	_, genErr := p.Generate(ctx, ai.Request{Model: "m"})
	if !errors.Is(genErr, context.Canceled) {
		t.Fatalf("a cancelled backoff produced %v, which invites retrying what the caller stopped", genErr)
	}
	if tr.requests != 1 {
		t.Fatalf("made %d requests after cancellation", tr.requests)
	}
}

// TestInterleavedToolCallFragmentsStayApart: a provider may stream two calls at
// once, alternating their fragments. Closing one block when the other's
// fragment arrives leaves the first call's remaining arguments homeless.
func TestInterleavedToolCallFragmentsStayApart(t *testing.T) {
	tr := &countingTransport{respond: func(int) *http.Response {
		return sse(
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"tc0","type":"function","function":{"name":"first","arguments":"{\"a"}}]},"finish_reason":null}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"tc1","type":"function","function":{"name":"second","arguments":"{\"b"}}]},"finish_reason":null}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\":1}"}}]},"finish_reason":null}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":"\":2}"}}]},"finish_reason":null}]}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		)
	}}
	p := newPort(t, tr, env{"DEEPSEEK_API_KEY": "k"})

	resp, err := p.Generate(context.Background(), ai.Request{Model: "m"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(resp.ToolCalls) != 2 {
		t.Fatalf("two interleaved calls became %d: %+v", len(resp.ToolCalls), resp.ToolCalls)
	}
	want := map[string]string{"tc0": `{"a":1}`, "tc1": `{"b":2}`}
	for _, c := range resp.ToolCalls {
		if want[c.ID] != c.Args {
			t.Fatalf("call %s reassembled as %q, want %q", c.ID, c.Args, want[c.ID])
		}
	}
	if resp.ToolCalls[0].ID != "tc0" || resp.ToolCalls[1].ID != "tc1" {
		t.Fatalf("calls arrived out of the order the model asked for: %+v", resp.ToolCalls)
	}
}

// TestCachedPromptTokensAreNotCountedTwice: prompt_tokens already includes the
// cached part, so reporting it as input and then adding the cache count would
// inflate every cached request — and could invent an overflow on one that fits.
func TestCachedPromptTokensAreNotCountedTwice(t *testing.T) {
	tr := &countingTransport{respond: func(int) *http.Response {
		return sse(`{"choices":[{"delta":{"content":"x"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1000,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":600}}}`)
	}}
	p := newPort(t, tr, env{"DEEPSEEK_API_KEY": "k"})
	resp, err := p.Generate(context.Background(), ai.Request{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage.InputTokens != 400 {
		t.Fatalf("input reported as %d; 1000 prompt tokens of which 600 were cached is 400 uncached",
			resp.Usage.InputTokens)
	}
	if resp.Usage.CacheReadTokens == nil || *resp.Usage.CacheReadTokens != 600 {
		t.Fatalf("cache read reported as %v", resp.Usage.CacheReadTokens)
	}
}

// TestACancelledStreamStillEnds: a consumer that watched a reply arrive keeps
// what arrived, and is told the reply was aborted rather than left guessing at
// a closed channel.
func TestACancelledStreamStillEnds(t *testing.T) {
	// Long enough that cancelling after the second event certainly happens while
	// the stream still has more to give. With a short body the reader can reach
	// the end first, and a stream that ran out without a stop reason ends as an
	// error for that reason — a different outcome, not a flake.
	var sb strings.Builder
	for i := 0; i < 500; i++ {
		sb.WriteString("data: " + `{"choices":[{"delta":{"content":"x"},"finish_reason":null}]}` + "\n\n")
	}
	body := sb.String()
	tr := &countingTransport{respond: func(int) *http.Response {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}
	}}
	p := newPort(t, tr, env{"DEEPSEEK_API_KEY": "k"})

	ctx, cancel := context.WithCancel(context.Background())
	// Released on every path, including the t.Fatal below: the mid-stream
	// cancel this test performs is a separate act.
	defer cancel()
	events, err := p.Stream(ctx, ai.Request{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}

	var terminal *ai.AssistantMessage
	read := 0
	for ev := range events {
		read++
		if read == 2 {
			cancel()
		}
		if ev.Final != nil {
			terminal = ev.Final
		}
	}
	if terminal == nil {
		t.Fatal("a cancelled stream closed with no terminal event, so a consumer cannot tell " +
			"an abort from a completed reply")
	}
	if terminal.StopReason != ai.StopAborted {
		t.Fatalf("terminal reason %v, want %v", terminal.StopReason, ai.StopAborted)
	}
}

// TestTextAfterInterleavedCallsClosesEveryBlock: several tool-call blocks can be
// open at once, and text arriving afterwards must close all of them. Closing
// only the most recent leaves the reply unable to end.
func TestTextAfterInterleavedCallsClosesEveryBlock(t *testing.T) {
	tr := &countingTransport{respond: func(int) *http.Response {
		return sse(
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a","type":"function","function":{"name":"f","arguments":"{}"}}]},"finish_reason":null}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"b","type":"function","function":{"name":"g","arguments":"{}"}}]},"finish_reason":null}]}`,
			`{"choices":[{"delta":{"content":"and then some text"},"finish_reason":null}]}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		)
	}}
	p := newPort(t, tr, env{"DEEPSEEK_API_KEY": "k"})

	resp, err := p.Generate(context.Background(), ai.Request{Model: "m"})
	if err != nil {
		t.Fatalf("text following two interleaved calls: %v", err)
	}
	// Compared call by call: a reply that kept the count and lost the identities
	// answers the count and cannot be dispatched.
	if len(resp.ToolCalls) != 2 {
		t.Fatalf("kept %d calls, want 2", len(resp.ToolCalls))
	}
	for at, want := range []ai.ToolCall{
		{ID: "a", Name: "f", Args: "{}"},
		{ID: "b", Name: "g", Args: "{}"},
	} {
		if resp.ToolCalls[at] != want {
			t.Errorf("call %d is %+v, want %+v", at, resp.ToolCalls[at], want)
		}
	}
	if resp.Content != "and then some text" {
		t.Fatalf("content %q", resp.Content)
	}
}

// TestABlockEndsBeforeTheNextBegins: a consumer rendering incrementally is told
// a block is finished before it is told another exists. Leaving earlier blocks
// open until the reply ends still produces a valid message, but the events
// arrive in an order that says a block was still growing when it was not.
func TestABlockEndsBeforeTheNextBegins(t *testing.T) {
	tr := &countingTransport{respond: func(int) *http.Response {
		return sse(
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a","type":"function","function":{"name":"f","arguments":"{}"}}]},"finish_reason":null}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"b","type":"function","function":{"name":"g","arguments":"{}"}}]},"finish_reason":null}]}`,
			`{"choices":[{"delta":{"content":"text"},"finish_reason":null}]}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		)
	}}
	p := newPort(t, tr, env{"DEEPSEEK_API_KEY": "k"})

	events, err := p.Stream(context.Background(), ai.Request{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	openAt := map[int]bool{}
	for ev := range events {
		switch ev.Kind {
		case ai.StreamTextStart, ai.StreamThinkingStart, ai.StreamToolCallStart:
			// Every tool-call block may be open at once, but a text block must
			// not begin while any of them still is.
			if ev.Kind == ai.StreamTextStart {
				for at, still := range openAt {
					if still {
						t.Fatalf("text block %d began while block %d was still open",
							ev.ContentIndex, at)
					}
				}
			}
			openAt[ev.ContentIndex] = true
		case ai.StreamTextEnd, ai.StreamThinkingEnd, ai.StreamToolCallEnd:
			openAt[ev.ContentIndex] = false
		}
	}
}

// TestExhaustionInsideARateLimitIsStillTerminal.
//
// DeepSeek reports an exhausted balance as its own status, so status alone
// separates it from a throttle here. This control holds the rule that outlives
// that convenience: classification happens BEFORE the retry decision, so a
// provider that reports exhaustion inside a 429 is still not retried. Both
// fixtures carry the same status; only the typed cause differs.
func TestExhaustionInsideARateLimitIsStillTerminal(t *testing.T) {
	for _, tc := range []struct {
		name        string
		body        string
		wantReqs    int
		wantFailure deepseek.Failure
	}{
		{
			name:     "an ordinary throttle is retried",
			body:     `{"error":{"message":"You are sending requests too quickly","code":"rate_limit"}}`,
			wantReqs: 2,
		},
		{
			name:        "exhaustion carried in the same status is not",
			body:        `{"error":{"message":"You have run out of balance","code":"insufficient_balance"}}`,
			wantReqs:    1,
			wantFailure: deepseek.FailureQuota,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := &retryTransport{responses: []*http.Response{
				status(429, tc.body),
				sse(`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`),
			}}
			p, err := deepseek.New(deepseek.Config{
				Model: "m", Transport: tr, Environment: env{"DEEPSEEK_API_KEY": "k"},
				MaxOutputTokens:  8,
				Retry:            deepseek.RetryPolicy{MaxRetries: 3, BaseDelay: time.Millisecond},
				DetectExhaustion: deepseek.ExhaustionInBody,
			})
			if err != nil {
				t.Fatal(err)
			}
			_, genErr := p.Generate(context.Background(), ai.Request{Model: "m"})
			if tr.requests != tc.wantReqs {
				t.Fatalf("made %d requests, want %d", tr.requests, tc.wantReqs)
			}
			// The request count alone would pass with the wrong classification,
			// so the typed cause is asserted too — that is the half of "same
			// status, different cause" the count cannot see.
			if tc.wantFailure == "" {
				if genErr != nil {
					t.Fatalf("the retry succeeded but returned %v", genErr)
				}
				return
			}
			var classified *deepseek.Error
			if !errors.As(genErr, &classified) {
				t.Fatalf("failure %v is not classified", genErr)
			}
			if classified.Failure != tc.wantFailure {
				t.Fatalf("classified %s, want %s", classified.Failure, tc.wantFailure)
			}
			if classified.Failure.Retryable() {
				t.Fatalf("%s is marked retryable", classified.Failure)
			}
		})
	}
}

// changingEnv hands out a different key on each lookup, as a rotation or a
// refresh would while a call is in flight.
type changingEnv struct {
	keys    []string
	lookups int
}

func (c *changingEnv) Lookup(context.Context, string) (string, error) {
	key := c.keys[len(c.keys)-1]
	if c.lookups < len(c.keys) {
		key = c.keys[c.lookups]
	}
	c.lookups++
	return key, nil
}

// TestOneCallResolvesItsCredentialOnce.
//
// A failure is scrubbed of the key the request was sent with. Resolving again
// to do the scrubbing removes the key configured NOW from a message about a
// request sent with the key configured THEN, so a key that had just been
// replaced survives into the report of the very request that used it.
func TestOneCallResolvesItsCredentialOnce(t *testing.T) {
	const sent, replacement = "key-in-flight-4a71", "key-that-replaced-it"
	e := &changingEnv{keys: []string{sent, replacement}}
	// The provider echoes what it was given, as a proxy reporting a rejected
	// header does.
	tr := &countingTransport{respond: func(int) *http.Response {
		return status(401, `{"error":{"message":"rejected Authorization: `+sent+`"}}`)
	}}
	p := newPort(t, tr, e)

	_, err := p.Generate(context.Background(), ai.Request{
		Model: "deepseek-v4-flash", Messages: []ai.Message{{Role: ai.RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if strings.Contains(err.Error(), sent) {
		t.Fatalf("the key the request was sent with survived into the failure: %v", err)
	}
	if e.lookups != 1 {
		t.Fatalf("one call read the environment %d times", e.lookups)
	}
}

// TestARetriedCallKeepsTheCredentialItStartedWith: a retry is another attempt at
// the same call, not a new one. Re-reading between attempts would send two
// requests under two identities with nothing recording that it happened, and
// would scrub the second attempt's failure with a key the first one used.
func TestARetriedCallKeepsTheCredentialItStartedWith(t *testing.T) {
	const first, second = "key-the-call-began-with", "key-that-arrived-midway"
	e := &changingEnv{keys: []string{first, second}}
	tr := &countingTransport{respond: func(n int) *http.Response {
		if n == 1 {
			return status(503, `{"error":{"message":"try again"}}`)
		}
		return status(401, `{"error":{"message":"rejected `+first+`"}}`)
	}}
	p, err := deepseek.New(deepseek.Config{
		Model: "deepseek-v4-flash", Transport: tr, Environment: e, MaxOutputTokens: 16,
		Retry: deepseek.RetryPolicy{MaxRetries: 1},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, genErr := p.Generate(context.Background(), ai.Request{
		Model: "deepseek-v4-flash", Messages: []ai.Message{{Role: ai.RoleUser, Content: "hi"}},
	})
	if genErr == nil {
		t.Fatal("expected a refusal")
	}
	if tr.requests != 2 {
		t.Fatalf("made %d requests, want 2", tr.requests)
	}
	if e.lookups != 1 {
		t.Fatalf("a retried call read the environment %d times", e.lookups)
	}
	if strings.Contains(genErr.Error(), first) {
		t.Fatalf("the key the call was made with survived into the failure: %v", genErr)
	}
	for _, sent := range tr.bodies {
		if strings.Contains(sent, second) {
			t.Fatal("an attempt used a credential the call did not begin with")
		}
	}
}

// haltingBody delivers what it was given and then fails, as a body does when
// the transport underneath it is stopped mid-reply.
type haltingBody struct {
	prefix *strings.Reader
	err    error
}

func (h *haltingBody) Read(p []byte) (int, error) {
	if h.prefix.Len() > 0 {
		return h.prefix.Read(p)
	}
	return 0, h.err
}

func (h *haltingBody) Close() error { return nil }

// TestAStreamStoppedMidReplyEndsAborted: a stopped call is not a failed one.
// The caller's context stays live here, so only the error chain can say what
// happened — and reported as a failure, a deadline would leave as retryable
// while the cause disappeared from errors.Is entirely.
func TestAStreamStoppedMidReplyEndsAborted(t *testing.T) {
	for name, cause := range map[string]error{
		"a cancellation": context.Canceled,
		"a deadline":     context.DeadlineExceeded,
	} {
		t.Run(name, func(t *testing.T) {
			tr := &countingTransport{respond: func(int) *http.Response {
				return &http.Response{StatusCode: 200, Body: &haltingBody{
					prefix: strings.NewReader("data: " +
						`{"choices":[{"delta":{"content":"partial"}}]}` + "\n\n"),
					err: fmt.Errorf("body stopped: %w", cause),
				}}
			}}
			events, err := newPort(t, tr, env{"DEEPSEEK_API_KEY": "k"}).Stream(
				context.Background(), ai.Request{
					Model: "deepseek-v4-flash", Messages: []ai.Message{{Role: ai.RoleUser, Content: "hi"}},
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
				t.Fatalf("the stream delivered %d terminal events, want exactly 1", terminals)
			}
			if final == nil {
				t.Fatal("the terminal carried no reply")
			}
			if final.StopReason != ai.StopAborted {
				t.Fatalf("a stopped call ended as %q, not aborted", final.StopReason)
			}
			if !errors.Is(final.Cause, cause) {
				t.Fatalf("the cause was lost: %v", final.Cause)
			}
			if _, classified := ai.FailureOf(final.Cause); classified {
				t.Fatalf("a stopped call was reported as a provider failure: %v", final.Cause)
			}
			if ai.Retryable(final.Cause) {
				t.Fatalf("a stopped call was judged worth repeating: %v", final.Cause)
			}
			// What had already arrived is still there. A consumer that watched
			// a reply appear should not have it vanish because the call ended.
			var shown strings.Builder
			for _, b := range final.Blocks {
				shown.WriteString(b.Text)
			}
			if shown.String() != "partial" {
				t.Fatalf("content delivered before the stop was lost: %q", shown.String())
			}
		})
	}
}

// TestABodyReadFailureDoesNotCarryTheCallsKey: a read that fails partway can
// name the request it was reading. Left to the shape pass alone, a key that
// does not look like one survives into the report — and the key to remove is
// the one this call was made with, which re-resolving may no longer return.
func TestABodyReadFailureDoesNotCarryTheCallsKey(t *testing.T) {
	const secret = "7c1e-not-shaped-like-a-key"
	tr := &countingTransport{respond: func(int) *http.Response {
		return &http.Response{StatusCode: 200, Body: &haltingBody{
			prefix: strings.NewReader("data: " +
				`{"choices":[{"delta":{"content":"partial"}}]}` + "\n\n"),
			err: fmt.Errorf("proxy dropped the connection for Authorization=%s", secret),
		}}
	}}
	events, err := newPort(t, tr, env{"DEEPSEEK_API_KEY": secret}).Stream(
		context.Background(), ai.Request{
			Model: "deepseek-v4-flash", Messages: []ai.Message{{Role: ai.RoleUser, Content: "hi"}},
		})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var final *ai.AssistantMessage
	for ev := range events {
		if ev.Terminal() {
			final = ev.Final
		}
	}
	if final == nil || final.Cause == nil {
		t.Fatal("the stream ended without a failure")
	}
	if final.StopReason != ai.StopError {
		t.Fatalf("a broken stream ended as %q, not an error", final.StopReason)
	}
	if strings.Contains(final.Cause.Error(), secret) || strings.Contains(final.ErrorMessage, secret) {
		t.Fatalf("the key this call used reached the failure: %v", final.Cause)
	}
}

// TestAToolsArgumentSchemaReachesTheWire pins the last hop of the tool
// declaration, asserted against the bytes rather than against the struct.
//
// This gap was found by a live call, not by this suite: a tool with a declared
// schema was serialised as a name and a description, and the model answered
// with an argument object it had invented. Nothing failed loudly — the model
// called the tool, and the call simply arrived without the argument the tool
// requires. Only the bytes can show it, because every layer above them held a
// schema that this one dropped.
func TestAToolsArgumentSchemaReachesTheWire(t *testing.T) {
	tr := &countingTransport{respond: func(int) *http.Response {
		return sse(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`)
	}}
	p := newPort(t, tr, env{"DEEPSEEK_API_KEY": "k"})

	_, err := p.Generate(context.Background(), ai.Request{
		Model: "m",
		Tools: []ai.ToolSpec{{
			Name:        "read",
			Description: "Read a file",
			Parameters: json.RawMessage(
				`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
		}},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(tr.bodies) == 0 {
		t.Fatal("nothing was sent")
	}

	var sent struct {
		Tools []struct {
			Function struct {
				Name       string `json:"name"`
				Parameters *struct {
					Type       string              `json:"type"`
					Required   []string            `json:"required"`
					Properties map[string]struct { // a property that lost its type is one the model cannot fill
						Type string `json:"type"`
					} `json:"properties"`
				} `json:"parameters"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal([]byte(tr.bodies[0]), &sent); err != nil {
		t.Fatalf("the request is not JSON: %v", err)
	}
	if len(sent.Tools) != 1 {
		t.Fatalf("one tool was offered, %d reached the wire", len(sent.Tools))
	}

	params := sent.Tools[0].Function.Parameters
	if params == nil {
		t.Fatalf("the tool reached the wire with no argument shape:\n%s", tr.bodies[0])
	}
	if params.Type != "object" {
		t.Fatalf("the arguments arrived as %q", params.Type)
	}
	if got := params.Properties["path"].Type; got != "string" {
		t.Fatalf("path arrived typed as %q", got)
	}
	if len(params.Required) != 1 || params.Required[0] != "path" {
		t.Fatalf("required arrived as %v", params.Required)
	}
}

// TestAToolTakingNoArgumentsSendsNoShape is the other half. An empty object on
// the wire tells the model there is a shape to fill in, which is a different
// claim from taking nothing.
func TestAToolTakingNoArgumentsSendsNoShape(t *testing.T) {
	tr := &countingTransport{respond: func(int) *http.Response {
		return sse(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`)
	}}
	p := newPort(t, tr, env{"DEEPSEEK_API_KEY": "k"})

	_, err := p.Generate(context.Background(), ai.Request{
		Model: "m",
		Tools: []ai.ToolSpec{{Name: "ping", Description: "no arguments"}},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if strings.Contains(tr.bodies[0], "parameters") {
		t.Fatalf("a tool taking no arguments sent an argument shape:\n%s", tr.bodies[0])
	}
}

// TestAThinkingRequestReachesTheWire, and says nothing when nothing was asked.
//
// Silence is not the same as asking for none: a model that reasons by default
// keeps doing so, and turning that off is a decision a caller makes rather than
// one an absent field makes for them.
func TestAThinkingRequestReachesTheWire(t *testing.T) {
	cases := map[string]struct {
		level ai.ThinkingLevel
		want  string
	}{
		"nothing asked": {level: "", want: ""},
		"asked for off": {level: ai.ThinkingOff, want: `"thinking":{"type":"disabled"}`},
		"asked for it":  {level: ai.ThinkingHigh, want: `"thinking":{"type":"enabled"}`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			tr := &countingTransport{respond: func(int) *http.Response {
				return sse(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`)
			}}
			p := newPort(t, tr, env{"DEEPSEEK_API_KEY": "sk-test-credential"})
			if _, err := p.Generate(context.Background(),
				ai.Request{Model: "m", Thinking: c.level}); err != nil {
				t.Fatalf("Generate: %v", err)
			}

			body := tr.bodies[0]
			if c.want == "" {
				if strings.Contains(body, "thinking") {
					t.Fatalf("a request asking for nothing carried a thinking field:\n%s", body)
				}
				return
			}
			if !strings.Contains(body, c.want) {
				t.Fatalf("the wire does not carry %s:\n%s", c.want, body)
			}
		})
	}
}
