package compaction

import (
	"strings"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/ai"
)

func TestBashExecutionContributesToCompactionAndHonorsExclusion(t *testing.T) {
	code := 0
	visible := ai.Message{
		Role: ai.RoleUser,
		BashExecution: &ai.BashExecution{
			Command: "printf visible", Output: "visible\n", ExitCode: &code,
		},
	}
	if EstimateTokens(visible) == 0 {
		t.Fatal("a visible bash execution had no compaction cost")
	}
	text := serialize([]ai.Message{visible})
	if !strings.Contains(text, "printf visible") || !strings.Contains(text, "visible") {
		t.Fatalf("bash execution was omitted from the summary text: %q", text)
	}

	hidden := visible
	hidden.BashExecution = &ai.BashExecution{
		Command: "printf hidden", Output: "hidden\n", ExcludeFromContext: true,
	}
	if EstimateTokens(hidden) != 0 || serialize([]ai.Message{hidden}) != "" {
		t.Fatalf("excluded bash execution reached compaction: cost=%d text=%q",
			EstimateTokens(hidden), serialize([]ai.Message{hidden}))
	}
}
