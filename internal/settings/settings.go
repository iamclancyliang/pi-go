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

	"github.com/iamclancyliang/pi-go/internal/ai"
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

	// Enabled switches compacting before the context overflows, once a run
	// leaves it within ReserveTokens of the model's window. Unset means on,
	// as in Pi. Recovering from an overflow is not governed by it.
	Enabled       *bool `json:"enabled,omitempty"`
	ReserveTokens int   `json:"reserveTokens,omitempty"`
}

// Retry is how a model call that failed transiently is tried again — Pi's
// retry group. Each field is a pointer so "unset" stays distinct from a value
// that happens to be the zero one: retrying switched off is not the same as
// nobody having said.
type Retry struct {
	Enabled     *bool `json:"enabled,omitempty"`
	MaxRetries  *int  `json:"maxRetries,omitempty"`
	BaseDelayMs *int  `json:"baseDelayMs,omitempty"`
}

// Resolved is the policy these settings describe, with Pi's defaults for
// whatever is unset: on, three retries, two seconds doubling.
func (r Retry) Resolved() (enabled bool, maxRetries int, baseDelayMs int) {
	enabled, maxRetries, baseDelayMs = true, 3, 2000
	if r.Enabled != nil {
		enabled = *r.Enabled
	}
	if r.MaxRetries != nil {
		maxRetries = *r.MaxRetries
	}
	if r.BaseDelayMs != nil {
		baseDelayMs = *r.BaseDelayMs
	}
	return enabled, maxRetries, baseDelayMs
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

	// DefaultThinkingLevel stands in for --thinking when the flag is absent.
	DefaultThinkingLevel string `json:"defaultThinkingLevel,omitempty"`

	// DefaultProjectTrust decides what happens when a project carries
	// configuration and nobody has said whether to trust it. Global only: a
	// project choosing its own trust default is the project trusting itself.
	DefaultProjectTrust DefaultProjectTrust `json:"defaultProjectTrust,omitempty"`

	Compaction Compaction `json:"compaction,omitempty"`

	Retry Retry `json:"retry,omitzero"`

	// SteeringMode and FollowUpMode are how many queued messages each drain
	// point takes: "one-at-a-time" (Pi's default) or "all".
	SteeringMode string `json:"steeringMode,omitempty"`
	FollowUpMode string `json:"followUpMode,omitempty"`

	// EnabledModels are the patterns model cycling goes through, standing in for
	// --models when the flag is absent.
	EnabledModels []string `json:"enabledModels,omitempty"`

	// HideThinkingBlock folds reasoning in the interface.
	HideThinkingBlock bool `json:"hideThinkingBlock,omitempty"`

	// DoubleEscapeAction is what escape twice on an empty editor opens: "tree"
	// (the default), "fork" or "none".
	DoubleEscapeAction string `json:"doubleEscapeAction,omitempty"`

	// TreeFilterMode is the filter /tree opens with.
	TreeFilterMode string `json:"treeFilterMode,omitempty"`

	// ShowHardwareCursor shows the terminal's cursor in the interface. Unset leaves
	// it to PI_HARDWARE_CURSOR.
	ShowHardwareCursor *bool `json:"showHardwareCursor,omitempty"`

	// EditorPaddingX is the blank columns beside the editor's text.
	EditorPaddingX *int `json:"editorPaddingX,omitempty"`

	// OutputPad is the blank columns beside messages: 0, or 1 when unset.
	OutputPad *int `json:"outputPad,omitempty"`

	// AutocompleteMaxVisible is how many completions show at once, 5 when unset.
	AutocompleteMaxVisible int `json:"autocompleteMaxVisible,omitempty"`

	Terminal Terminal `json:"terminal,omitzero"`

	BranchSummary BranchSummary `json:"branchSummary,omitzero"`
}

// Terminal is how the interface treats the terminal.
type Terminal struct {
	// ClearOnShrink redraws to clear rows a shrinking frame left behind. Unset
	// leaves it to PI_CLEAR_ON_SHRINK.
	ClearOnShrink *bool `json:"clearOnShrink,omitempty"`
}

