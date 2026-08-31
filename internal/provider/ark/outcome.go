package ark

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/iamclancyliang/pi-go/internal/ai"
)

// The failure vocabulary is shared, not this package's own: a caller of the
// model boundary does not know which provider answered. What stays here is the
// mapping, because only this package knows how Ark says things.
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

// wireError is the SDK's own error envelope, taken from the type it decodes
// into rather than from prose about the API: `model.ErrorResponse` in
// volcengine-go-sdk/service/arkruntime, which carries a code, a message, a type
// and the request id.
type wireError struct {
	Error struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		Type      string `json:"type"`
		RequestID string `json:"request_id"`
	} `json:"error"`
}

// classifyStatus maps an HTTP status onto a failure.
//
// **The status is all this port classifies on**, and that is a real limitation
// rather than a preference. This provider sends its own error codes, and no
// source available here records what they are: the SDK carries no vocabulary of
// them — it decides retries from the status alone — and the vendor's
// documentation is rendered in a browser rather than served as text. A mapping
// written from memory would be a guess that looks like knowledge, and the way
// this repository has been wrong before is exactly that.
//
// What it costs is precision this provider could give: a moderation refusal and
// an exhausted balance both arrive somewhere in 4xx, and are reported as plain
// refusals. The code and the request id still reach the caller in the detail,
// so a person reading the failure has what a machine here could not use.
func classifyStatus(status int) Failure {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return FailureAuth
	case http.StatusPaymentRequired:
		return FailureQuota
	case http.StatusTooManyRequests:
		return FailureThrottled
	case http.StatusRequestTimeout:
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

// failureFrom builds a classified error from a refused response body.
//
// The body is not copied verbatim: a provider that echoes the request would put
// the credential into an error that a caller then logs. Only its own message
// survives, and only after scrubbing.
func failureFrom(status int, raw []byte, key string) error {
	var body wireError
	parsed := json.Unmarshal(raw, &body) == nil

	detail := ""
	if parsed {
		detail = strings.TrimSpace(body.Error.Message)
		if code := body.Error.Code; code != "" {
			// Carried rather than mapped: this port cannot say what the code
			// means, and dropping it would take that judgement away from the
			// person reading the failure too.
			detail = code + ": " + detail
		}
		if id := body.Error.RequestID; id != "" {
			// The one thing a user can give the provider's own support that
			// nothing here can reconstruct.
			detail += " (request " + id + ")"
		}
	}
	if strings.TrimSpace(detail) == "" {
		detail = fmt.Sprintf("unparsed body, %d bytes", len(raw))
	}
	return fail(classifyStatus(status), status, scrub(detail, key))
}

// classifier is what the shared port asks this package for.
type classifier struct{}

// Refusal classifies a non-2xx response.
func (classifier) Refusal(status int, body []byte, key string) error {
	return failureFrom(status, body, key)
}

// RetryAdvice: this provider sends no header saying whether to try again, and
// inventing one would tell a caller something the provider never said.
func (classifier) RetryAdvice(http.Header) *bool { return nil }

// TerminalFailure: a failure reported inside a 200 would arrive as an error
// code this port cannot classify, for the same reason the status is all it
// classifies on. Claiming otherwise would be classifying nothing.
func (classifier) TerminalFailure(string) (error, bool) { return nil, false }
