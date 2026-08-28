// Package cli parses a command line and decides how pi-go runs.
//
// Kept out of cmd/ so it can be tested without building a binary and running
// it: mode resolution in particular has a rule that is easy to get wrong and
// invisible when it is.
package cli

import (
	"sort"
	"strings"
)

// Mode is what --mode accepts.
//
// Deliberately NOT the set of things the application can run as: the flag takes
// three values and the application has four modes. Treating them as one set is
// wrong twice over — see ResolveAppMode.
type Mode string

const (
	ModeText Mode = "text"
	ModeJSON Mode = "json"
	ModeRPC  Mode = "rpc"
)

// Diagnostic is something worth telling the user about their command line.
type Diagnostic struct {
	Warning bool
	Message string
}

// Args is a parsed command line.
type Args struct {
	Provider     string
	Model        string
	APIKey       string
	SystemPrompt string

	// Mode is empty when --mode was absent OR carried a value the flag does
	// not accept. Pi ignores an unrecognised value rather than failing, and the
	// resolution below then treats it as absent — so `--mode interactive`,
	// which reads as though it should work, quietly means "decide for me".
	Mode Mode

	Print   bool
	Help    bool
	Version bool
	NoTools bool

	// Continue resumes the most recent conversation in this directory.
	Continue bool

	// Resume names one to reopen: a session id, or a path to its file.
	Resume string

	// NoSession keeps the conversation in memory only. It is what a scripted
	// caller wants — a run that leaves nothing behind — and what a user wants
	// when the question is throwaway.
	NoSession bool

	// SessionDir overrides where sessions are kept.
	SessionDir string

	// Messages are the prompts to send, in order.
	Messages []string

	// FileArgs are the @-prefixed paths. Collected but not yet used: the
	// content they attach is a feature of its own.
	FileArgs []string

	// Unknown holds --flags this build does not recognise. Pi keeps these for
	// extensions to claim, and keeping them here means an extension host can
	// later be given the same thing rather than a re-parse.
	Unknown map[string]string

	Diagnostics []Diagnostic
}

// notPorted are flags Pi accepts that this build does not act on.
//
// Listed so they produce a warning rather than silence. Pi recognises them, so
// letting them fall through to Unknown would leave a user believing a flag took
// effect — the failure mode a parser must never have.
var notPorted = map[string]bool{
	"name": true, "session": true, "session-id": true, "fork": true,
	"append-system-prompt": true, "thinking": true, "models": true,
	"tools": true, "exclude-tools": true, "no-builtin-tools": true,
	"extensions": true, "no-extensions": true, "export": true,
	"no-skills": true, "skills": true, "prompt-templates": true,
	"no-prompt-templates": true, "themes": true, "use-theme": true,
	"no-themes": true, "no-context-files": true, "list-models": true,
	"offline": true, "tui-mode": true, "verbose": true,
	"approve": true, "no-approve": true,
}

// ParseArgs reads a command line the way Pi reads one.
func ParseArgs(argv []string) Args {
	parsed := Args{Unknown: map[string]string{}}

	takesValue := map[string]*string{
		"--provider":      &parsed.Provider,
		"--model":         &parsed.Model,
		"--api-key":       &parsed.APIKey,
		"--system-prompt": &parsed.SystemPrompt,
		"--session-dir":   &parsed.SessionDir,
	}

	for i := 0; i < len(argv); i++ {
		arg := argv[i]

		if target, known := takesValue[arg]; known {
			if i+1 < len(argv) {
				*target = argv[i+1]
				i++
			}
			continue
		}

		switch arg {
		case "--help", "-h":
			parsed.Help = true
			continue
		case "--version", "-v":
			parsed.Version = true
			continue
		case "--no-tools":
			parsed.NoTools = true
			continue
		case "--no-session":
			parsed.NoSession = true
			continue
		case "--continue", "-c":
			parsed.Continue = true
			continue
		case "--resume", "-r":
			// The name is optional: bare --resume means "the most recent",
			// which is the same thing --continue asks for. A following flag or
			// @path is not a session name.
			if i+1 < len(argv) {
				next := argv[i+1]
				if !strings.HasPrefix(next, "-") && !strings.HasPrefix(next, "@") {
					parsed.Resume = next
					i++
					continue
				}
			}
			parsed.Continue = true
			continue
		case "--print", "-p":
			parsed.Print = true
			// The prompt may follow the flag directly. An @path is a file
			// argument rather than a message, and a following flag is a flag —
			// except `---`, which cannot be one and so is text.
			if i+1 < len(argv) {
				next := argv[i+1]
				if !strings.HasPrefix(next, "@") &&
					(!strings.HasPrefix(next, "-") || strings.HasPrefix(next, "---")) {
					parsed.Messages = append(parsed.Messages, next)
					i++
				}
			}
			continue
		case "--mode":
			if i+1 < len(argv) {
				switch Mode(argv[i+1]) {
				case ModeText, ModeJSON, ModeRPC:
					parsed.Mode = Mode(argv[i+1])
				default:
					parsed.Diagnostics = append(parsed.Diagnostics, Diagnostic{
						Warning: true,
						Message: "--mode " + argv[i+1] + " is not one of text, json or rpc; deciding from the terminal instead",
					})
				}
				i++
			}
			continue
		}

		switch {
		case strings.HasPrefix(arg, "@"):
			parsed.FileArgs = append(parsed.FileArgs, arg[1:])
		case strings.HasPrefix(arg, "--"):
			name, value := arg[2:], ""
			if at := strings.Index(name, "="); at >= 0 {
				name, value = name[:at], name[at+1:]
			} else if i+1 < len(argv) &&
				!strings.HasPrefix(argv[i+1], "-") && !strings.HasPrefix(argv[i+1], "@") {
				value = argv[i+1]
				i++
			}
			if notPorted[name] {
				parsed.Diagnostics = append(parsed.Diagnostics, Diagnostic{
					Warning: true,
					Message: "--" + name + " is a Pi flag this build does not implement yet; it is being ignored",
				})
				continue
			}
			parsed.Unknown[name] = value
		case strings.HasPrefix(arg, "-"):
			parsed.Diagnostics = append(parsed.Diagnostics, Diagnostic{
				Message: "Unknown option: " + arg,
			})
		default:
			parsed.Messages = append(parsed.Messages, arg)
		}
	}
	return parsed
}

// UnknownNames lists the unrecognised flags, ordered so a report reads the same
// way twice.
func (a Args) UnknownNames() []string {
	out := make([]string, 0, len(a.Unknown))
	for name := range a.Unknown {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Failed reports whether any diagnostic is an error rather than a warning.
func (a Args) Failed() bool {
	for _, d := range a.Diagnostics {
		if !d.Warning {
			return true
		}
	}
	return false
}
