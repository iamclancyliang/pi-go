// Package qianfan reaches Baidu's Qianfan models.
//
// **It does not drive eino-ext's Qianfan component**, and that is the one thing
// worth reading before anything else here.
//
// That component cannot meet this repository's port contract. Its SDK builds
// its own client — `&http.Client{}` at
// bce-qianfan-sdk/go/qianfan@v0.0.14/requestor.go:195 — and neither the SDK nor
// the component accepts one, so there is no transport to hang anything on. Four
// obligations rest on that seam: counting the requests one call sends,
// classifying a refusal from the provider's own body before an SDK turns it
// into prose, keeping the credential out of an error, and running a test
// without spending money. Its credentials are also a process-wide singleton
// (config.go:90), so two ports in one process could not hold different keys.
//
// So this port speaks the provider's OpenAI-compatible v2 endpoint through the
// shared chat-completions dialect instead. ADR-0007 settles WHICH providers
// this repository reaches — the ones eino-ext's components name — not which
// component must be driven to reach them.
//
// What is given up is whatever the vendor SDK does beyond chat completions.
// What is kept is every property the other ports are held to, plus one they do
// not have: the failure vocabulary here is the SDK's own named constants rather
// than a reading of prose.
package qianfan

import (
	"context"
	"net/http"

	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"

	"github.com/iamclancyliang/pi-go/internal/ai"
	cc "github.com/iamclancyliang/pi-go/internal/provider/chatcompletions"
)

// providerName labels a failure for diagnosis. Nothing branches on it.
const providerName = "qianfan"

// DefaultBaseURL is the provider's OpenAI-compatible v2 endpoint.
//
// The host is the SDK's own console base URL
// (bce-qianfan-sdk/go/qianfan@v0.0.14/config.go:34); the `/v2` prefix is what
// makes it the compatible surface rather than the classic one, whose paths and
// authentication are different.
const DefaultBaseURL = "https://qianfan.baidubce.com/v2"

// EnvVars is the ordered list of variables a credential may come from.
//
// The SDK's own name for a bearer credential (config.go:29). The IAM
// access-key/secret-key pair it also accepts is deliberately not read here: it
// is a Baidu Cloud account credential rather than one scoped to this service,
// it is signed per request rather than sent, and treating the two as
// interchangeable would let a caller hand over the broad one by accident.
var EnvVars = []string{"QIANFAN_BEARER_TOKEN"}

// Resolve finds the credential for this provider.
func Resolve(ctx context.Context, env ai.Environment, stored string) (ai.Credential, error) {
	return ai.ResolveCredential(ctx, providerName, env, stored, EnvVars)
}

// Config describes one provider instance.
type Config struct {
	// Model is the model this port serves, as Baidu names it — "ernie-4.5-turbo-128k".
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
	// off.
	ContextWindow int
}

// Port reaches Qianfan.
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
		// This endpoint is the provider's OpenAI-compatible one, which is the
		// whole premise of reaching it this way. So the capture reads the
		// bytes, and this port keeps the served model, per-field usage presence
		// and the tool-call renumbering guard that the ports on vendor wires
		// have to give up.
		Wire: true,
	})
}

// newModel builds this provider's adapter for one call.
//
// The generic OpenAI-compatible component, because that is what the endpoint
// is. Nothing about this provider varies from it except the base URL and how
// its refusals read.
func newModel(ctx context.Context, req cc.ModelRequest) (model.ToolCallingChatModel, error) {
	outputCap := req.MaxOutputTokens
	return einoopenai.NewChatModel(ctx, &einoopenai.ChatModelConfig{
		APIKey:     req.APIKey,
		BaseURL:    req.BaseURL,
		Model:      req.Model,
		MaxTokens:  &outputCap,
		HTTPClient: req.HTTPClient,
	})
}

// scrub removes a credential from text about to become an error.
func scrub(text, key string) string { return ai.ScrubSecret(text, key) }
