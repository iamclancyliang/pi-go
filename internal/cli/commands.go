package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/auth"
	"github.com/iamclancyliang/pi-go/internal/compaction"
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
	"changelog":     "there is no changelog yet",
	"hotkeys":       "there are no keybindings yet",
	"trust":         "there is no project trust yet",
	"reload":        "there is nothing reloadable yet",
	"scoped-models": "there is no model catalogue to scope",
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

	// workingDir is what the conversation belongs to, and where a fork lands.
	workingDir string

	// reload rebuilds the session from the store without changing which store
	// it is. Moving within a conversation changes what the store says the
	// conversation IS, and a session left holding the old path would send the
	// branch the user just left.
	reload func() error

	// switchModel points the conversation at a different model, and at a
	// different provider when one is named. Held as a function because opening
	// a provider can fail, and a run left pointing at a port that did not open
	// is worse than one that refused to switch.
	switchModel func(provider, model string) error

	// modelProvider and modelName report what is answering now.
	modelProvider func() string
	modelName     func() string

	// confirm asks the user a yes-or-no question, reading from the same input
	// the conversation comes from.
	confirm func() bool

	// compact shortens the conversation. Held as a function because it makes a
	// billed model call and needs the port this run opened.
	compact func(instructions string) error
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
		{Name: "tree", Summary: "show the shape of this conversation, or go back to a point: /tree [id]", run: runTree},
		{Name: "fork", Summary: "copy this conversation up to a point into a new one: /fork <id>", run: runFork},
		{Name: "clone", Summary: "copy this conversation as it stands into a new one", run: runClone},
		{Name: "name", Summary: "what to call this conversation: /name [text]", run: runName},
		{Name: "import", Summary: "open a conversation from a file: /import <path>", run: runImport},
		{Name: "copy", Summary: "copy the last answer to the clipboard", run: runCopy},
		{Name: "model", Summary: "switch model: /model [provider/]<model>", run: runModel},
		{Name: "compact", Summary: "summarise the older part of this conversation: /compact [focus]", run: runCompact},
		{Name: "login", Summary: "save a credential for a provider: /login <provider>", run: runLogin},
		{Name: "logout", Summary: "forget a saved credential: /logout [provider]", run: runLogout},
		{Name: "share", Summary: "upload this conversation as a secret GitHub gist", run: runShare},
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

	body := renderConversation(c.conversation.ID, c.session.Snapshot().Messages)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		fmt.Fprintf(c.errOut, "could not export: %v\n", err)
		return false
	}
	fmt.Fprintf(c.out, "exported to %s\n", path)
	return false
}

// runTree shows the shape of the conversation, or moves within it.
//
// With no argument it lists; with an id it stands there. Both from one command
// because they are one act: a person looks in order to choose, and separating
// them would mean naming an id from a listing they had to ask for separately.
func runTree(c *commandContext, arg string) bool {
	if c.conversation.Store == nil {
		fmt.Fprintln(c.errOut, "this conversation is not recorded, so it has no shape to show")
		return false
	}
	if arg != "" {
		return moveTo(c, arg)
	}

	nodes, err := c.conversation.Store.Tree(context.Background())
	if err != nil {
		fmt.Fprintf(c.errOut, "could not read the conversation: %v\n", err)
		return false
	}
	if len(nodes) == 0 {
		fmt.Fprintln(c.out, "  nothing recorded yet")
		return false
	}
	for _, n := range nodes {
		// The marker says where the conversation stands, and the tip marker
		// says where a branch can be picked up again. Without both, a listing
		// of a branched conversation cannot be acted on.
		here := " "
		if n.OnPath {
			here = "*"
		}
		tip := " "
		if n.IsLeaf {
			tip = "+"
		}
		fmt.Fprintf(c.out, " %s%s %s  %s\n", here, tip, shortID(n.ID), n.Summary)
	}
	fmt.Fprintln(c.out, "\n  * on the current path   + a branch tip   /tree <id> to go back to one")
	return false
}

func moveTo(c *commandContext, id string) bool {
	full, err := resolveEntry(c, id)
	if err != nil {
		fmt.Fprintf(c.errOut, "%v\n", err)
		return false
	}
	if err := c.conversation.Store.MoveTo(context.Background(), full); err != nil {
		fmt.Fprintf(c.errOut, "could not go back: %v\n", err)
		return false
	}
	// The session is rebuilt from the store, because the conversation the model
	// is shown must be the one the store now says it is — a session left
	// holding the old path would send the branch the user just left.
	if err := c.reload(); err != nil {
		fmt.Fprintf(c.errOut, "could not reopen at that point: %v\n", err)
		return false
	}
	fmt.Fprintf(c.out, "at %s · %d messages\n",
		shortID(full), len(c.session.Snapshot().Messages))
	return false
}

