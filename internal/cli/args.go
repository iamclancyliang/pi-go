// Package cli parses a command line and decides how pi-go runs.
//
// Kept out of cmd/ so it can be tested without building a binary and running
// it: mode resolution in particular has a rule that is easy to get wrong and
// invisible when it is.
package cli

import (
	"sort"
	"strings"

	"github.com/iamclancyliang/pi-go/internal/ai"
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

	// NoBuiltinTools turns off the built-in set. It differs from NoTools only
	// once tools can come from somewhere else; today both leave nothing.
	NoBuiltinTools bool

	// Tools is an allowlist of tool names, and wins over the two switches above
	// as Pi's does. Nil means none was given; empty means one was, naming nothing.
	Tools []string

	// ExcludeTools removes names from whatever set was otherwise chosen.
	ExcludeTools []string

	// Name is what to call the conversation being opened. NameGiven tells an
	// empty value, which is refused, from the flag being absent.
	Name      string
	NameGiven bool

	// Session names one conversation to work in: a path, or an id or its prefix.
	Session string

	// SessionID is an exact id to open, or to create when no conversation here
	// has it. Scripts use it to come back to a conversation they named.
	SessionID string

	// Fork names a conversation to copy into a new one in this directory.
	Fork string

	// Export names a session file to write out, instead of running anything.
	Export string

	// ListModels asks for the model table instead of a run, narrowed by
	// ListModelsPattern when one followed the flag.
	ListModels        bool
	ListModelsPattern string

	// Offline keeps a run from reaching the network on its own account. Pi's
	// flag covers its startup checks, which this build does not make; what is
	// left for it to govern is asking a provider for its models.
	Offline bool

	// ProjectTrust overrides the recorded trust decision for this run only. Nil
	// leaves it to the record.
	ProjectTrust *bool

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

	// Models are the comma-separated patterns that limit model cycling. They
	// are resolved against the provider listing when a cycle is requested, so
	// parsing a command line never reaches the network.
	Models []string

	// Thinking is how much reasoning to ask for.
	Thinking ai.ThinkingLevel

	// AppendSystemPrompt is added after the assembled prompt, for a caller
	// adding to the default rather than replacing it. Repeatable, and the
	// pieces join in the order given.
	AppendSystemPrompt []string

	// NoContextFiles keeps the project's own instructions out of the prompt.
	NoContextFiles bool

	// Messages are the prompts to send, in order.
	Messages []string

	// FileArgs are the @-prefixed paths, whose contents go ahead of the first
	// message. See PrepareInitialMessage.
	FileArgs []string

	// Unknown holds --flags this build does not recognise. Pi keeps these for
	// extensions to claim, and keeping them here means an extension host can
	// later be given the same thing rather than a re-parse.
	Unknown map[string]string

	Diagnostics []Diagnostic
}

// notPorted are flags Pi accepts that this build does not act on, and whether
// each takes a value.
//
// Listed so they produce a warning rather than silence. Pi recognises them, so
// letting them fall through to Unknown would leave a user believing a flag took
// effect — the failure mode a parser must never have. The value matters as much:
// a switch that swallowed the next word would lose the user's prompt with it.
var notPorted = map[string]bool{
	"extension": true, "no-extensions": false,
	"skill": true, "no-skills": false,
	"prompt-template": true, "no-prompt-templates": false,
	"theme": true, "use-theme": true, "no-themes": false,
	"tui-mode": true, "verbose": false,
}

// shortNotPorted are the short spellings of the same flags. Without them an
// accepted Pi option would fail the whole command line as an unknown one.
var shortNotPorted = map[string]string{
	"-e": "extension", "-ne": "no-extensions", "-ns": "no-skills", "-np": "no-prompt-templates",
}

