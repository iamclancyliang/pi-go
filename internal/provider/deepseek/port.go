package deepseek

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/iamclancyliang/pi-go/internal/ai"
)

// DefaultBaseURL is the provider's documented endpoint.
const DefaultBaseURL = "https://api.deepseek.com"

// Transport is how a request reaches the network.
//
// Injected, and there is no default: a caller supplies it, so a test cannot
// reach the network by omission. It is also where requests are counted, which
// is how the number of requests a call makes is established by observation
// rather than by reading configuration.
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

	// Store holds credentials. Optional: without one, a credential can only
	// come from the environment.
	//
	// No raw key appears on this struct. A configuration object is printed
	// eventually — by a log line, a test failure, %+v in someone's debugging —
	// and a secret that lives here would go with it. The store holds the value
	// unexported instead, so keeping it out of a report is a property of the
	// type rather than a rule everyone has to remember.
	Store Store

	// ProviderID keys this provider's credential in the store.
	ProviderID string

	// MaxOutputTokens caps the reply. It reaches the wire as max_tokens, which
	// is the field this provider reads.
	MaxOutputTokens int

	// ClassifyBody refines a status-derived failure using the response body.
	// Optional, and nil for this provider: DeepSeek reports an exhausted
	// balance as its own status, so nothing needs to read a body to find it.
	ClassifyBody BodyClassifier

	// Retry bounds retries of one request. The zero value is one request and no
	// retry, which is what ships.
	Retry RetryPolicy

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

// String and GoString keep the configured credential out of anything that
// formats this value. A struct holding a secret will eventually be printed —
// by a log line, a test failure, or %+v in someone's debugging — so the
// protection belongs on the type rather than on everyone who touches it.
func (p *Port) String() string {
	return "deepseek.Port{Model:" + p.cfg.Model + ", BaseURL:" + p.cfg.BaseURL + "}"
}
func (p *Port) GoString() string { return p.String() }

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
	if cfg.MaxOutputTokens <= 0 {
		// A zero would be dropped by omitempty and the request would carry no
		// cap at all. An uncapped reply is a bill nobody chose.
		return nil, fmt.Errorf("deepseek: MaxOutputTokens must be positive, got %d", cfg.MaxOutputTokens)
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
	if cfg.ProviderID == "" {
		cfg.ProviderID = "deepseek"
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
	MaxTokens int `json:"max_tokens"`

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
	// No fallback to the configured model. A request that names nothing would
	// otherwise reach whatever this port happened to be built with, and a
	// caller reading the reply would have no way to know which.
	out := wireRequest{Model: req.Model, Stream: stream, MaxTokens: maxTokens}
	if stream {
		out.StreamOptions = &struct {
			IncludeUsage bool `json:"include_usage"`
		}{IncludeUsage: true}
	}
	for _, m := range req.Messages {
		// This provider requires an assistant's reasoning to come back with the
		// next request. A history that carried it but did not resend it would
		// look complete and still break the conversation.
		wm := wireMessage{
			Role:             string(m.Role),
			Content:          m.Content,
			ReasoningContent: m.Reasoning,
			ToolCallID:       m.ToolCallID,
		}
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
	cred, err := p.resolve(ctx)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+cred.Key())
	resp, err := p.cfg.Transport.Do(httpReq)
	if err != nil {
		// Cancellation and a deadline are the caller's own outcomes, not the
		// provider's. Rewriting them as a transient provider failure would tell
		// a caller to retry the request it just cancelled, and would hide the
		// cause from errors.Is.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, &Error{Failure: FailureTransient, Detail: scrub(err.Error(), cred.Key())}
	}
	return resp, nil
}

// failureFrom builds a classified error from a non-2xx response.
//
// The body is NOT copied verbatim. A provider that echoes the request — or a
// proxy that echoes headers — would put the credential into an error that a
// caller then logs. Only the provider's own message field is kept, and it is
// scrubbed of anything credential-shaped even so.
func failureFrom(resp *http.Response, key string) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	return failureFromBytes(resp.StatusCode, raw, key)
}

// failureFromBytes classifies a failure from bytes already read.
func failureFromBytes(status int, raw []byte, key string) error {
	return failureWith(classifyStatus(status), status, raw, key)
}

