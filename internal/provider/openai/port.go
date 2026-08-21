package openai

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/iamclancyliang/pi-go/internal/ai"
)

// Credentials supplies the key for a call.
//
// The port RECEIVES a resolved credential rather than resolving one. Ownership
// of that decision — a stored value winning over the environment, a blank value
// counting as unset, absence being a typed failure — belongs in one place for
// every provider, not re-implemented per provider with its own subtly different
// order.
type Credentials interface {
	Resolve(ctx context.Context) (string, error)
}

// CredentialFunc adapts a function to Credentials.
type CredentialFunc func(ctx context.Context) (string, error)

// Resolve implements Credentials.
func (f CredentialFunc) Resolve(ctx context.Context) (string, error) { return f(ctx) }

// DefaultBaseURL is the provider's documented endpoint.
const DefaultBaseURL = "https://api.openai.com/v1"

// Config describes one provider instance.
//
// No raw key appears here. A configuration object is printed eventually, and a
// secret living on one goes with it.
type Config struct {
	// Model is required. There is no default and no catalog to consult.
	Model string

	// Transport carries requests. Required, and there is no default: a test
	// cannot reach the network by omission.
	Transport http.RoundTripper

	// Credentials supplies the key. Required, and there is no default: a test
	// cannot reach a real credential by omission.
	Credentials Credentials

	// MaxOutputTokens caps the reply. Required and positive: a cap that can be
	// omitted is not a cap.
	MaxOutputTokens int

	// BaseURL defaults to DefaultBaseURL.
	BaseURL string
}

// Port reaches OpenAI's Responses API.
type Port struct {
	cfg Config
}

// String and GoString keep configuration out of anything that formats a port.
func (p *Port) String() string {
	return "openai.Port{Model:" + p.cfg.Model + ", BaseURL:" + p.cfg.BaseURL + "}"
}
func (p *Port) GoString() string { return p.String() }

// New builds a Port, refusing a configuration that could not work.
func New(cfg Config) (*Port, error) {
	switch {
	case cfg.Model == "":
		return nil, fmt.Errorf("openai: a model is required")
	case cfg.Transport == nil:
		return nil, fmt.Errorf("openai: a transport is required; there is no default")
	case cfg.Credentials == nil:
		return nil, fmt.Errorf("openai: a credential source is required; there is no default")
	case cfg.MaxOutputTokens <= 0:
		return nil, fmt.Errorf("openai: MaxOutputTokens must be positive, got %d", cfg.MaxOutputTokens)
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	return &Port{cfg: cfg}, nil
}

// resolve asks for the credential this request authenticates with.
func (p *Port) resolve(ctx context.Context) (string, error) {
	key, err := p.cfg.Credentials.Resolve(ctx)
	if err != nil {
		return "", err
	}
	if key == "" {
		return "", fmt.Errorf("openai: the credential source returned nothing")
	}
	return key, nil
}

// scrub removes a credential from text about to become an error.
func scrub(text, key string) string {
	if key != "" {
		text = strings.ReplaceAll(text, key, "<redacted>")
	}
	return credentialShape.ReplaceAllString(text, "<redacted>")
}

// credentialShape matches key formats and bearer headers, so a value that is
// not the configured key still does not survive into a report.
var credentialShape = regexp.MustCompile(`(?i)(sk-[A-Za-z0-9_-]{4,}|bearer\s+\S+)`)

// usageFrom turns a captured terminal into this repository's usage.
//
// Every field stays absent unless the provider reported it. The adapter's own
// conversion cannot do this, which is the whole reason the terminal is captured
// before it runs.
func usageFrom(t terminal) ai.Usage {
	used := ai.Usage{}
	if t.InputTokens == nil && t.OutputTokens == nil &&
		t.CachedTokens == nil && t.ReasoningTokens == nil {
		return used
	}
	used.Reported = true
	if t.InputTokens != nil {
		used.InputTokens = *t.InputTokens
	}
	if t.OutputTokens != nil {
		used.OutputTokens = *t.OutputTokens
	}
	if t.CachedTokens != nil {
		v := *t.CachedTokens
		used.CacheReadTokens = &v
		// Input is the uncached remainder, as it is elsewhere in this
		// repository: the provider reports the whole prompt.
		used.InputTokens -= v
		if used.InputTokens < 0 {
			used.InputTokens = 0
		}
	}
	if t.ReasoningTokens != nil {
		v := *t.ReasoningTokens
		used.ReasoningTokens = &v
	}
	return used
}

// failureFromStatus maps a provider status onto this repository's outcomes.
//
// The set is the same one every provider in this repository answers with, so a
// caller branches on the outcome rather than on which provider produced it.
func failureFromStatus(status, incomplete, errorCode string) (ai.StopReason, error) {
	// A failure reported inside a 200 can name its own reason. Classifying by
	// the ending alone would call an exhausted balance an interruption, which
	// reads as "try again later" for something that cannot succeed.
	if failure, ok := failureFromCode(errorCode); ok {
		return ai.StopError, &Error{Failure: failure, Detail: errorCode}
	}
	switch status {
	case "completed":
		return ai.StopEnd, nil
	case "incomplete":
		// A reply the provider cut short. "max_output_tokens" is the cap doing
		// its job; anything else is a failure this repository has not mapped.
		if incomplete == "max_output_tokens" {
			return ai.StopLength, nil
		}
		if incomplete == "content_filter" {
			return ai.StopError, &Error{Failure: FailureRefused,
				Detail: "the provider's filters removed the content"}
		}
		return ai.StopError, &Error{Failure: FailureInterrupted,
			Detail: "reply incomplete: " + incomplete}
	case "failed", "cancelled", "expired":
		return ai.StopError, &Error{Failure: FailureInterrupted, Detail: "reply " + status}
	case "":
		return ai.StopError, &Error{Failure: FailureUnknown,
			Detail: "the provider reported no status"}
	default:
		// An unrecognised terminal state cannot be assumed complete.
		return ai.StopError, &Error{Failure: FailureUnknown,
			Detail: fmt.Sprintf("unrecognised status %q", status)}
	}
}

// failureFromCode maps the provider's own error code onto a classification.
func failureFromCode(code string) (Failure, bool) {
	switch code {
	case "insufficient_quota", "billing_hard_limit_reached", "account_deactivated":
		return FailureQuota, true
	case "invalid_api_key", "invalid_organization":
		return FailureAuth, true
	case "rate_limit_exceeded":
		return FailureThrottled, true
	case "server_error":
		return FailureTransient, true
	case "content_filter":
		return FailureRefused, true
	}
	return "", false
}
