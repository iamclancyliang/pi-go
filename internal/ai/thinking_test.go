package ai_test

import (
	"slices"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/ai"
)

// TestAModelOffersOffThroughHighUntilAMappingSaysMore. xhigh and max are
// provider-specific values that only a per-model mapping can name, and none is
// recorded, so asking for them lands on high rather than on a value the
// provider may refuse.
func TestAModelOffersOffThroughHighUntilAMappingSaysMore(t *testing.T) {
	want := []ai.ThinkingLevel{ai.ThinkingOff, ai.ThinkingMinimal, ai.ThinkingLow, ai.ThinkingMedium, ai.ThinkingHigh}
	for _, m := range [][2]string{{"deepseek", "deepseek-chat"}, {"openai", "never-recorded"}} {
		if got := ai.AvailableThinkingLevels(m[0], m[1]); !slices.Equal(got, want) {
			t.Errorf("%s/%s offers %v, want %v", m[0], m[1], got, want)
		}
	}
}

// TestClampingPrefersMoreThenLess is Pi's order: someone who asked for more
// reasoning than a model offers wants as much as it has.
func TestClampingPrefersMoreThenLess(t *testing.T) {
	five := ai.AvailableThinkingLevels("openai", "never-recorded")
	cases := []struct {
		available []ai.ThinkingLevel
		asked     ai.ThinkingLevel
		want      ai.ThinkingLevel
	}{
		{five, ai.ThinkingMedium, ai.ThinkingMedium},
		{five, ai.ThinkingMax, ai.ThinkingHigh},
		{five, ai.ThinkingXHigh, ai.ThinkingHigh},
		{[]ai.ThinkingLevel{ai.ThinkingOff}, ai.ThinkingHigh, ai.ThinkingOff},
		{[]ai.ThinkingLevel{ai.ThinkingOff, ai.ThinkingHigh}, ai.ThinkingLow, ai.ThinkingHigh},
	}
	for _, c := range cases {
		if got := ai.ClampThinkingLevel(c.available, c.asked); got != c.want {
			t.Errorf("clamp(%v, %s) = %s, want %s", c.available, c.asked, got, c.want)
		}
	}
}

// TestCyclingWrapsAndStartsOverFromAnUnofferedLevel.
func TestCyclingWrapsAndStartsOverFromAnUnofferedLevel(t *testing.T) {
	five := ai.AvailableThinkingLevels("openai", "never-recorded")
	if got := ai.NextThinkingLevel(five, ai.ThinkingLow); got != ai.ThinkingMedium {
		t.Errorf("after low came %s", got)
	}
	if got := ai.NextThinkingLevel(five, ai.ThinkingHigh); got != ai.ThinkingOff {
		t.Errorf("after high came %s, want the cycle to wrap to off", got)
	}
	if got := ai.NextThinkingLevel(five, ai.ThinkingMax); got != ai.ThinkingOff {
		t.Errorf("after an unoffered level came %s, want the first", got)
	}
}