func runFork(c *commandContext, arg string) bool {
	if arg == "" {
		fmt.Fprintln(c.errOut, "/fork needs the id of the point to fork at; /tree lists them")
		return false
	}
	full, err := resolveEntry(c, arg)
	if err != nil {
		fmt.Fprintf(c.errOut, "%v\n", err)
		return false
	}
	return branch(c, full)
}

func runClone(c *commandContext, _ string) bool {
	if c.conversation.Store == nil {
		fmt.Fprintln(c.errOut, "this conversation is not recorded, so there is nothing to copy")
		return false
	}
	return branch(c, c.conversation.Store.Leaf())
}

// branch copies the conversation up to an entry into a new one and moves there.
func branch(c *commandContext, leaf string) bool {
	if c.conversation.Store == nil {
		fmt.Fprintln(c.errOut, "this conversation is not recorded, so there is nothing to copy")
		return false
	}
	now := time.Now()
	id := session.NewSessionID(now)
	path := filepath.Join(
		session.DirFor(c.conversation.Dir, c.workingDir), session.FileName(id, now))

	forked, err := c.conversation.Store.BranchInto(
		context.Background(), path, c.workingDir, id, leaf)
	if err != nil {
		fmt.Fprintf(c.errOut, "could not fork: %v\n", err)
		return false
	}
	forked.Close()

	// Reached through the ordinary resume path, so a fork lands in the same
	// state a resumed conversation does rather than in one assembled here.
	next := c.args
	next.Continue, next.Resume = false, path
	if err := c.reopen(next); err != nil {
		fmt.Fprintf(c.errOut, "forked to %s but could not open it: %v\n", path, err)
		return false
	}
	fmt.Fprintf(c.out, "forked to %s · %d messages\n",
		c.conversation.ID, len(c.session.Snapshot().Messages))
	return false
}

// resolveEntry turns what a person typed into an entry id.
//
// By prefix, because /tree shows shortened ids and typing a full one is not
// something to ask of anybody. An ambiguous prefix is refused: standing at the
// wrong point is not something to discover from the conversation afterwards.
func resolveEntry(c *commandContext, prefix string) (string, error) {
	if c.conversation.Store == nil {
		return "", fmt.Errorf("this conversation is not recorded, so it has no entries")
	}
	nodes, err := c.conversation.Store.Tree(context.Background())
	if err != nil {
		return "", err
	}
	var matched []string
	for _, n := range nodes {
		if strings.HasPrefix(n.ID, prefix) {
			matched = append(matched, n.ID)
		}
	}
	switch len(matched) {
	case 0:
		return "", fmt.Errorf("no entry here starts with %q; /tree lists them", prefix)
	case 1:
		return matched[0], nil
	default:
		short := make([]string, 0, len(matched))
		for _, id := range matched {
			short = append(short, shortID(id))
		}
		return "", fmt.Errorf("%q matches %s", prefix, strings.Join(short, ", "))
	}
}

// shortID is how an id appears in a listing: long enough to be unambiguous in
// one conversation, short enough to retype.
//
// It must reach PAST the time an id starts with. An id is a millisecond
// followed by randomness, so a prefix that stops inside the millisecond is the
// same for every entry written in that millisecond — which two turns of one
// conversation routinely are. Cutting there produced a listing whose ids the
// command that reads them could never resolve.
const shortIDLength = 20

func shortID(id string) string {
	if len(id) <= shortIDLength {
		return id
	}
	return id[:shortIDLength]
}

// runName sets or shows what this conversation is called.
//
// With no argument it shows the current name, matching Pi: asking what
// something is called is the other half of naming it, and making that a
// separate command would be a command nobody would find.
func runName(c *commandContext, arg string) bool {
	if arg == "" {
		if name := c.session.Name(); name != "" {
			fmt.Fprintf(c.out, "  %s\n", name)
			return false
		}
		fmt.Fprintln(c.errOut, "usage: /name <text>")
		return false
	}
	if err := c.session.SetName(arg); err != nil {
		fmt.Fprintf(c.errOut, "could not set the name: %v\n", err)
		return false
	}
	fmt.Fprintf(c.out, "named %q\n", c.session.Name())
	return false
}

// runImport opens a conversation stored somewhere other than the session
// directory.
//
// No confirmation, unlike Pi, and the difference is real rather than an
// omission: Pi replaces the running session, so it asks before discarding it.
// Here the conversation being left is already on disk and can be resumed again,
// so there is nothing to lose and nothing to ask about.
//
// It reads this repository's own session files. Pi's are a different format —
// ADR-0006 gives each its own — so importing one fails as an unreadable file
// rather than half-loading.
func runImport(c *commandContext, arg string) bool {
	if arg == "" {
		fmt.Fprintln(c.errOut, "usage: /import <path.jsonl>")
		return false
	}
	if _, err := os.Stat(arg); err != nil {
		fmt.Fprintf(c.errOut, "no session file at %s\n", arg)
		return false
	}
	next := c.args
	next.Continue, next.Resume = false, arg
	if err := c.reopen(next); err != nil {
		fmt.Fprintf(c.errOut, "could not import: %v\n", err)
		return false
	}
	fmt.Fprintf(c.out, "imported %s · %d messages\n",
		c.conversation.ID, len(c.session.Snapshot().Messages))
	return false
}

