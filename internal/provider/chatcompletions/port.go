package chatcompletions

import (
	"context"
	"fmt"
	"net/http"

	"github.com/cloudwego/eino/components/model"

	"github.com/iamclancyliang/pi-go/internal/ai"
)

// ModelRequest is what a dialect needs to build the adapter's model.
//
// The fields a provider's component actually varies on. Anything a component
// takes that this does not carry is a knob no port here has needed, and adding
// one is a decision rather than a default.
type ModelRequest struct {
	APIKey     string
	BaseURL    string
	Model      string
	HTTPClient *http.Client

	// MaxOutputTokens caps the reply. It reaches the REQUEST rather than only
	// the construction: a cap that exists in a config and not on the wire is a
	// bill nobody chose.
	MaxOutputTokens int
}

// Config is a chat-completions port.
//
// Everything here except the last two fields is what any port needs. The last
// two are the whole of what a provider supplies: how to build its adapter's
// model, and how to read what its refusals mean.
type Config struct {
	// Provider labels a failure for diagnosis. Nothing branches on it.
	Provider string

	Model           string
	BaseURL         string
	Transport       http.RoundTripper
	Credential      ai.Credential
	MaxOutputTokens int

	// ContextWindow enables the count-based overflow checks. Zero leaves them
	// off, which is the safe direction: failing to detect an overflow reports
	// a refusal a caller already handles, while inventing one buys a shortened
	// context nobody needed.
	ContextWindow int

	// NewModel builds the adapter's model for one call.
	NewModel func(context.Context, ModelRequest) (model.ToolCallingChatModel, error)

	// Classifier reads what this provider's statuses and codes mean.
	Classifier Classifier
}

// Port is the shared implementation every chat-completions provider uses.
type Port struct {
	cfg Config
}

// String and GoString keep the configured credential out of anything that
// formats this value.
func (p *Port) String() string {
	return p.cfg.Provider + ".Port{Model:" + p.cfg.Model + ", BaseURL:" + p.cfg.BaseURL + "}"
}
func (p *Port) GoString() string { return p.String() }

// New builds a Port, refusing a configuration that could not work.
//
// The checks are here rather than at the first call because a missing transport
// or an impossible window should fail where it is configured, not once a user
// is waiting for a reply.
func New(cfg Config) (*Port, error) {
	switch {
	case cfg.Provider == "":
		return nil, fmt.Errorf("chatcompletions: a provider name is required")
	case cfg.Model == "":
		return nil, fmt.Errorf("%s: a model is required", cfg.Provider)
	case cfg.Transport == nil:
		return nil, fmt.Errorf("%s: a transport is required; there is no default", cfg.Provider)
	case cfg.NewModel == nil:
		return nil, fmt.Errorf("%s: no way to build the model was supplied", cfg.Provider)
	case cfg.Classifier == nil:
		return nil, fmt.Errorf("%s: no classifier was supplied", cfg.Provider)
	case cfg.Credential.Key() == "":
		// Typed rather than prose, and refused here rather than at the first
		// request: a caller can tell "nothing configured" from "the provider
		// rejected what we sent", and learns it before anything is billed.
		return nil, &ai.ProviderError{
			Provider: cfg.Provider, Failure: ai.FailureAuth,
			Detail: "no credential was supplied for this provider",
		}
	case cfg.MaxOutputTokens <= 0:
		return nil, fmt.Errorf("%s: MaxOutputTokens must be positive, got %d",
			cfg.Provider, cfg.MaxOutputTokens)
	case cfg.ContextWindow < 0:
		return nil, fmt.Errorf("%s: ContextWindow %d is negative", cfg.Provider, cfg.ContextWindow)
	}
	return &Port{cfg: cfg}, nil
}

// Generate answers in one piece.
//
// The stream collected, not a second implementation: two request-building paths
// drift, and only one of them ends up covered by the tests that matter.
func (p *Port) Generate(ctx context.Context, req ai.Request) (ai.Response, error) {
	events, err := p.Stream(ctx, req)
	if err != nil {
		return ai.Response{}, err
	}
	return ai.Collect(p.cfg.Provider, events)
}

// fail builds a classified failure from this provider.
func (p *Port) fail(f ai.Failure, status int, detail string) *ai.ProviderError {
	return &ai.ProviderError{Provider: p.cfg.Provider, Failure: f, Status: status, Detail: detail}
}

// wireFailure types an error that came from the wire rather than from a
// response this package could classify.
func (p *Port) wireFailure(stage, key string, err error) error {
	return ai.WireFailure(p.cfg.Provider, stage, key, err)
}

// overflow reports a context overflow inferred from reported counts.
//
// Absent usage disables the check rather than reading as zero: silence is not a
// measurement, and a window compared against a zero it invented would never
// fire — or, with the comparison the other way, always would.
func (p *Port) overflow(reason ai.StopReason, used ai.Usage) error {
	window := p.cfg.ContextWindow
	if window <= 0 || !used.Reported {
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

// Compile-time proof that this satisfies both boundaries.
var (
	_ ai.Port          = (*Port)(nil)
	_ ai.StreamingPort = (*Port)(nil)
)
