package openai

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/iamclancyliang/pi-go/internal/ai"
)

// Environment is where a credential is read from.
//
// Injected, with no fallback to the process environment: omitting it fails
// rather than quietly reaching the real one.
type Environment interface {
	Lookup(ctx context.Context, name string) (string, error)
}

// EnvVars is the ordered list of variables a credential may come from.
var EnvVars = []string{"OPENAI_API_KEY"}

// ErrNoCredential reports that no credential was found.
var ErrNoCredential = errors.New("openai: no credential")

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

	// Environment supplies the credential. Required.
	Environment Environment

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
	case cfg.Environment == nil:
		return nil, fmt.Errorf("openai: an environment is required; there is no default")
	case cfg.MaxOutputTokens <= 0:
		return nil, fmt.Errorf("openai: MaxOutputTokens must be positive, got %d", cfg.MaxOutputTokens)
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	return &Port{cfg: cfg}, nil
}

// resolve produces the credential a request authenticates with.
func (p *Port) resolve(ctx context.Context) (string, error) {
	for _, name := range EnvVars {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		value, err := p.cfg.Environment.Lookup(ctx, name)
		if err != nil {
			return "", fmt.Errorf("openai: reading %s: %w", name, err)
		}
		if value != "" {
			return value, nil
		}
	}
	return "", fmt.Errorf("%w: %s is not set", ErrNoCredential, EnvVars[0])
}

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
func failureFromStatus(status string, incomplete string) (ai.StopReason, error) {
	switch status {
	case "completed":
		return ai.StopEnd, nil
	case "incomplete":
		// A reply the provider cut short. "max_output_tokens" is the cap doing
		// its job; anything else is a failure this repository has not mapped.
		if incomplete == "max_output_tokens" {
			return ai.StopLength, nil
		}
		return ai.StopError, fmt.Errorf("openai: reply incomplete: %s", incomplete)
	case "failed", "cancelled", "expired":
		return ai.StopError, fmt.Errorf("openai: reply %s", status)
	case "":
		return ai.StopError, errors.New("openai: the provider reported no status")
	default:
		// An unrecognised terminal state cannot be assumed complete.
		return ai.StopError, fmt.Errorf("openai: unrecognised status %q", status)
	}
}
