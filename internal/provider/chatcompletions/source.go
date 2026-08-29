package chatcompletions

import (
	"github.com/cloudwego/eino/schema"

	"github.com/iamclancyliang/pi-go/internal/ai"
)

// Source is what a port knows about a reply beyond the messages themselves.
//
// The streaming loop is written against the framework's own message stream and
// is the same for every provider. What differs is how much of the provider's
// own answer a port can still see underneath it — and that varies because the
// providers do not share a wire.
//
// A port that can read its provider's bytes answers all of this precisely. One
// that cannot answers what the framework carries and says so by returning the
// zero value, which every caller here treats as "not known" rather than as a
// measurement. Each such gap is a real loss and is recorded where the port
// declares it, not hidden behind a default that reads like an answer.
type Source interface {
	// Refusal is the classified reason the request was refused, or nil.
	//
	// Available to any port that can wrap its transport, because a refusal is
	// an ordinary JSON body — reading it needs the provider's error shape, not
	// its streaming format. A port without it reports whatever prose the
	// adapter produced, and a caller cannot tell a throttle from an exhausted
	// balance.
	Refusal() error

	// Usage is what the reply reported consuming.
	//
	// The framework's own count is passed in as what is available without
	// reading the wire. A port that read the wire may answer more precisely:
	// the framework's counts are plain integers, so they cannot distinguish a
	// field the provider omitted from one it reported as zero, while the raw
	// bytes can.
	Usage(meta *schema.TokenUsage) ai.Usage

	// ServedModel is the model the provider says answered, or "" when the port
	// cannot see it. Not the model requested: providers substitute, and a reply
	// that does not say which model produced it is one nobody can attribute.
	ServedModel() string

	// CheckAnnounced reports a stream whose tool-call positions the provider
	// renumbered, or nil when the port cannot see them.
	//
	// The adapter renumbers items contiguously from zero whatever arrived, so a
	// gap is invisible after conversion. A port that cannot check this accepts
	// an order it cannot verify.
	CheckAnnounced() error
}

// MetaSource is what a port that cannot read its provider's wire answers with.
//
// Everything comes from the framework's own message metadata, which carries a
// finish reason and a usage object and nothing else. Three things are therefore
// unavailable, and each is a stated loss rather than an oversight:
//
//   - the served model, so a substitution cannot be reported;
//   - per-field usage presence, so a count the provider omitted and one it
//     reported as zero look the same. Object-level presence survives: no usage
//     object at all is still distinguishable from one full of zeroes;
//   - the tool-call positions, so a renumbering would go unnoticed.
//
// Refusals are NOT among them when a transport can be wrapped, which is why
// that stays a separate field.
type MetaSource struct {
	// Capture is an optional refusal capture. Nil means the port could not wrap
	// its transport and reports the adapter's prose instead.
	Capture *Capture
}

// Refusal is the classified refusal when one was captured.
func (m MetaSource) Refusal() error {
	if m.Capture == nil {
		return nil
	}
	return m.Capture.Refusal()
}

// Usage converts the framework's own counts.
//
// A nil usage object is silence and stays silence: a provider that reported
// nothing has not reported that the call was free.
func (m MetaSource) Usage(meta *schema.TokenUsage) ai.Usage {
	if meta == nil {
		return ai.Usage{}
	}
	counts := ai.ReportedCounts{
		InputTokens:  &meta.PromptTokens,
		OutputTokens: &meta.CompletionTokens,
	}
	if cached := meta.PromptTokenDetails.CachedTokens; cached > 0 {
		// Only when positive. The field is a plain integer, so a zero here
		// cannot be told from an absent one, and claiming a measured zero would
		// assert something the framework never carried.
		counts.CachedTokens = &cached
	}
	if reasoning := meta.CompletionTokensDetails.ReasoningTokens; reasoning > 0 {
		counts.ReasoningTokens = &reasoning
	}
	return counts.Usage()
}

// ServedModel is unavailable without the wire.
func (MetaSource) ServedModel() string { return "" }

// CheckAnnounced cannot see tool-call positions without the wire.
func (MetaSource) CheckAnnounced() error { return nil }

// wireSource answers from the provider's own bytes.
type wireSource struct {
	capture *Capture
	port    *Port
}

func (w wireSource) Refusal() error { return w.capture.Refusal() }

func (w wireSource) Usage(*schema.TokenUsage) ai.Usage {
	// The framework's count is ignored: the wire carries the same numbers with
	// per-field presence intact, which is strictly more.
	return UsageOf(w.capture.Last())
}

func (w wireSource) ServedModel() string { return w.capture.Last().Model }

func (w wireSource) CheckAnnounced() error { return w.port.checkAnnounced(w.capture) }
