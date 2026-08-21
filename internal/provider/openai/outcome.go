package openai

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/iamclancyliang/pi-go/internal/ai"
)

// The failure vocabulary is shared, not this package's own: a caller of the
// model boundary does not know which provider answered, so it must be able to
// tell an exhausted balance from a throttle without learning either provider.
// What stays here is the mapping — statuses, the provider's own codes, and its
// wording — because only this package knows how OpenAI says things.
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

// providerName labels a failure for diagnosis. Nothing branches on it.
const providerName = "openai"

// fail builds a classified failure from this provider.
func fail(f Failure, status int, detail string) *Error {
	return &Error{Provider: providerName, Failure: f, Status: status, Detail: detail}
}

// wireFailure types an error that came from the wire rather than from a
// response this package could classify.
//
// It leaves typed because the outcome set is closed: a caller branches on a
// classification, and an error that carries none can only be read as prose.
// What is not recognised becomes FailureUnknown rather than something
// retryable, since guessing that an unrecognised failure would survive a repeat
// buys another billed request on no evidence.
func wireFailure(stage string, err error) error {
	failure := FailureUnknown
	if transient(err) {
		failure = FailureTransient
	}
	return fail(failure, 0, stage+": "+scrub(err.Error(), ""))
}

// transient reports an error that a later attempt might survive.
//
// A truncated body and a refused connection are the same kind of thing here:
// the exchange did not complete, and nothing was learned about whether the
// request itself is acceptable.
func transient(err error) bool {
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return true
	}
	// *url.Error is what an HTTP client wraps every failure in, and it reports
	// itself as a net.Error whatever it holds. Matching it would classify a
	// request that will fail identically on every attempt as one worth
	// repeating, so the wrapper is stepped over and its cause judged instead.
	var wrapper *url.Error
	if errors.As(err, &wrapper) {
		return transient(wrapper.Err)
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}

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
	case contextOverflowCode:
		// Not a Failure: the runtime recovers from this by shortening, so it
		// leaves this package as the shared sentinel rather than as a refusal.
		return "", false
	}
	if body.Error.Type == "insufficient_quota" {
		return FailureQuota, true
	}
	return "", false
}

// contextOverflowCode is the provider's own name for a request that did not
// fit. Recognised from the code rather than its prose, so a change of wording
// cannot silently disable the recovery it drives.
const contextOverflowCode = "context_length_exceeded"

// isContextOverflow reports a refusal for a request that was too large.
func isContextOverflow(raw []byte) bool {
	var body wireError
	if err := json.Unmarshal(raw, &body); err != nil {
		return false
	}
	return body.Error.Code == contextOverflowCode
}

// retryAdvice reads the provider's own instruction about trying again.
//
// Header-derived, so it applies only to a status-derived outcome. A failure
// reported inside a 200 is not governed by a transport header that has already
// called the exchange successful.
func retryAdvice(h http.Header) *bool {
	switch strings.ToLower(strings.TrimSpace(h.Get("x-should-retry"))) {
	case "true":
		yes := true
		return &yes
	case "false":
		no := false
		return &no
	}
	return nil
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
	return fail(failure, status, scrub(detail, key))
}