// nameList reads a comma-separated list of tool names. Never nil, because a list
// that was given and names nothing is a different request from no list at all.
func nameList(value string) []string {
	names := []string{}
	for _, name := range strings.Split(value, ",") {
		if name = strings.TrimSpace(name); name != "" {
			names = append(names, name)
		}
	}
	return names
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
		"--session":       &parsed.Session,
		"--session-id":    &parsed.SessionID,
		"--fork":          &parsed.Fork,
		"--export":        &parsed.Export,
	}
	warnNotPorted := func(name string) {
		parsed.Diagnostics = append(parsed.Diagnostics, Diagnostic{
			Warning: true,
			Message: "--" + name + " is a Pi flag this build does not implement yet; it is being ignored",
		})
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
		case "--no-tools", "-nt":
			parsed.NoTools = true
			continue
		case "--no-builtin-tools", "-nbt":
			parsed.NoBuiltinTools = true
			continue
		case "--tools", "-t":
			if i+1 < len(argv) {
				parsed.Tools = nameList(argv[i+1])
				i++
			}
			continue
		case "--exclude-tools", "-xt":
			if i+1 < len(argv) {
				parsed.ExcludeTools = nameList(argv[i+1])
				i++
			}
			continue
		case "--name", "-n":
			if i+1 < len(argv) {
				parsed.Name = argv[i+1]
				parsed.NameGiven = true
				i++
			} else {
				parsed.Diagnostics = append(parsed.Diagnostics,
					Diagnostic{Message: "--name requires a value"})
			}
			continue
		case "--list-models":
			parsed.ListModels = true
			if i+1 < len(argv) && !strings.HasPrefix(argv[i+1], "-") && !strings.HasPrefix(argv[i+1], "@") {
				parsed.ListModelsPattern = argv[i+1]
				i++
			}
			continue
		case "--offline":
			parsed.Offline = true
			continue
		case "--approve", "-a", "--no-approve", "-na":
			trusted := arg == "--approve" || arg == "-a"
			parsed.ProjectTrust = &trusted
			continue
		case "--no-session":
			parsed.NoSession = true
			continue
		case "--no-context-files", "-nc":
			parsed.NoContextFiles = true
			continue
		case "--models":
			if i+1 < len(argv) && !strings.HasPrefix(argv[i+1], "-") {
				for _, pattern := range strings.Split(argv[i+1], ",") {
					// Keep empty entries so a malformed list is diagnosed when
					// cycling instead of silently broadening to every model.
					parsed.Models = append(parsed.Models, strings.TrimSpace(pattern))
				}
				i++
			} else {
				parsed.Diagnostics = append(parsed.Diagnostics,
					Diagnostic{Message: "--models requires a comma-separated pattern list"})
			}
			continue
		case "--thinking":
			if i+1 < len(argv) {
				level, err := ai.ParseThinkingLevel(argv[i+1])
				if err != nil {
					// Refused rather than ignored: a caller who asked for more
					// reasoning and silently got the default would read the
					// answer as what the model produces when it thinks hard.
					parsed.Diagnostics = append(parsed.Diagnostics,
						Diagnostic{Message: err.Error()})
				} else {
					parsed.Thinking = level
				}
				i++
			}
			continue
		case "--append-system-prompt":
			if i+1 < len(argv) {
				parsed.AppendSystemPrompt = append(parsed.AppendSystemPrompt, argv[i+1])
				i++
			}
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

		if name, known := shortNotPorted[arg]; known {
			warnNotPorted(name)
			if notPorted[name] && i+1 < len(argv) {
				i++
			}
			continue
		}

		switch {
		case strings.HasPrefix(arg, "@"):
			parsed.FileArgs = append(parsed.FileArgs, arg[1:])
		case strings.HasPrefix(arg, "--"):
			name, value := arg[2:], ""
			at := strings.Index(name, "=")
			if at >= 0 {
				name, value = name[:at], name[at+1:]
			}
			takesValue, ported := notPorted[name]
			if ported && !takesValue {
				warnNotPorted(name)
				continue
			}
			if at < 0 && i+1 < len(argv) &&
				!strings.HasPrefix(argv[i+1], "-") && !strings.HasPrefix(argv[i+1], "@") {
				value = argv[i+1]
				i++
			}
			if ported {
				warnNotPorted(name)
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
