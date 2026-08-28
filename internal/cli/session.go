package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

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
