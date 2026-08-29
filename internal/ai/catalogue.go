package ai

// The model facts this repository has recorded, and where each came from.
//
// Small on purpose (ADR-0007). It carries only the fields something here reads,
// only for models a source has been recorded for, and every value names that
// source in the comment beside it. A number nobody can point at does not go in:
// a context window guessed at is worse than none, because the guess is acted on.
//
// eino-ext, which supplies this repository's provider adapters, carries none of
// this — its MaxTokens is a request field, not a model property, and it has no
// window, reasoning flag or thinking mapping anywhere. So the provider set
// follows eino-ext and these facts do not.

// ModelFacts is what is known about one model.
//
// Every field is optional and zero means UNKNOWN, not zero. A caller must be
// able to tell "this model's window is not recorded" from "this model has no
// window", because the first leaves a check disabled and the second would fail
// every request.
type ModelFacts struct {
	// ContextWindow is how many tokens the model accepts. Zero leaves
	// count-based overflow detection off, which is where it already was.
	ContextWindow int

	// MaxOutputTokens is the largest reply the model will produce. Zero leaves
	// the caller's own default.
	MaxOutputTokens int

	// Reasoning says the model can be asked to think before answering. A model
	// with no entry is not assumed either way — see ReasoningKnown.
	Reasoning bool

	// ReasoningKnown distinguishes "recorded as not reasoning" from "not
	// recorded". Without it a missing entry would silently mean the same as a
	// model measured not to reason, and a thinking request would be dropped for
	// a model that supports one.
	ReasoningKnown bool
}

// catalogue is keyed by provider then by model id.
var catalogue = map[string]map[string]ModelFacts{
	"deepseek": {
		"deepseek-chat": {
			// Both measured by this repository, not read from a catalogue.
			//
			// The window is the provider's own refusal naming it, recorded at
			// conformance/testdata/deepseek-large-request-rejected.json by the
			// probe the owner authorized on 2026-08-29: "This model's maximum
			// context length is 1048576 tokens."
			ContextWindow: 1048576,
			// TestLiveDeepSeekAcceptsAThinkingRequest: asking for off produced
			// no reasoning and asking for high produced some, so the field is
			// read rather than tolerated.
			Reasoning:      true,
			ReasoningKnown: true,
			// MaxOutputTokens is left unknown. The probe's rejection named the
			// completion budget this repository sent, not the model's own cap,
			// so no source for it has been recorded.
		},
	},
}

// Facts reports what is known about a model, and whether anything is.
//
// The served model is often not the one requested — DeepSeek answers a
// deepseek-chat request as deepseek-v4-flash — so a caller looking up what it
// ASKED for gets the facts recorded for that name, which is the only name it
// can act on before the reply arrives.
func Facts(provider, model string) (ModelFacts, bool) {
	byModel, known := catalogue[provider]
	if !known {
		return ModelFacts{}, false
	}
	facts, found := byModel[model]
	return facts, found
}

// KnownModels lists the models a provider has recorded facts for.
//
// NOT the models a provider offers — this repository has no such list, and
// presenting these as one would show a user a fraction of what they can call.
// It exists so a report can say what is recorded.
func KnownModels(provider string) []string {
	byModel, known := catalogue[provider]
	if !known {
		return nil
	}
	out := make([]string, 0, len(byModel))
	for id := range byModel {
		out = append(out, id)
	}
	sortStrings(out)
	return out
}

// KnownProviders lists the providers with any recorded facts, ordered.
func KnownProviders() []string {
	out := make([]string, 0, len(catalogue))
	for provider := range catalogue {
		out = append(out, provider)
	}
	sortStrings(out)
	return out
}

func sortStrings(in []string) {
	for i := 1; i < len(in); i++ {
		for j := i; j > 0 && in[j] < in[j-1]; j-- {
			in[j], in[j-1] = in[j-1], in[j]
		}
	}
}
