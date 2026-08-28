package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/iamclancyliang/pi-go/internal/settings"
	"github.com/iamclancyliang/pi-go/internal/trust"
)

// settingKeys maps what a user types to the field it moves.
//
// A table rather than reflection, so each key's parsing and its description
// live in one row a reader can check — and so the listing, the setter and the
// validation cannot drift apart.
var settingKeys = []settingKey{
	{
		name: "defaultProvider", hint: "provider when --provider is absent",
		get: func(s *settings.Settings) string { return s.DefaultProvider },
		set: func(s *settings.Settings, v string) error { s.DefaultProvider = v; return nil },
	},
	{
		name: "defaultModel", hint: "model when --model is absent",
		get: func(s *settings.Settings) string { return s.DefaultModel },
		set: func(s *settings.Settings, v string) error { s.DefaultModel = v; return nil },
	},
	{
		name: "sessionDir", hint: "where conversations and credentials live",
		get: func(s *settings.Settings) string { return s.SessionDir },
		set: func(s *settings.Settings, v string) error { s.SessionDir = v; return nil },
	},
	{
		name: "shellPath", hint: "interpreter the bash tool runs",
		get: func(s *settings.Settings) string { return s.ShellPath },
		set: func(s *settings.Settings, v string) error { s.ShellPath = v; return nil },
	},
	{
		name: "shellCommandPrefix", hint: "line run before every bash command",
		get: func(s *settings.Settings) string { return s.ShellCommandPrefix },
		set: func(s *settings.Settings, v string) error { s.ShellCommandPrefix = v; return nil },
	},
	{
		name: "defaultTools", hint: "built-in tools to offer, comma-separated; empty means all",
		get: func(s *settings.Settings) string { return strings.Join(s.DefaultTools, ",") },
		set: func(s *settings.Settings, v string) error {
			if v == "" {
				s.DefaultTools = nil
				return nil
			}
			var names []string
			for _, name := range strings.Split(v, ",") {
				if trimmed := strings.TrimSpace(name); trimmed != "" {
					names = append(names, trimmed)
				}
			}
			s.DefaultTools = names
			return nil
		},
	},
	{
		name: "quietStartup", hint: "suppress the session banner",
		get: func(s *settings.Settings) string { return strconv.FormatBool(s.QuietStartup) },
		set: func(s *settings.Settings, v string) error {
			parsed, err := strconv.ParseBool(v)
			if err != nil {
				return fmt.Errorf("quietStartup takes true or false")
			}
			s.QuietStartup = parsed
			return nil
		},
	},
	{
		name: "defaultProjectTrust", hint: "ask, always or never, when a project carries configuration",
		get: func(s *settings.Settings) string { return string(s.DefaultProjectTrust) },
		set: func(s *settings.Settings, v string) error {
			switch settings.DefaultProjectTrust(v) {
			case settings.TrustAsk, settings.TrustAlways, settings.TrustNever, "":
				s.DefaultProjectTrust = settings.DefaultProjectTrust(v)
				return nil
			}
			return fmt.Errorf("defaultProjectTrust takes ask, always or never")
		},
	},
	{
		name: "compaction.keepRecentTokens", hint: "how much of the end /compact keeps verbatim",
		get: func(s *settings.Settings) string {
			if s.Compaction.KeepRecentTokens == 0 {
				return ""
			}
			return strconv.Itoa(s.Compaction.KeepRecentTokens)
		},
		set: func(s *settings.Settings, v string) error {
			if v == "" {
				s.Compaction.KeepRecentTokens = 0
				return nil
			}
			parsed, err := strconv.Atoi(v)
			if err != nil || parsed < 0 {
				return fmt.Errorf("compaction.keepRecentTokens takes a non-negative number")
			}
			s.Compaction.KeepRecentTokens = parsed
			return nil
		},
	},
}

type settingKey struct {
	name string
	hint string
	get  func(*settings.Settings) string
	set  func(*settings.Settings, string) error
}

