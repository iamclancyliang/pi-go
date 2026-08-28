package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/session"
)

// Command is something typed into a session that acts on the session rather
// than being sent to the model.
type Command struct {
	Name string

	// Summary is what /help shows, and what tells a user which to reach for.
	Summary string

	// run acts on the session. Returning stop true ends the loop.
	run func(c *commandContext, arg string) (stop bool)
}

// notImplemented are Pi's slash commands this build does not have.
//
// Named individually rather than left to fall through as unknown. A user typing
// one of Pi's commands has a reasonable expectation, and "unknown command"
// tells them they mistyped it when they did not — the same failure the flag
// parser refuses to have.
var notImplemented = map[string]string{
	"settings":      "there is no settings store yet",
	"scoped-models": "there is no model cycling yet",
	"import":        "use --resume with a path instead",
	"share":         "there is no gist integration",
	"copy":          "there is no clipboard integration",
	"name":          "a session has no durable display name yet",
	"changelog":     "there is no changelog yet",
	"hotkeys":       "there are no keybindings yet",
	"fork":          "a session is a line here, not yet a tree",
	"clone":         "a session is a line here, not yet a tree",
	"tree":          "a session is a line here, not yet a tree",
	"trust":         "there is no project trust yet",
	"login":         "credentials come from the environment",
	"logout":        "credentials come from the environment",
	"compact":       "no summariser is configured yet",
	"reload":        "there is nothing reloadable yet",
	"model":         "switching model mid-session is not wired up yet",
}

// commandContext is what a command may act on.
type commandContext struct {
	session      *session.Session
	conversation *Conversation
	out          io.Writer
	errOut       io.Writer

	// reopen swaps the conversation the loop is working in. Held as a function
	// rather than done in place because opening one can fail, and a session
	// left half-swapped is worse than one that refused to swap.
	reopen func(args Args) error

	// args is the command line this session started with, so a command that
	// opens another conversation inherits the same session directory.
	args Args
}

var commands = map[string]Command{}

func init() {
	for _, c := range []Command{
		{Name: "help", Summary: "list these commands", run: runHelp},
		{Name: "quit", Summary: "end the session", run: func(*commandContext, string) bool { return true }},
		{Name: "exit", Summary: "end the session", run: func(*commandContext, string) bool { return true }},
		{Name: "session", Summary: "show what this session is and what it has cost", run: runSessionInfo},
		{Name: "new", Summary: "start a fresh conversation", run: runNew},
		{Name: "resume", Summary: "reopen another conversation: /resume [id]", run: runResume},
		{Name: "export", Summary: "write this conversation to a file: /export <path>", run: runExport},
	} {
		commands[c.Name] = c
	}
}

// dispatch runs a slash command. It reports whether the line was one.
func dispatch(c *commandContext, line string) (handled, stop bool) {
	if !strings.HasPrefix(line, "/") {
		return false, false
	}
	name, arg, _ := strings.Cut(strings.TrimPrefix(line, "/"), " ")
	name = strings.TrimSpace(name)
	arg = strings.TrimSpace(arg)

	if cmd, known := commands[name]; known {
		return true, cmd.run(c, arg)
	}
	if why, known := notImplemented[name]; known {
		fmt.Fprintf(c.errOut, "/%s is a Pi command this build does not have: %s\n", name, why)
		return true, false
	}
	fmt.Fprintf(c.errOut, "unknown command /%s; /help lists what there is\n", name)
	return true, false
}

func runHelp(c *commandContext, _ string) bool {
	names := make([]string, 0, len(commands))
	for name := range commands {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(c.out, "  /%-9s %s\n", name, commands[name].Summary)
	}
	// The absent ones are listed too, so a user can see the shape of what is
	// coming rather than discovering each gap by typing into it.
	missing := make([]string, 0, len(notImplemented))
	for name := range notImplemented {
		missing = append(missing, "/"+name)
	}
	sort.Strings(missing)
	fmt.Fprintf(c.out, "\nnot here yet: %s\n", strings.Join(missing, " "))
	return false
}

func runSessionInfo(c *commandContext, _ string) bool {
	snapshot := c.session.Snapshot()
	used := c.session.Usage()

	fmt.Fprintf(c.out, "  messages   %d\n", len(snapshot.Messages))
	if c.conversation.Path == "" {
		fmt.Fprintln(c.out, "  recorded   no (--no-session)")
	} else {
		fmt.Fprintf(c.out, "  id         %s\n", c.conversation.ID)
		fmt.Fprintf(c.out, "  file       %s\n", c.conversation.Path)
	}
	if !used.Reported {
		// Said rather than shown as zero: a provider that reported nothing has
		// not reported that the session was free.
		fmt.Fprintln(c.out, "  usage      not reported")
		return false
	}
	fmt.Fprintf(c.out, "  input      %d\n", used.InputTokens)
	fmt.Fprintf(c.out, "  output     %d\n", used.OutputTokens)
	if used.CacheReadTokens != nil {
		fmt.Fprintf(c.out, "  cache read %d\n", *used.CacheReadTokens)
	}
	return false
}

func runNew(c *commandContext, _ string) bool {
	fresh := c.args
	fresh.Continue, fresh.Resume = false, ""
	if err := c.reopen(fresh); err != nil {
		fmt.Fprintf(c.errOut, "could not start a new conversation: %v\n", err)
		return false
	}
	fmt.Fprintf(c.out, "started %s\n", c.conversation.ID)
	return false
}

func runResume(c *commandContext, arg string) bool {
	next := c.args
	next.Continue, next.Resume = arg == "", arg
	if err := c.reopen(next); err != nil {
		fmt.Fprintf(c.errOut, "could not resume: %v\n", err)
		return false
	}
	fmt.Fprintf(c.out, "resumed %s · %d messages\n",
		c.conversation.ID, len(c.session.Snapshot().Messages))
	return false
}

// runExport writes the conversation somewhere the user chose.
//
// Text, not the session file: an export is for reading, and copying the record
// would hand someone a format built for this program to reopen. Pi's default is
// HTML, which needs a renderer this build does not have — named as the
// difference rather than passed off as the same thing.
func runExport(c *commandContext, arg string) bool {
	if arg == "" {
		fmt.Fprintln(c.errOut, "/export needs a path")
		return false
	}
	path, err := filepath.Abs(arg)
	if err != nil {
		fmt.Fprintf(c.errOut, "could not export: %v\n", err)
		return false
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# pi-go session %s\n# exported %s\n\n",
		c.conversation.ID, time.Now().Format(time.RFC3339))
	for _, m := range c.session.Snapshot().Messages {
		if strings.TrimSpace(m.Content) == "" {
			continue
		}
		who := string(m.Role)
		if m.Role == ai.RoleAssistant {
			who = "assistant"
		}
		fmt.Fprintf(&b, "## %s\n\n%s\n\n", who, m.Content)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		fmt.Fprintf(c.errOut, "could not export: %v\n", err)
		return false
	}
	fmt.Fprintf(c.out, "exported to %s\n", path)
	return false
}
