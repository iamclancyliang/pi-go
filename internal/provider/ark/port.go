// Package ark reaches Volcengine's Ark models.
//
// The component drives Volcengine's own SDK rather than the chat-completions
// adapter, so the capture cannot read this wire's bytes: usage and the served
// model come from the framework's metadata, at the cost recorded on
// chatcompletions.MetaSource. Refusals are still classified, because those are
// ordinary JSON.
//
// API-key authentication only. The SDK also accepts an access-key/secret-key
// pair, which is a different credential with a different lifetime and a
// different blast radius — a Volcengine account key rather than a key scoped to
// this service. One Config covering both would let a caller supply the narrow
// one and reach a provider that wanted the broad one.
package ark

import (
	"context"
	"net/http"

	einoark "github.com/cloudwego/eino-ext/components/model/ark"
	"github.com/cloudwego/eino/components/model"

	"github.com/iamclancyliang/pi-go/internal/ai"
	cc "github.com/iamclancyliang/pi-go/internal/provider/chatcompletions"
)

// providerName labels a failure for diagnosis. Nothing branches on it.
const providerName = "ark"

// EnvVars is the ordered list of variables a credential may come from.
//
// The name Volcengine's own tooling uses, so a machine already set up for the
// vendor's CLI needs nothing further.
var EnvVars = []string{"ARK_API_KEY"}

// Resolve finds the credential for this provider.
func Resolve(ctx context.Context, env ai.Environment, stored string) (ai.Credential, error) {
	return ai.ResolveCredential(ctx, providerName, env, stored, EnvVars)
}

// Config describes one provider instance.
type Config struct {
	// Model is what this port serves. On this provider that is either a model
	// name or an ENDPOINT id — "ep-20240101120000-abcde" — which is an account's
	// own deployment rather than a name the vendor publishes.
	//
	// Not a default: a request names its own model, and one naming a different
	// model is refused rather than quietly served by this port. That matters
	// more here than elsewhere, because an endpoint id is billed to whoever
	// created it.
	Model string

	// Transport carries requests. Required, and there is no default: a test
	// cannot reach the network by omission.
	Transport http.RoundTripper

	// Credential is the key this port authenticates with, already resolved.
	Credential ai.Credential

	// MaxOutputTokens caps the reply. Required and positive: a cap that can be
	// omitted is not a cap.
	MaxOutputTokens int

	// BaseURL overrides the component's own default. Empty leaves it.
	BaseURL string

	// Region overrides the component's own default. Empty leaves it.
	Region string

	// ContextWindow enables the count-based overflow checks. Zero leaves them
	// off.
	ContextWindow int
}

// Port reaches Ark.
type Port = cc.Port

// New builds a Port, refusing a configuration that could not work.
func New(cfg Config) (*Port, error) {
	return cc.New(cc.Config{
		Provider:        providerName,
		Model:           cfg.Model,
		BaseURL:         cfg.BaseURL,
		Transport:       cfg.Transport,
		Credential:      cfg.Credential,
		MaxOutputTokens: cfg.MaxOutputTokens,
		ContextWindow:   cfg.ContextWindow,
		Classifier:      classifier{},
		NewModel:        newModel(cfg),
		// The vendor SDK's wire is not the chat-completions format this
		// capture parses. Claiming otherwise without a credential to check it
		// against would be a guess that fails as a missing terminal frame —
		// which reads as a broken stream rather than as a wrong assumption.
		Wire: false,
	})
}

// noRetries is what this port asks the vendor SDK for.
//
// The SDK retries twice by default, on 429 and every 5xx. That is three
// requests for one call: three bills, two of them for a decision the caller
// never made and never sees, since only the last failure survives. Every port
// here sends exactly one request per call, and this repository accounts for
// attempts explicitly rather than letting a dependency make them invisibly.
//
// Unlike the Anthropic SDK, this one exposes the knob, so the answer is given
// in the configuration rather than smuggled in through a response header.
var noRetries = 0

// newModel builds this provider's adapter for one call.
func newModel(cfg Config) func(context.Context, cc.ModelRequest) (model.ToolCallingChatModel, error) {
	return func(ctx context.Context, req cc.ModelRequest) (model.ToolCallingChatModel, error) {
		maxTokens := req.MaxOutputTokens
		config := &einoark.ChatModelConfig{
			APIKey:     req.APIKey,
			Model:      req.Model,
			MaxTokens:  &maxTokens,
			HTTPClient: req.HTTPClient,
			BaseURL:    req.BaseURL,
			Region:     cfg.Region,
			RetryTimes: &noRetries,
		}
		return einoark.NewChatModel(ctx, config)
	}
}

// scrub removes the credential from text about to become an error.
func scrub(text, key string) string { return ai.ScrubSecret(text, key) }
