package trust_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/trust"
)

// TestADecisionCoversWhatIsUnderIt is what "trust the parent folder" means.
func TestADecisionCoversWhatIsUnderIt(t *testing.T) {
	agentDir := t.TempDir()
	work := t.TempDir()
	store := trust.Open(agentDir)

	if err := store.Set(work, trust.Trusted); err != nil {
		t.Fatalf("Set: %v", err)
	}
	nested := filepath.Join(work, "deep", "inside")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	decision, from, err := store.Get(nested)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if decision != trust.Trusted {
		t.Fatalf("a directory under a trusted one came back %v", decision)
	}
	resolved, _ := filepath.EvalSymlinks(work)
	if from != resolved {
		t.Fatalf("the decision came from %q, want the recorded ancestor %q", from, resolved)
	}
}

// TestTheNearestEntryWins, so one refused subdirectory inside a trusted tree
// stays refused.
func TestTheNearestEntryWins(t *testing.T) {
	agentDir := t.TempDir()
	work := t.TempDir()
	inner := filepath.Join(work, "vendor")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	store := trust.Open(agentDir)
	store.Set(work, trust.Trusted)
	store.Set(inner, trust.Refused)

	if decision, _, _ := store.Get(inner); decision != trust.Refused {
		t.Fatalf("the nearer refusal lost to the ancestor: %v", decision)
	}
	if decision, _, _ := store.Get(work); decision != trust.Trusted {
		t.Fatalf("the refusal leaked upward: %v", decision)
	}
}

// TestNobodyHasSaidIsItsOwnState, distinct from refused: undecided asks.
func TestNobodyHasSaidIsItsOwnState(t *testing.T) {
	decision, from, err := trust.Open(t.TempDir()).Get(t.TempDir())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if decision != trust.Undecided || from != "" {
		t.Fatalf("an unasked directory came back %v from %q", decision, from)
	}
}

// TestForgettingReopensTheQuestion: recording undecided removes the entry.
func TestForgettingReopensTheQuestion(t *testing.T) {
	agentDir := t.TempDir()
	work := t.TempDir()
	store := trust.Open(agentDir)
	store.Set(work, trust.Refused)
	if err := store.Set(work, trust.Undecided); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if decision, _, _ := store.Get(work); decision != trust.Undecided {
		t.Fatalf("forgetting left %v", decision)
	}
}

// TestDecisionsSurviveTheProcess.
func TestDecisionsSurviveTheProcess(t *testing.T) {
	agentDir := t.TempDir()
	work := t.TempDir()
	trust.Open(agentDir).Set(work, trust.Trusted)

	if decision, _, _ := trust.Open(agentDir).Get(work); decision != trust.Trusted {
		t.Fatalf("a decision did not survive reopening: %v", decision)
	}
}

// TestTwoNamesForOneDirectoryShareOneAnswer, or a symlinked path would be asked
// separately and could answer differently.
func TestTwoNamesForOneDirectoryShareOneAnswer(t *testing.T) {
	agentDir := t.TempDir()
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}

	store := trust.Open(agentDir)
	store.Set(link, trust.Trusted)
	if decision, _, _ := store.Get(real); decision != trust.Trusted {
		t.Fatalf("the real path did not share the link's answer: %v", decision)
	}
}
