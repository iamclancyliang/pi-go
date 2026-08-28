package settings_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/settings"
)

func write(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing: %v", err)
	}
	return path
}

// TestAMissingFileIsAnEmptyScope, because most users never create one and that
// is not an error.
func TestAMissingFileIsAnEmptyScope(t *testing.T) {
	got, err := settings.Load(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.DefaultModel != "" || got.DefaultTools != nil || got.Compaction.KeepRecentTokens != 0 {
		t.Fatalf("a missing file loaded as %+v", got)
	}
}

// TestAMisspeltKeyIsAnErrorNotAShrug. A key that parses cleanly and does
// nothing is a setting the user believes is on, and the belief survives until
// whatever it was meant to change goes wrong.
func TestAMisspeltKeyIsAnErrorNotAShrug(t *testing.T) {
	path := write(t, t.TempDir(), "settings.json", `{"defualtModel":"x"}`)
	if _, err := settings.Load(path); err == nil {
		t.Fatal("a misspelt key was accepted")
	}
}

// TestAnInvalidValueNamesItsFileAndChoices, so the user can fix it without
// reading this program.
func TestAnInvalidValueNamesItsFileAndChoices(t *testing.T) {
	path := write(t, t.TempDir(), "settings.json", `{"defaultProjectTrust":"maybe"}`)
	_, err := settings.Load(path)
	if err == nil {
		t.Fatal("an invalid trust default was accepted")
	}
	for _, want := range []string{path, "ask", "always", "never"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the failure does not mention %q: %v", want, err)
		}
	}
}

// TestTheProjectScopeWinsWhereItSpeaks and is silent where it does not.
func TestTheProjectScopeWinsWhereItSpeaks(t *testing.T) {
	global := settings.Settings{
		DefaultProvider: "deepseek",
		DefaultModel:    "deepseek-chat",
		ShellPath:       "bash",
	}
	project := settings.Settings{DefaultModel: "deepseek-reasoner"}

	merged := settings.Merge(global, project)
	if merged.DefaultModel != "deepseek-reasoner" {
		t.Fatalf("the project's model did not win: %+v", merged)
	}
	if merged.DefaultProvider != "deepseek" || merged.ShellPath != "bash" {
		t.Fatalf("silence in the project scope erased global values: %+v", merged)
	}
}

// TestAProjectCannotSetItsOwnTrustDefault: that would be the project trusting
// itself.
func TestAProjectCannotSetItsOwnTrustDefault(t *testing.T) {
	merged := settings.Merge(
		settings.Settings{DefaultProjectTrust: settings.TrustAsk},
		settings.Settings{DefaultProjectTrust: settings.TrustAlways})
	if merged.DefaultProjectTrust != settings.TrustAsk {
		t.Fatalf("a project raised its own trust default to %q", merged.DefaultProjectTrust)
	}
}

// TestAnEmptyToolListIsAbsentAndAnExplicitOneWins. nil means "did not say";
// an empty list would mean "no tools", and the merge must keep them apart.
func TestAnEmptyToolListIsAbsentAndAnExplicitOneWins(t *testing.T) {
	global := settings.Settings{DefaultTools: []string{"read", "grep"}}
	if merged := settings.Merge(global, settings.Settings{}); len(merged.DefaultTools) != 2 {
		t.Fatalf("a silent project erased the global tool list: %+v", merged.DefaultTools)
	}
	project := settings.Settings{DefaultTools: []string{"read"}}
	if merged := settings.Merge(global, project); len(merged.DefaultTools) != 1 {
		t.Fatalf("an explicit project tool list did not win: %+v", merged.DefaultTools)
	}
}

// TestSavingRoundTrips, and through a rename so a failure leaves the previous
// file.
func TestSavingRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	want := settings.Settings{
		DefaultModel: "deepseek-chat",
		Compaction:   settings.Compaction{KeepRecentTokens: 8000},
	}
	if err := settings.Save(path, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := settings.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.DefaultModel != want.DefaultModel ||
		got.Compaction.KeepRecentTokens != want.Compaction.KeepRecentTokens {
		t.Fatalf("came back as %+v", got)
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Fatalf("saving left extra files: %v", entries)
	}
}