// BranchSummary is how a branch left behind by /tree is summarised.
type BranchSummary struct {
	// ReserveTokens is held back from the model's window for the prompt and reply.
	ReserveTokens int `json:"reserveTokens,omitempty"`
	// SkipPrompt never asks, and leaves the branch unsummarised.
	SkipPrompt bool `json:"skipPrompt,omitempty"`
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
	if s.Compaction.ReserveTokens < 0 {
		return fmt.Errorf("settings: %s: compaction.reserveTokens is negative", path)
	}
	for key, mode := range map[string]string{"steeringMode": s.SteeringMode, "followUpMode": s.FollowUpMode} {
		if mode != "" && mode != "all" && mode != "one-at-a-time" {
			return fmt.Errorf("settings: %s: %s is %q; it takes all or one-at-a-time", path, key, mode)
		}
	}
	switch s.DoubleEscapeAction {
	case "", "tree", "fork", "none":
	default:
		return fmt.Errorf("settings: %s: doubleEscapeAction is %q; it takes tree, fork or none", path, s.DoubleEscapeAction)
	}
	switch s.TreeFilterMode {
	case "", "default", "no-tools", "user-only", "labeled-only", "all":
	default:
		return fmt.Errorf("settings: %s: treeFilterMode is %q; it takes default, no-tools, user-only, labeled-only or all", path, s.TreeFilterMode)
	}
	if s.EditorPaddingX != nil && (*s.EditorPaddingX < 0 || *s.EditorPaddingX > 3) {
		return fmt.Errorf("settings: %s: editorPaddingX takes 0 to 3", path)
	}
	if s.OutputPad != nil && *s.OutputPad != 0 && *s.OutputPad != 1 {
		return fmt.Errorf("settings: %s: outputPad takes 0 or 1", path)
	}
	if s.AutocompleteMaxVisible != 0 && (s.AutocompleteMaxVisible < 3 || s.AutocompleteMaxVisible > 20) {
		return fmt.Errorf("settings: %s: autocompleteMaxVisible takes 3 to 20", path)
	}
	if s.BranchSummary.ReserveTokens < 0 {
		return fmt.Errorf("settings: %s: branchSummary.reserveTokens is negative", path)
	}
	if s.Retry.MaxRetries != nil && *s.Retry.MaxRetries < 0 {
		return fmt.Errorf("settings: %s: retry.maxRetries is negative", path)
	}
	if s.Retry.BaseDelayMs != nil && *s.Retry.BaseDelayMs < 0 {
		return fmt.Errorf("settings: %s: retry.baseDelayMs is negative", path)
	}
	if s.DefaultThinkingLevel != "" {
		if _, err := ai.ParseThinkingLevel(s.DefaultThinkingLevel); err != nil {
			return fmt.Errorf("settings: %s: %w", path, err)
		}
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
	if project.DefaultThinkingLevel != "" {
		merged.DefaultThinkingLevel = project.DefaultThinkingLevel
	}
	// DefaultProjectTrust is deliberately NOT merged from the project: a
	// project choosing its own trust default is the project trusting itself.
	if project.Compaction.KeepRecentTokens != 0 {
		merged.Compaction.KeepRecentTokens = project.Compaction.KeepRecentTokens
	}
	if project.Compaction.Enabled != nil {
		merged.Compaction.Enabled = project.Compaction.Enabled
	}
	if project.Compaction.ReserveTokens != 0 {
		merged.Compaction.ReserveTokens = project.Compaction.ReserveTokens
	}
	if project.SteeringMode != "" {
		merged.SteeringMode = project.SteeringMode
	}
	if project.FollowUpMode != "" {
		merged.FollowUpMode = project.FollowUpMode
	}
	if project.EnabledModels != nil {
		merged.EnabledModels = append([]string(nil), project.EnabledModels...)
	}
	if project.HideThinkingBlock {
		merged.HideThinkingBlock = true
	}
	if project.DoubleEscapeAction != "" {
		merged.DoubleEscapeAction = project.DoubleEscapeAction
	}
	if project.TreeFilterMode != "" {
		merged.TreeFilterMode = project.TreeFilterMode
	}
	if project.ShowHardwareCursor != nil {
		merged.ShowHardwareCursor = project.ShowHardwareCursor
	}
	if project.EditorPaddingX != nil {
		merged.EditorPaddingX = project.EditorPaddingX
	}
	if project.OutputPad != nil {
		merged.OutputPad = project.OutputPad
	}
	if project.AutocompleteMaxVisible != 0 {
		merged.AutocompleteMaxVisible = project.AutocompleteMaxVisible
	}
	if project.Terminal.ClearOnShrink != nil {
		merged.Terminal.ClearOnShrink = project.Terminal.ClearOnShrink
	}
	if project.BranchSummary.ReserveTokens != 0 {
		merged.BranchSummary.ReserveTokens = project.BranchSummary.ReserveTokens
	}
	if project.BranchSummary.SkipPrompt {
		merged.BranchSummary.SkipPrompt = true
	}
	if project.Retry.Enabled != nil {
		merged.Retry.Enabled = project.Retry.Enabled
	}
	if project.Retry.MaxRetries != nil {
		merged.Retry.MaxRetries = project.Retry.MaxRetries
	}
	if project.Retry.BaseDelayMs != nil {
		merged.Retry.BaseDelayMs = project.Retry.BaseDelayMs
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
