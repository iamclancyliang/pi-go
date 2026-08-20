package deepseek_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

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
