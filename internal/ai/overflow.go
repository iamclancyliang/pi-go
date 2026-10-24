package ai

import (
	"errors"
	"fmt"
	"regexp"
)

// overflowWordings are refusal texts recorded as a context overflow, matched
// in any provider's own error message.
//
// Every entry is a wording pi's test suite exercises against a real message
// (packages/ai/test/overflow.test.ts at the pin), not one inferred from
// documentation: a matcher written from a guess tests only the matcher. pi's
// detector carries more patterns than these, documented by example in its
// source but not exercised; they stay out until a response recording them
// exists. The patterns themselves are pi's (packages/ai/src/utils/overflow.ts).
//
// A port that can compare the two numbers an overflow carries does that first
// and keeps doing it (DeepSeek): numbers are the condition itself, and prose
// is the provider's to reword. This list is the fallback for every provider
// whose wording nobody here has recorded, so that the one failure this
// repository can recover from is not reported as an ordinary refusal.
var overflowWordings = []*regexp.Regexp{
	// Ollama: "prompt too long; exceeded max context length by 100918
	// tokens" (overflow.test.ts:33).
	regexp.MustCompile(`(?i)prompt too long; exceeded (?:max )?context length`),
	// Together AI: "The input (516368 tokens) is longer than the model's
	// context length (262144 tokens)." (overflow.test.ts:38).
	regexp.MustCompile(`(?i)input \(\d+ tokens\) is longer than the model'?s context length \(\d+ tokens\)`),
	// LiteLLM-wrapped OpenAI: "Requested token count exceeds the model's
	// maximum context length of 131072 tokens." (overflow.test.ts:45), and the
	// OpenAI-compatible parenthesised form "Input length (265330) exceeds
	// model's maximum context length (262144)." (overflow.test.ts:52).
	regexp.MustCompile(`(?i)exceeds (?:the )?(?:model'?s )?maximum context length(?: of [\d,]+ tokens?|\s*\([\d,]+\))`),
	// OpenRouter/Poolside: "Input length 131393 exceeds the maximum allowed
	// input length of 131040 tokens." (overflow.test.ts:59).
	regexp.MustCompile(`(?i)exceeds (?:the )?maximum allowed input length of [\d,]+ tokens?`),
	// DS4: "Prompt has 256468 tokens, but the configured context size is
	// 256000 tokens", also with grouped digits (overflow.test.ts:66).
	regexp.MustCompile(`(?i)prompt has [\d,]+ tokens?, but the configured context size is [\d,]+ tokens?`),
}

// notOverflowWordings exclude a message even when it also matches an overflow
// wording. pi keeps these because a throttle can talk about tokens — Bedrock
// rate limiting reads "Too many tokens, please wait" — and a false positive
// is the expensive direction: it spends a second billed request shortening a
// conversation that was never too long.
var notOverflowWordings = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^(?:Throttling error|Service unavailable):`),
	regexp.MustCompile(`(?i)rate limit`),
	regexp.MustCompile(`(?i)too many requests`),
}

// IsOverflowWording reports whether a provider's own refusal text is one
// recorded as a context overflow.
func IsOverflowWording(text string) bool {
	for _, p := range notOverflowWordings {
		if p.MatchString(text) {
			return false
		}
	}
	for _, p := range overflowWordings {
		if p.MatchString(text) {
			return true
		}
	}
	return false
}

// RecogniseOverflow marks a classified provider refusal as a context overflow
// when the provider's own message is a recorded overflow wording, and returns
// every other error unchanged.
//
// Only a ProviderError is read, and only its Detail — the provider's message
// as the port scrubbed it. An error that merely quotes overflow-like text is
// not a provider saying so, and recovery must key on what a failure is, not on
// what its text happens to contain.
//
// The result wraps both the sentinel and the original, so errors.Is finds
// ErrContextOverflow while errors.As still reaches the ProviderError and the
// spend it carries.
//
// Ports call this where they classify a refusal; nothing above the model
// boundary reads provider wording.
func RecogniseOverflow(err error) error {
	if err == nil || errors.Is(err, ErrContextOverflow) {
		return err
	}
	var refused *ProviderError
	if !errors.As(err, &refused) || !IsOverflowWording(refused.Detail) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrContextOverflow, err)
}
