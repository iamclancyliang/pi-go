package claude

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
// mapping, because only this package knows how Anthropic says things.
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

// wireError is this provider's error envelope. It types its own errors rather
// than leaving a caller to read the status alone.
type wireError struct {
	Type  string `json:"type"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// failureFromType maps the provider's own error type onto a failure.
//
// The type is more precise than the status it arrives with, which is why it is
// asked first. 529 is this provider's own: the service is up and over capacity,
// which is worth another attempt where a 400 never is.
func failureFromType(kind string) (Failure, bool) {
	switch kind {
	case "authentication_error":
		return FailureAuth, true
	case "permission_error":
		// Distinct from authentication on purpose: the key is valid and the
		// account may not use this model. Reporting it as auth sends a user to
		// replace a key that is working.
		return FailureAuth, true
	case "rate_limit_error":
		return FailureThrottled, true
	case "overloaded_error":
		return FailureTransient, true
	case "api_error":
		return FailureTransient, true
	case "invalid_request_error", "not_found_error", "request_too_large":
		return FailureRefused, true
	}
	return FailureUnknown, false
}

// classifyStatus maps an HTTP status onto a failure, for a body that did not
// carry a type this package recognises.
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
//	prompt is too long: 213462 tokens > 200000 maximum
//
// Recorded by pi at packages/ai/src/utils/overflow.ts:11 at the pin, and
// asserted against a live provider at packages/ai/test/context-overflow.ts:103.
// pi matches the phrase; this compares the NUMBERS, which is stronger: prose is
// the provider's to reword, and requiring used > limit means a message merely
// mentioning a token count cannot be mistaken for a refusal about one.
var overflowCounts = regexp.MustCompile(`(\d+) tokens > (\d+) maximum`)

// overflowFrom reports a request the provider refused for exceeding what fits.
//
// Two conditions, not one. The token overflow above is the ordinary case. The
// other is a 413 typed request_too_large — a limit on the BYTES of the request
// rather than its tokens, which pi records separately at overflow.ts:12 and
// which a token comparison cannot see because the provider never counted any.
// Both are recovered the same way, by sending less.
func overflowFrom(status int, body wireError) (error, bool) {
	if status == http.StatusRequestEntityTooLarge || body.Error.Type == "request_too_large" {
		return fmt.Errorf("%w: the provider refused the request as too large to accept",
			ai.ErrContextOverflow), true
	}
	if body.Error.Type != "invalid_request_error" {
		return nil, false
	}
	match := overflowCounts.FindStringSubmatch(body.Error.Message)
	if match == nil {
		return nil, false
	}
	used, err := strconv.Atoi(match[1])
	if err != nil {
		return nil, false
	}
	limit, err := strconv.Atoi(match[2])
	if err != nil || used <= limit {
		return nil, false
	}
	return fmt.Errorf("%w: the provider refused %d tokens against a %d maximum",
		ai.ErrContextOverflow, used, limit), true
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
		if err, ok := overflowFrom(status, body); ok {
			return err
		}
	}

	failure := classifyStatus(status)
	if parsed {
		if refined, ok := failureFromType(body.Error.Type); ok {
			failure = refined
		}
	}

	detail := ""
	if parsed {
		detail = strings.TrimSpace(body.Error.Message)
		if kind := body.Error.Type; kind != "" {
			detail = kind + ": " + detail
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

// RetryAdvice reads this provider's own instruction about trying again.
//
// Read from where noInternalRetry put it: this port has to answer the SDK's
// question before the provider's answer can reach here, so the answer is moved
// rather than overwritten. `retry-after` is deliberately not read as advice —
// it says WHEN rather than whether, and reading it as "yes" would turn a delay
// the provider asked for into an immediate retry.
func (classifier) RetryAdvice(h http.Header) *bool { return retryAdvice(h) }

// TerminalFailure classifies a failure reported inside a successful response.
//
// The Messages stream carries `error` events mid-stream, and the component
// surfaces them as errors rather than as an ending this could read. So there is
// no code to classify here, and claiming one would be classifying nothing.
func (classifier) TerminalFailure(string) (error, bool) { return nil, false }
