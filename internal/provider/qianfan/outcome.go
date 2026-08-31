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

// This provider answers on two surfaces, and they do not share a vocabulary.
//
// **The compatible v2 endpoint this port reaches sends STRING codes** inside
// the OpenAI envelope, together with a request id at the top level. Recorded
// from real refusals on 2026-08-31, with the repository owner's credential:
//
//	401 {"error":{"code":"invalid_model","message":"The model does not exist or you do not have access to it.","type":"invalid_request_error"},"id":"as-bkv66krfus"}
//	401 {"error":{"code":"invalid_iam_token","message":"invalid_iam_token","type":"invalid_request_error"},"id":"as-r43wh8e35y"}
//	400 {"error":{"code":"invalid_argument","message":"messages cannot be empty","type":"invalid_request_error"},"id":"as-h9k27ds9ah"}
//	403 {"error":{"code":"account_overdue","message":"Access denied due to overdue account","type":"access_denied"},"id":"as-xp1jmkxe03"}
//
// The numeric codes below are the CLASSIC surface's, named as the vendor's own
// SDK names them at bce-qianfan-sdk@v0.0.14/consts.go:38-65. They are kept
// because a caller may point BaseURL at that surface, and because they are
// still the better source for what a number means. They were written first,
// from the SDK alone, on the assumption that a compatible endpoint would carry
// the same codes. **It does not.** A live run is what established that, and
// without one this port would have classified every refusal on its status.
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

// failureFromName maps the compatible surface's string code onto a failure.
//
// **The mapping that matters most here is invalid_model.** This provider
// answers a model it does not know, or one the account cannot use, with HTTP
// **401** — measured, not assumed. Classified on the status alone that is an
// authentication failure, and the user is sent to replace a credential that is
// working perfectly while the actual problem is the name they typed. It is a
// refusal, and the detail says which model.
//
// **The second is account_overdue.** This provider answers an account in
// arrears with HTTP **403**, which every port here reads as authentication —
// and that sends a user to check a credential that is valid while what they
// actually have to do is settle a bill. It is quota: the account is known, and
// out of money.
//
// Both were found by running against a real account, and neither is guessable
// from the vendor's SDK, whose vocabulary belongs to the other surface
// entirely.
//
// A name this port does not know falls through to the status, which is the safe
// direction: an unrecognised code classified by guesswork would be worse than
// one classified by the only fact left.
func failureFromName(name string) (Failure, bool) {
	switch name {
	case "invalid_model":
		return FailureRefused, true
	case "account_overdue":
		return FailureQuota, true
	case "invalid_iam_token", "invalid_access_token", "unauthorized", "permission_denied":
		return FailureAuth, true
	case "invalid_argument", "invalid_request", "invalid_parameter", "unsupported":
		return FailureRefused, true
	case "rate_limit_exceeded", "too_many_requests", "server_high_load":
		return FailureThrottled, true
	case "quota_exceeded", "insufficient_quota", "balance_not_enough", "arrearage":
		return FailureQuota, true
	case "internal_error", "service_unavailable", "server_error":
		return FailureTransient, true
	}
	return FailureUnknown, false
}

// failureFromCode maps one of the classic surface's numeric codes onto a
// failure.
//
// Two distinctions the status alone would lose, and both change what a caller
// should do:
//
//   - A **daily or total** limit reached (17, 19) is quota, not a throttle.
//     Waiting out an allowance already spent is waiting until tomorrow.
//   - An **expired** token (111) is not an invalid one (110). Both are
//     authentication, and the detail says which, because one is fixed by
//     refreshing and the other by replacing.
//
// The retryable set the SDK itself keeps — 2, 15, 18, 336100, 336501, 336502
// (config.go:100) — is exactly what maps here to throttled or transient. That
// agreement is a check rather than a coincidence to lean on.
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

// wireError covers both shapes this provider answers with.
//
// The compatible v2 endpoint sends the OpenAI envelope with a STRING code and a
// request id beside it; the classic surface sends a flat numeric
// `error_code`/`error_msg` pair (base_model.go:73). Reading only one would leave
// the other classified on its status alone.
type wireError struct {
	// The classic surface's shape.
	ErrorCode int    `json:"error_code"`
	ErrorMsg  string `json:"error_msg"`

	// The compatible surface's shape. Code is a string there and a number on
	// the classic surface, which is why they cannot share a field.
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`

	// RequestID sits beside the error rather than inside it, and is the one
	// thing a user can give this provider's support that nothing here can
	// reconstruct.
	RequestID string `json:"id"`
}

// code returns the classic surface's numeric code, and whether there was one.
//
// A string code that happens to be a number counts too: the two surfaces are
// the same service, and a number is a number whichever field carried it. A
// code that is NOT a number is a name, read by failureFromName instead.
func (w wireError) code() (int, bool) {
	if w.ErrorCode != 0 {
		return w.ErrorCode, true
	}
	n, err := strconv.Atoi(strings.TrimSpace(w.Error.Code))
	if err != nil {
		return 0, false
	}
	return n, true
}

// name returns the compatible surface's string code, when it is not a number.
func (w wireError) name() string {
	raw := strings.TrimSpace(w.Error.Code)
	if raw == "" {
		return ""
	}
	if _, err := strconv.Atoi(raw); err == nil {
		return ""
	}
	return raw
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
		// The code before the status, whichever surface sent it. This is where
		// a model name that does not exist stops being reported as a bad
		// credential: the provider answers that one with a 401.
		if name := body.name(); name != "" {
			if refined, known := failureFromName(name); known {
				failure = refined
			}
			// Carried whether or not it was recognised: an unmapped code is
			// still the provider's own name for what happened, and a reader can
			// look it up where this port could not.
			detail = name + ": " + detail
		} else if code, ok := body.code(); ok {
			if refined, known := failureFromCode(code); known {
				failure = refined
			}
			detail = strconv.Itoa(code) + ": " + detail
		}
		if id := strings.TrimSpace(body.RequestID); id != "" {
			detail += " (request " + id + ")"
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
	trimmed := strings.TrimSpace(code)
	if n, err := strconv.Atoi(trimmed); err == nil {
		if failure, known := failureFromCode(n); known {
			return fail(failure, 0, trimmed), true
		}
		return nil, false
	}
	if failure, known := failureFromName(trimmed); known {
		return fail(failure, 0, trimmed), true
	}
	return nil, false
}