// runSettings shows the effective settings, or changes one.
//
// Pi opens a menu; this is the same capability as lines. Writes go to the
// GLOBAL scope: writing the merged view back would copy the project's values
// into the user's global file, and the project file is the project's to edit.
func runSettings(c *commandContext, arg string) bool {
	if arg == "" {
		for _, key := range settingKeys {
			value := key.get(&c.config.Effective)
			if value == "" {
				value = "(unset)"
			}
			source := ""
			if key.get(&c.config.Global) != key.get(&c.config.Effective) {
				source = "  [from the project]"
			}
			fmt.Fprintf(c.out, "  %-28s %s%s\n", key.name, value, source)
		}
		if c.config.ProjectLoaded {
			fmt.Fprintf(c.out, "\n  project settings: %s\n", c.config.ProjectPath)
		}
		fmt.Fprintln(c.out, "\n  /settings <key> <value> sets one; /settings <key> \"\" clears it")
		return false
	}

	name, value, _ := strings.Cut(arg, " ")
	value = strings.TrimSpace(value)
	value = strings.Trim(value, `"`)
	for i := range settingKeys {
		key := &settingKeys[i]
		if key.name != name {
			continue
		}
		if err := key.set(&c.config.Global, value); err != nil {
			fmt.Fprintf(c.errOut, "%v\n", err)
			return false
		}
		if err := settings.Save(settings.GlobalPath(c.config.AgentDir), c.config.Global); err != nil {
			fmt.Fprintf(c.errOut, "could not save: %v\n", err)
			return false
		}
		// The effective view moves with it, so /settings shows what was just
		// done — but what a running session already built from the old value
		// keeps it, and the listing is not the place to pretend otherwise.
		if err := key.set(&c.config.Effective, value); err == nil && c.config.ProjectLoaded {
			// Re-merge so a project override is not clobbered by the global edit.
			project, loadErr := settings.Load(c.config.ProjectPath)
			if loadErr == nil {
				c.config.Effective = settings.Merge(c.config.Global, project)
			}
		}
		fmt.Fprintf(c.out, "set %s · applies where it is read; /reload re-reads now\n", key.name)
		return false
	}
	fmt.Fprintf(c.errOut, "no setting called %q; /settings lists them\n", name)
	return false
}

// runTrust reports how this project stands, or records a decision.
func runTrust(c *commandContext, arg string) bool {
	store := trust.Open(c.config.AgentDir)
	switch arg {
	case "":
		if !c.config.ProjectPresent {
			fmt.Fprintf(c.out, "  this project carries no configuration; there is nothing to trust\n")
			return false
		}
		fmt.Fprintf(c.out, "  project configuration: %s\n", c.config.ProjectPath)
		switch c.config.Trust {
		case trust.Trusted:
			fmt.Fprintf(c.out, "  trusted, decided at %s\n", c.config.TrustedFrom)
		case trust.Refused:
			fmt.Fprintf(c.out, "  refused, decided at %s\n", c.config.TrustedFrom)
		default:
			fmt.Fprintln(c.out, "  undecided; it is not being loaded")
		}
		if c.config.ProjectPresent && !c.config.ProjectLoaded {
			fmt.Fprintln(c.out, "  its settings are NOT in effect")
		}
		fmt.Fprintln(c.out, "  /trust yes · /trust no · /trust forget")
		return false
	case "yes", "no":
		decision := trust.Trusted
		if arg == "no" {
			decision = trust.Refused
		}
		if err := store.Set(c.workingDir, decision); err != nil {
			fmt.Fprintf(c.errOut, "could not record it: %v\n", err)
			return false
		}
		fmt.Fprintf(c.out, "%s · /reload applies it to this session\n", decision)
		return false
	case "forget":
		if err := store.Set(c.workingDir, trust.Undecided); err != nil {
			fmt.Fprintf(c.errOut, "could not forget it: %v\n", err)
			return false
		}
		fmt.Fprintln(c.out, "forgotten; the question will be asked again")
		return false
	default:
		fmt.Fprintln(c.errOut, "usage: /trust [yes|no|forget]")
		return false
	}
}

// runReload re-reads the settings files and applies what can be applied to a
// running session.
//
// Pi reloads six kinds of resource; settings are the one this build has. What
// cannot move mid-session — the provider port already opened, the tools already
// offered to the model — is named rather than silently left, because "reloaded"
// covering half the truth is how a user comes to believe a change is live.
func runReload(c *commandContext, _ string) bool {
	if c.reloadConfig == nil {
		fmt.Fprintln(c.errOut, "nothing reloadable in this mode")
		return false
	}
	changed, err := c.reloadConfig()
	if err != nil {
		fmt.Fprintf(c.errOut, "could not reload: %v\n", err)
		return false
	}
	fmt.Fprintln(c.out, "settings re-read")
	if changed {
		fmt.Fprintln(c.out, "note: the model, provider and tool set were built at startup; a new run picks those up")
	}
	return false
}
