package conformance

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/provider/deepseek"
	"github.com/iamclancyliang/pi-go/internal/provider/openai"
)

// TestOneCallerHandlesBothProvidersWithoutKnowingWhich.
//
// A caller of the model boundary does not know which provider answered. If it
// had to switch on a provider's own error type to tell an exhausted balance
// from a throttle, the boundary would have hidden nothing — the dependency
// would just have moved into every caller. The classification below is read the
// same way for both, and nothing here names a provider.
func TestOneCallerHandlesBothProvidersWithoutKnowingWhich(t *testing.T) {
	// This is the caller. It sees only ai.Port and ai.Failure.
	decide := func(err error) (ai.Failure, bool) {
		return ai.FailureOf(err)
	}

	for _, tc := range []struct {
		name      string
		port      ai.Port
		want      ai.Failure
		retryable bool
	}{
		{
			name:      "openai exhausted balance",
			port:      openaiPortReturning(t, 429, `{"error":{"code":"insufficient_quota","message":"gone"}}`),
			want:      ai.FailureQuota,
			retryable: false,
		},
		{
			name:      "openai ordinary throttle",
			port:      openaiPortReturning(t, 429, `{"error":{"code":"rate_limit_exceeded","message":"slow"}}`),
			want:      ai.FailureThrottled,
			retryable: true,
		},
		{
			name:      "deepseek exhausted balance",
			port:      deepseekPortReturning(t, 402, `{"error":{"message":"Insufficient Balance"}}`),
			want:      ai.FailureQuota,
			retryable: false,
		},
		{
			name:      "deepseek ordinary throttle",
			port:      deepseekPortReturning(t, 429, `{"error":{"message":"too quick"}}`),
			want:      ai.FailureThrottled,
			retryable: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.port.Generate(context.Background(), ai.Request{
				Model:    modelFor(tc.port),
				Messages: []ai.Message{{Role: ai.RoleUser, Content: "hi"}},
			})
			got, ok := decide(err)
			if !ok {
				t.Fatalf("a caller could not classify %v without knowing the provider", err)
			}
			if got != tc.want {
				t.Fatalf("classified %s, want %s", got, tc.want)
			}
			if got.Retryable() != tc.retryable {
				t.Fatalf("retryable %v, want %v", got.Retryable(), tc.retryable)
			}
			// The same sentinel matches whichever provider produced it.
			if !errors.Is(err, &ai.ProviderError{Failure: tc.want}) {
				t.Fatalf("errors.Is did not match the shared classification for %v", err)
			}
			// Naming a provider narrows the match: a caller that deliberately
			// asks about one provider must not be answered about another.
			if errors.Is(err, &ai.ProviderError{Provider: "a-provider-that-did-not-answer", Failure: tc.want}) {
				t.Fatal("a failure matched a provider that did not produce it")
			}
		})
	}
}

// modelFor keeps the fixtures readable; a caller would already know its model.
func modelFor(port ai.Port) string {
	if _, ok := port.(*openai.Port); ok {
		return "gpt-test"
	}
	return "deepseek-v4-flash"
}

// TestAProvidersOwnRetryInstructionReachesOneCaller.
//
// Both providers are told the same thing by their transport — repeat this, or
// do not — and a caller that does not know which answered has to be able to act
// on it. A provider saying not to retry a 503 knows something about its own
// state that the status does not carry; an instruction that stops at the
// adapter is one that caller never learns of.
func TestAProvidersOwnRetryInstructionReachesOneCaller(t *testing.T) {
	for _, tc := range []struct {
		name string
		port ai.Port
		want bool
	}{
		{
			name: "a transient status the first provider says not to repeat",
			port: openaiPortRefusing(t, 503, `{"error":{"message":"down"}}`, "false"),
			want: false,
		},
		{
			name: "a transient status the second provider says not to repeat",
			port: deepseekPortRefusing(t, 503, `{"error":{"message":"down"}}`, "false"),
			want: false,
		},
		{
			name: "the same status from the first provider with no instruction",
			port: openaiPortRefusing(t, 503, `{"error":{"message":"down"}}`, ""),
			want: true,
		},
		{
			name: "the same status from the second provider with no instruction",
			port: deepseekPortRefusing(t, 503, `{"error":{"message":"down"}}`, ""),
			want: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.port.Generate(context.Background(), ai.Request{
				Model:    modelFor(tc.port),
				Messages: []ai.Message{{Role: ai.RoleUser, Content: "hi"}},
			})
			if err == nil {
				t.Fatal("expected a refusal")
			}
			if got := ai.Retryable(err); got != tc.want {
				t.Fatalf("Retryable %v, want %v, for %v", got, tc.want, err)
			}
		})
	}
}

func openaiPortRefusing(t *testing.T, status int, body, advice string) ai.Port {
	t.Helper()
	return openaiPortResponding(t, refusal(status, body, advice))
}

func deepseekPortRefusing(t *testing.T, status int, body, advice string) ai.Port {
	t.Helper()
	p, err := deepseek.New(deepseek.Config{
		Model: "deepseek-v4-flash", MaxOutputTokens: 16, Environment: staticEnv{},
		Transport: transportFunc(func(*http.Request) (*http.Response, error) {
			return refusal(status, body, advice), nil
		}),
	})
	if err != nil {
		t.Fatalf("deepseek.New: %v", err)
	}
	return p
}

// refusal is one recorded rejection, optionally carrying the provider's own
// instruction about trying again.
func refusal(status int, body, advice string) *http.Response {
	header := http.Header{"Content-Type": []string{"application/json"}}
	if advice != "" {
		header.Set("x-should-retry", advice)
	}
	return &http.Response{
		StatusCode: status,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func openaiPortResponding(t *testing.T, resp *http.Response) ai.Port {
	t.Helper()
	p, err := openai.New(openai.Config{
		Model: "gpt-test", MaxOutputTokens: 16,
		Credential: ai.StoredCredential("test-key", "a test"),
		Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return resp, nil
		}),
	})
	if err != nil {
		t.Fatalf("openai.New: %v", err)
	}
	return p
}

func openaiPortReturning(t *testing.T, status int, body string) ai.Port {
	t.Helper()
	p, err := openai.New(openai.Config{
		Model: "gpt-test", MaxOutputTokens: 16,
		Credential: ai.StoredCredential("test-key", "a test"),
		Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: status,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(body)),
			}, nil
		}),
	})
	if err != nil {
		t.Fatalf("openai.New: %v", err)
	}
	return p
}

func deepseekPortReturning(t *testing.T, status int, body string) ai.Port {
	t.Helper()
	p, err := deepseek.New(deepseek.Config{
		Model: "deepseek-v4-flash", MaxOutputTokens: 16, Environment: staticEnv{},
		Transport: transportFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: status,
				Body:       io.NopCloser(strings.NewReader(body)),
			}, nil
		}),
	})
	if err != nil {
		t.Fatalf("deepseek.New: %v", err)
	}
	return p
}

// roundTripperFunc and transportFunc adapt a function to the two shapes the two
// providers ask for: one takes an http.RoundTripper, the other its own Do.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

type staticEnv struct{}

func (staticEnv) Lookup(context.Context, string) (string, error) { return "test-key", nil }
