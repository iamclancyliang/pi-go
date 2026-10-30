package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/session"
)

// Conversation is the session a run works in, and how to close it.
type Conversation struct {
	Session *session.Session

	// Resumed says an earlier conversation was reopened. Reported to the user,
	// because continuing silently is indistinguishable from starting fresh
	// until the model answers something it should not have known.
	Resumed bool

	// Path is where it is being recorded, or empty when nothing is.
	Path string

	// ID identifies this conversation, and is what --resume takes. Shown to
	// the user, because an id they cannot see is one they cannot ask for.
	ID string

	// Store is the durable record, when there is one. Held so the commands
	// that work on the shape of a conversation — moving within it, copying it
	// — have something to work on; nil for a run that keeps nothing.
	Store *session.FileStore

	// Dir is the session directory this conversation belongs to, so a fork
	// lands beside it rather than wherever the default points.
	Dir string

	// Notice is something opening it found worth saying, empty when nothing was.
	Notice string

	close func() error
}

// Close releases the session file.
func (c *Conversation) Close() error {
	if c == nil || c.close == nil {
		return nil
	}
	return c.close()
}

// OpenConversation decides which conversation a run belongs to and opens it,
// with nobody to ask about a conversation from another directory.
func OpenConversation(args Args, workingDir, system string) (*Conversation, error) {
	return OpenConversationAsking(args, workingDir, system, nil)
}

// OpenConversationAsking opens the conversation this command line names, asking
// before copying one in from another directory. A nil ask means nobody can be
// asked, and the copy is refused rather than assumed.
func OpenConversationAsking(args Args, workingDir, system string, ask func(string) bool) (*Conversation, error) {
	if err := checkConversationFlags(args); err != nil {
		return nil, err
	}
	conversation, err := openNamedConversation(args, workingDir, system, ask)
	if err != nil {
		return nil, err
	}
	if args.NameGiven {
		if err := conversation.Session.SetName(args.Name); err != nil {
			conversation.Close()
			return nil, err
		}
	}
	return conversation, nil
}

// checkConversationFlags refuses the combinations that ask for two conversations
// at once, and a name that could not be recorded. Refused before anything is
// opened, so a mistake on the command line leaves no empty session behind it.
func checkConversationFlags(args Args) error {
	conflicts := func(flag string, others ...bool) error {
		names := []string{"--session", "--continue", "--resume", "--no-session"}
		var found []string
		for at, on := range others {
			if on {
				found = append(found, names[at])
			}
		}
		if len(found) > 0 {
			return fmt.Errorf("%s cannot be combined with %s", flag, strings.Join(found, ", "))
		}
		return nil
	}
	if args.NameGiven && strings.TrimSpace(args.Name) == "" {
		return fmt.Errorf("--name requires a non-empty value")
	}
	resuming := args.Continue
	if args.Fork != "" {
		if err := conflicts("--fork", args.Session != "", resuming, args.Resume != "", args.NoSession); err != nil {
			return err
		}
	}
	if args.SessionID != "" {
		if err := conflicts("--session-id", args.Session != "", resuming, args.Resume != ""); err != nil {
			return err
		}
		if !validSessionID.MatchString(args.SessionID) {
			return fmt.Errorf("session id must contain only letters, digits, '-', '_' and '.', " +
				"and start and end with a letter or digit")
		}
	}
	return nil
}

