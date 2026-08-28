package session

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// EnvAgentDir overrides where sessions are kept.
const EnvAgentDir = "PI_GO_AGENT_DIR"

// AgentDir is where this build keeps everything that outlives a run.
//
// Its own directory rather than Pi's. ADR-0006 gives this repository a native
// wire and rules out interoperability, so a shared directory would offer a user
// sessions from one program that the other cannot read — and would put files
// this build wrote where another program will try to.
func AgentDir() (string, error) {
	if dir := strings.TrimSpace(os.Getenv(EnvAgentDir)); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("session: %w", err)
	}
	return filepath.Join(home, ".pi-go", "agent"), nil
}

// DirFor is where the sessions for one working directory live.
//
// Sessions are grouped by the directory they ran in, because that is how they
// are looked for: a person resuming work asks for "the session I was just in
// here", not for one out of every session on the machine. The path is encoded
// into a single name rather than mirrored as nested directories, so an empty
// tree is never left behind when a project is deleted.
func DirFor(agentDir, workingDir string) string {
	resolved, err := filepath.Abs(workingDir)
	if err != nil {
		resolved = workingDir
	}
	safe := strings.TrimPrefix(filepath.ToSlash(resolved), "/")
	safe = strings.NewReplacer("/", "-", ":", "-", "\\", "-").Replace(safe)
	return filepath.Join(agentDir, "sessions", "--"+safe+"--")
}

// FileName is what one session is called on disk.
//
// The timestamp leads so a directory listing is in the order the sessions
// happened, and the id follows so two started in the same second do not collide.
func FileName(id string, at time.Time) string {
	stamp := at.UTC().Format("2006-01-02T15-04-05.000Z")
	return stamp + "_" + id + ".jsonl"
}

// Info describes a stored session without loading its conversation.
type Info struct {
	Path     string
	ID       string
	Dir      string
	Started  time.Time
	Modified time.Time

	// Entries is how much was recorded, across every branch. A session holding
	// nothing is passed over: resuming one is indistinguishable from starting
	// fresh, and it is almost always the empty file a previous run left behind.
	Entries int

	// Opening is the first thing the user asked, which is how a person
	// recognises a session in a list.
	Opening string
}

// List returns the sessions for a working directory, most recently modified
// first.
//
// A file that cannot be read is skipped rather than failing the listing: one
// corrupt session must not make every other one unreachable.
func List(agentDir, workingDir string) ([]Info, error) {
	dir := DirFor(agentDir, workingDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("session: %w", err)
	}

	var found []Info
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := Describe(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		found = append(found, info)
	}
	sort.Slice(found, func(i, j int) bool {
		if !found[i].Modified.Equal(found[j].Modified) {
			return found[i].Modified.After(found[j].Modified)
		}
		// A stable tiebreak, so two sessions touched in the same instant are
		// listed the same way twice.
		return found[i].Path > found[j].Path
	})
	return found, nil
}

// Describe reads one session's summary without keeping its conversation.
func Describe(path string) (Info, error) {
	f, err := os.Open(path)
	if err != nil {
		return Info{}, fmt.Errorf("session: %w", err)
	}
	defer f.Close()

	header, records, err := readAll(f)
	if err != nil {
		return Info{}, err
	}
	stat, err := f.Stat()
	if err != nil {
		return Info{}, fmt.Errorf("session: %w", err)
	}

	info := Info{
		Path:     path,
		ID:       header.ID,
		Dir:      header.Dir,
		Modified: stat.ModTime(),
		// Everything recorded, not just the current branch. A listing answers
		// "is there anything here", and a conversation the user branched away
		// from is still something they may want back.
		Entries: len(records),
	}
	if started, err := time.Parse(time.RFC3339Nano, header.Timestamp); err == nil {
		info.Started = started
	}
	for _, r := range records {
		if r.Message != nil && r.Message.Role == "user" && strings.TrimSpace(r.Message.Content) != "" {
			info.Opening = strings.TrimSpace(r.Message.Content)
			break
		}
	}
	return info, nil
}

// MostRecent is the session to continue when the user does not name one.
//
// Sessions holding nothing are passed over. An empty file is what a run that
// reached no model leaves behind, and continuing one gives the user a fresh
// conversation while telling them it resumed — which is worse than saying there
// was nothing to resume.
func MostRecent(agentDir, workingDir string) (Info, bool, error) {
	all, err := List(agentDir, workingDir)
	if err != nil {
		return Info{}, false, err
	}
	for _, info := range all {
		if info.Entries > 0 {
			return info, true, nil
		}
	}
	return Info{}, false, nil
}
