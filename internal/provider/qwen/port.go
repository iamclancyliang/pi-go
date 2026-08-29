// Package qwen reaches Qwen's OpenAI-compatible endpoint.
//
// A thin wrapper now: the streaming, the capture and the message conversion are
// the chat-completions dialect's and live in internal/provider/chatcompletions.
// What remains here is what is genuinely Qwen's — where its endpoint is, which
// variable carries its key, and what its statuses and error codes mean.
package qwen

import (
	"context"
	"net/http"

	einoqwen "github.com/cloudwego/eino-ext/components/model/qwen"
	"github.com/cloudwego/eino/components/model"

	"github.com/iamclancyliang/pi-go/internal/ai"
	cc "github.com/iamclancyliang/pi-go/internal/provider/chatcompletions"
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
type Config struct {
	// Model is the model this port serves. Required, and not a default: a
	// request names its own model, and one naming a different model is refused
	// rather than quietly served by this port.
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
	// off, which is the safe direction.
	ContextWindow int
}

// Port reaches Qwen.
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
		Transport:       cfg.Transport,
		Credential:      cfg.Credential,
		MaxOutputTokens: cfg.MaxOutputTokens,
		ContextWindow:   cfg.ContextWindow,
		Classifier:      classifier{},
		NewModel:        newModel,
	})
}

// newModel builds this provider's adapter for one call.
func newModel(ctx context.Context, req cc.ModelRequest) (model.ToolCallingChatModel, error) {
	outputCap := req.MaxOutputTokens
	return einoqwen.NewChatModel(ctx, &einoqwen.ChatModelConfig{
		APIKey:     req.APIKey,
		BaseURL:    req.BaseURL,
		Model:      req.Model,
		MaxTokens:  &outputCap,
		HTTPClient: req.HTTPClient,
	})
}

// scrub removes a credential from text about to become an error.
func scrub(text, key string) string { return ai.ScrubSecret(text, key) }
