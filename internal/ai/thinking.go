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
