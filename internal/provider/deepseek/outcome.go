package deepseek

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/iamclancyliang/pi-go/internal/ai"
)

// Failure is why a call did not produce a usable reply.
//
// Classification happens here, at the boundary, where the status code and the
// stop reason still exist. It is carried as a value from this point on: no
// decision downstream reads the text of an error, because a provider that
// rewords a message would silently change what this repository does with it —
// and for a billing failure, that means paying to retry something that cannot
// succeed.
type Failure string

const (
	// FailureQuota is an exhausted balance. Never retried: the request cannot
	// succeed and each attempt consumes balance.
	FailureQuota Failure = "quota_exhausted"

	// FailureAuth is a rejected credential.
	FailureAuth Failure = "authentication_rejected"

	// FailureRefused is a request the provider will not serve as written,
	// including a reply whose content its filters removed. Asking again
	// unchanged is refused again.
	FailureRefused Failure = "provider_refused"

	// FailureThrottled is ordinary rate limiting, distinct from an exhausted
	// balance despite both being about limits.
	FailureThrottled Failure = "rate_limited"

	// FailureTransient is a server-side or transport failure that a later
	// attempt might survive.
	FailureTransient Failure = "transient"

	// FailureInterrupted is the provider abandoning a request mid-flight. It
	// arrives inside a 200, so a mapping that reads only the status calls it a
	// success and hands back a partial reply as the model's final word.
	//
	// Not retried: the documentation says the request was interrupted, not that
	// a repeat would succeed, and "sounds transient" is not evidence.
	FailureInterrupted Failure = "interrupted"

	// FailureUnknown is a terminal state this repository does not recognise. It
	// is a failure rather than a success, because an unrecognised ending cannot
	// be assumed complete.
	FailureUnknown Failure = "unknown"
)

// Retryable reports whether another identical attempt is worth its cost.
//
// This is this repository's decision, not the provider's: the documentation
// states what each condition means, never what a caller should do about it.
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

	// Usage is what the attempts behind this failure reported using. A request
	// the provider read is a request the provider read, answered or not.
	Usage []ai.Usage

	// Status is the HTTP status, or 0 when the failure has no response.
	Status int

	// Detail is for a human reading a report. Nothing branches on it.
	Detail string
}

func (e *Error) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("deepseek: %s (status %d): %s", e.Failure, e.Status, e.Detail)
	}
	return fmt.Sprintf("deepseek: %s: %s", e.Failure, e.Detail)
}

// Is lets errors.Is match on the classification, so a caller can ask about the
// kind of failure without unwrapping to a concrete type.
func (e *Error) Is(target error) bool {
	other, ok := target.(*Error)
	return ok && other.Failure == e.Failure
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

// Consumed reports what the failed call used, so a ledger can hold it.
func (e *Error) Consumed() []ai.Usage { return e.Usage }

// BodyClassifier refines a status-derived failure using the response body.
//
// It exists because a status code is not always enough: some providers report
// an exhausted balance inside an ordinary rate-limit response, and retrying
// that spends money on a request that cannot succeed. DeepSeek does not — it
// has its own status for exhaustion — so this is nil by default and the
// classification stays purely typed.
type BodyClassifier func(status int, body []byte) Failure

// ExhaustionInBody recognises an exhausted balance reported inside another
// status, using the provider's own error code rather than prose.
//
// This is the one place a body is inspected, and it may only make a failure
// MORE terminal. It cannot turn a terminal failure into a retryable one, so a
// misreading here can waste a retry at worst, never spend against a balance
// that is already gone.
func ExhaustionInBody(status int, body []byte) Failure {
	if status != http.StatusTooManyRequests {
		return ""
	}
	var parsed struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return ""
	}
	if parsed.Error.Code == "insufficient_balance" {
		return FailureQuota
	}
	return ""
}
