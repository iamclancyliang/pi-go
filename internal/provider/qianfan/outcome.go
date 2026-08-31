package qianfan

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/iamclancyliang/pi-go/internal/ai"
)

// The failure vocabulary is shared, not this package's own: a caller of the
// model boundary does not know which provider answered. What stays here is the
// mapping, because only this package knows how Qianfan says things.
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

// This provider's error codes, named as its own SDK names them at
// bce-qianfan-sdk/go/qianfan@v0.0.14/consts.go:38-65.
//
// Copied from the SDK rather than from documentation on purpose: these are the
// constants the vendor's own client compiles against, so they are what the
// service actually sends rather than what a page about it says. It is the best
// source any port here has for a failure vocabulary — better than the vendor
// whose SDK carries none, where this repository classifies on the status alone
// and says so.
const (
	codeServiceUnavailable     = 2
	codeRequestLimitReached    = 4
	codeNoPermissionToAccess   = 6
	codeGetServiceTokenFailed  = 13
	codeAppNotExist            = 15
	codeDailyLimitReached      = 17
	codeQPSLimitReached        = 18
	codeTotalRequestLimit      = 19
	codeInvalidRequest         = 100
	codeAPITokenInvalid        = 110
	codeAPITokenExpired        = 111
	codeInternalError          = 336000
	codeInvalidArgument        = 336001
	codeInvalidJSON            = 336002
	codeInvalidParam           = 336003
	codePermissionError        = 336004
	codeAPINameNotExist        = 336005
	codeServerHighLoad         = 336100
	codeInvalidHTTPMethod      = 336101
	codeInvalidArgumentSystem  = 336104
	codeInvalidArgumentSetting = 336105
	codeRPMLimitReached        = 336501
	codeTPMLimitReached        = 336502
	codeConsoleInternalError   = 500000
)

// failureFromCode maps one of this provider's codes onto a failure.
//
// Two distinctions the status code alone would lose, and both change what a
// caller should do:
//
//   - A **daily or total** limit reached (17, 19) is quota, not a throttle. Both
//     arrive as the same kind of refusal, and waiting out a daily allowance that
//     is already spent is waiting until tomorrow.
//   - An **expired** token (111) is not an invalid one (110). Both are
//     authentication, and the detail says which, because one is fixed by
//     refreshing and the other by replacing.
//
// The retryable set the SDK itself keeps — 2, 15, 18, 336100, 336501, 336502
// (config.go:100) — is exactly what maps here to throttled or transient. That
// agreement is not a coincidence to rely on, but it is a check: a mapping that
// called one of those terminal would contradict the vendor's own client.
func failureFromCode(code int) (Failure, bool) {
	switch code {
	case codeAPITokenInvalid, codeAPITokenExpired, codeGetServiceTokenFailed:
		return FailureAuth, true
	case codeNoPermissionToAccess, codePermissionError:
		return FailureAuth, true
	case codeDailyLimitReached, codeTotalRequestLimit:
		return FailureQuota, true
	case codeQPSLimitReached, codeRPMLimitReached, codeTPMLimitReached, codeRequestLimitReached:
		return FailureThrottled, true
	case codeServiceUnavailable, codeServerHighLoad, codeInternalError, codeConsoleInternalError,
		codeAppNotExist:
		return FailureTransient, true
	case codeInvalidRequest, codeInvalidArgument, codeInvalidJSON, codeInvalidParam,
		codeAPINameNotExist, codeInvalidHTTPMethod, codeInvalidArgumentSystem,
		codeInvalidArgumentSetting:
		return FailureRefused, true
	}
	return FailureUnknown, false
}

// classifyStatus maps an HTTP status onto a failure, for a body carrying no
// code this package knows.
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

// wireError covers both shapes this endpoint can answer with.
//
// The compatible surface may answer in either: the OpenAI envelope it is
// compatible with, or this provider's own flat `error_code`/`error_msg` pair
// (base_model.go:73). Reading only one of them would leave the other classified
// on its status alone — and the flat pair is the one carrying the vocabulary
// above, so guessing wrong in that direction costs the most.
type wireError struct {
	// The provider's own shape.
	ErrorCode int    `json:"error_code"`
	ErrorMsg  string `json:"error_msg"`

	// The OpenAI-compatible shape. Code is a string there and a number here,
	// which is exactly why they cannot share a field.
	Error struct {
		Code    json.RawMessage `json:"code"`
		Message string          `json:"message"`
		Type    string          `json:"type"`
	} `json:"error"`
}

// code returns the provider's numeric error code from whichever shape carried
// it, and whether there was one.
func (w wireError) code() (int, bool) {
	if w.ErrorCode != 0 {
		return w.ErrorCode, true
	}
	raw := strings.Trim(strings.TrimSpace(string(w.Error.Code)), `"`)
	if raw == "" {
		return 0, false
	}
	// A compatible surface can carry the same number as a string. Only a
	// number is used: a code that is not one is this provider's own name for
	// something, and inventing an integer for it would classify on a value the
	// provider never sent.
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, false
	}
	return n, true
}

// message returns whichever shape carried the message.
func (w wireError) message() string {
	if msg := strings.TrimSpace(w.ErrorMsg); msg != "" {
		return msg
	}
	return strings.TrimSpace(w.Error.Message)
}

// failureFrom builds a classified error from a refused response body.
//
// The body is not copied verbatim: a provider that echoes the request would put
// the credential into an error that a caller then logs. Only its own message
// survives, and only after scrubbing.
func failureFrom(status int, raw []byte, key string) error {
	var body wireError
	parsed := json.Unmarshal(raw, &body) == nil

	failure := classifyStatus(status)
	detail := ""
	if parsed {
		detail = body.message()
		if code, ok := body.code(); ok {
			if refined, known := failureFromCode(code); known {
				failure = refined
			}
			// Carried whether or not it was recognised: an unmapped code is
			// still the provider's own name for what happened, and the person
			// reading the failure can look it up where this port could not.
			detail = strconv.Itoa(code) + ": " + detail
		}
	}
	if strings.TrimSpace(detail) == "" {
		detail = fmt.Sprintf("unparsed body, %d bytes", len(raw))
	}
	return fail(failure, status, scrub(detail, key))
}

// classifier is what the shared dialect asks this package for.
type classifier struct{}

// Refusal classifies a non-2xx response.
func (classifier) Refusal(status int, body []byte, key string) error {
	return failureFrom(status, body, key)
}

// RetryAdvice: this provider sends no header saying whether to try again. Its
// opinion travels as an error code instead, and that is already read above.
func (classifier) RetryAdvice(http.Header) *bool { return nil }

// TerminalFailure classifies a failure reported inside a 200.
//
// The compatible surface reports a refusal as a status, so this is reached only
// if one arrives mid-stream. The same codes mean the same things there.
func (classifier) TerminalFailure(code string) (error, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(code))
	if err != nil {
		return nil, false
	}
	failure, known := failureFromCode(n)
	if !known {
		return nil, false
	}
	return fail(failure, 0, code), true
}
