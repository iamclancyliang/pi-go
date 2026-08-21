package openai

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/iamclancyliang/pi-go/internal/ai"
)

// Failure is why a call did not produce a usable reply.
//
// Classification happens at the boundary, where the status and the provider's
// own error code still exist, and is carried as a value from there on. Nothing
// downstream reads an error's text: a provider that rewords a message would
// otherwise change what this repository does with it, and for a billing failure
// that means paying to retry something that cannot succeed.
type Failure string

const (
	// FailureQuota is an exhausted balance or quota. Never retried: the request
	// cannot succeed and each attempt spends more of what is already gone.
	FailureQuota Failure = "quota_exhausted"

	// FailureAuth is a rejected credential.
	FailureAuth Failure = "authentication_rejected"

	// FailureRefused is a request the provider will not serve as written,
	// including a reply its filters removed.
	FailureRefused Failure = "provider_refused"

	// FailureThrottled is ordinary rate limiting, which is a different thing
	// from an exhausted balance even when both arrive as the same status.
	FailureThrottled Failure = "rate_limited"

	// FailureTransient is a server-side or transport failure a later attempt
	// might survive.
	FailureTransient Failure = "transient"

	// FailureInterrupted is the provider abandoning a reply mid-flight. Not
	// retried: the documentation says it was interrupted, not that a repeat
	// would succeed.
	FailureInterrupted Failure = "interrupted"

	// FailureUnknown is a terminal state this repository does not recognise. It
	// is a failure rather than a success, because an unrecognised ending cannot
	// be assumed complete.
	FailureUnknown Failure = "unknown"
)

// Retryable reports whether another identical attempt is worth its cost.
//
// This repository's decision, not the provider's: documentation says what a
// condition means, never what a caller should do about it.
func (f Failure) Retryable() bool {
	switch f {
	case FailureThrottled, FailureTransient:
		return true
	default:
		return false
	}
}

// Error carries a classified failure.
type Error struct {
	Failure Failure

	// Status is the HTTP status, or 0 when the failure has no response.
	Status int

	// Detail is for a human reading a report. Nothing branches on it.
	Detail string

	// Usage is what the attempts behind this failure reported using.
	Usage []ai.Usage
}

func (e *Error) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("openai: %s (status %d): %s", e.Failure, e.Status, e.Detail)
	}
	return fmt.Sprintf("openai: %s: %s", e.Failure, e.Detail)
}

// Is lets errors.Is match on the classification, so a caller can ask about the
// kind of failure without unwrapping to a concrete type.
func (e *Error) Is(target error) bool {
	other, ok := target.(*Error)
	return ok && other.Failure == e.Failure
}

// Consumed reports what the failed call used, so a ledger can hold it.
func (e *Error) Consumed() []ai.Usage { return e.Usage }

// classifyStatus maps an HTTP status onto a failure.
func classifyStatus(status int) Failure {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return FailureAuth
	case http.StatusTooManyRequests:
		return FailureThrottled
	case http.StatusRequestTimeout, http.StatusConflict:
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
type wireError struct {
	Error struct {
		Type    string `json:"type"`
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// classifyBody refines a status-derived failure using the provider's own codes.
//
// It may only make a failure MORE terminal, which is why it answers with a
// failure and a boolean rather than replacing the classification outright: a
// path that could turn an exhausted quota back into something retryable would
// spend against a balance that is already gone.
func classifyBody(status int, raw []byte) (Failure, bool) {
	var body wireError
	if err := json.Unmarshal(raw, &body); err != nil {
		return "", false
	}
	switch body.Error.Code {
	case "insufficient_quota", "billing_hard_limit_reached", "account_deactivated":
		return FailureQuota, true
	case "invalid_api_key", "invalid_organization":
		return FailureAuth, true
	case "context_length_exceeded":
		// Not a Failure: the runtime recovers from this by shortening, so it
		// leaves this package as the shared sentinel rather than as a refusal.
		return "", false
	}
	if body.Error.Type == "insufficient_quota" {
		return FailureQuota, true
	}
	return "", false
}

// isContextOverflow reports a refusal for a request that was too large.
//
// Recognised from the provider's own error code rather than its prose, so a
// change of wording cannot silently disable the recovery this drives.
func isContextOverflow(raw []byte) bool {
	var body wireError
	if err := json.Unmarshal(raw, &body); err != nil {
		return false
	}
	return body.Error.Code == "context_length_exceeded"
}

// failureFrom builds a classified error from a non-2xx response body.
//
// The body is not copied verbatim: a provider that echoes the request would put
// the credential into an error that a caller then logs. Only its own message
// survives, and only after scrubbing.
func failureFrom(status int, raw []byte, key string) error {
	if isContextOverflow(raw) {
		return fmt.Errorf("%w: the provider refused the request as too large", ai.ErrContextOverflow)
	}
	failure := classifyStatus(status)
	if refined, ok := classifyBody(status, raw); ok {
		failure = refined
	}
	var body wireError
	detail := ""
	if err := json.Unmarshal(raw, &body); err == nil {
		detail = body.Error.Message
		if body.Error.Code != "" {
			detail = body.Error.Code + ": " + detail
		}
	}
	if detail == "" {
		detail = fmt.Sprintf("unparsed body, %d bytes", len(raw))
	}
	return &Error{Failure: failure, Status: status, Detail: scrub(detail, key)}
}
