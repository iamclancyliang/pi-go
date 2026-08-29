// Package ollama reaches a model running on this machine.
//
// The only provider here with no credential and no bill. That changes what the
// port has to be careful about — nothing is being spent — and what it must
// still get right: a local server that is not running, or is running without
// the model asked for, has to say so plainly rather than as a transport error.
//
// It speaks Ollama's own wire rather than the chat-completions one, so the
// capture cannot read its bytes. Refusals are still classified, because those
// are ordinary JSON; usage and the served model come from the framework's own
// metadata instead. What that costs is recorded on chatcompletions.MetaSource.
package ollama

import (
	"context"
	"net/http"
	"time"

	einoollama "github.com/cloudwego/eino-ext/components/model/ollama"
	"github.com/cloudwego/eino/components/model"

	"github.com/iamclancyliang/pi-go/internal/ai"
	cc "github.com/iamclancyliang/pi-go/internal/provider/chatcompletions"
)

// DefaultBaseURL is where Ollama listens when nobody has moved it.
const DefaultBaseURL = "http://localhost:11434"

// providerName labels a failure for diagnosis. Nothing branches on it.
const providerName = "ollama"

// EnvVars is the ordered list of variables a credential may come from.
//
// Empty, and that is the point: a local server needs no credential. It is
// declared so the shape matches every other provider — a caller enumerating
// them does not have to special-case the one that authenticates differently by
// authenticating not at all.
var EnvVars = []string{}

// Resolve answers with the placeholder credential a local server does not check.
//
// A credential is required by the shared port, which every other provider needs
// and this one does not. Rather than make the requirement optional — and lose
// the check that catches a missing key for the providers that do need one —
// this supplies a value that is obviously not a secret.
func Resolve(context.Context, ai.Environment, string) (ai.Credential, error) {
	return ai.StoredCredential(localPlaceholder, "a local server needs no credential"), nil
}

// localPlaceholder is not a secret and is never sent. It exists because the
// shared port refuses an empty credential, which is the right rule for every
// provider that has one.
const localPlaceholder = "local"

// Config describes one local model.
type Config struct {
	// Model is the model this port serves, as Ollama names it — "llama3.2" or
	// "qwen2.5-coder:7b". Required.
	Model string

	// Transport carries requests. Required, and there is no default: a test
	// cannot reach even localhost by omission.
	Transport http.RoundTripper

	// MaxOutputTokens caps the reply. Required and positive.
	MaxOutputTokens int

	// BaseURL defaults to DefaultBaseURL.
	BaseURL string

	// ContextWindow enables the count-based overflow checks. Zero leaves them
	// off. A local model's window is set when it is loaded rather than by the
	// provider, so this is the caller's to supply if they know it.
	ContextWindow int

	// KeepAlive is how long the server holds the model in memory after this
	// call. Zero leaves the server's own default.
	//
	// It matters more here than a timeout would elsewhere: loading a model is
	// seconds to minutes, so a session that lets it unload between turns pays
	// that on every turn.
	KeepAlive time.Duration
}

// Port reaches a local Ollama server.
type Port = cc.Port

// New builds a Port, refusing a configuration that could not work.
func New(cfg Config) (*Port, error) {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	credential, _ := Resolve(context.Background(), nil, "")
	return cc.New(cc.Config{
		Provider:        providerName,
		Model:           cfg.Model,
		BaseURL:         cfg.BaseURL,
		Transport:       cfg.Transport,
		Credential:      credential,
		MaxOutputTokens: cfg.MaxOutputTokens,
		ContextWindow:   cfg.ContextWindow,
		Classifier:      classifier{},
		NewModel:        newModel(cfg),
		// Ollama speaks its own wire, so the capture cannot read the reply's
		// bytes. Refusals are still classified; usage and the served model come
		// from the framework's metadata.
		Wire: false,
	})
}

// newModel builds this provider's adapter for one call.
func newModel(cfg Config) func(context.Context, cc.ModelRequest) (model.ToolCallingChatModel, error) {
	return func(ctx context.Context, req cc.ModelRequest) (model.ToolCallingChatModel, error) {
		options := &einoollama.Options{NumPredict: req.MaxOutputTokens}
		chatConfig := &einoollama.ChatModelConfig{
			BaseURL:    req.BaseURL,
			Model:      req.Model,
			HTTPClient: req.HTTPClient,
			Options:    options,
		}
		if cfg.KeepAlive > 0 {
			keepAlive := cfg.KeepAlive
			chatConfig.KeepAlive = &keepAlive
		}
		return einoollama.NewChatModel(ctx, chatConfig)
	}
}

// scrub removes a credential from text about to become an error.
//
// There is no credential to remove here, and it stays because the shared
// classifier signature carries one: a provider that quietly skipped scrubbing
// would be the one to get it wrong if it ever gained a key.
func scrub(text, key string) string { return ai.ScrubSecret(text, key) }
