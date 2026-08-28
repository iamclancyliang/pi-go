package session_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/session"
)

// record writes a session for workingDir holding the given opening line, and
// returns its path.
func record(t *testing.T, agentDir, workingDir, id, opening string) string {
	t.Helper()
	dir := session.DirFor(agentDir, workingDir)
	path := filepath.Join(dir, session.FileName(id, time.Now()))
	store, err := session.OpenFileStore(path, workingDir, id)
	if err != nil {
		t.Fatalf("opening %s: %v", id, err)
	}
	defer store.Close()
	if opening != "" {
		if err := store.Append(context.Background(),
			session.Entry{Message: &ai.Message{Role: ai.RoleUser, Content: opening}}); err != nil {
			t.Fatalf("appending: %v", err)
		}
	}
	return path
}

// TestSessionsAreGroupedByWhereTheyRan. A person resuming asks for "the one I
// was just in HERE", so a listing that mixed in every project on the machine
// would be unusable.
func TestSessionsAreGroupedByWhereTheyRan(t *testing.T) {
	agentDir := t.TempDir()
	here, there := t.TempDir(), t.TempDir()

	record(t, agentDir, here, "a", "in here")
	record(t, agentDir, there, "b", "somewhere else")

	got, err := session.List(agentDir, here)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("listing this directory found %d sessions", len(got))
	}
	if got[0].Opening != "in here" {
		t.Fatalf("it found the wrong one: %+v", got[0])
	}
}

// TestListingIsNewestFirst, which is the order the answer is wanted in.
func TestListingIsNewestFirst(t *testing.T) {
	agentDir := t.TempDir()
	work := t.TempDir()

	first := record(t, agentDir, work, "a", "older")
	time.Sleep(10 * time.Millisecond)
	second := record(t, agentDir, work, "b", "newer")

	// Touched explicitly, so the order does not rest on how fast the test ran.
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(first, past, past); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	got, err := session.List(agentDir, work)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("found %d sessions", len(got))
	}
	if got[0].Path != second || got[1].Path != first {
		t.Fatalf("the listing is in the wrong order: %v", []string{got[0].Opening, got[1].Opening})
	}
}

// TestAnEmptySessionIsNotWhatContinueResumes. It is what a run that reached no
// model leaves behind, and resuming one hands the user a fresh conversation
// while telling them it continued.
func TestAnEmptySessionIsNotWhatContinueResumes(t *testing.T) {
	agentDir := t.TempDir()
	work := t.TempDir()

	real := record(t, agentDir, work, "a", "a real conversation")
	time.Sleep(10 * time.Millisecond)
	record(t, agentDir, work, "b", "") // the empty file a later run left

	got, found, err := session.MostRecent(agentDir, work)
	if err != nil {
		t.Fatalf("MostRecent: %v", err)
	}
	if !found {
		t.Fatal("there was a conversation to continue and none was found")
	}
	if got.Path != real {
		t.Fatalf("it chose the empty session: %+v", got)
	}
}

// TestNothingToResumeIsSaidRatherThanGuessed.
func TestNothingToResumeIsSaidRatherThanGuessed(t *testing.T) {
	_, found, err := session.MostRecent(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatalf("MostRecent: %v", err)
	}
	if found {
		t.Fatal("a directory with no sessions offered one")
	}
}

// TestOneCorruptSessionDoesNotHideTheRest. A listing that failed whole would
// make every other conversation unreachable because of one bad file.
func TestOneCorruptSessionDoesNotHideTheRest(t *testing.T) {
	agentDir := t.TempDir()
	work := t.TempDir()
	record(t, agentDir, work, "good", "a real conversation")

	bad := filepath.Join(session.DirFor(agentDir, work), session.FileName("bad", time.Now()))
	if err := os.WriteFile(bad, []byte("this is not json\n"), 0o600); err != nil {
		t.Fatalf("writing: %v", err)
	}

	got, err := session.List(agentDir, work)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].Opening != "a real conversation" {
		t.Fatalf("a corrupt file hid the readable one: %+v", got)
	}
}

// TestTheSessionDirectoryIsOneNamePerProject, so deleting a project does not
// leave an empty tree behind, and two projects cannot collide.
func TestTheSessionDirectoryIsOneNamePerProject(t *testing.T) {
	agentDir := "/agents"
	a := session.DirFor(agentDir, "/home/someone/work/project-a")
	b := session.DirFor(agentDir, "/home/someone/work/project-b")

	if a == b {
		t.Fatal("two projects share a session directory")
	}
	if filepath.Dir(a) != filepath.Join(agentDir, "sessions") {
		t.Fatalf("sessions are nested rather than named: %s", a)
	}
	if strings.Contains(filepath.Base(a), "/") {
		t.Fatalf("the encoded name still holds a separator: %s", a)
	}
}

// TestAnAgentDirectoryOfItsOwn: ADR-0006 rules out Pi interoperability, so
// sharing Pi's directory would offer a user sessions the other program cannot
// read, and put this build's files where that one will try to.
func TestAnAgentDirectoryOfItsOwn(t *testing.T) {
	t.Setenv(session.EnvAgentDir, "")
	dir, err := session.AgentDir()
	if err != nil {
		t.Fatalf("AgentDir: %v", err)
	}
	if strings.Contains(dir, "/.pi/") || strings.HasSuffix(dir, "/.pi") {
		t.Fatalf("this build writes into Pi's directory: %s", dir)
	}
	if !strings.Contains(dir, ".pi-go") {
		t.Fatalf("the agent directory is not this build's own: %s", dir)
	}
}

// TestTheAgentDirectoryCanBeOverridden, which is what lets a test — or a user
// keeping projects apart — put sessions somewhere else.
func TestTheAgentDirectoryCanBeOverridden(t *testing.T) {
	t.Setenv(session.EnvAgentDir, "/somewhere/else")
	dir, err := session.AgentDir()
	if err != nil {
		t.Fatalf("AgentDir: %v", err)
	}
	if dir != "/somewhere/else" {
		t.Fatalf("the override was ignored: %s", dir)
	}
}