// failureWith builds a failure whose classification was already decided.
func failureWith(failure Failure, status int, raw []byte, key string) error {
	var body struct {
		Error struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	detail := ""
	if err := json.Unmarshal(raw, &body); err == nil {
		detail = strings.TrimSpace(body.Error.Message)
		if body.Error.Code != "" {
			detail = body.Error.Code + ": " + detail
		}
	}
	if detail == "" {
		// Nothing recognisable. Report the shape, never the content.
		detail = fmt.Sprintf("unparsed body, %d bytes", len(raw))
	}
	return &Error{
		Failure: failure,
		Status:  status,
		Detail:  scrub(detail, key),
	}
}

// scrub removes the credential from text that is about to become an error.
func scrub(text, key string) string {
	if key != "" {
		text = strings.ReplaceAll(text, key, "<redacted>")
	}
	return credentialShape.ReplaceAllString(text, "<redacted>")
}

// credentialShape matches this provider's key format and bearer headers, so a
// value that is not the configured key still does not survive into a report.
var credentialShape = regexp.MustCompile(`(?i)(sk-[A-Za-z0-9_-]{4,}|bearer\s+\S+)`)

// credentialForScrubbing resolves the key only so that it can be removed from a
// message. It returns empty rather than failing: scrubbing must never be the
// thing that breaks reporting a failure.
func (p *Port) credentialForScrubbing(ctx context.Context) string {
	cred, err := p.resolve(ctx)
	if err != nil {
		return ""
	}
	return cred.Key()
}

// resolve produces the credential a request authenticates with.
//
// Distinct from Store.Read, which is for display: resolution is what a request
// uses, and keeping the two apart is what stops a caller authenticating with a
// value that was only ever meant to be shown.
func (p *Port) resolve(ctx context.Context) (Credential, error) {
	stored := ""
	if p.cfg.Store != nil {
		if held, err := p.cfg.Store.Read(ctx, p.cfg.ProviderID); err == nil {
			stored = held.Key()
		} else if !errors.Is(err, ErrNoStoredCredential) {
			return Credential{}, err
		}
	}
	return Resolve(ctx, p.cfg.Environment, stored)
}

// Attempt is what one request to the provider consumed.
//
// Kept per attempt rather than summed in place: a retried call spends on every
// attempt, and a ledger holding only the last one undercounts exactly the spend
// the retry created. A total is derived from these, never accumulated into.
type Attempt struct {
	Usage ai.Usage
}

// send performs the request, retrying within the configured budget.
//
// It returns the successful response and every attempt made to get it. The
// number of attempts is a fact about what was sent, which is why it travels
// with the response rather than being inferred from configuration.
func (p *Port) send(ctx context.Context, body wireRequest) (*http.Response, []Attempt, error) {
	var attempts []Attempt
	for attempt := 0; ; attempt++ {
		resp, err := p.post(ctx, body)
		if err != nil {
			if isCallerCancellation(err) {
				return nil, attempts, err
			}
			// A transport failure produced no response to read a header from.
			decision, capErr := decideRetry(nil, FailureTransient, attempt, p.cfg.Retry)
			if capErr != nil {
				return nil, attempts, capErr
			}
			if !decision.retry {
				return nil, attempts, withAttempts(err, attempts)
			}
			attempts = append(attempts, Attempt{})
			if waitErr := wait(ctx, decision.after); waitErr != nil {
				return nil, attempts, waitErr
			}
			continue
		}
		if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
			return resp, attempts, nil
		}

		// The body is read once, here, and used for both the classification and
		// the message: reading it twice would need it buffered anyway, and the
		// classification has to happen BEFORE the retry decision.
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		failure := classifyStatus(resp.StatusCode)
		if p.cfg.ClassifyBody != nil {
			if refined := p.cfg.ClassifyBody(resp.StatusCode, raw); refined != "" {
				failure = refined
			}
		}
		decision, capErr := decideRetry(resp, failure, attempt, p.cfg.Retry)
		if capErr != nil {
			resp.Body.Close()
			return nil, attempts, capErr
		}
		if !decision.retry {
			defer resp.Body.Close()
			// The attempt that failed last read the request too, so it joins
			// the earlier ones.
			final := append(attempts, Attempt{Usage: usageFromBytes(raw)})
			// The attempts behind this failure travel with it. Returning them
			// alongside an error nobody reads them from is how a call that
			// failed after several billed attempts ledgers nothing at all.
			return nil, final, withAttempts(
				failureWith(failure, resp.StatusCode, raw, p.credentialForScrubbing(ctx)), final)
		}
		// What a failed attempt reported using, if it said. A rate-limit body
		// rarely carries usage, but an attempt that did read the request and
		// then failed must not be recorded as free.
		attempts = append(attempts, Attempt{Usage: usageFromBytes(raw)})
		resp.Body.Close()
		if waitErr := wait(ctx, decision.after); waitErr != nil {
			return nil, attempts, waitErr
		}
	}
}

// usageFromBytes reads usage from an error body, when one is there.
func usageFromBytes(raw []byte) ai.Usage {
	var body struct {
		Usage *wireUsage `json:"usage"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || body.Usage == nil {
		return ai.Usage{}
	}
	return body.Usage.toUsage()
}

// withAttempts attaches what earlier attempts used to a classified failure, so
// the counts survive on the only thing the caller receives.
func withAttempts(err error, attempts []Attempt) error {
	var classified *Error
	if !errors.As(err, &classified) || len(attempts) == 0 {
		return err
	}
	used := make([]ai.Usage, 0, len(attempts))
	for _, a := range attempts {
		if a.Usage.Reported {
			used = append(used, a.Usage)
		}
	}
	if len(used) == 0 {
		return err
	}
	withUsage := *classified
	withUsage.Usage = append(append([]ai.Usage(nil), used...), classified.Usage...)
	return &withUsage
}
