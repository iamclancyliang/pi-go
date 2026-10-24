package ai

import (
	"fmt"
	"strings"
)

// ThinkingLevel is how much a model is asked to reason before answering.
//
// Seven levels rather than a boolean, because the tradeoff is not on or off:
// reasoning costs tokens and latency, and how much is worth spending depends on
// the question. The names are Pi's, so a habit carries between them.
type ThinkingLevel string

const (
	ThinkingOff     ThinkingLevel = "off"
	ThinkingMinimal ThinkingLevel = "minimal"
	ThinkingLow     ThinkingLevel = "low"
	ThinkingMedium  ThinkingLevel = "medium"
	ThinkingHigh    ThinkingLevel = "high"
	ThinkingXHigh   ThinkingLevel = "xhigh"
	ThinkingMax     ThinkingLevel = "max"
)

// ThinkingLevels are the accepted values, weakest first. The order is the
// meaning: a caller cycling through them expects each step to ask for more.
var ThinkingLevels = []ThinkingLevel{
	ThinkingOff, ThinkingMinimal, ThinkingLow, ThinkingMedium,
	ThinkingHigh, ThinkingXHigh, ThinkingMax,
}

// ParseThinkingLevel reads a level, naming the alternatives when it cannot.
//
// An unrecognised level is refused rather than treated as unset. A caller who
// asked for more reasoning and silently got the default would read the answer
// as what the model produces when it thinks hard, which is the one conclusion
// they must not draw.
func ParseThinkingLevel(raw string) (ThinkingLevel, error) {
	candidate := ThinkingLevel(strings.ToLower(strings.TrimSpace(raw)))
	for _, level := range ThinkingLevels {
		if candidate == level {
			return level, nil
		}
	}
	names := make([]string, len(ThinkingLevels))
	for i, level := range ThinkingLevels {
		names[i] = string(level)
	}
	return "", fmt.Errorf("ai: %q is not a thinking level; it takes %s",
		raw, strings.Join(names, ", "))
}

// Valid reports a level this repository recognises. The empty level is valid
// and means the request says nothing, leaving the provider's own default.
func (l ThinkingLevel) Valid() bool {
	if l == "" {
		return true
	}
	for _, level := range ThinkingLevels {
		if l == level {
			return true
		}
	}
	return false
}

// AvailableThinkingLevels are the levels a model can be asked for, weakest
// first.
//
// Pi's rule (getSupportedThinkingLevels, packages/ai/src/models.ts): a model
// that does not reason takes only off; one that does takes off through high,
// and xhigh and max only when its catalogue entry maps them to something the
// provider accepts. No entry here records such a mapping — Pi's come from a
// generated catalogue that is not in the pinned tree — so no model offers
// them yet, and asking for one lands on high.
//
// A model nothing is recorded about is treated as reasoning, the same policy
// that lets a thinking request reach it at all: refusing would make every
// unrecorded model unable to reason, and unrecorded is the normal case.
func AvailableThinkingLevels(provider, model string) []ThinkingLevel {
	facts, recorded := Facts(provider, model)
	if recorded && facts.ReasoningKnown && !facts.Reasoning {
		return []ThinkingLevel{ThinkingOff}
	}
	return []ThinkingLevel{ThinkingOff, ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh}
}

// SupportsThinking reports whether a model takes any level beyond off.
func SupportsThinking(provider, model string) bool {
	return len(AvailableThinkingLevels(provider, model)) > 1
}

// ClampThinkingLevel is the nearest available level to the one asked for.
//
// Pi's order of preference (clampThinkingLevel): the level itself, then the
// nearest stronger one, then the nearest weaker one. Stronger first, because
// someone who asked for more reasoning than a model offers wants as much as it
// has, not the least.
func ClampThinkingLevel(available []ThinkingLevel, level ThinkingLevel) ThinkingLevel {
	if len(available) == 0 {
		return ThinkingOff
	}
	offered := func(l ThinkingLevel) bool {
		for _, a := range available {
			if a == l {
				return true
			}
		}
		return false
	}
	if offered(level) {
		return level
	}
	at := -1
	for i, l := range ThinkingLevels {
		if l == level {
			at = i
		}
	}
	if at < 0 {
		return available[0]
	}
	for i := at; i < len(ThinkingLevels); i++ {
		if offered(ThinkingLevels[i]) {
			return ThinkingLevels[i]
		}
	}
	for i := at - 1; i >= 0; i-- {
		if offered(ThinkingLevels[i]) {
			return ThinkingLevels[i]
		}
	}
	return available[0]
}

// NextThinkingLevel is the level after current in available, wrapping round.
// A current level not offered starts the cycle from the beginning, as Pi's
// does (an index of -1 steps to the first).
func NextThinkingLevel(available []ThinkingLevel, current ThinkingLevel) ThinkingLevel {
	if len(available) == 0 {
		return ThinkingOff
	}
	at := -1
	for i, l := range available {
		if l == current {
			at = i
		}
	}
	return available[(at+1)%len(available)]
}
