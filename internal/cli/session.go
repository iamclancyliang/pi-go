package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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

	close func() error
}

// Close releases the session file.
func (c *Conversation) Close() error {
	if c == nil || c.close == nil {
		return nil
	}
	return c.close()
}

// OpenConversation decides which conversation a run belongs to and opens it.
//
// Four cases, and they are not interchangeable. --no-session keeps everything
// in memory. --resume names one. --continue takes the most recent in this
// directory. Anything else starts a new one, recorded so the NEXT run can
// continue it — a session that is only recorded when asked for cannot be
// resumed, because the asking happens after the conversation worth keeping.
func OpenConversation(args Args, workingDir, system string) (*Conversation, error) {
	if args.NoSession {
		// No store: a conversation that keeps nothing has no shape to navigate
		// and nothing to copy, which the commands check for rather than
		// discovering through a nil.
		return &Conversation{Session: session.New(system)}, nil
	}

	agentDir := args.SessionDir
	if agentDir == "" {
		resolved, err := session.AgentDir()
		if err != nil {
			return nil, err
		}
		agentDir = resolved
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
	id := session.NewSessionID(now)
	path := filepath.Join(session.DirFor(agentDir, workingDir), session.FileName(id, now))
	// The SAME id names the file and identifies the record inside it. Two ids
	// for one conversation means the one a listing shows is not the one the
	// file is called, and --resume then takes a name that finds nothing.
	return reopen(path, workingDir, system, id, false)
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
