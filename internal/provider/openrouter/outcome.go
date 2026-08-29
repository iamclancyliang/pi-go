package openrouter

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/iamclancyliang/pi-go/internal/ai"
)

// The failure vocabulary is shared, not this package's own: a caller of the
// model boundary does not know which provider answered, so it must be able to
// tell an exhausted balance from a throttle without learning either provider.
// What stays here is the mapping — statuses, the provider's own codes, and its
// wording — because only this package knows how this provider says things.
type Failure = ai.Failure

const (
	FailureQuota       = ai.FailureQuota
	FailureAuth        = ai.FailureAuth
	FailureRefused     = ai.FailureRefused
	FailureThrottled   = ai.FailureThrottled
	FailureTransient   = ai.FailureTransient
	FailureInterrupted = ai.FailureInterrupted
	FailureUnknown     = ai.FailureUnknown
)

// Error is this provider's failure, carried in the shared type.
type Error = ai.ProviderError

// fail builds a classified failure from this provider.
func fail(f Failure, status int, detail string) *Error {
	return &Error{Provider: providerName, Failure: f, Status: status, Detail: detail}
}

// stopped reports an error that says the call was stopped rather than failed.
func stopped(err error) bool { return ai.Stopped(err) }

// wireFailure types an error that came from the wire rather than from a
// response this package could classify.
func wireFailure(stage, key string, err error) error {
	return ai.WireFailure(providerName, stage, key, err)
}

// classifyStatus maps an HTTP status onto a failure.
//
// The codes are OpenRouter's documented ones, and two of them are why this
// mapping cannot be shared with the other OpenAI-compatible ports:
//
//   - 403 is a MODERATION refusal here, not an authentication failure. Reading
//     it as auth would send a user to check a key that is working fine while
//     the actual problem is what they asked for.
//   - 502 and 503 are about the model behind the aggregator rather than the
//     aggregator itself — one is down, the other has no provider available for
//     that model. Both are worth another attempt, and both would read as an
//     ordinary server fault under a naive 5xx rule.
func classifyStatus(status int) Failure {
	switch status {
	case http.StatusUnauthorized:
		return FailureAuth
	case http.StatusPaymentRequired:
		// Documented as insufficient credits, which is an exhausted balance
		// rather than a throttle: retrying spends nothing and fixes nothing.
		return FailureQuota
	case http.StatusForbidden:
		return FailureRefused
	case http.StatusRequestTimeout:
		return FailureTransient
	case http.StatusTooManyRequests:
		return FailureThrottled
	case http.StatusBadGateway, http.StatusServiceUnavailable:
		return FailureTransient
	}
	switch {
	case status >= 500:
		return FailureTransient
	case status >= 400:
		return FailureRefused
	}
	return FailureUnknown
}

// wireError is the provider's own error report.
//
// OpenRouter wraps errors the OpenAI way and puts its own numeric status in
// `code`, which is why that field is read as a number rather than a name.
type wireError struct {
	Error struct {
		Code     int            `json:"code"`
		Message  string         `json:"message"`
		Metadata map[string]any `json:"metadata,omitempty"`
	} `json:"error"`
}

// message is the provider's own wording.
func (w wireError) message() string { return strings.TrimSpace(w.Error.Message) }

// moderated reports a refusal the provider attributes to content moderation.
//
// Recognised from the metadata key the provider attaches rather than from the
// message, because the message is the moderator's prose and changes. It is
// classified the same as any other refusal — the distinction is for the person
// reading the failure, who otherwise cannot tell "this cannot succeed" from
// "this model is unavailable".
func (w wireError) moderated() bool {
	if w.Error.Metadata == nil {
		return false
	}
	_, flagged := w.Error.Metadata["reasons"]
	return flagged
}

// failureFrom builds a classified error from a non-2xx response.
//
// The body is NOT copied verbatim. A provider that echoes the request — or a
// gateway that echoes headers — would put the credential into an error that a
// caller then logs. Only the provider's own message is kept, scrubbed even so.
func failureFrom(status int, raw []byte, key string) error {
	failure := classifyStatus(status)
	var body wireError
	detail := ""
	if err := json.Unmarshal(raw, &body); err == nil {
		detail = scrub(body.message(), key)
		if body.moderated() {
			// Said in the failure a person reads, not branched on: the
			// classification is the same refusal either way, but "this content
			// was refused" and "this model is unavailable" send someone to
			// look in very different places.
			detail = "moderation refused this request: " + detail
		}
	}
	if detail == "" {
		detail = "the provider refused this request"
	}
	return fail(failure, status, detail)
}

// retryAdvice reads this provider's own instruction about trying again.
//
// OpenRouter answers with Retry-After on a throttle, which says WHEN rather
// than WHETHER. Present at all means the provider expects another attempt to
// work; absent says nothing either way, which is not the same as "do not".
func retryAdvice(h http.Header) *bool {
	if strings.TrimSpace(h.Get("Retry-After")) == "" {
		return nil
	}
	yes := true
	return &yes
}

// classifier is what the shared chat-completions capture asks this package for.
type classifier struct{}

// Refusal classifies a non-2xx response.
func (classifier) Refusal(status int, body []byte, key string) error {
	return failureFrom(status, body, key)
}

// RetryAdvice reads this provider's own instruction about trying again.
func (classifier) RetryAdvice(h http.Header) *bool { return retryAdvice(h) }

// TerminalFailure classifies a failure reported inside a 200.
//
// OpenRouter reports mid-stream failures with the same numeric code it would
// have used as a status, so the status mapping answers this too — which is why
// there is no second table. A code that is not a status this repository maps
// leaves the ending to the finish reason.
func (classifier) TerminalFailure(code string) (error, bool) {
	status, err := strconv.Atoi(strings.TrimSpace(code))
	if err != nil {
		return nil, false
	}
	failure := classifyStatus(status)
	if failure == FailureUnknown {
		return nil, false
	}
	return fail(failure, status, "the reply failed with code "+code), true
}
