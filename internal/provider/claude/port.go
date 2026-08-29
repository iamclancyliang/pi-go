// Package claude reaches Anthropic's models.
//
// It speaks the Messages API rather than the chat-completions dialect, so the
// capture cannot read its bytes: usage and the served model come from the
// framework's metadata, at the cost recorded on chatcompletions.MetaSource.
// Refusals are still classified, because those are ordinary JSON.
//
// Direct API access only. The component also reaches these models through
// Bedrock and Vertex, each with its own credential shape — AWS keys or a GCP
// service account — and neither is the Anthropic key this port resolves.
// Pretending one Config covers all three would let a caller supply an API key
// and reach a provider that never sees it.
package claude

import (
	"context"
	"net/http"

	einoclaude "github.com/cloudwego/eino-ext/components/model/claude"
	"github.com/cloudwego/eino/components/model"

	"github.com/iamclancyliang/pi-go/internal/ai"
	cc "github.com/iamclancyliang/pi-go/internal/provider/chatcompletions"
)

// providerName labels a failure for diagnosis. Nothing branches on it.
const providerName = "claude"

// EnvVars is the ordered list of variables a credential may come from.
//
// The key first: a token is the OAuth session's, which expires, and preferring
// it would make a run stop working for a reason the user never changed.
var EnvVars = []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"}

// Resolve finds the credential for this provider.
func Resolve(ctx context.Context, env ai.Environment, stored string) (ai.Credential, error) {
	return ai.ResolveCredential(ctx, providerName, env, stored, EnvVars)
}

// Config describes one provider instance.
type Config struct {
	// Model is the model this port serves, as Anthropic names it —
	// "claude-sonnet-4-5-20250929".
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
	//
	// This provider requires it on the wire as well — a Messages request
	// without max_tokens is refused — so there is no configuration where
	// leaving it out would have worked.
	MaxOutputTokens int

	// BaseURL overrides the component's own default. Empty leaves it, which is
	// the ordinary case; a value here is a proxy or an enterprise endpoint.
	BaseURL string

	// ContextWindow enables the count-based overflow checks. Zero leaves them
	// off.
	ContextWindow int
}

// Port reaches Anthropic.
type Port = cc.Port

// New builds a Port, refusing a configuration that could not work.
func New(cfg Config) (*Port, error) {
	// Wrapped only when there is something to wrap: wrapping nothing would
	// produce a non-nil transport that carries no requests, and the shared
	// port's "a transport is required" check would pass on a configuration
	// that cannot work.
	transport := cfg.Transport
	if transport != nil {
		transport = noInternalRetry{inner: transport}
	}
	return cc.New(cc.Config{
		Provider: providerName,
		Model:    cfg.Model,
		BaseURL:  cfg.BaseURL,
		// Wrapped so the vendor SDK cannot retry inside one call. Inside the
		// shared capture rather than outside it: the capture hands the
		// response back unchanged, so the header is already there when the SDK
		// reads it.
		Transport:       transport,
		Credential:      cfg.Credential,
		MaxOutputTokens: cfg.MaxOutputTokens,
		ContextWindow:   cfg.ContextWindow,
		Classifier:      classifier{},
		NewModel:        newModel,
		// The Messages API is not the chat-completions format, so the capture
		// cannot read the reply's bytes.
		Wire: false,
	})
}

// newModel builds this provider's adapter for one call.
func newModel(ctx context.Context, req cc.ModelRequest) (model.ToolCallingChatModel, error) {
	config := &einoclaude.Config{
		APIKey:     req.APIKey,
		Model:      req.Model,
		MaxTokens:  req.MaxOutputTokens,
		HTTPClient: req.HTTPClient,
	}
	if req.BaseURL != "" {
		// A pointer, so "not overridden" and "overridden to empty" stay
		// distinct — the component reads nil as its own default, and an empty
		// string as an endpoint that resolves to nothing.
		base := req.BaseURL
		config.BaseURL = &base
	}
	return einoclaude.NewChatModel(ctx, config)
}

// scrub removes the credential from text about to become an error.
func scrub(text, key string) string { return ai.ScrubSecret(text, key) }