// runCopy puts the last thing the assistant said on the clipboard.
func runCopy(c *commandContext, _ string) bool {
	text, found := lastAnswer(c.session)
	if !found {
		fmt.Fprintln(c.errOut, "no answers to copy yet")
		return false
	}
	if err := copyToClipboard(text); err != nil {
		fmt.Fprintf(c.errOut, "could not copy: %v\n", err)
		return false
	}
	fmt.Fprintln(c.out, "copied the last answer")
	return false
}

// runModel switches which model answers from here on.
//
// Pi takes a search term, matches it against a model catalogue, and opens a
// selector when nothing matches exactly. There is no catalogue here — it is not
// in the pinned source — so this takes the name directly. A provider may be
// given with it, since two providers can offer similarly-named models and the
// bill goes to only one of them.
//
// The change applies from the NEXT turn, which is also Pi's rule: a turn's model
// is the one it was executed with, and changing that afterwards would describe
// a conversation that did not happen.
func runModel(c *commandContext, arg string) bool {
	if arg == "" {
		fmt.Fprintf(c.out, "  %s/%s\n", c.modelProvider(), c.modelName())
		fmt.Fprintln(c.errOut, "usage: /model [provider/]<model>")
		return false
	}

	provider, model := "", arg
	if at := strings.Index(arg, "/"); at >= 0 {
		provider, model = arg[:at], arg[at+1:]
	}
	if strings.TrimSpace(model) == "" {
		fmt.Fprintln(c.errOut, "that names a provider but no model")
		return false
	}
	if err := c.switchModel(provider, model); err != nil {
		fmt.Fprintf(c.errOut, "could not switch: %v\n", err)
		return false
	}
	fmt.Fprintf(c.out, "model %s/%s · from the next turn\n", c.modelProvider(), c.modelName())
	return false
}

// runCompact replaces the older part of the conversation with a summary.
//
// The same thing the runtime does on its own when a request is refused for
// exceeding the model's context — asked for early, before the refusal, which is
// why it exists as a command at all.
func runCompact(c *commandContext, arg string) bool {
	if c.compact == nil {
		fmt.Fprintln(c.errOut, "no summariser is configured for this run")
		return false
	}
	before := len(c.session.Snapshot().Messages)
	if err := c.compact(arg); err != nil {
		var nothing *compaction.ErrNothingToCompact
		if errors.As(err, &nothing) {
			// Worth saying once, not worth retrying: a conversation short
			// enough to leave alone is not a failure.
			fmt.Fprintf(c.errOut, "%v\n", err)
			return false
		}
		fmt.Fprintf(c.errOut, "could not compact: %v\n", err)
		return false
	}
	// Truth is unchanged — compaction shortens what the MODEL sees, not what
	// happened — so the count that moved is the projection's.
	fmt.Fprintf(c.out, "compacted · a summary now stands in for the older part of %d messages\n", before)
	return false
}

// runLogin saves a credential so it does not have to be given again.
//
// The key is read from the terminal with echo off rather than taken as an
// argument. A key on the command line lands in shell history and in the
// scrollback of the session it was typed into, and both outlive the terminal.
func runLogin(c *commandContext, arg string) bool {
	if arg == "" {
		fmt.Fprintf(c.errOut, "usage: /login <provider> — one of %s\n",
			strings.Join(ProviderNames(), ", "))
		return false
	}
	if _, known := Providers[arg]; !known {
		fmt.Fprintf(c.errOut, "unknown provider %q; this build has %s\n",
			arg, strings.Join(ProviderNames(), ", "))
		return false
	}

	store, err := AuthStore(c.args)
	if err != nil {
		fmt.Fprintf(c.errOut, "could not open the credential store: %v\n", err)
		return false
	}
	fmt.Fprintf(c.out, "key for %s (not shown): ", arg)
	key, err := readSecret()
	fmt.Fprintln(c.out)
	if err != nil {
		fmt.Fprintf(c.errOut, "could not read the key: %v\n", err)
		return false
	}
	if strings.TrimSpace(key) == "" {
		// Storing an empty key would look like being logged in while every
		// request fails as an authentication error.
		fmt.Fprintln(c.errOut, "nothing entered; no credential was saved")
		return false
	}
	if err := store.Set(arg, auth.APIKey(key)); err != nil {
		fmt.Fprintf(c.errOut, "could not save: %v\n", err)
		return false
	}
	fmt.Fprintf(c.out, "saved a credential for %s in %s\n", arg, store.Path())
	return false
}

