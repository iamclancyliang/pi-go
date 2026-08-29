package qwen

import (
	"encoding/json"
	"fmt"
	"github.com/iamclancyliang/pi-go/internal/ai"
	"net/http"
	"strings"
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

// providerName labels a failure for diagnosis. Nothing branches on it.
const providerName = "qwen"

// fail builds a classified failure from this provider.
func fail(f Failure, status int, detail string) *Error {
	return &Error{Provider: providerName, Failure: f, Status: status, Detail: detail}
}

// contextOverflowCode is the provider's own name for a request that did not
// fit. Recognised from the code rather than its prose, so a change of wording
// cannot silently disable the recovery it drives.
const contextOverflowCode = "context_length_exceeded"

// stopped reports an error that says the call was stopped rather than failed.
//
// The judgement is shared: what a stop is does not vary by provider, only where
// one can appear does.
func stopped(err error) bool { return ai.Stopped(err) }

// wireFailure types an error that came from the wire rather than from a
// response this package could classify.
//
// Shared, because none of what it decides is this provider's to decide
// differently: a stop is the caller's outcome, an incomplete exchange may
// survive a repeat, and the key this call was made with belongs in no report.
func wireFailure(stage, key string, err error) error {
	return ai.WireFailure(providerName, stage, key, err)
}

// retryAdvice reads this provider's own instruction about trying again.
//
// Applies only to a status-derived outcome: a failure reported inside a 200 is
// not governed by a transport header that has already called that exchange
// successful.
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

// classifyStatus maps an HTTP status onto a failure.
func classifyStatus(status int) Failure {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return FailureAuth
	case http.StatusPaymentRequired:
		return FailureQuota
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
	// A refusal may also arrive without the wrapper, which is how this
	// provider's own gateway reports some of them.
	Code    string `json:"code"`
	Message string `json:"message"`
}

// code is the provider's own code however it wrapped it.
func (w wireError) code() string {
	if w.Error.Code != "" {
		return w.Error.Code
	}
	return w.Code
}

// message is the provider's own wording however it wrapped it.
func (w wireError) message() string {
	if w.Error.Message != "" {
		return w.Error.Message
	}
	return w.Message
}

// failureFromCode maps the provider's own error code onto a classification.
//
// It answers only for codes that are more terminal than a status implies: a
// path that could turn an exhausted quota back into something retryable would
// spend against a balance that is already gone.
func failureFromCode(code string) (Failure, bool) {
	switch strings.ToLower(code) {
	case "insufficient_quota", "arrearage", "allocated_quota_exceeded",
		"insufficientaccountbalance", "insufficient_account_balance":
		return FailureQuota, true
	case "invalid_api_key", "invalidapikey", "unauthorized":
		return FailureAuth, true
	case "throttling", "throttling.ratequota", "requests_rate_limit",
		"rate_limit_exceeded", "limit_requests":
		return FailureThrottled, true
	case "internal_error", "server_error", "internalerror":
		return FailureTransient, true
	case "data_inspection_failed", "content_filter", "response_censored":
		return FailureRefused, true
	}
	return "", false
}

// isContextOverflow reports a refusal for a request that was too large.
func isContextOverflow(code string) bool {
	switch strings.ToLower(code) {
	case contextOverflowCode, "range_of_input_length_exceeded_limit":
		return true
	}
	return false
}

// failureFrom builds a classified error from a refused response body.
//
// The body is not copied verbatim: a provider that echoes the request would put
// the credential into an error that a caller then logs. Only its own message
// survives, and only after scrubbing.
func failureFrom(status int, raw []byte, key string) error {
	var body wireError
	parsed := json.Unmarshal(raw, &body) == nil

	if parsed && isContextOverflow(body.code()) {
		return fmt.Errorf("%w: the provider refused the request as too large", ai.ErrContextOverflow)
	}
	failure := classifyStatus(status)
	if parsed {
		if refined, ok := failureFromCode(body.code()); ok {
			failure = refined
		}
	}
	detail := ""
	if parsed {
		detail = body.message()
		if c := body.code(); c != "" {
			detail = c + ": " + detail
		}
	}
	if detail == "" {
		detail = fmt.Sprintf("unparsed body, %d bytes", len(raw))
	}
	return fail(failure, status, scrub(detail, key))
}

// endingFrom maps the provider's finish reason onto this repository's outcomes.
//
// The set is the same one every provider in this repository answers with, so a
// caller branches on the outcome rather than on which provider produced it.
func endingFrom(finish, errorCode string) (ai.StopReason, error) {
	// An overflow reported inside a 200 is the same condition as one reported
	// by a status, and leaves as the same sentinel. Classifying it by the
	// ending instead would call it an interruption, which is never retried — so
	// the one failure this repository can recover from, by shortening, would be
	// the one it gives up on.
	if isContextOverflow(errorCode) {
		return ai.StopError, fmt.Errorf("%w: the provider refused the request as too large",
			ai.ErrContextOverflow)
	}
	// A failure reported inside a 200 can name its own reason. Classifying by
	// the ending alone would call an exhausted balance an interruption, which
	// reads as "try again later" for something that cannot succeed.
	if failure, ok := failureFromCode(errorCode); ok {
		return ai.StopError, fail(failure, 0, errorCode)
	}
	if errorCode != "" {
		return ai.StopError, fail(FailureUnknown, 0, "the reply failed: "+errorCode)
	}

	switch finish {
	case "stop":
		return ai.StopEnd, nil
	case "tool_calls", "function_call":
		return ai.StopToolUse, nil
	case "length":
		return ai.StopLength, nil
	case "content_filter":
		return ai.StopError, fail(FailureRefused, 0, "the provider's filters removed the content")
	case "":
		// The stream ended without the provider saying why, so the reply is not
		// known to be complete. Reporting it as finished would hand back a
		// partial answer as the model's last word.
		return ai.StopError, fail(FailureUnknown, 0, "the stream ended without a finish reason")
	default:
		return ai.StopError, fail(FailureUnknown, 0, fmt.Sprintf("unrecognised finish reason %q", finish))
	}
}

// classifier is what the shared chat-completions capture asks this package for.
//
// The seam is small because the difference is small: everything about watching
// one of these streams is the same, and only what a status or an error code
// MEANS belongs to a provider.
type classifier struct{}

// Refusal classifies a non-2xx response.
func (classifier) Refusal(status int, body []byte, key string) error {
	return failureFrom(status, body, key)
}

// RetryAdvice reads this provider's own instruction about trying again.
func (classifier) RetryAdvice(h http.Header) *bool { return retryAdvice(h) }
