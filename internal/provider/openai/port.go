package openai

import (
	"context"
	"fmt"
	"net/http"

	"github.com/iamclancyliang/pi-go/internal/ai"
)

// EnvVars is the ordered list of variables a credential may come from.
var EnvVars = []string{"OPENAI_API_KEY"}

// Resolve finds the credential this provider authenticates with.
//
// Which source wins, and how a blank value is treated, is the shared rule
// rather than this package's: a user configuring two providers expects one
// answer to "why did my key not take effect", not one per provider.
func Resolve(ctx context.Context, env ai.Environment, stored string) (ai.Credential, error) {
	return ai.ResolveCredential(ctx, providerName, env, stored, EnvVars)
}

// DefaultBaseURL is the provider's documented endpoint.
const DefaultBaseURL = "https://api.openai.com/v1"

// Config describes one provider instance.
//
// No raw key appears here. A configuration object is printed eventually, and a
// secret living on one goes with it.
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
	// Required, and there is no default: a test cannot reach a real credential
	// by omission.
	//
	// A resolved value rather than a resolver on purpose. Resolution answers
	// "which configured source wins", which is one question for the process
	// and not one per request: a port that resolved on the request path would
	// let two calls a second apart authenticate as different identities with
	// nothing recording that they had. It also keeps a caller's resolver — and
	// whatever it holds — off the request path entirely.
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

// Port reaches OpenAI's Responses API.
type Port struct {
	cfg Config
}

// String and GoString keep a Config out of anything that formats it.
//
// A Config holds a resolved credential, and formatting one would print every
// field it can reach. Describing the Config instead of ranging over it keeps
// that impossible whatever a caller put in it.
func (c Config) String() string {
	return "openai.Config{Model:" + c.Model + ", BaseURL:" + c.BaseURL + "}"
}

// GoString matches String, so %#v cannot reach further than %v.
func (c Config) GoString() string { return c.String() }

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
	case cfg.Credential.Key() == "":
		// Typed rather than prose, and refused here rather than at the first
		// request: a caller can tell "nothing configured" from "the provider
		// rejected what we sent", and learns it before anything is billed.
		return nil, fail(FailureAuth, 0, "no credential was supplied for this provider")
	case cfg.MaxOutputTokens <= 0:
		return nil, fmt.Errorf("openai: MaxOutputTokens must be positive, got %d", cfg.MaxOutputTokens)
	case cfg.ContextWindow < 0:
		return nil, fmt.Errorf("openai: ContextWindow %d is negative", cfg.ContextWindow)
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
// Only the mapping from this provider's fields is local; what presence means,
// and how a cached prompt is counted, is the shared rule. The adapter's own
// conversion cannot do either, which is the whole reason the terminal is
// captured before it runs.
func usageFrom(t terminal) ai.Usage {
	return ai.ReportedCounts{
		InputTokens:     t.InputTokens,
		OutputTokens:    t.OutputTokens,
		CachedTokens:    t.CachedTokens,
		ReasoningTokens: t.ReasoningTokens,
	}.Usage()
}

// failureFromStatus maps a provider status onto this repository's outcomes.
//
// The set is the same one every provider in this repository answers with, so a
// caller branches on the outcome rather than on which provider produced it.
func failureFromStatus(status, incomplete, errorCode string) (ai.StopReason, error) {
	// An overflow reported inside a 200 is the same condition as one reported
	// by a status, and it leaves as the same sentinel. Classifying it by the
	// ending instead would call it an interruption, and an interruption is not
	// retried — so the one failure this repository can actually recover from,
	// by shortening, would be the one it gives up on.
	if errorCode == contextOverflowCode {
		return ai.StopError, fmt.Errorf("%w: the provider refused the request as too large",
			ai.ErrContextOverflow)
	}
	// A failure reported inside a 200 can name its own reason. Classifying by
	// the ending alone would call an exhausted balance an interruption, which
	// reads as "try again later" for something that cannot succeed.
	if failure, ok := failureFromCode(errorCode); ok {
		return ai.StopError, fail(failure, 0, errorCode)
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
			return ai.StopError, fail(FailureRefused, 0, "the provider's filters removed the content")
		}
		return ai.StopError, fail(FailureInterrupted, 0, "reply incomplete: "+incomplete)
	case "failed", "cancelled", "expired":
		return ai.StopError, fail(FailureInterrupted, 0, "reply "+status)
	case "":
		return ai.StopError, fail(FailureUnknown, 0, "the provider reported no status")
	default:
		// An unrecognised terminal state cannot be assumed complete.
		return ai.StopError, fail(FailureUnknown, 0, fmt.Sprintf("unrecognised status %q", status))
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

// overflow reports a context overflow inferred from reported counts.
//
// Absent usage disables the checks rather than reading as zero: treating
// silence as zero would make the second check fire on every reply.
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
		// Cached prompt tokens are cheaper, not smaller: they occupy the same
		// room, and counting only the uncached part would miss an overflow on
		// exactly the requests a cache makes common.
		input += *used.CacheReadTokens
	}

	// A reply that ended normally while its input exceeded the window: the
	// provider accepted more than fits and silently dropped the rest.
	if reason == ai.StopEnd && input > window {
		return fmt.Errorf("%w: %d input tokens against a %d window",
			ai.ErrContextOverflow, input, window)
	}
	// A length stop that produced nothing, with the window full: the input
	// consumed the whole context and left no room to answer in.
	if reason == ai.StopLength && used.OutputTokens == 0 && input >= window*99/100 {
		return fmt.Errorf("%w: %d input tokens filled a %d window, leaving no output",
			ai.ErrContextOverflow, input, window)
	}
	return nil
}
