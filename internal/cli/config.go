package cli

import (
	"fmt"
	"os"

	"github.com/iamclancyliang/pi-go/internal/session"
	"github.com/iamclancyliang/pi-go/internal/settings"
	"github.com/iamclancyliang/pi-go/internal/tools"
	"github.com/iamclancyliang/pi-go/internal/trust"
)

// Config is what a run resolved before starting: the effective settings, and
// how each question about the project was answered.
type Config struct {
	// Effective is the merged view a run acts on.
	Effective settings.Settings

	// Global is the global scope alone, which is what /settings edits: writing
	// the merged view back would copy the project's values into the user's
	// global file.
	Global settings.Settings

	// ProjectPath is where this project's settings live, present or not.
	ProjectPath string

	// ProjectLoaded says the project scope was read and merged.
	ProjectLoaded bool

	// ProjectPresent says the file exists, loaded or not. The difference is
	// what /trust reports: a project with no configuration has nothing to
	// trust, and one whose configuration was skipped should say so.
	ProjectPresent bool

	// Trust is how the project stands, and TrustedFrom names the recorded
	// directory that decided it when one did.
	Trust       trust.Decision
	TrustedFrom string

	// AgentDir is where global state lives, after --session-dir is honoured.
	AgentDir string
}

// ResolveConfig loads settings the way a run must: global always, project only
// when the project is trusted.
//
// The gate exists because a project directory is somebody else's input. A
// cloned repository carrying .pi-go/settings.json would otherwise configure
// the tool that is about to run shell commands on its contents — the shell
// path and the command prefix are settings, and both execute.
func ResolveConfig(args Args, workingDir string, ask func(prompt string) bool) (Config, error) {
	agentDir := args.SessionDir
	if agentDir == "" {
		resolved, err := session.AgentDir()
		if err != nil {
			return Config{}, err
		}
		agentDir = resolved
	}

	global, err := settings.Load(settings.GlobalPath(agentDir))
	if err != nil {
		// A broken global file is reported, not skipped: every preference in
		// it would silently revert to defaults, and the user would meet that
		// as the wrong model answering.
		return Config{}, err
	}

	cfg := Config{
		Effective:   global,
		Global:      global,
		ProjectPath: settings.ProjectPath(workingDir),
		AgentDir:    agentDir,
	}
	if args.SessionDir == "" && global.SessionDir != "" {
		// The setting stands in for the flag, and like the flag it moves the
		// whole agent directory. Resolved before trust so the trust store
		// itself is read from where the user said things live.
		cfg.AgentDir = global.SessionDir
	}

	if _, err := os.Stat(cfg.ProjectPath); err != nil {
		if os.IsNotExist(err) {
			// Nothing to trust, nothing to ask about.
			return cfg, nil
		}
		return Config{}, fmt.Errorf("settings: %w", err)
	}
	cfg.ProjectPresent = true

	store := trust.Open(cfg.AgentDir)
	decision, from, err := store.Get(workingDir)
	if err != nil {
		return Config{}, err
	}
	cfg.Trust, cfg.TrustedFrom = decision, from

	if decision == trust.Undecided {
		switch global.DefaultProjectTrust {
		case settings.TrustAlways:
			decision = trust.Trusted
		case settings.TrustNever:
			decision = trust.Refused
		default:
			if ask == nil {
				// No way to ask means no consent, and no consent means the
				// project does not configure the tool. Print mode lands here.
				decision = trust.Refused
			} else if ask(fmt.Sprintf(
				"Trust %s?\nIt carries %s, which can set the shell every command runs in.",
				workingDir, cfg.ProjectPath)) {
				decision = trust.Trusted
				if err := store.Set(workingDir, trust.Trusted); err != nil {
					return Config{}, err
				}
				cfg.TrustedFrom = workingDir
			} else {
				decision = trust.Refused
				if err := store.Set(workingDir, trust.Refused); err != nil {
					return Config{}, err
				}
				cfg.TrustedFrom = workingDir
			}
		}
		cfg.Trust = decision
	}

	if decision != trust.Trusted {
		return cfg, nil
	}

	project, err := settings.Load(cfg.ProjectPath)
	if err != nil {
		// The project's own file being broken is the project's problem to hear
		// about, not a reason to silently run without it.
		return Config{}, err
	}
	cfg.Effective = settings.Merge(global, project)
	cfg.ProjectLoaded = true
	return cfg, nil
}

// ApplyDefaults fills in what the command line left unsaid from the effective
// settings.
//
// The flag always wins: a setting is the standing preference, and the command
// line is what the user said THIS time.
func ApplyDefaults(args Args, effective settings.Settings) Args {
	if args.Provider == "" {
		args.Provider = effective.DefaultProvider
	}
	if args.Model == "" {
		args.Model = effective.DefaultModel
	}
	if args.SessionDir == "" {
		args.SessionDir = effective.SessionDir
	}
	return args
}

// BuildTools assembles the registry a run offers, honouring the settings that
// shape it.
//
// defaultTools narrows the set by name, and an unknown name is an error rather
// than a skip: a filter that silently drops a misspelt entry offers a tool set
// the user did not choose.
func BuildTools(root string, effective settings.Settings) (*tools.Registry, error) {
	registry := tools.NewRegistry()
	wanted := map[string]bool{}
	for _, name := range effective.DefaultTools {
		wanted[name] = true
	}

	known := map[string]bool{}
	for _, tool := range tools.BuiltIn(root) {
		known[tool.Name()] = true
		if len(wanted) > 0 && !wanted[tool.Name()] {
			continue
		}
		if shaped, ok := tool.(*tools.Bash); ok {
			shaped.Shell = effective.ShellPath
			shaped.CommandPrefix = effective.ShellCommandPrefix
		}
		if err := registry.Register(tool); err != nil {
			return nil, err
		}
	}
	for name := range wanted {
		if !known[name] {
			return nil, fmt.Errorf(
				"settings: defaultTools names %q, which is not a built-in tool", name)
		}
	}
	return registry, nil
}
