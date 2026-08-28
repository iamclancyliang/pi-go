package ai

// ReportedCounts is what a provider's own bytes said a call used.
//
// Every field is a pointer because absent and zero are different answers, and
// the difference survives only if it is carried. A provider that reports
// nothing has not reported zero: reading silence as a measurement bills a
// caller for a number nobody produced, and reading a measured zero as silence
// loses the only evidence that the call was free.
//
// This is what a provider's terminal holds. Turning it into a Usage is the same
// arithmetic everywhere, which is why it lives here rather than once per
// provider — the rule about what a cached token means is not a provider's to
// decide differently.
type ReportedCounts struct {
	InputTokens     *int
	OutputTokens    *int
	CachedTokens    *int
	ReasoningTokens *int
}

// Usage turns what a provider reported into this repository's ledger entry.
//
// The prompt is reported whole, and the cached part of it is reported again
// beside it. Input here is the uncached remainder, so the two can be added
// without counting the cached tokens twice — and a caller that wants the whole
// prompt adds them back deliberately rather than by accident.
func (c ReportedCounts) Usage() Usage {
	used := Usage{}
	if c.InputTokens == nil && c.OutputTokens == nil &&
		c.CachedTokens == nil && c.ReasoningTokens == nil {
		return used
	}
	used.Reported = true
	if c.InputTokens != nil {
		used.InputTokens = *c.InputTokens
	}
	if c.OutputTokens != nil {
		used.OutputTokens = *c.OutputTokens
	}
	if c.CachedTokens != nil {
		// Copied rather than aliased: the caller keeps its own pointer, and a
		// ledger entry that can be edited from outside records nothing.
		cached := *c.CachedTokens
		used.CacheReadTokens = &cached
		used.InputTokens -= cached
		if used.InputTokens < 0 {
			used.InputTokens = 0
		}
	}
	if c.ReasoningTokens != nil {
		reasoning := *c.ReasoningTokens
		used.ReasoningTokens = &reasoning
	}
	return used
}
