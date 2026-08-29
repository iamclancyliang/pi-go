package deepseek

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"

	"github.com/iamclancyliang/pi-go/internal/ai"
)

// The provider refuses an oversized request as a 400 whose type and code are
// both `invalid_request_error` — the same pair it uses for any malformed
// request. Status and code therefore cannot tell an overflow from a typo in a
// field name; only the message distinguishes them, and it does so by carrying
// the two numbers that define the condition:
//
//	This model's maximum context length is 1048576 tokens. However, you
//	requested 2911951 tokens (2911935 in the messages, 16 in the completion).
//	Please reduce the length of the messages or completion.
//
// Recorded from a real rejection at
// conformance/testdata/deepseek-large-request-rejected.json, under the probe
// the repository owner authorized on 2026-08-29. Before that response existed
// this could only have been guessed at, and a matcher written from a guess
// tests the matcher.
var (
	overflowLimit     = regexp.MustCompile(`maximum context length is (\d+) tokens`)
	overflowRequested = regexp.MustCompile(`you requested (\d+) tokens`)
)

// overflowFromRejection reports a request the provider refused for exceeding
// the model's context, and how far over it was.
//
// It compares the two NUMBERS rather than matching the sentence around them.
// Prose is the provider's to reword — a changed adjective, a translated
// message, a reordered clause — and a detector built on it fails silently the
// day that happens. The numbers are the condition itself, and requiring
// requested > limit means a message that merely mentions a context length
// cannot be mistaken for a refusal about one.
//
// Failing to match is not an error: the caller keeps the ordinary refusal it
// already had. That is the safe direction — a missed overflow costs one
// recovery that does not happen, while a false positive spends a second billed
// request shortening a conversation that was never too long.
func overflowFromRejection(status int, raw []byte) (error, bool) {
	if status != 400 {
		return nil, false
	}
	var body struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, false
	}
	// The type is checked even though it is generic: it costs nothing and it
	// stops this from ever looking at a 400 that is not a request complaint.
	if body.Error.Type != "invalid_request_error" {
		return nil, false
	}

	limit, ok := firstNumber(overflowLimit, body.Error.Message)
	if !ok {
		return nil, false
	}
	requested, ok := firstNumber(overflowRequested, body.Error.Message)
	if !ok {
		return nil, false
	}
	if requested <= limit {
		// A message naming both numbers without exceeding one is not this
		// condition, whatever else it is.
		return nil, false
	}
	return fmt.Errorf("%w: the provider refused %d tokens against a %d window",
		ai.ErrContextOverflow, requested, limit), true
}

func firstNumber(pattern *regexp.Regexp, text string) (int, bool) {
	match := pattern.FindStringSubmatch(text)
	if match == nil {
		return 0, false
	}
	n, err := strconv.Atoi(match[1])
	if err != nil {
		return 0, false
	}
	return n, true
}
