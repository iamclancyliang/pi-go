package deepseek

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
)

// RetryPolicy bounds retries of one request.
//
// MaxRetries counts attempts AFTER the first, so a call makes at most
// MaxRetries+1 requests. Zero is the shipped value: one request, no retry. A
// positive value exists so the behaviour can be driven and observed; nothing
// enables it by default, because every extra attempt is another billed request.
type RetryPolicy struct {
	MaxRetries int

	// BaseDelay is the first backoff. Each later attempt doubles it.
	BaseDelay time.Duration

	// MaxDelay refuses a server-requested wait longer than this rather than
	// sleeping for it. A provider asking for ten minutes should surface as a
	// failure, not as a process that appears to have hung.
	MaxDelay time.Duration
}

// DefaultMaxDelay caps a server-requested wait when a policy names none.
const DefaultMaxDelay = 60 * time.Second

// retryDecision is whether to try again, and after how long.
type retryDecision struct {
	retry bool
	after time.Duration
}

// decideRetry classifies a response and, if it is worth another attempt, says
// when.
//
// Whether to try again is the shared judgement, not this package's: an
// exhausted balance is terminal before any retry question is asked, and an
// explicit instruction from the provider outranks an inference drawn from a
// status code. Deciding it here would let two providers disagree about the same
// evidence. Reading the header stays here, because the header is this
// provider's.
func decideRetry(resp *http.Response, failure Failure, attempt int, policy RetryPolicy) (retryDecision, error) {
	classified := &ai.ProviderError{Failure: failure}
	if resp != nil {
		classified.Advice = retryAdvice(resp.Header)
	}
	if !classified.Retryable() {
		return retryDecision{}, nil
	}
	if attempt >= policy.MaxRetries {
		return retryDecision{}, nil
	}

	delay := policy.BaseDelay << attempt
	if resp != nil {
		if requested, ok := serverRequestedDelay(resp.Header); ok {
			max := policy.MaxDelay
			if max <= 0 {
				max = DefaultMaxDelay
			}
			if requested > max {
				return retryDecision{}, fail(failure, resp.StatusCode, "server asked for a "+requested.String()+
					" wait, longer than the "+max.String()+" cap")
			}
			delay = requested
		}
	}
	return retryDecision{retry: true, after: delay}, nil
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

// serverRequestedDelay reads the provider's own instruction about when to
// return. Milliseconds first: it is the more precise of the two.
func serverRequestedDelay(h http.Header) (time.Duration, bool) {
	if raw := h.Get("retry-after-ms"); raw != "" {
		if ms, err := strconv.ParseFloat(strings.TrimSpace(raw), 64); err == nil && ms >= 0 {
			return time.Duration(ms * float64(time.Millisecond)), true
		}
	}
	if raw := h.Get("retry-after"); raw != "" {
		if secs, err := strconv.ParseFloat(strings.TrimSpace(raw), 64); err == nil && secs >= 0 {
			return time.Duration(secs * float64(time.Second)), true
		}
		if when, err := http.ParseTime(strings.TrimSpace(raw)); err == nil {
			if d := time.Until(when); d > 0 {
				return d, true
			}
			return 0, true
		}
	}
	return 0, false
}

// wait sleeps, or returns the caller's own cancellation unchanged.
//
// A cancelled backoff is the caller's outcome, not the provider's: reporting it
// as a provider failure would invite a retry of the request they just stopped.
func wait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// isCallerCancellation reports an error that belongs to the caller.
func isCallerCancellation(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
