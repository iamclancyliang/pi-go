package ai

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
)

// Stopped reports an error that says a call was stopped rather than that it
// failed.
//
// Read from the error chain rather than from the caller's context: a transport
// can report a stop it was told about before that context is observably done,
// and a call that was stopped is over either way. Asking only the context
// misses exactly that case, and misreads it as the provider breaking.
func Stopped(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// Transient reports an error that a later attempt might survive.
//
// A truncated body and a refused connection are the same kind of thing here:
// the exchange did not complete, and nothing was learned about whether the
// request itself is acceptable.
func Transient(err error) bool {
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return true
	}
	// *url.Error is what an HTTP client wraps every failure in, and it reports
	// itself as a net.Error whatever it holds. Matching it would classify a
	// request that will fail identically on every attempt as one worth
	// repeating, so the wrapper is stepped over and its cause judged instead.
	var wrapper *url.Error
	if errors.As(err, &wrapper) {
		return Transient(wrapper.Err)
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}

// WireFailure types an error that came from the wire rather than from a
// response a provider could classify.
//
// Three things at once, because they are one decision. A stop returns as it
// arrived, so a caller can tell its own cancellation from a provider breaking
// and does not retry what it just stopped — and a deadline does not leave as
// something worth repeating. What is recognised as incomplete becomes
// transient; what is not becomes unknown rather than retryable, since guessing
// that an unrecognised failure would survive a repeat buys another billed
// request on no evidence. And the secret this call was made with is removed by
// identity, because a transport error names the request it failed on, headers
// and all, and a key that does not look like one survives the shape pass.
//
// The stage says what was being attempted. It is for a person reading a report;
// nothing branches on it.
func WireFailure(provider, stage, secret string, err error) error {
	if Stopped(err) {
		return err
	}
	failure := FailureUnknown
	if Transient(err) {
		failure = FailureTransient
	}
	return &ProviderError{
		Provider: provider,
		Failure:  failure,
		Detail:   stage + ": " + ScrubSecret(err.Error(), secret),
	}
}
