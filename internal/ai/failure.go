package ai

import (
	"errors"
	"fmt"
)

// Failure is why a call did not produce a usable reply.
//
// Provider-independent on purpose. A caller of Port does not know which
// provider answered, so it cannot branch on a provider's own error type without
// learning every provider — and the whole point of the boundary is that it does
// not have to. Each provider maps its own statuses, codes and wording onto this
// set; nothing above the boundary reads any of those.
type Failure string

const (
	// FailureQuota is an exhausted balance or quota. Never retried: the request
	// cannot succeed and each attempt spends more of what is already gone.
	FailureQuota Failure = "quota_exhausted"

	// FailureAuth is a rejected or missing credential.
	FailureAuth Failure = "authentication_rejected"

	// FailureRefused is a request the provider will not serve as written,
	// including a reply its filters removed.
	FailureRefused Failure = "provider_refused"

	// FailureThrottled is ordinary rate limiting — a different thing from an
	// exhausted balance even when both arrive as the same status.
	FailureThrottled Failure = "rate_limited"

	// FailureTransient is a server-side or transport failure that a later
	// attempt might survive.
	FailureTransient Failure = "transient"

	// FailureInterrupted is the provider abandoning a reply mid-flight. Not
	// retried: that it was interrupted is not evidence a repeat would succeed.
	FailureInterrupted Failure = "interrupted"

	// FailureUnknown is a terminal state no provider mapping recognised. It is
	// a failure rather than a success, because an unrecognised ending cannot be
	// assumed complete.
	FailureUnknown Failure = "unknown"
)

// Retryable reports whether another identical attempt is worth its cost.
//
// This repository's decision, not any provider's: documentation says what a
// condition means, never what a caller should do about it.
func (f Failure) Retryable() bool {
	switch f {
	case FailureThrottled, FailureTransient:
		return true
	default:
		return false
	}
}

// ProviderError is a classified failure from any provider.
//
// Provider is carried for diagnosis and never for branching: code that switches
// on it has re-created the dependency this type exists to remove.
type ProviderError struct {
	Provider string
	Failure  Failure

	// Status is the HTTP status, or 0 when the failure had no response.
	Status int

	// Detail is for a human reading a report. Nothing branches on it.
	Detail string

	// Used is what the attempts behind this failure reported consuming.
	Used []Usage

	// Advice is the provider's own instruction about retrying, when it gave
	// one. It outranks the classification, which is only an inference drawn
	// from a status code: a provider that says not to retry a 503 knows
	// something about its own state that the status does not carry, and one
	// asking for a retry of a status that usually means stop knows the same.
	//
	// An exhausted balance is the exception and stays terminal either way.
	// Every attempt against it spends more of what is already gone, and who
	// asked for the attempt does not change that.
	Advice *bool
}

// Retryable reports whether another identical attempt is worth its cost,
// preferring the provider's own instruction over the classification's default.
func (e *ProviderError) Retryable() bool {
	if e.Failure == FailureQuota {
		return false
	}
	if e.Advice != nil {
		return *e.Advice
	}
	return e.Failure.Retryable()
}

func (e *ProviderError) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("%s: %s (status %d): %s", e.Provider, e.Failure, e.Status, e.Detail)
	}
	return fmt.Sprintf("%s: %s: %s", e.Provider, e.Failure, e.Detail)
}

// Is matches on the classification, so a caller can ask what kind of failure
// happened without knowing which provider produced it.
func (e *ProviderError) Is(target error) bool {
	other, ok := target.(*ProviderError)
	if !ok {
		return false
	}
	// A bare classification matches any provider's version of it; naming a
	// provider as well narrows to that one.
	if other.Provider != "" && other.Provider != e.Provider {
		return false
	}
	return other.Failure == e.Failure
}

// Consumed reports what the failed call used, so a ledger can hold it.
//
// A copy, because the failure is the record of what was spent. Handing out the
// slice — and the optional counts it points at — would let a reader that
// adjusts what it got change what every later reader sees, and a spend that can
// be rewritten after the fact is not a record of anything.
func (e *ProviderError) Consumed() []Usage { return CloneUsages(e.Used) }

// Retryable reports whether an error is worth another identical attempt.
//
// The way a caller above the boundary asks, without unwrapping to anything
// provider-specific. An error carrying no classification is not retried: this
// repository does not guess at an unrecognised failure's cost.
func Retryable(err error) bool {
	var classified *ProviderError
	if errors.As(err, &classified) {
		return classified.Retryable()
	}
	return false
}

// FailureOf reports the classification of an error, if it carries one.
//
// The way callers above the boundary ask "what went wrong" without unwrapping
// to anything provider-specific.
func FailureOf(err error) (Failure, bool) {
	var classified *ProviderError
	if errors.As(err, &classified) {
		return classified.Failure, true
	}
	return "", false
}
