// Package settings is how a preference outlives the command line that first
// expressed it.
//
// Two layers: a global file in the agent directory, and a project file in the
// working directory's .pi-go. The project layer wins where they disagree,
// because the more specific intent is the newer one — and it is only read at
// all when the project is trusted, since a cloned repository writing this
// process's defaults is a repository configuring the tool that is about to run
// shell commands.
//
// Every key here is one this build actually reads. Pi has forty-nine; most
// configure subsystems pi-go does not have, and a settings file full of keys
// nothing honours is the same failure as a flag that is silently ignored — the
// user believes something took effect. Keys are added when the thing they
// configure exists.
package settings

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FileName is what a settings file is called in either scope.
const FileName = "settings.json"

// ProjectDirName is the directory a project's own configuration lives in.
//
// pi-go's own, not Pi's .pi: ADR-0006 separates the two programs' files, and a
// shared directory would have this build acting on configuration written for
// the other one's features.
const ProjectDirName = ".pi-go"

// DefaultProjectTrust is what happens when a project asks to configure the
// tool and nobody has decided whether to let it.
type DefaultProjectTrust string

const (
	TrustAsk    DefaultProjectTrust = "ask"
	TrustAlways DefaultProjectTrust = "always"
	TrustNever  DefaultProjectTrust = "never"
)

// Compaction bounds what /compact and overflow recovery keep verbatim.
type Compaction struct {
	KeepRecentTokens int `json:"keepRecentTokens,omitempty"`
}

// Settings are the preferences this build honours.
type Settings struct {
	// DefaultProvider and DefaultModel stand in for --provider and --model
	// when the flags are absent.
	DefaultProvider string `json:"defaultProvider,omitempty"`
	DefaultModel    string `json:"defaultModel,omitempty"`

	// SessionDir stands in for --session-dir.
	SessionDir string `json:"sessionDir,omitempty"`

	// ShellPath is the interpreter the bash tool runs. Empty means bash.
	ShellPath string `json:"shellPath,omitempty"`

	// ShellCommandPrefix is prepended to every bash command, on its own line —
	// how a user gets aliases or a required environment into every command the
	// model runs.
	ShellCommandPrefix string `json:"shellCommandPrefix,omitempty"`

	// DefaultTools names the built-in tools to offer. Empty means all of them.
	DefaultTools []string `json:"defaultTools,omitempty"`

	// QuietStartup suppresses the session banner.
	QuietStartup bool `json:"quietStartup,omitempty"`

	// DefaultProjectTrust decides what happens when a project carries
	// configuration and nobody has said whether to trust it. Global only: a
	// project choosing its own trust default is the project trusting itself.
	DefaultProjectTrust DefaultProjectTrust `json:"defaultProjectTrust,omitempty"`

	Compaction Compaction `json:"compaction,omitempty"`
}

// GlobalPath is where the global scope lives.
func GlobalPath(agentDir string) string { return filepath.Join(agentDir, FileName) }

// ProjectPath is where a project's scope lives.
func ProjectPath(workingDir string) string {
	return filepath.Join(workingDir, ProjectDirName, FileName)
}

// Load reads one scope. A missing file is an empty scope, not an error.
func Load(path string) (Settings, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Settings{}, nil
		}
		return Settings{}, fmt.Errorf("settings: %w", err)
	}
	var s Settings
	// Unknown keys are an error, not a shrug. A misspelt key that parses
	// cleanly is a setting the user believes is on, and the belief survives
	// until whatever it was meant to change goes wrong.
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&s); err != nil {
		return Settings{}, fmt.Errorf("settings: %s: %w", path, err)
	}
	if err := s.validate(path); err != nil {
		return Settings{}, err
	}
	return s, nil
}

func (s Settings) validate(path string) error {
	switch s.DefaultProjectTrust {
	case "", TrustAsk, TrustAlways, TrustNever:
	default:
		return fmt.Errorf("settings: %s: defaultProjectTrust is %q; it takes ask, always or never",
			path, s.DefaultProjectTrust)
	}
	if s.Compaction.KeepRecentTokens < 0 {
		return fmt.Errorf("settings: %s: compaction.keepRecentTokens is negative", path)
	}
	return nil
}

// Merge lays the project scope over the global one.
//
// Field by field rather than by reflection: each field's zero value is its
// "not set", and a merge that cannot tell "set to nothing" from "absent" is
// decided here, per field, where the difference is visible.
func Merge(global, project Settings) Settings {
	merged := global
	if project.DefaultProvider != "" {
		merged.DefaultProvider = project.DefaultProvider
	}
	if project.DefaultModel != "" {
		merged.DefaultModel = project.DefaultModel
	}
	if project.SessionDir != "" {
		merged.SessionDir = project.SessionDir
	}
	if project.ShellPath != "" {
		merged.ShellPath = project.ShellPath
	}
	if project.ShellCommandPrefix != "" {
		merged.ShellCommandPrefix = project.ShellCommandPrefix
	}
	if project.DefaultTools != nil {
		merged.DefaultTools = append([]string(nil), project.DefaultTools...)
	}
	if project.QuietStartup {
		merged.QuietStartup = true
	}
	// DefaultProjectTrust is deliberately NOT merged from the project: a
	// project choosing its own trust default is the project trusting itself.
	if project.Compaction.KeepRecentTokens != 0 {
		merged.Compaction.KeepRecentTokens = project.Compaction.KeepRecentTokens
	}
	return merged
}

// Save writes one scope, through a neighbour and a rename so a failure
// part-way leaves the previous file rather than a truncated one.
func Save(path string, s Settings) error {
	if err := s.validate(path); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("settings: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("settings: %w", err)
	}
	temporary := path + ".writing"
	if err := os.WriteFile(temporary, append(encoded, '\n'), 0o644); err != nil {
		return fmt.Errorf("settings: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		os.Remove(temporary)
		return fmt.Errorf("settings: %w", err)
	}
	return nil
}