// runLogout forgets a saved credential.
//
// With no argument it lists what is saved rather than removing everything: a
// bare command that destroys all credentials is one somebody runs once by
// accident.
func runLogout(c *commandContext, arg string) bool {
	store, err := AuthStore(c.args)
	if err != nil {
		fmt.Fprintf(c.errOut, "could not open the credential store: %v\n", err)
		return false
	}
	saved, err := store.Providers()
	if err != nil {
		fmt.Fprintf(c.errOut, "could not read the credential store: %v\n", err)
		return false
	}

	if arg == "" {
		if len(saved) == 0 {
			fmt.Fprintln(c.out, "  no saved credentials")
			return false
		}
		for _, name := range saved {
			fmt.Fprintf(c.out, "  %s\n", name)
		}
		fmt.Fprintln(c.errOut, "usage: /logout <provider>")
		return false
	}
	if err := store.Remove(arg); err != nil {
		fmt.Fprintf(c.errOut, "could not remove: %v\n", err)
		return false
	}
	// The environment may still carry one, and a user told they are logged out
	// while requests keep succeeding has been told something false.
	if p, known := Providers[arg]; known {
		for _, v := range p.EnvVars {
			if strings.TrimSpace(os.Getenv(v)) != "" {
				fmt.Fprintf(c.out, "forgot the saved credential for %s, but %s is still set\n", arg, v)
				return false
			}
		}
	}
	fmt.Fprintf(c.out, "forgot the credential for %s\n", arg)
	return false
}

// runShare uploads the conversation as a secret gist, through the GitHub CLI.
//
// Through gh rather than the API directly, as Pi does: it already holds the
// user's GitHub authentication, and asking for a token of our own would be a
// second credential to store and revoke for something the machine can already
// do.
//
// It asks first, which Pi does not. The difference is deliberate: a coding
// conversation carries source code, and tool output can carry things that were
// never meant to leave the machine — a key printed by a command, a line from a
// config file. "Secret" gist means unlisted, not private, and an upload cannot
// be recalled from whatever has already fetched it. The user typing /share is
// asking to publish; being told what is about to be published is the difference
// between that and finding out afterwards.
func runShare(c *commandContext, _ string) bool {
	messages := c.session.Snapshot().Messages
	if len(messages) == 0 {
		fmt.Fprintln(c.errOut, "there is no conversation to share yet")
		return false
	}
	if err := exec.Command("gh", "auth", "status").Run(); err != nil {
		if _, missing := exec.LookPath("gh"); missing != nil {
			fmt.Fprintln(c.errOut, "the GitHub CLI (gh) is not installed: https://cli.github.com/")
			return false
		}
		fmt.Fprintln(c.errOut, "the GitHub CLI is not logged in; run 'gh auth login' first")
		return false
	}

	fmt.Fprintf(c.out,
		"upload %d messages to a secret gist? it is unlisted, not private, and cannot be recalled [y/N]: ",
		len(messages))
	if !c.confirm() {
		fmt.Fprintln(c.out, "not shared")
		return false
	}

	// Markdown rather than Pi's HTML: there is no renderer here, and sharing
	// something this build cannot produce would be worse than sharing the
	// content in a form GitHub already displays.
	file, err := os.CreateTemp("", "pi-go-session-*.md")
	if err != nil {
		fmt.Fprintf(c.errOut, "could not prepare the upload: %v\n", err)
		return false
	}
	// Removed whatever happens: it holds the whole conversation, and leaving it
	// in a shared temporary directory is the disclosure this command asked
	// about.
	defer os.Remove(file.Name())
	if _, err := file.WriteString(renderConversation(c.conversation.ID, messages)); err != nil {
		file.Close()
		fmt.Fprintf(c.errOut, "could not prepare the upload: %v\n", err)
		return false
	}
	file.Close()

	out, err := exec.Command("gh", "gist", "create", "--public=false", file.Name()).Output()
	if err != nil {
		fmt.Fprintf(c.errOut, "could not create the gist: %v\n", err)
		return false
	}
	url := strings.TrimSpace(string(out))
	if url == "" {
		fmt.Fprintln(c.errOut, "the gist was created but gh reported no URL")
		return false
	}
	fmt.Fprintf(c.out, "shared: %s\n", url)
	return false
}

// renderConversation is the readable form used by /export and /share.
func renderConversation(id string, messages []ai.Message) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# pi-go session %s\n# exported %s\n\n", id, time.Now().Format(time.RFC3339))
	for _, m := range messages {
		if strings.TrimSpace(m.Content) == "" {
			continue
		}
		fmt.Fprintf(&b, "## %s\n\n%s\n\n", string(m.Role), m.Content)
	}
	return b.String()
}
