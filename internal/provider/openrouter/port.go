// Package openrouter reaches models through OpenRouter.
//
// An aggregator rather than a model vendor: one endpoint and one credential in
// front of many providers' models, addressed as "vendor/model". That is why it
// comes first among the ports eino-ext makes reachable — a single port opens
// far more than a single vendor's catalogue.
//
// It also means this port cannot know much about the model it is serving. Which
// vendor answers, what window it has, whether it reasons — all vary per model
// id, and none of it is recorded here (ADR-0007). The port carries what the
// caller configured and lets absent facts stay absent.
package openrouter

import (
	"context"
	"net/http"

	einoopenrouter "github.com/cloudwego/eino-ext/components/model/openrouter"
	"github.com/cloudwego/eino/components/model"

	"github.com/iamclancyliang/pi-go/internal/ai"
	cc "github.com/iamclancyliang/pi-go/internal/provider/chatcompletions"
)

// DefaultBaseURL is the provider's documented OpenAI-compatible endpoint.
const DefaultBaseURL = "https://openrouter.ai/api/v1"

// EnvVars is the ordered list of variables a credential may come from.
var EnvVars = []string{"OPENROUTER_API_KEY"}

// providerName labels a failure for diagnosis. Nothing branches on it.
const providerName = "openrouter"

// Resolve finds the credential this provider authenticates with.
//
// Which source wins, and how a blank value is treated, is the shared rule
// rather than this package's: a user configuring two providers expects one
// answer to "why did my key not take effect", not one per provider.
func Resolve(ctx context.Context, env ai.Environment, stored string) (ai.Credential, error) {
	return ai.ResolveCredential(ctx, providerName, env, stored, EnvVars)
}

// Config describes one provider instance.
type Config struct {
	// Model is the model this port serves, as OpenRouter addresses it —
	// "vendor/model", such as "anthropic/claude-sonnet-4".
	//
	// Not a default: a request names its own model, and one naming a different
	// model is refused rather than quietly served by this port.
	Model string

	// Transport carries requests. Required, and there is no default: a test
	// cannot reach the network by omission.
	Transport http.RoundTripper

	// Credential is the key this port authenticates with, already resolved.
	Credential ai.Credential

	// MaxOutputTokens caps the reply. Required and positive: a cap that can be
	// omitted is not a cap.
	MaxOutputTokens int

	// BaseURL defaults to DefaultBaseURL.
	BaseURL string

	// ContextWindow enables the count-based overflow checks. Zero leaves them
	// off, which is the safe direction — and zero is the normal case here,
	// since the window belongs to whichever vendor's model was addressed and
	// this repository records facts per model, not per aggregator.
	ContextWindow int

	// Referer and Title are OpenRouter's optional attribution headers. They
	// identify the calling application on its public leaderboards, and are sent
	// only when set: a caller who did not ask to be listed should not be.
	Referer string
	Title   string
}

// Port reaches OpenRouter.
type Port = cc.Port

// New builds a Port, refusing a configuration that could not work.
func New(cfg Config) (*Port, error) {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	return cc.New(cc.Config{
		Provider:        providerName,
		Model:           cfg.Model,
		BaseURL:         cfg.BaseURL,
		Transport:       attribute(cfg.Transport, cfg.Referer, cfg.Title),
		Credential:      cfg.Credential,
		MaxOutputTokens: cfg.MaxOutputTokens,
		ContextWindow:   cfg.ContextWindow,
		Classifier:      classifier{},
		NewModel:        newModel,
	})
}

// newModel builds this provider's adapter for one call.
//
// The attribution headers are not set here: they are HTTP headers, the
// adapter's config has no place for them, and putting them in the body would
// send them to a model as content. They go on the transport instead.
func newModel(ctx context.Context, req cc.ModelRequest) (model.ToolCallingChatModel, error) {
	outputCap := req.MaxOutputTokens
	return einoopenrouter.NewChatModel(ctx, &einoopenrouter.Config{
		APIKey:     req.APIKey,
		BaseURL:    req.BaseURL,
		Model:      req.Model,
		MaxTokens:  &outputCap,
		HTTPClient: req.HTTPClient,
	})
}

// scrub removes a credential from text about to become an error.
func scrub(text, key string) string { return ai.ScrubSecret(text, key) }

// attribute adds OpenRouter's optional attribution headers.
//
// Headers rather than request fields, which is what they are — the adapter's
// config has no place for them, and putting them in the body would send them
// to a model as content. Wrapped around the caller's transport so they are
// added once, on every request, without a port having to remember.
//
// Absent when unset. A caller who did not ask to appear on a public
// leaderboard should not.
func attribute(inner http.RoundTripper, referer, title string) http.RoundTripper {
	if referer == "" && title == "" {
		return inner
	}
	return &attributed{inner: inner, referer: referer, title: title}
}

type attributed struct {
	inner   http.RoundTripper
	referer string
	title   string
}

func (a *attributed) RoundTrip(req *http.Request) (*http.Response, error) {
	// Cloned before writing: a RoundTripper must not modify the request it was
	// given, and the caller may still be holding it.
	cloned := req.Clone(req.Context())
	if a.referer != "" {
		cloned.Header.Set("HTTP-Referer", a.referer)
	}
	if a.title != "" {
		cloned.Header.Set("X-Title", a.title)
	}
	return a.inner.RoundTrip(cloned)
}
