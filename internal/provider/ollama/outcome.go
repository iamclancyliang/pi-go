package ollama

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/iamclancyliang/pi-go/internal/ai"
)

// The failure vocabulary is shared, not this package's own: a caller of the
// model boundary does not know which provider answered. What stays here is the
// mapping, because only this package knows how a local server says things.
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

// classifyStatus maps an HTTP status onto a failure.
//
// A local server has a smaller vocabulary than a hosted one, and two of the
// classifications every other port needs cannot occur: nothing is billed, so
// there is no quota, and nothing is shared, so there is no throttle. Mapping a
// status onto either would tell a user to wait for capacity that is entirely
// their own machine.
//
// What it does have that hosted providers do not is a 404 that means "that
// model is not pulled" — a refusal the user fixes with one command, and one
// that must not read as a server fault worth retrying.
func classifyStatus(status int) Failure {
	switch status {
	case http.StatusNotFound:
		return FailureRefused
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

// wireError is the server's own error report: a bare message, not the wrapped
// object hosted providers send.
type wireError struct {
	Error string `json:"error"`
}

// failureFrom builds a classified error from a non-2xx response.
func failureFrom(status int, raw []byte, key string) error {
	var body wireError
	detail := ""
	if err := json.Unmarshal(raw, &body); err == nil {
		detail = scrub(strings.TrimSpace(body.Error), key)
	}
	if detail == "" {
		// The message may not have survived as JSON. Overflow is still read
		// off the raw text, because losing that one costs a recovery.
		detail = scrub(strings.TrimSpace(string(raw)), key)
	}
	if err, ok := overflowFrom(status, detail); ok {
		return err
	}
	if detail == "" {
		detail = "the local server refused this request"
	}
	// A model that was never pulled is the one failure here a user fixes
	// immediately, so it says how rather than only what.
	if status == http.StatusNotFound && strings.Contains(strings.ToLower(detail), "model") {
		detail += "; pull it first with: ollama pull <model>"
	}
	return fail(classifyStatus(status), status, detail)
}

// overflowMessage recognises the server's own report of a prompt that did not
// fit.
//
// The pattern is pi's, not a guess: packages/ai/src/utils/overflow.ts:57 at the
// pin, exercised there by packages/ai/test/overflow.test.ts:33 against
//
//	400 `prompt too long; exceeded max context length by 100918 tokens`
//
// Unlike a hosted provider's rejection, this message carries ONE number — how
// far over, not the limit and the request. So there are no two numbers to
// compare, and the phrase itself has to be the signal. That is weaker: a
// reworded message stops matching. It is still worth having, because the
// alternative is reporting the one failure this repository can recover from as
// an ordinary refusal it cannot.
var overflowMessage = regexp.MustCompile(`(?i)prompt too long; exceeded (?:max )?context length`)

// overflowFrom reports a request the server refused for exceeding the loaded
// model's context.
//
// A local server can also TRUNCATE an oversized prompt silently and answer
// normally — pi records this at packages/ai/test/context-overflow.test.ts:679
// and does not detect it either. Nothing here can: a truncated answer looks
// exactly like an answer. It is recorded in the parity matrix rather than
// papered over with a guess about reported input counts, which would misfire on
// every model whose tokeniser this repository cannot run.
func overflowFrom(status int, detail string) (error, bool) {
	if status != http.StatusBadRequest {
		return nil, false
	}
	if !overflowMessage.MatchString(detail) {
		return nil, false
	}
	return fmt.Errorf("%w: the local server refused the prompt as too long for the loaded model",
		ai.ErrContextOverflow), true
}

// classifier is what the shared port asks this package for.
type classifier struct{}

// Refusal classifies a non-2xx response.
func (classifier) Refusal(status int, body []byte, key string) error {
	return failureFrom(status, body, key)
}

// RetryAdvice: a local server sends no retry header, and inventing one would
// tell a caller something the server never said.
func (classifier) RetryAdvice(http.Header) *bool { return nil }

// TerminalFailure: this server reports failures by status rather than inside a
// successful reply, so there is no error code to classify.
func (classifier) TerminalFailure(string) (error, bool) { return nil, false }
