// Package gemini reaches Google's Gemini models.
//
// The component takes a client rather than a configuration, so this port builds
// one — which is where the transport is injected and where the backend is
// pinned. Its wire is not the chat-completions format, so usage and the served
// model come from the framework's metadata, at the cost recorded on
// chatcompletions.MetaSource. Refusals are still classified, because those are
// ordinary JSON.
package gemini

import (
	"context"
	"net/http"

	einogemini "github.com/cloudwego/eino-ext/components/model/gemini"
	"github.com/cloudwego/eino/components/model"
	"google.golang.org/genai"

	"github.com/iamclancyliang/pi-go/internal/ai"
	cc "github.com/iamclancyliang/pi-go/internal/provider/chatcompletions"
)

// providerName labels a failure for diagnosis. Nothing branches on it.
const providerName = "gemini"

// EnvVars is the ordered list of variables a credential may come from.
//
// Both are the SDK's own, and the more specific one is preferred: a machine
// with GOOGLE_API_KEY set for some other Google service should not have that
// key spent here by accident.
var EnvVars = []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"}

// Resolve finds the credential for this provider.
func Resolve(ctx context.Context, env ai.Environment, stored string) (ai.Credential, error) {
	return ai.ResolveCredential(ctx, providerName, env, stored, EnvVars)
}

// Config describes one provider instance.
type Config struct {
	// Model is the model this port serves, as Google names it —
	// "gemini-2.5-pro".
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

	// BaseURL overrides the SDK's own endpoint. Empty leaves it.
	BaseURL string

	// ContextWindow enables the count-based overflow checks. Zero leaves them
	// off.
	ContextWindow int
}

// Port reaches Gemini.
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
		NewModel:        newModel,
		// Gemini's wire is its own, not the chat-completions format the
		// capture parses.
		Wire: false,
	})
}

// newModel builds this provider's adapter for one call.
func newModel(ctx context.Context, req cc.ModelRequest) (model.ToolCallingChatModel, error) {
	clientConfig := &genai.ClientConfig{
		APIKey:     req.APIKey,
		HTTPClient: req.HTTPClient,
		// Pinned rather than left to the SDK's own detection. Unset, the SDK
		// reads GOOGLE_GENAI_USE_VERTEXAI from the environment and switches to
		// Vertex — a different endpoint, a different credential shape, and a
		// different bill — because of a variable this port never saw. A
		// provider is chosen by asking for it, not by what is exported.
		Backend: genai.BackendGeminiAPI,
	}
	if req.BaseURL != "" {
		clientConfig.HTTPOptions = genai.HTTPOptions{BaseURL: req.BaseURL}
	}
	client, err := genai.NewClient(ctx, clientConfig)
	if err != nil {
		return nil, err
	}
	maxTokens := req.MaxOutputTokens
	return einogemini.NewChatModel(ctx, &einogemini.Config{
		Client:    client,
		Model:     req.Model,
		MaxTokens: &maxTokens,
	})
}

// scrub removes the credential from text about to become an error.
func scrub(text, key string) string { return ai.ScrubSecret(text, key) }
