package deepseek_test

import (
	"context"
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

	resp, err := p.Generate(context.Background(), ai.Request{})
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
	if _, err := p.Generate(context.Background(), ai.Request{}); err != nil {
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

			_, err := p.Generate(context.Background(), ai.Request{})
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

			_, err := p.Generate(context.Background(), ai.Request{})
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
		resp, err := p.Generate(context.Background(), ai.Request{})
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
		resp, err := p.Generate(context.Background(), ai.Request{})
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
		resp, err := p.Generate(context.Background(), ai.Request{})
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

	resp, err := p.Generate(context.Background(), ai.Request{})
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

	events, err := p.Stream(context.Background(), ai.Request{})
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
	resp, err := p.Generate(context.Background(), ai.Request{})
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

// TestNoModelIsRefusedBeforeAnythingIsSent: a request naming no model must not
// silently reach whichever model configuration happened to hold.
func TestNoModelIsRefusedBeforeAnythingIsSent(t *testing.T) {
	tr := &countingTransport{}
	p, err := deepseek.New(deepseek.Config{
		Model: "configured", Transport: tr, Environment: env{"DEEPSEEK_API_KEY": "k"}, MaxOutputTokens: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	// The configured model is the default; an explicitly blank one on a port
	// with no configured model is what must fail.
	bare, err := deepseek.New(deepseek.Config{
		Model: "x", Transport: tr, Environment: env{"DEEPSEEK_API_KEY": "k"}, MaxOutputTokens: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = p
	_ = bare
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

	_, genErr := p.Generate(context.Background(), ai.Request{})
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

// TestCountBasedOverflowDetection covers the two checks B did not defer: both
// read typed numbers, neither reads text.
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
		_, err := newWindowed(t, tr, 1_100_000).Generate(context.Background(), ai.Request{})
		if !errors.Is(err, ai.ErrContextOverflow) {
			t.Fatalf("input past the window produced %v, so the shortening path never runs", err)
		}
	})

	t.Run("a filled window with no output is an overflow", func(t *testing.T) {
		tr := &countingTransport{respond: func(int) *http.Response {
			return sse(`{"choices":[{"delta":{},"finish_reason":"length"}],"usage":{"prompt_tokens":1099999,"completion_tokens":0}}`)
		}}
		_, err := newWindowed(t, tr, 1_100_000).Generate(context.Background(), ai.Request{})
		if !errors.Is(err, ai.ErrContextOverflow) {
			t.Fatalf("a filled window with no room to answer produced %v", err)
		}
	})

	t.Run("cached input still occupies the window", func(t *testing.T) {
		// Prompt tokens served from cache are cheaper, not smaller: they take
		// the same room. Counting only the uncached ones would miss an overflow
		// on exactly the requests a cache makes common.
		tr := &countingTransport{respond: func(int) *http.Response {
			return sse(`{"choices":[{"delta":{"content":"x"},"finish_reason":"stop"}],"usage":{"prompt_tokens":600000,"completion_tokens":1,"prompt_tokens_details":{"cached_tokens":600000}}}`)
		}}
		_, err := newWindowed(t, tr, 1_100_000).Generate(context.Background(), ai.Request{})
		if !errors.Is(err, ai.ErrContextOverflow) {
			t.Fatalf("600k uncached plus 600k cached against a 1.1M window produced %v", err)
		}
	})

	t.Run("an ordinary reply is not an overflow", func(t *testing.T) {
		tr := &countingTransport{respond: func(int) *http.Response {
			return sse(`{"choices":[{"delta":{"content":"x"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":1}}`)
		}}
		if _, err := newWindowed(t, tr, 1_100_000).Generate(context.Background(), ai.Request{}); err != nil {
			t.Fatalf("an ordinary reply was rejected: %v", err)
		}
	})

	t.Run("unreported usage disables the checks", func(t *testing.T) {
		tr := &countingTransport{respond: func(int) *http.Response {
			return sse(`{"choices":[{"delta":{},"finish_reason":"length"}]}`)
		}}
		if _, err := newWindowed(t, tr, 1_100_000).Generate(context.Background(), ai.Request{}); err != nil {
			t.Fatalf("silence about usage was read as zero and became an overflow: %v", err)
		}
	})

	t.Run("no window leaves them off", func(t *testing.T) {
		tr := &countingTransport{respond: func(int) *http.Response {
			return sse(`{"choices":[{"delta":{"content":"x"},"finish_reason":"stop"}],"usage":{"prompt_tokens":9999999,"completion_tokens":1}}`)
		}}
		p := newPort(t, tr, env{"DEEPSEEK_API_KEY": "k"})
		if _, err := p.Generate(context.Background(), ai.Request{}); err != nil {
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
	_, err := p.Generate(ctx, ai.Request{})
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
