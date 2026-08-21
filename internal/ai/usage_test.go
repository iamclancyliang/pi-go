package ai_test

import (
	"testing"

	"github.com/iamclancyliang/pi-go/internal/ai"
)

func ptr(n int) *int { return &n }

// TestTotalCountsEveryReportedToken.
//
// A prompt served partly from cache is not a smaller prompt. Input is the
// uncached remainder, so a total that adds only input and output silently drops
// whatever the cache supplied — on the calls that hit the cache, which is most
// of them.
func TestTotalCountsEveryReportedToken(t *testing.T) {
	for _, tc := range []struct {
		name  string
		usage ai.Usage
		want  int
	}{
		{
			name:  "nothing cached",
			usage: ai.Usage{InputTokens: 1000, OutputTokens: 5, Reported: true},
			want:  1005,
		},
		{
			name: "most of the prompt cached",
			usage: ai.Usage{
				InputTokens: 400, OutputTokens: 5, CacheReadTokens: ptr(600), Reported: true,
			},
			want: 1005,
		},
		{
			name: "a reported zero cache read changes nothing",
			usage: ai.Usage{
				InputTokens: 1000, OutputTokens: 5, CacheReadTokens: ptr(0), Reported: true,
			},
			want: 1005,
		},
		{
			name: "reasoning is inside output, not beside it",
			usage: ai.Usage{
				InputTokens: 100, OutputTokens: 50, ReasoningTokens: ptr(20), Reported: true,
			},
			want: 150,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.usage.Total(); got != tc.want {
				t.Fatalf("Total() = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestTheSamePromptTotalsTheSameWhetherCached: the total is what the call used,
// and caching changes what it costs rather than what it consumed.
func TestTheSamePromptTotalsTheSameWhetherCached(t *testing.T) {
	cold := ai.Usage{InputTokens: 1000, OutputTokens: 10, Reported: true}
	warm := ai.Usage{
		InputTokens: 250, OutputTokens: 10, CacheReadTokens: ptr(750), Reported: true,
	}
	if cold.Total() != warm.Total() {
		t.Fatalf("the same 1000-token prompt totals %d cold and %d warm",
			cold.Total(), warm.Total())
	}
}

// TestASnapshotDoesNotChangeAfterItIsTaken: the optional counts are pointers, so
// a copied struct still shares them. A ledger entry a caller can edit records
// nothing.
func TestASnapshotDoesNotChangeAfterItIsTaken(t *testing.T) {
	original := ai.Usage{
		InputTokens: 10, OutputTokens: 2,
		CacheReadTokens: ptr(5), ReasoningTokens: ptr(1), Reported: true,
	}
	snapshot := original.Clone()

	*original.CacheReadTokens = 999
	*original.ReasoningTokens = 999

	if *snapshot.CacheReadTokens != 5 || *snapshot.ReasoningTokens != 1 {
		t.Fatalf("editing the original rewrote the snapshot: cache=%d reasoning=%d",
			*snapshot.CacheReadTokens, *snapshot.ReasoningTokens)
	}
}
