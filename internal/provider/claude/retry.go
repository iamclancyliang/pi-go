package claude

import "net/http"

const (
	// retryHeader is the SDK's own override for whether a response is worth
	// another attempt. It is checked BEFORE the status code, which is what
	// makes it usable as an off switch.
	retryHeader = "x-should-retry"

	// carriedAdvice is where the provider's own answer is kept once the switch
	// above has been thrown. It is this repository's header, on a response that
	// never leaves the process.
	carriedAdvice = "x-pi-go-provider-should-retry"
)

// noInternalRetry stops the vendor SDK from retrying inside one call, without
// throwing away what the provider said about retrying.
//
// The SDK underneath this port retries twice by default, on 408, 409, 429 and
// every 5xx. That is three requests for one call: three bills, two of them for
// a decision the caller never made and never sees, since only the last failure
// survives. Every other port here sends exactly one request per call, and this
// repository accounts for attempts explicitly rather than letting a dependency
// make them invisibly.
//
// There is no configuration for it — the component this port drives exposes no
// retry knob, and the SDK's own option is not reachable through it — so the
// answer is given where the SDK asks the question.
//
// The rule this follows is the one every port here follows: NOTHING inside a
// port retries, and the provider's instruction travels out with the failure so
// the caller who does decide can use it. So the provider's own answer is not
// overwritten and lost; it is moved to a header this package owns, and read
// back by RetryAdvice.
type noInternalRetry struct{ inner http.RoundTripper }

func (n noInternalRetry) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := n.inner.RoundTrip(req)
	if err != nil || resp == nil {
		// A connection that never produced a response is retried by the SDK
		// regardless, and there is no header to attach an answer to. That path
		// is the caller's to bound with a context.
		return resp, err
	}
	if resp.Header == nil {
		resp.Header = http.Header{}
	}
	if said := resp.Header.Get(retryHeader); said != "" {
		resp.Header.Set(carriedAdvice, said)
	}
	resp.Header.Set(retryHeader, "false")
	return resp, nil
}

// retryAdvice reads what the provider said, from where the switch above put it.
func retryAdvice(h http.Header) *bool {
	switch h.Get(carriedAdvice) {
	case "true":
		yes := true
		return &yes
	case "false":
		no := false
		return &no
	}
	return nil
}
