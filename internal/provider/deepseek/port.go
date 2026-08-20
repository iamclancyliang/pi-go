package deepseek

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/iamclancyliang/pi-go/internal/ai"
)

// DefaultBaseURL is the provider's documented endpoint.
const DefaultBaseURL = "https://api.deepseek.com"

// Transport is how a request reaches the network.
//
// Injected, and there is no default: a caller supplies it, so a test cannot
// reach the network by omission. It is also where requests are counted, which
// is how the cost of a call is established by observation rather than by
// reading configuration.
type Transport interface {
	Do(req *http.Request) (*http.Response, error)
}

// Config describes one provider instance.
type Config struct {
	// Model is required. There is no default: a request must name what it
	// wants, and no catalog exists here to consult.
	Model string

	// Transport and Environment are required.
	Transport   Transport
	Environment Environment

	// BaseURL defaults to DefaultBaseURL.
	BaseURL string

	// StoredKey, when set, wins over the environment.
	StoredKey string

	// MaxOutputTokens caps the reply. It reaches the wire as max_tokens, which
	// is the field this provider reads.
	MaxOutputTokens int

	// ContextWindow enables the count-based overflow checks when it is a value
	// someone measured or was given authoritatively. Zero leaves them off.
	//
	// A published figure is not such a value. One recorded request of 1,015,083
	// prompt tokens was accepted against a documented "1M", so a window taken
	// from that wording would classify accepted replies as overflows and buy a
	// shortened retry of each.
	ContextWindow int
}

// Port reaches DeepSeek.
type Port struct {
	cfg Config
}

// New builds a Port, rejecting a configuration that could not work.
//
// The checks are here rather than at the first call because a missing transport
// or an impossible window should fail where it is configured, not once a user is
// waiting for a reply.
func New(cfg Config) (*Port, error) {
	if cfg.Model == "" {
		return nil, fmt.Errorf("deepseek: a model is required")
	}
	if cfg.Transport == nil {
		return nil, fmt.Errorf("deepseek: a transport is required; there is no default")
	}
	if cfg.Environment == nil {
		return nil, fmt.Errorf("deepseek: an environment is required; there is no default")
	}
	if cfg.ContextWindow < 0 {
		return nil, fmt.Errorf("deepseek: context window %d is negative", cfg.ContextWindow)
	}
	if cfg.ContextWindow > 0 && cfg.ContextWindow <= acceptedPromptTokens {
		// A window at or below a size the provider is known to accept would
		// report accepted replies as overflows.
		return nil, fmt.Errorf(
			"deepseek: context window %d is at or below %d, a size this provider is recorded accepting",
			cfg.ContextWindow, acceptedPromptTokens)
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	return &Port{cfg: cfg}, nil
}

// acceptedPromptTokens is a recorded lower bound on what this provider accepts,
// from one request of that size answered with 200. It says nothing about where
// the real limit is, only that it is above this.
const acceptedPromptTokens = 1_015_083

type wireMessage struct {
	Role             string     `json:"role"`
	Content          string     `json:"content"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []wireCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
}

type wireCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Index    int    `json:"index,omitempty"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type wireRequest struct {
	Model    string        `json:"model"`
	Messages []wireMessage `json:"messages"`
	Stream   bool          `json:"stream"`

	// MaxTokens is deliberately `max_tokens`, which is the field this provider
	// reads. The modern OpenAI field is max_completion_tokens; sending that one
	// here is not rejected loudly — whether it is ignored is not documented —
	// so a cap sent under the wrong name may simply not exist.
	MaxTokens int `json:"max_tokens,omitempty"`

	// StreamOptions asks for usage. Without it the reply is fine and the ledger
	// is empty, so a spend check would compare against nothing.
	StreamOptions *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options,omitempty"`

	Tools []wireTool `json:"tools,omitempty"`
}

type wireTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"function"`
}

// buildRequest maps a Request onto the wire.
//
// `store` is never sent: this provider does not support it. Nothing here is read
// from a generated catalog, so the shape does not depend on an artefact this
// repository does not control.
func (p *Port) buildRequest(req ai.Request, stream bool, maxTokens int) wireRequest {
	model := req.Model
	if model == "" {
		model = p.cfg.Model
	}
	out := wireRequest{Model: model, Stream: stream, MaxTokens: maxTokens}
	if stream {
		out.StreamOptions = &struct {
			IncludeUsage bool `json:"include_usage"`
		}{IncludeUsage: true}
	}
	for _, m := range req.Messages {
		wm := wireMessage{Role: string(m.Role), Content: m.Content, ToolCallID: m.ToolCallID}
		for i, c := range m.ToolCalls {
			wc := wireCall{ID: c.ID, Type: "function", Index: i}
			wc.Function.Name = c.Name
			wc.Function.Arguments = c.Args
			wm.ToolCalls = append(wm.ToolCalls, wc)
		}
		out.Messages = append(out.Messages, wm)
	}
	for _, t := range req.Tools {
		wt := wireTool{Type: "function"}
		wt.Function.Name = t.Name
		wt.Function.Description = t.Description
		out.Tools = append(out.Tools, wt)
	}
	return out
}

func (p *Port) post(ctx context.Context, body wireRequest) (*http.Response, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("deepseek: encoding request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.cfg.BaseURL+"/chat/completions", bytes.NewReader(encoded))
	if err != nil {
		return nil, fmt.Errorf("deepseek: building request: %w", err)
	}
	cred, err := Resolve(ctx, p.cfg.Environment, p.cfg.StoredKey)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+cred.Key())
	resp, err := p.cfg.Transport.Do(httpReq)
	if err != nil {
		return nil, &Error{Failure: FailureTransient, Detail: err.Error()}
	}
	return resp, nil
}

// failureFrom builds a classified error from a non-2xx response.
func failureFrom(resp *http.Response) error {
	detail, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	return &Error{
		Failure: classifyStatus(resp.StatusCode),
		Status:  resp.StatusCode,
		Detail:  string(bytes.TrimSpace(detail)),
	}
}