// validSessionID is Pi's rule. The id becomes part of a file name, so this also
// keeps a path out of it.
var validSessionID = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?$`)

// openNamedConversation is the choice itself. --no-session keeps everything in
// memory; --fork copies one; --session and --resume name one; --continue takes
// the most recent here; --session-id opens or creates an exact id. Anything else
// starts a new one, recorded so the NEXT run can continue it — a session that is
// only recorded when asked for cannot be resumed, because the asking happens
// after the conversation worth keeping.
func openNamedConversation(args Args, workingDir, system string, ask func(string) bool) (*Conversation, error) {
	if args.NoSession {
		// No store: a conversation that keeps nothing has no shape to navigate
		// and nothing to copy, which the commands check for rather than
		// discovering through a nil.
		return &Conversation{Session: session.New(system), ID: args.SessionID}, nil
	}

	agentDir := args.SessionDir
	if agentDir == "" {
		resolved, err := session.AgentDir()
		if err != nil {
			return nil, err
		}
		agentDir = resolved
	}

	if args.Fork != "" {
		if args.SessionID != "" {
			existing, err := sessionWithID(agentDir, workingDir, args.SessionID)
			if err != nil {
				return nil, err
			}
			if existing != "" {
				return nil, fmt.Errorf("session already exists with id %q", args.SessionID)
			}
		}
		found, err := locateSession(agentDir, workingDir, args.Fork)
		if err != nil {
			return nil, err
		}
		if _, err := os.Stat(found.path); err != nil {
			return nil, fmt.Errorf("no session file at %s", found.path)
		}
		return forkInto(found.path, agentDir, workingDir, system, args.SessionID)
	}

	if args.Session != "" {
		found, err := locateSession(agentDir, workingDir, args.Session)
		if err != nil {
			return nil, err
		}
		if found.otherDir == "" {
			return reopen(found.path, workingDir, system, "", true)
		}
		// A conversation carries the paths and assumptions of where it ran, so
		// it is copied here rather than continued in place, and only on a yes.
		if ask == nil {
			return nil, fmt.Errorf("that session belongs to %s; use --fork to copy it here", found.otherDir)
		}
		if !ask(fmt.Sprintf("Session found in %s.\nFork it into this directory?", found.otherDir)) {
			return nil, fmt.Errorf("not forked; nothing was opened")
		}
		return forkInto(found.path, agentDir, workingDir, system, "")
	}

	if args.Resume != "" {
		path, err := resolveSessionName(agentDir, workingDir, args.Resume)
		if err != nil {
			return nil, err
		}
		return reopen(path, workingDir, system, "", true)
	}

	if args.Continue {
		info, found, err := session.MostRecent(agentDir, workingDir)
		if err != nil {
			return nil, err
		}
		if !found {
			// Said rather than silently starting fresh: a user who asked to
			// continue and was given a blank conversation will assume the
			// history was lost, not that there was none.
			return nil, fmt.Errorf("no conversation to continue in %s", workingDir)
		}
		return reopen(info.Path, workingDir, system, "", true)
	}

	now := time.Now()
	id := args.SessionID
	notice := ""
	if id != "" {
		existing, err := sessionWithID(agentDir, workingDir, id)
		if err != nil {
			return nil, err
		}
		if existing != "" {
			return reopen(existing, workingDir, system, "", true)
		}
		notice = fmt.Sprintf("no session here has the id %q; starting a new one with it", id)
	} else {
		id = session.NewSessionID(now)
	}
	path := filepath.Join(session.DirFor(agentDir, workingDir), session.FileName(id, now))
	// The SAME id names the file and identifies the record inside it. Two ids
	// for one conversation means the one a listing shows is not the one the
	// file is called, and --resume then takes a name that finds nothing.
	conversation, err := reopen(path, workingDir, system, id, false)
	if err != nil {
		return nil, err
	}
	conversation.Notice = notice
	return conversation, nil
}

// WithoutConversationChoice is these args once the conversation they named is
// open. The commands that move between conversations reopen from the same args,
// and /new must not fork the source again or rename what it starts.
func (a Args) WithoutConversationChoice() Args {
	a.Session, a.SessionID, a.Fork = "", "", ""
	a.Name, a.NameGiven = "", false
	return a
}

// sessionLocation is where a name led. otherDir is set when the session belongs
// to a different working directory than this run's.
type sessionLocation struct {
	path     string
	otherDir string
}

// locateSession turns what the user typed into a session. A path is taken as
// one, and may not exist yet: --session can name the file a new conversation
// starts in. Anything else is an id or its prefix, looked for here first and
// then everywhere. As in resolveSessionName, an ambiguous prefix is refused.
func locateSession(agentDir, workingDir, name string) (sessionLocation, error) {
	if strings.ContainsAny(name, "/\\") || strings.HasSuffix(name, ".jsonl") {
		return sessionLocation{path: absoluteUnder(workingDir, name)}, nil
	}
	here, err := session.List(agentDir, workingDir)
	if err != nil {
		return sessionLocation{}, err
	}
	if found, err := matchSessionID(here, name); found != nil || err != nil {
		if err != nil {
			return sessionLocation{}, err
		}
		return sessionLocation{path: found.Path}, nil
	}
	everywhere, err := session.ListAll(agentDir)
	if err != nil {
		return sessionLocation{}, err
	}
	found, err := matchSessionID(everywhere, name)
	if err != nil {
		return sessionLocation{}, err
	}
	if found == nil {
		return sessionLocation{}, fmt.Errorf("no session found matching %q", name)
	}
	return sessionLocation{path: found.Path, otherDir: found.Dir}, nil
}

// matchSessionID finds an id exactly, or failing that by an unambiguous prefix.
// Nil means nothing matched.
func matchSessionID(all []session.Info, name string) (*session.Info, error) {
	var matched []session.Info
	for _, info := range all {
		if info.ID == name {
			return &info, nil
		}
		if strings.HasPrefix(info.ID, name) {
			matched = append(matched, info)
		}
	}
	switch len(matched) {
	case 0:
		return nil, nil
	case 1:
		return &matched[0], nil
	default:
		ids := make([]string, 0, len(matched))
		for _, info := range matched {
			ids = append(ids, info.ID)
		}
		return nil, fmt.Errorf("%q matches more than one session: %s", name, strings.Join(ids, ", "))
	}
}

// sessionWithID is the path of the conversation here with exactly this id, or
// empty when there is none.
func sessionWithID(agentDir, workingDir, id string) (string, error) {
	all, err := session.List(agentDir, workingDir)
	if err != nil {
		return "", err
	}
	for _, info := range all {
		if info.ID == id {
			return info.Path, nil
		}
	}
	return "", nil
}

// forkInto copies a whole conversation into a new one in this directory, and
// opens the copy. The source is only read.
func forkInto(source, agentDir, workingDir, system, id string) (*Conversation, error) {
	now := time.Now()
	if id == "" {
		id = session.NewSessionID(now)
	}
	from, err := session.OpenFileStore(source, workingDir, id)
	if err != nil {
		return nil, err
	}
	defer from.Close()
	path := filepath.Join(session.DirFor(agentDir, workingDir), session.FileName(id, now))
	copied, err := from.BranchInto(context.Background(), path, workingDir, id, from.Leaf())
	if err != nil {
		return nil, err
	}
	if err := copied.Close(); err != nil {
		return nil, err
	}
	return reopen(path, workingDir, system, "", true)
}

// absoluteUnder resolves a path the way a person typing it means: from the
// working directory, with ~ as their home.
func absoluteUnder(workingDir, path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[1:])
		}
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(workingDir, path)
}

// agentDirOf recovers the agent directory a session path sits under, so a fork
// lands beside the conversation it came from rather than wherever the default
// points — which matters when a run was given --session-dir.
func agentDirOf(path string) string {
	// <agentDir>/sessions/<encoded working dir>/<file>
	return filepath.Dir(filepath.Dir(filepath.Dir(path)))
}

func reopen(path, workingDir, system, id string, resumed bool) (*Conversation, error) {
	if id == "" {
		// Reopening: the file already carries an identity, and the store keeps
		// it. This value is only used when the file turns out to be new.
		id = session.NewSessionID(time.Now())
	}
	store, err := session.OpenFileStore(path, workingDir, id)
	if err != nil {
		return nil, err
	}
	sess, err := session.Restore(context.Background(), system, store)
	if err != nil {
		store.Close()
		return nil, err
	}
	return &Conversation{
		Session: sess,
		Resumed: resumed,
		Path:    path,
		ID:      store.ID(),
		Store:   store,
		Dir:     agentDirOf(path),
		close:   store.Close,
	}, nil
}

// resolveSessionName turns what the user typed into a path.
//
// A path is taken as one; anything else is matched against the session ids in
// this directory, by prefix, so a person can type the first few characters the
// listing showed them. An ambiguous prefix is refused rather than resolved to
// whichever came first: reopening the wrong conversation is not something to
// discover from its contents.
func resolveSessionName(agentDir, workingDir, name string) (string, error) {
	if strings.ContainsAny(name, "/\\") || strings.HasSuffix(name, ".jsonl") {
		if _, err := os.Stat(name); err != nil {
			return "", fmt.Errorf("no session file at %s", name)
		}
		return name, nil
	}

	all, err := session.List(agentDir, workingDir)
	if err != nil {
		return "", err
	}
	var matched []session.Info
	for _, info := range all {
		if strings.HasPrefix(info.ID, name) {
			matched = append(matched, info)
		}
	}
	switch len(matched) {
	case 0:
		return "", fmt.Errorf("no session in %s starts with %q", workingDir, name)
	case 1:
		return matched[0].Path, nil
	default:
		ids := make([]string, 0, len(matched))
		for _, info := range matched {
			ids = append(ids, info.ID)
		}
		return "", fmt.Errorf("%q matches more than one session: %s", name, strings.Join(ids, ", "))
	}
}

// RestoreRecorded fills in, from a conversation being continued, what this
// run did not choose for itself: the model it was last using, when neither
// --provider nor --model was given, and its thinking level, when --thinking
// was not. Pi restores both the same way (createAgentSession at the pin), and
// before any default from settings, so a resumed conversation carries on as
// it was rather than as the next run happens to default.
//
// A conversation with no messages has nothing to continue, and is left to
// the defaults like a new one. Restored reports whether a model came from the
// record, so a caller that cannot open it can say so and fall back.
func RestoreRecorded(args Args, sess *session.Session) (out Args, restored bool) {
	if len(sess.Snapshot().Messages) == 0 {
		return args, false
	}
	if args.Provider == "" && args.Model == "" {
		if provider, model, ok := sess.RecordedModel(); ok {
			args.Provider, args.Model, restored = provider, model, true
		}
	}
	if args.Thinking == "" {
		if level, ok := sess.RecordedThinking(); ok {
			args.Thinking = level
		}
	}
	return args, restored
}

// recordInUse writes the model and thinking level a run is about to use into
// its conversation, so the next run that continues it can restore them.
// Recording what is already recorded writes nothing, and asking for nothing
// records nothing.
func recordInUse(sess *session.Session, provider, model string, thinking ai.ThinkingLevel) error {
	if provider != "" && model != "" {
		if err := sess.RecordModel(provider, model); err != nil {
			return err
		}
	}
	if thinking != "" {
		if err := sess.RecordThinking(thinking); err != nil {
			return err
		}
	}
	return nil
}
