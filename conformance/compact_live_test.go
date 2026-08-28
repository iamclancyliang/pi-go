package conformance

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/cli"
	"github.com/iamclancyliang/pi-go/internal/compaction"
)

// TestLiveDeepSeekSummarisesAConversation is the check a scripted model cannot
// make: whether a real provider, given Pi's prompt and a conversation as text,
// returns a checkpoint in the shape the next call has to read — and whether it
// summarises rather than continuing the conversation it was shown.
func TestLiveDeepSeekSummarisesAConversation(t *testing.T) {
	liveOrSkip(t)

	transport := &countingRoundTripper{inner: http.DefaultTransport}
	port, _, model, err := cli.Open(cli.Args{}, transport)
	if err != nil {
		t.Fatalf("opening the provider: %v", err)
	}

	// A conversation with a goal, a decision and a file in it, long enough that
	// the budget forces a cut.
	filler := strings.Repeat("context that is not important. ", 200)
	var messages []ai.Message
	messages = append(messages,
		ai.Message{Role: ai.RoleUser, Content: "I want to add retry logic to internal/http/client.go. " + filler},
		ai.Message{Role: ai.RoleAssistant, Content: "Understood. I will use exponential backoff. " + filler},
		ai.Message{Role: ai.RoleUser, Content: "We decided against jitter because the tests need determinism. " + filler},
		ai.Message{Role: ai.RoleAssistant, Content: "Noted, no jitter. " + filler},
	)
	for i := 0; i < 12; i++ {
		messages = append(messages,
			ai.Message{Role: ai.RoleUser, Content: "keep going. " + filler},
			ai.Message{Role: ai.RoleAssistant, Content: "still working. " + filler})
	}

	c := &compaction.Compactor{Model: port, ModelName: model, KeepRecentTokens: 2000}
	summary, kept, err := c.Summarize(context.Background(), messages)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}

	// The format is what the next call reads instead of the conversation.
	for _, section := range []string{"## Goal", "## Next Steps"} {
		if !strings.Contains(summary, section) {
			t.Fatalf("the summary is not in the checkpoint format:\n%s", summary)
		}
	}
	// The specifics are what make a summary usable rather than prose about a
	// conversation.
	if !strings.Contains(summary, "client.go") {
		t.Fatalf("the summary lost the file path it was told about:\n%s", summary)
	}
	if len(kept) == 0 || len(kept) >= len(messages) {
		t.Fatalf("the budget kept %d of %d messages", len(kept), len(messages))
	}
	// A model handed real turns continues them. This one must have summarised.
	if strings.HasPrefix(strings.TrimSpace(summary), "still working") {
		t.Fatalf("the model continued the conversation instead of summarising it:\n%s", summary)
	}

	t.Logf("kept %d of %d messages", len(kept), len(messages))
	t.Logf("summary:\n%s", summary)
}
