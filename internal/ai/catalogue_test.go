package ai_test

import (
	"testing"

	"github.com/iamclancyliang/pi-go/internal/ai"
)

// TestAnUnrecordedModelSaysSoRatherThanAnsweringZero. Zero is a real value for
// every field here, so a caller must be able to tell "not recorded" from
// "recorded as none" — the first leaves a check disabled, the second would fail
// every request.
func TestAnUnrecordedModelSaysSoRatherThanAnsweringZero(t *testing.T) {
	if _, recorded := ai.Facts("deepseek", "a-model-nobody-measured"); recorded {
		t.Fatal("an unrecorded model reported facts")
	}
	if _, recorded := ai.Facts("a-provider-with-no-entries", "anything"); recorded {
		t.Fatal("an unknown provider reported facts")
	}
}

// TestTheMeasuredDeepSeekFactsAreWhatWasMeasured. Both came from this
// repository's own evidence, and a changed number here is a claim about a
// measurement that nobody re-took.
func TestTheMeasuredDeepSeekFactsAreWhatWasMeasured(t *testing.T) {
	facts, recorded := ai.Facts("deepseek", "deepseek-chat")
	if !recorded {
		t.Fatal("the one model with recorded facts has none")
	}
	// The provider's own refusal named this, and the fixture holds it.
	if facts.ContextWindow != 1048576 {
		t.Fatalf("the recorded window is %d; the probe recorded 1048576", facts.ContextWindow)
	}
	if !facts.ReasoningKnown || !facts.Reasoning {
		t.Fatalf("reasoning came back known=%v value=%v; the live test measured it supported",
			facts.ReasoningKnown, facts.Reasoning)
	}
	// No source was recorded for the output cap, so it must stay unknown
	// rather than carry a plausible number.
	if facts.MaxOutputTokens != 0 {
		t.Fatalf("an output cap of %d is recorded with no source", facts.MaxOutputTokens)
	}
}

// TestNotRecordedIsNotTheSameAsNotReasoning. Without the separate flag, a model
// nobody measured would be indistinguishable from one measured not to reason,
// and a thinking request would be dropped for a model that supports one.
func TestNotRecordedIsNotTheSameAsNotReasoning(t *testing.T) {
	unrecorded, found := ai.Facts("deepseek", "some-future-model")
	if found {
		t.Skip("this model has since been recorded; pick another")
	}
	if unrecorded.ReasoningKnown {
		t.Fatal("an unrecorded model claims its reasoning is known")
	}
}

// TestTheCatalogueSaysWhatItHasAndNotWhatAProviderOffers. Presenting these as a
// provider's model list would show a user a fraction of what they can call.
func TestTheCatalogueSaysWhatItHasAndNotWhatAProviderOffers(t *testing.T) {
	providers := ai.KnownProviders()
	if len(providers) == 0 {
		t.Fatal("no provider has recorded facts")
	}
	for _, provider := range providers {
		models := ai.KnownModels(provider)
		if len(models) == 0 {
			t.Fatalf("%s is listed with no recorded models", provider)
		}
		for _, model := range models {
			if _, recorded := ai.Facts(provider, model); !recorded {
				t.Fatalf("%s/%s is listed but has no facts", provider, model)
			}
		}
	}
	if len(ai.KnownModels("a-provider-with-no-entries")) != 0 {
		t.Fatal("an unknown provider listed models")
	}
}
