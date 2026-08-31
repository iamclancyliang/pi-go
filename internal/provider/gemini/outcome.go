package gemini

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/iamclancyliang/pi-go/internal/ai"
)

// The failure vocabulary is shared, not this package's own: a caller of the
// model boundary does not know which provider answered. What stays here is the
// mapping, because only this package knows how Google says things.
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

// wireError is this provider's error envelope, taken from the type the SDK
// decodes into — genai.APIError, which carries the status code again as a
// field, a message, and Google's own canonical status name.
type wireError struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}

// failureFromStatusName maps Google's canonical status onto a failure.
//
// These are google.rpc.Code names, shared across every Google API rather than
// invented for this one, and the SDK decodes the field they arrive in. They are
// asked before the HTTP status because one HTTP status carries several of them:
// 429 is RESOURCE_EXHAUSTED whether a per-minute rate was hit or a daily quota
// was spent, and those are not the same answer — one is worth waiting out, the
// other is not. What separates them is not in the status name either, so the
// distinction that CAN be made here is made, and the rest travels as detail.
func failureFromStatusName(name string) (Failure, bool) {
	switch name {
	case "UNAUTHENTICATED":
		return FailureAuth, true
	case "PERMISSION_DENIED":
		// The key is valid and this project may not use this model or has not
		// enabled the API. Reported as auth because that is the class, and the
		// detail says which.
		return FailureAuth, true
	case "RESOURCE_EXHAUSTED":
		return FailureThrottled, true
	case "UNAVAILABLE", "INTERNAL", "DEADLINE_EXCEEDED", "ABORTED":
		return FailureTransient, true
	case "INVALID_ARGUMENT", "NOT_FOUND", "FAILED_PRECONDITION", "OUT_OF_RANGE":
		return FailureRefused, true
	}
	return FailureUnknown, false
}

// classifyStatus maps an HTTP status onto a failure, for a body that did not
// carry a status name this package recognises.
func classifyStatus(status int) Failure {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return FailureAuth
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

// The provider refuses an oversized prompt with a message carrying both numbers
// that define the condition:
//
//	The input token count (1196265) exceeds the maximum number of tokens
//	allowed (1048575)
//
// Recorded by pi at packages/ai/src/utils/overflow.ts:16 at the pin. pi matches
// the phrase; this compares the NUMBERS, which is stronger — prose is the
// provider's to reword, and requiring counted > allowed means a message merely
// mentioning a token count cannot be mistaken for a refusal about one.
var overflowCounts = regexp.MustCompile(
	`input token count \((\d+)\) exceeds the maximum number of tokens allowed \((\d+)\)`)

// overflowFrom reports a request the provider refused for exceeding what fits.
func overflowFrom(message string) (error, bool) {
	match := overflowCounts.FindStringSubmatch(message)
	if match == nil {
		return nil, false
	}
	counted, err := strconv.Atoi(match[1])
	if err != nil {
		return nil, false
	}
	allowed, err := strconv.Atoi(match[2])
	if err != nil || counted <= allowed {
		return nil, false
	}
	return fmt.Errorf("%w: the provider counted %d tokens against a %d maximum",
		ai.ErrContextOverflow, counted, allowed), true
}

// failureFrom builds a classified error from a refused response body.
//
// The body is not copied verbatim: a provider that echoes the request would put
// the credential into an error that a caller then logs. Only its own message
// survives, and only after scrubbing.
func failureFrom(status int, raw []byte, key string) error {
	var body wireError
	parsed := json.Unmarshal(raw, &body) == nil

	if parsed {
		if err, ok := overflowFrom(body.Error.Message); ok {
			return err
		}
	}

	failure := classifyStatus(status)
	if parsed {
		if refined, ok := failureFromStatusName(body.Error.Status); ok {
			failure = refined
		}
	}

	detail := ""
	if parsed {
		detail = strings.TrimSpace(body.Error.Message)
		if name := body.Error.Status; name != "" {
			detail = name + ": " + detail
		}
	}
	if detail == "" {
		detail = fmt.Sprintf("unparsed body, %d bytes", len(raw))
	}
	return fail(failure, status, scrub(detail, key))
}

// classifier is what the shared port asks this package for.
type classifier struct{}

// Refusal classifies a non-2xx response.
func (classifier) Refusal(status int, body []byte, key string) error {
	return failureFrom(status, body, key)
}

// RetryAdvice: this provider answers a throttle with a RetryInfo detail saying
// WHEN, not whether. That is a different question from this one, and reading it
// as "yes" would turn a delay the provider asked for into an immediate retry.
func (classifier) RetryAdvice(http.Header) *bool { return nil }

// TerminalFailure: this provider reports failures by status rather than inside
// a successful reply, so there is no error code here to classify.
func (classifier) TerminalFailure(string) (error, bool) { return nil, false }
