package deepseek

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/iamclancyliang/pi-go/internal/ai"
)

// The failure vocabulary is shared, not this package's own: a caller of the
// model boundary does not know which provider answered, so it must be able to
// tell an exhausted balance from a throttle without learning either provider.
// What stays here is the mapping — statuses, the provider's own codes, and its
// wording — because only this package knows how DeepSeek says things.
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
const providerName = "deepseek"

// fail builds a classified failure from this provider.
func fail(f Failure, status int, detail string) *Error {
	return &Error{Provider: providerName, Failure: f, Status: status, Detail: detail}
}

// stopped reports an error that says the call was stopped rather than failed.
//
// Read from the chain rather than from the caller's context: a body can report
// a stop it was told about before that context is observably done, and a call
// that was stopped is over either way.
func stopped(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// classifyStatus maps an HTTP status onto a failure.
//
// The codes are DeepSeek's documented ones. 403, 408 and 409 are absent from
// that documentation, so they are not claimed here: 403 joins authentication
// because that is what the class means, and the rest fall to transient only
// when the status says server.
func classifyStatus(status int) Failure {
	switch status {
	case http.StatusPaymentRequired: // 402: documented as "You have run out of balance"
		return FailureQuota
	case http.StatusUnauthorized, http.StatusForbidden:
		return FailureAuth
	case http.StatusTooManyRequests:
		return FailureThrottled
	}
	switch {
	case status >= 500:
		return FailureTransient
	case status >= 400:
		return FailureRefused
	}
	return FailureUnknown
}

// stopReason maps a provider stop reason onto a reply ending or a failure.
//
// All five documented values map. An unrecognised value is a failure: a
// provider reporting a terminal state this repository has not mapped has said
// something about the reply that is not "it is complete".
func stopReason(raw string) (ok bool, truncated bool, failure Failure) {
	switch raw {
	case "stop":
		return true, false, ""
	case "length":
		return true, true, ""
	case "tool_calls":
		return true, false, ""
	case "content_filter":
		return false, false, FailureRefused
	case "insufficient_system_resource":
		return false, false, FailureInterrupted
	default:
		return false, false, FailureUnknown
	}
}

// ExhaustionDetector reports whether a response body confirms an exhausted
// balance that its status code did not.
//
// It returns a BOOLEAN rather than a failure, so the one-way rule is a property
// of the type rather than a promise in a comment: this can turn something into
// quota, and it cannot turn quota into anything. An earlier version took a
// failure and documented that it "may only make a failure more terminal" —
// which the type happily allowed a caller to violate, downgrading an exhausted
// balance into a retryable throttle and spending against a balance that is
// already gone.
//
// It exists because a status code is not always enough: some providers report
// exhaustion inside an ordinary rate-limit response. DeepSeek does not — it has
// its own status for it — so this is nil by default.
type ExhaustionDetector func(status int, body []byte) bool

// ExhaustionInBody recognises an exhausted balance reported inside another
// status, using the provider's own error code rather than prose.
func ExhaustionInBody(status int, body []byte) bool {
	if status != http.StatusTooManyRequests {
		return false
	}
	var parsed struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return false
	}
	return parsed.Error.Code == "insufficient_balance"
}
