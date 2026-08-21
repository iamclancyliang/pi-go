package qwen

import (
	"context"
	"fmt"
	"net/http"

	"github.com/iamclancyliang/pi-go/internal/ai"
)

// DefaultBaseURL is the provider's documented OpenAI-compatible endpoint.
const DefaultBaseURL = "https://dashscope-intl.aliyuncs.com/compatible-mode/v1"

// EnvVars is the ordered list of variables a credential may come from.
var EnvVars = []string{"DASHSCOPE_API_KEY"}

// Resolve finds the credential this provider authenticates with.
//
// Which source wins, and how a blank value is treated, is the shared rule
// rather than this package's: a user configuring two providers expects one
// answer to "why did my key not take effect", not one per provider.
func Resolve(ctx context.Context, env ai.Environment, stored string) (ai.Credential, error) {
	return ai.ResolveCredential(ctx, providerName, env, stored, EnvVars)
}

// Config describes one provider instance.
//
// No raw key appears here beyond the resolved credential, which keeps itself
// out of anything that formats it.
type Config struct {
	// Model is the model this port serves. Required.
	//
	// It is not a default: a request names its own model, and one naming a
	// different model is refused rather than quietly served by this port. A
	// value that only gets validated and printed is a second source of truth
	// about which model is in play, and the wrong one to believe when
	// diagnosing a reply.
	Model string

	// Transport carries requests. Required, and there is no default: a test
	// cannot reach the network by omission.
	Transport http.RoundTripper

	// Credential is the key this port authenticates with, already resolved.
	// Required, and there is no default.
	//
	// A resolved value rather than a resolver: this provider reads no store, so
	// which source wins is settled once and nothing can vary between requests.
	Credential ai.Credential

	// MaxOutputTokens caps the reply. Required and positive: a cap that can be
	// omitted is not a cap.
	MaxOutputTokens int

	// BaseURL defaults to DefaultBaseURL.
	BaseURL string

	// ContextWindow enables the count-based overflow checks. Zero leaves them
	// off, which is the safe direction: failing to detect an overflow reports a
	// refusal a caller already handles, while inventing one buys a shortened
	// retry of a request that was fine.
	//
	// It must be a measured or authoritatively given value. A figure taken from
	// documentation is not evidence: a published context length is often
	// rounded, and a threshold below the real limit turns accepted replies into
	// overflows.
	ContextWindow int
}

// Port reaches Qwen's OpenAI-compatible endpoint.
type Port struct {
	cfg Config
}

// String and GoString keep a Config out of anything that formats it.
func (c Config) String() string {
	return "qwen.Config{Model:" + c.Model + ", BaseURL:" + c.BaseURL + "}"
}

// GoString matches String, so %#v cannot reach further than %v.
func (c Config) GoString() string { return c.String() }

// String and GoString keep configuration out of anything that formats a port.
func (p *Port) String() string {
	return "qwen.Port{Model:" + p.cfg.Model + ", BaseURL:" + p.cfg.BaseURL + "}"
}
func (p *Port) GoString() string { return p.String() }

// New builds a Port, refusing a configuration that could not work.
func New(cfg Config) (*Port, error) {
	switch {
	case cfg.Model == "":
		return nil, fmt.Errorf("qwen: a model is required")
	case cfg.Transport == nil:
		return nil, fmt.Errorf("qwen: a transport is required; there is no default")
	case cfg.Credential.Key() == "":
		// Typed rather than prose, and refused here rather than at the first
		// request: a caller can tell "nothing configured" from "the provider
		// rejected what we sent", and learns it before anything is billed.
		return nil, fail(FailureAuth, 0, "no credential was supplied for this provider")
	case cfg.MaxOutputTokens <= 0:
		return nil, fmt.Errorf("qwen: MaxOutputTokens must be positive, got %d", cfg.MaxOutputTokens)
	case cfg.ContextWindow < 0:
		return nil, fmt.Errorf("qwen: ContextWindow %d is negative", cfg.ContextWindow)
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	return &Port{cfg: cfg}, nil
}

// scrub removes a credential from text about to become an error.
func scrub(text, key string) string { return ai.ScrubSecret(text, key) }

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

// overflow reports a context overflow inferred from reported counts.
//
// Absent usage disables the check rather than reading as zero: silence is not a
// measurement, and a window compared against a zero it invented would never
// fire — or, with the comparison the other way, always would.
func (p *Port) overflow(reason ai.StopReason, used ai.Usage) error {
	window := p.cfg.ContextWindow
	if window <= 0 || !used.Reported {
		// The presence half cannot change an answer today: counts nobody
		// reported are zero, and zero exceeds no window that construction
		// already required to be positive. It stays because it is the premise
		// the comparisons below rest on — a check that reads silence as a
		// measurement is wrong even on the day it happens to agree.
		return nil
	}
	input := used.InputTokens
	if used.CacheReadTokens != nil {
		// Cached prompt tokens are cheaper than uncached ones and occupy the
		// same room. Counting only the uncached part would miss an overflow on
		// exactly the requests a cache makes common.
		input += *used.CacheReadTokens
	}
	if reason == ai.StopEnd && input > window {
		return fmt.Errorf("%w: the provider accepted %d input tokens against a %d window",
			ai.ErrContextOverflow, input, window)
	}
	if reason == ai.StopLength && input+used.OutputTokens > window {
		return fmt.Errorf("%w: %d input and output tokens against a %d window",
			ai.ErrContextOverflow, input+used.OutputTokens, window)
	}
	return nil
}
