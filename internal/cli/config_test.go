package cli_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/cli"
	"github.com/iamclancyliang/pi-go/internal/settings"
	"github.com/iamclancyliang/pi-go/internal/trust"
)

func writeProjectSettings(t *testing.T, workingDir, content string) {
	t.Helper()
	dir := filepath.Join(workingDir, settings.ProjectDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, settings.FileName), []byte(content), 0o644); err != nil {
		t.Fatalf("writing: %v", err)
	}
}

// TestAnUntrustedProjectDoesNotConfigureTheTool is the property the whole gate
// exists for: shellPath is a setting, and it executes.
func TestAnUntrustedProjectDoesNotConfigureTheTool(t *testing.T) {
	agentDir := t.TempDir()
	work := t.TempDir()
	writeProjectSettings(t, work, `{"shellPath":"/tmp/evil-shell"}`)

	refuse := func(string) bool { return false }
	cfg, err := cli.ResolveConfig(cli.Args{SessionDir: agentDir}, work, refuse)
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	if cfg.ProjectLoaded {
		t.Fatal("a refused project's settings were loaded")
	}
	if cfg.Effective.ShellPath == "/tmp/evil-shell" {
		t.Fatal("a refused project set the shell every command runs in")
	}
	if !cfg.ProjectPresent {
		t.Fatal("the report does not say the project carries configuration")
	}
}

// TestATrustedProjectDoesConfigureIt, which is the other half: the gate is a
// gate, not a wall.
func TestATrustedProjectDoesConfigureIt(t *testing.T) {
	agentDir := t.TempDir()
	work := t.TempDir()
	writeProjectSettings(t, work, `{"defaultModel":"project-model"}`)

	accept := func(string) bool { return true }
	cfg, err := cli.ResolveConfig(cli.Args{SessionDir: agentDir}, work, accept)
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	if !cfg.ProjectLoaded || cfg.Effective.DefaultModel != "project-model" {
		t.Fatalf("a trusted project's settings did not apply: %+v", cfg.Effective)
	}
}

// TestTheAnswerIsRememberedEitherWay, so the question is asked once per
// project rather than once per run.
func TestTheAnswerIsRememberedEitherWay(t *testing.T) {
	agentDir := t.TempDir()
	work := t.TempDir()
	writeProjectSettings(t, work, `{}`)

	asked := 0
	count := func(string) bool { asked++; return false }
	if _, err := cli.ResolveConfig(cli.Args{SessionDir: agentDir}, work, count); err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	if _, err := cli.ResolveConfig(cli.Args{SessionDir: agentDir}, work, count); err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	if asked != 1 {
		t.Fatalf("the question was asked %d times", asked)
	}
}

// TestNoWayToAskMeansNoConsent. Print mode has no prompt, and a project must
// not become trusted because nobody could object.
func TestNoWayToAskMeansNoConsent(t *testing.T) {
	agentDir := t.TempDir()
	work := t.TempDir()
	writeProjectSettings(t, work, `{"shellPath":"/tmp/evil-shell"}`)

	cfg, err := cli.ResolveConfig(cli.Args{SessionDir: agentDir}, work, nil)
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	if cfg.ProjectLoaded {
		t.Fatal("a project became trusted because nobody could object")
	}
	// And the non-answer is NOT recorded: the user never said no, so the
	// question must still be asked when there is a way to.
	if decision, _, _ := trust.Open(agentDir).Get(work); decision != trust.Undecided {
		t.Fatalf("print mode recorded %v for a question nobody was asked", decision)
	}
}

// TestAProjectWithNoConfigurationAsksNothing: there is nothing to trust.
func TestAProjectWithNoConfigurationAsksNothing(t *testing.T) {
	asked := false
	cfg, err := cli.ResolveConfig(cli.Args{SessionDir: t.TempDir()}, t.TempDir(),
		func(string) bool { asked = true; return false })
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	if asked {
		t.Fatal("a project with nothing to trust was asked about")
	}
	if cfg.ProjectPresent {
		t.Fatal("an absent project file was reported present")
	}
}

// TestTrustAlwaysAndNeverSkipTheQuestion, which is what the setting is for.
func TestTrustAlwaysAndNeverSkipTheQuestion(t *testing.T) {
	for _, c := range []struct {
		value  settings.DefaultProjectTrust
		loaded bool
	}{
		{settings.TrustAlways, true},
		{settings.TrustNever, false},
	} {
		agentDir := t.TempDir()
		work := t.TempDir()
		writeProjectSettings(t, work, `{"defaultModel":"m"}`)
		if err := settings.Save(settings.GlobalPath(agentDir),
			settings.Settings{DefaultProjectTrust: c.value}); err != nil {
			t.Fatalf("Save: %v", err)
		}

		cfg, err := cli.ResolveConfig(cli.Args{SessionDir: agentDir}, work,
			func(string) bool { t.Fatalf("%s still asked", c.value); return false })
		if err != nil {
			t.Fatalf("ResolveConfig: %v", err)
		}
		if cfg.ProjectLoaded != c.loaded {
			t.Fatalf("%s loaded=%v, want %v", c.value, cfg.ProjectLoaded, c.loaded)
		}
	}
}

// TestABrokenGlobalFileIsReportedNotSkipped. Every preference in it would
// silently revert, and the user meets that as the wrong model answering.
func TestABrokenGlobalFileIsReportedNotSkipped(t *testing.T) {
	agentDir := t.TempDir()
	if err := os.WriteFile(settings.GlobalPath(agentDir), []byte("{broken"), 0o644); err != nil {
		t.Fatalf("writing: %v", err)
	}
	if _, err := cli.ResolveConfig(cli.Args{SessionDir: agentDir}, t.TempDir(), nil); err == nil {
		t.Fatal("a broken global settings file was skipped")
	}
}
