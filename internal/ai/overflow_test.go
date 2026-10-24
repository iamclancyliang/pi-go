package ai_test

import (
	"errors"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/ai"
)

// TestTheRecordedOverflowWordingsAreRecognised holds every message pi's suite
// records as an overflow, verbatim, status prefix and wrapping included —
// pi matches the whole error message, and so does this.
func TestTheRecordedOverflowWordingsAreRecognised(t *testing.T) {
	for _, message := range []string{
		"400 `prompt too long; exceeded max context length by 100918 tokens`",
		"400 The input (516368 tokens) is longer than the model's context length (262144 tokens).",
		"Error: 503 litellm.ServiceUnavailableError: litellm.MidStreamFallbackError: " +
			"litellm.APIConnectionError: APIConnectionError: OpenAIException - Requested token count " +
			"exceeds the model's maximum context length of 131072 tokens.",
		"Error: 400 Input length (265330) exceeds model's maximum context length (262144).",
		"Provider returned error: Input length 131393 exceeds the maximum allowed input length of 131040 tokens.",
		"400 Prompt has 256468 tokens, but the configured context size is 256000 tokens",
		"Prompt has 5,958,968 tokens, but the configured context size is 256,000 tokens",
	} {
		if !ai.IsOverflowWording(message) {
			t.Errorf("not recognised as an overflow: %q", message)
		}
	}
}

// TestFailuresThatAreNotOverflowsStayUnrecognised holds pi's negatives. Each
// is the expensive direction: recognising one spends a second billed request
// shortening a conversation that was never too long.
func TestFailuresThatAreNotOverflowsStayUnrecognised(t *testing.T) {
	for _, message := range []string{
		"500 `model runner crashed unexpectedly`",
		"Throttling error: Too many tokens, please wait before trying again.",
		"Service unavailable: The service is temporarily unavailable.",
		"Rate limit exceeded, please retry after 30 seconds.",
		"Too many requests. Please slow down.",
	} {
		if ai.IsOverflowWording(message) {
			t.Errorf("recognised as an overflow: %q", message)
		}
	}
}

// TestAThrottleIsNotAnOverflowEvenWhenItTalksLikeOne: an exclusion outranks a
// match, because a provider rate-limiting by tokens can phrase it in an
// overflow's terms.
func TestAThrottleIsNotAnOverflowEvenWhenItTalksLikeOne(t *testing.T) {
	const message = "Rate limit: Input length 131393 exceeds the maximum allowed input length of 131040 tokens."
	if ai.IsOverflowWording(message) {
		t.Fatalf("a rate limit was recognised as an overflow: %q", message)
	}
}

// TestARefusalInOverflowWordingBecomesAnOverflow, while staying the refusal it
// was: the spend it carries is what the ledger reads, and losing it would
// record a billed call as free.
func TestARefusalInOverflowWordingBecomesAnOverflow(t *testing.T) {
	refused := &ai.ProviderError{
		Provider: "openrouter", Failure: ai.FailureRefused, Status: 400,
		Detail: "Input length 131393 exceeds the maximum allowed input length of 131040 tokens.",
	}
	refused.Record(ai.Usage{InputTokens: 7})

	err := ai.RecogniseOverflow(refused)

	if !errors.Is(err, ai.ErrContextOverflow) {
		t.Fatalf("not recognised as an overflow: %v", err)
	}
	var classified *ai.ProviderError
	if !errors.As(err, &classified) {
		t.Fatalf("the refusal is no longer reachable: %v", err)
	}
	if used := classified.Consumed(); len(used) != 1 || used[0].InputTokens != 7 {
		t.Fatalf("the spend did not survive: %+v", used)
	}
}

// TestOnlyAProvidersOwnRefusalIsRead. Text that merely quotes an overflow's
// wording — a wire failure, a wrapped log line — is not a provider saying the
// request was too large.
func TestOnlyAProvidersOwnRefusalIsRead(t *testing.T) {
	quoted := errors.New("prompt too long; exceeded max context length by 5 tokens")
	if got := ai.RecogniseOverflow(quoted); errors.Is(got, ai.ErrContextOverflow) {
		t.Fatalf("an unclassified error was recognised from its text: %v", got)
	}

	other := &ai.ProviderError{Provider: "ark", Failure: ai.FailureRefused, Status: 400,
		Detail: "invalid parameter: temperature"}
	if got := ai.RecogniseOverflow(other); got != error(other) {
		t.Fatalf("an ordinary refusal was changed: %v", got)
	}
	if ai.RecogniseOverflow(nil) != nil {
		t.Fatal("no error became one")
	}
}

// TestAnOverflowReportedMidStreamIsRecognised. pi's LiteLLM wording arrives as
// a mid-stream fallback error, after the reply began, so the refusal paths at
// the transport never see it. Every streaming port ends a failed reply
// through the accumulator, which is where it is recognised.
func TestAnOverflowReportedMidStreamIsRecognised(t *testing.T) {
	acc := ai.NewAccumulator("m")
	if _, err := acc.Begin(); err != nil {
		t.Fatal(err)
	}
	cause := &ai.ProviderError{Provider: "openai", Failure: ai.FailureTransient, Status: 503,
		Detail: "litellm.MidStreamFallbackError: OpenAIException - Requested token count exceeds " +
			"the model's maximum context length of 131072 tokens."}

	ev, err := acc.Fail(ai.StopError, cause)
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(ev.Final.Cause, ai.ErrContextOverflow) {
		t.Fatalf("a mid-stream overflow ended as %v", ev.Final.Cause)
	}
}

// TestAnAbortIsNeverRecognisedAsAnOverflow. A reply the caller stopped did not
// fail, and recovering from it would ask again for an answer nobody wants.
func TestAnAbortIsNeverRecognisedAsAnOverflow(t *testing.T) {
	acc := ai.NewAccumulator("m")
	if _, err := acc.Begin(); err != nil {
		t.Fatal(err)
	}
	cause := &ai.ProviderError{Provider: "ollama", Failure: ai.FailureRefused,
		Detail: "prompt too long; exceeded max context length by 5 tokens"}

	ev, err := acc.Fail(ai.StopAborted, cause)
	if err != nil {
		t.Fatal(err)
	}
	if errors.Is(ev.Final.Cause, ai.ErrContextOverflow) {
		t.Fatalf("an aborted reply was recognised as an overflow: %v", ev.Final.Cause)
	}
}
