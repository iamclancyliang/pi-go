package cli_test

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/cli"
	"github.com/iamclancyliang/pi-go/internal/settings"
)

func toolNames(t *testing.T, args cli.Args, effective settings.Settings) string {
	t.Helper()
	registry, err := cli.ToolsFor(args, t.TempDir(), effective)
	if err != nil {
		t.Fatalf("building tools: %v", err)
	}
	var names []string
	for _, tool := range registry.All() {
		names = append(names, tool.Name())
	}
	return strings.Join(names, " ")
}

func TestTheToolFlagsChooseTheSet(t *testing.T) {
	configured := settings.Settings{DefaultTools: []string{"read", "bash"}}
	cases := []struct {
		name string
		args cli.Args
		want string
	}{
		{"settings when nothing is said", cli.Args{}, "bash read"},
		{"--tools replaces settings", cli.Args{Tools: []string{"grep"}}, "grep"},
		{"--tools beats --no-tools", cli.Args{Tools: []string{"grep"}, NoTools: true}, "grep"},
		{"--tools naming nothing is none", cli.Args{Tools: []string{}}, ""},
		{"--no-tools", cli.Args{NoTools: true}, ""},
		{"--no-builtin-tools", cli.Args{NoBuiltinTools: true}, ""},
		{"--exclude-tools narrows settings", cli.Args{ExcludeTools: []string{"bash"}}, "read"},
		{"--exclude-tools narrows --tools", cli.Args{Tools: []string{"grep", "ls"}, ExcludeTools: []string{"ls"}}, "grep"},
		{"excluding what is absent changes nothing", cli.Args{ExcludeTools: []string{"ask_question"}}, "bash read"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := toolNames(t, c.args, configured); got != c.want {
				t.Fatalf("offered %q, want %q", got, c.want)
			}
		})
	}
}

// TestAMisspeltToolIsRefused. A set quietly missing the tool asked for is not the
// set the user chose.
func TestAMisspeltToolIsRefused(t *testing.T) {
	_, err := cli.ToolsFor(cli.Args{Tools: []string{"raed"}}, t.TempDir(), settings.Settings{})
	if err == nil || !strings.Contains(err.Error(), `--tools names "raed"`) {
		t.Fatalf("got %v", err)
	}
}

func TestPipedInputFilesAndTheMessageJoinInPisOrder(t *testing.T) {
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("alpha"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "empty.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	args := cli.Args{FileArgs: []string{"a.txt", "empty.txt"}, Messages: []string{"explain", "then this"}}
	got, err := cli.PrepareInitialMessage(args, work, " piped\n")
	if err != nil {
		t.Fatal(err)
	}
	want := "piped<file name=\"" + filepath.Join(work, "a.txt") + "\">\nalpha\n</file>\nexplain"
	if len(got.Messages) != 2 || got.Messages[0] != want || got.Messages[1] != "then this" {
		t.Fatalf("messages came back as %q, want first %q", got.Messages, want)
	}
	if args.Messages[0] != "explain" {
		t.Fatal("the caller's messages were edited")
	}
}

func TestNothingPipedLeavesTheMessagesAlone(t *testing.T) {
	got, err := cli.PrepareInitialMessage(cli.Args{Messages: []string{"hi"}}, t.TempDir(), " \n")
	if err != nil || len(got.Messages) != 1 || got.Messages[0] != "hi" {
		t.Fatalf("got %q, %v", got.Messages, err)
	}
	none, err := cli.PrepareInitialMessage(cli.Args{}, t.TempDir(), "")
	if err != nil || len(none.Messages) != 0 {
		t.Fatalf("got %q, %v", none.Messages, err)
	}
}

func TestAMissingFileEndsTheRun(t *testing.T) {
	_, err := cli.PrepareInitialMessage(cli.Args{FileArgs: []string{"nope.txt"}}, t.TempDir(), "")
	if err == nil || !strings.Contains(err.Error(), "file not found") {
		t.Fatalf("got %v", err)
	}
}

// TestAnImageIsRefusedRatherThanSentAsText. Named .txt, because the bytes decide.
func TestAnImageIsRefusedRatherThanSentAsText(t *testing.T) {
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "shot.txt"), []byte("\x89PNG\r\n\x1a\nrest"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := cli.PrepareInitialMessage(cli.Args{FileArgs: []string{"shot.txt"}}, work, "")
	if err == nil || !strings.Contains(err.Error(), "image") {
		t.Fatalf("got %v", err)
	}
}

func TestConflictingConversationFlagsAreRefused(t *testing.T) {
	dir := t.TempDir()
	cases := []cli.Args{
		{SessionDir: dir, Fork: "a", Session: "b"},
		{SessionDir: dir, Fork: "a", Continue: true},
		{SessionDir: dir, Fork: "a", NoSession: true},
		{SessionDir: dir, SessionID: "a", Session: "b"},
		{SessionDir: dir, SessionID: "a", Resume: "b"},
		{SessionDir: dir, SessionID: "../escape"},
		{SessionDir: dir, Name: "  ", NameGiven: true},
	}
	for _, args := range cases {
		if _, err := cli.OpenConversation(args, t.TempDir(), "s"); err == nil {
			t.Fatalf("%+v was accepted", args)
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("a refused command line left %v behind", entries)
	}
}

func TestSessionIDCreatesThenFindsTheSameConversation(t *testing.T) {
	dir, work := t.TempDir(), t.TempDir()
	first, err := cli.OpenConversation(cli.Args{SessionDir: dir, SessionID: "my-run.1"}, work, "s")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != "my-run.1" || first.Resumed || first.Notice == "" {
		t.Fatalf("created %+v", first)
	}
	if err := first.Session.Append(userMessage("remember")); err != nil {
		t.Fatal(err)
	}
	first.Close()

	second, err := cli.OpenConversation(cli.Args{SessionDir: dir, SessionID: "my-run.1"}, work, "s")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if second.Path != first.Path || !second.Resumed || second.Notice != "" ||
		len(second.Session.Snapshot().Messages) != 1 {
		t.Fatalf("reopened %+v, want the file %s", second, first.Path)
	}
}

func TestForkCopiesWithoutTouchingTheSource(t *testing.T) {
	dir, work := t.TempDir(), t.TempDir()
	source, err := cli.OpenConversation(cli.Args{SessionDir: dir}, work, "s")
	if err != nil {
		t.Fatal(err)
	}
	if err := source.Session.Append(userMessage("original")); err != nil {
		t.Fatal(err)
	}
	source.Close()
	before, err := os.ReadFile(source.Path)
	if err != nil {
		t.Fatal(err)
	}

	elsewhere := t.TempDir()
	fork, err := cli.OpenConversation(cli.Args{SessionDir: dir, Fork: source.ID, SessionID: "copy"}, elsewhere, "s")
	if err != nil {
		t.Fatal(err)
	}
	defer fork.Close()
	if fork.ID != "copy" || fork.Path == source.Path || len(fork.Session.Snapshot().Messages) != 1 {
		t.Fatalf("forked %+v", fork)
	}
	if !strings.Contains(fork.Path, filepath.Base(fork.Dir)) && fork.Dir != dir {
		t.Fatalf("the fork landed in %s", fork.Path)
	}
	after, err := os.ReadFile(source.Path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("the source changed: %v", err)
	}

	if _, err := cli.OpenConversation(cli.Args{SessionDir: dir, Fork: source.ID, SessionID: "copy"}, elsewhere, "s"); err == nil {
		t.Fatal("forking onto an id already in use succeeded")
	}
	if _, err := cli.OpenConversation(cli.Args{SessionDir: dir, Fork: "no-such"}, elsewhere, "s"); err == nil {
		t.Fatal("forking nothing succeeded")
	}
}

func TestSessionFromAnotherDirectoryForksOnlyOnAYes(t *testing.T) {
	dir, work, here := t.TempDir(), t.TempDir(), t.TempDir()
	source, err := cli.OpenConversation(cli.Args{SessionDir: dir}, work, "s")
	if err != nil {
		t.Fatal(err)
	}
	if err := source.Session.Append(userMessage("original")); err != nil {
		t.Fatal(err)
	}
	source.Close()

	args := cli.Args{SessionDir: dir, Session: source.ID}
	if _, err := cli.OpenConversation(args, here, "s"); err == nil || !strings.Contains(err.Error(), "--fork") {
		t.Fatalf("with nobody to ask: %v", err)
	}
	no := func(string) bool { return false }
	if _, err := cli.OpenConversationAsking(args, here, "s", no); err == nil {
		t.Fatal("a no still opened something")
	}
	yes := func(string) bool { return true }
	forked, err := cli.OpenConversationAsking(args, here, "s", yes)
	if err != nil {
		t.Fatal(err)
	}
	defer forked.Close()
	if forked.ID == source.ID || len(forked.Session.Snapshot().Messages) != 1 {
		t.Fatalf("forked %+v", forked)
	}

	local, err := cli.OpenConversation(args, work, "s")
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	if local.Path != source.Path {
		t.Fatalf("the session's own directory opened %s", local.Path)
	}
}

// TestSessionPathStartsANewFileThere: Pi keeps the explicit path for a file that
// does not exist yet, and a relative one is from the working directory.
func TestSessionPathStartsANewFileThere(t *testing.T) {
	work := t.TempDir()
	conversation, err := cli.OpenConversation(
		cli.Args{SessionDir: t.TempDir(), Session: "sub/mine.jsonl"}, work, "s")
	if err != nil {
		t.Fatal(err)
	}
	defer conversation.Close()
	if conversation.Path != filepath.Join(work, "sub", "mine.jsonl") {
		t.Fatalf("recorded at %s", conversation.Path)
	}
}

func TestNameIsRecordedWhenTheConversationOpens(t *testing.T) {
	conversation, err := cli.OpenConversation(
		cli.Args{SessionDir: t.TempDir(), Name: " auth work ", NameGiven: true}, t.TempDir(), "s")
	if err != nil {
		t.Fatal(err)
	}
	defer conversation.Close()
	if conversation.Session.Name() != "auth work" {
		t.Fatalf("named %q", conversation.Session.Name())
	}
}

// TestSpentChoicesDoNotApplyAgain. /new reopens from these args, and must not fork
// the source a second time or rename what it starts.
func TestSpentChoicesDoNotApplyAgain(t *testing.T) {
	got := cli.Args{Session: "a", SessionID: "b", Fork: "c", Name: "d", NameGiven: true, Model: "kept"}.WithoutConversationChoice()
	if got.Session != "" || got.SessionID != "" || got.Fork != "" || got.Name != "" || got.NameGiven || got.Model != "kept" {
		t.Fatalf("got %+v", got)
	}
}

func TestExportWritesThePageWithoutTouchingTheSession(t *testing.T) {
	dir, work := t.TempDir(), t.TempDir()
	source, err := cli.OpenConversation(cli.Args{SessionDir: dir}, work, "s")
	if err != nil {
		t.Fatal(err)
	}
	if err := source.Session.Append(userMessage("exported words")); err != nil {
		t.Fatal(err)
	}
	source.Close()
	before, _ := os.ReadFile(source.Path)

	written, err := cli.ExportFile(source.Path, cli.Args{Messages: []string{"out.html"}}, work)
	if err != nil {
		t.Fatal(err)
	}
	if written != filepath.Join(work, "out.html") {
		t.Fatalf("wrote %s", written)
	}
	page, err := os.ReadFile(written)
	if err != nil || !strings.Contains(string(page), "exported words") {
		t.Fatalf("the page does not hold the conversation: %v", err)
	}
	after, _ := os.ReadFile(source.Path)
	if !bytes.Equal(before, after) {
		t.Fatal("exporting changed the session file")
	}

	named, err := cli.ExportFile(source.Path, cli.Args{}, work)
	if err != nil || !strings.HasPrefix(filepath.Base(named), "pi-go-session-") {
		t.Fatalf("default name %q, %v", named, err)
	}
	if _, err := cli.ExportFile("missing.jsonl", cli.Args{}, work); err == nil || !strings.Contains(err.Error(), "file not found") {
		t.Fatalf("a missing file: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(work, "missing.jsonl")); statErr == nil {
		t.Fatal("exporting a missing file created it")
	}
}

func clearCredentials(t *testing.T) {
	t.Helper()
	for _, name := range cli.ProviderNames() {
		for _, v := range cli.Providers[name].EnvVars {
			t.Setenv(v, "")
		}
	}
	t.Setenv("PI_OFFLINE", "")
}

// refuseNetwork fails a test that leaves the machine.
type refuseNetwork struct{ t *testing.T }

func (r refuseNetwork) RoundTrip(req *http.Request) (*http.Response, error) {
	r.t.Fatalf("a request left the machine: %s", req.URL)
	return nil, nil
}

func TestListModelsShowsWhatIsRecordedForConfiguredProviders(t *testing.T) {
	clearCredentials(t)
	t.Setenv("DEEPSEEK_API_KEY", "k")
	var out, errOut bytes.Buffer
	code := cli.RunListModels(context.Background(), cli.Args{SessionDir: t.TempDir(), ListModels: true, Offline: true},
		cli.Streams{Out: &out, Err: &errOut}, refuseNetwork{t})
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "provider") {
		t.Fatalf("listed:\n%s", out.String())
	}
	fields := strings.Fields(lines[1])
	if strings.Join(fields, " ") != "deepseek deepseek-chat 1.0M - yes" {
		t.Fatalf("the row reads %q", fields)
	}
}

func TestListModelsFiltersAndSaysWhenNothingMatches(t *testing.T) {
	clearCredentials(t)
	t.Setenv("DEEPSEEK_API_KEY", "k")
	var out bytes.Buffer
	cli.RunListModels(context.Background(),
		cli.Args{SessionDir: t.TempDir(), ListModels: true, ListModelsPattern: "zzz", Offline: true},
		cli.Streams{Out: &out, Err: &out}, refuseNetwork{t})
	if !strings.Contains(out.String(), `No models matching "zzz"`) {
		t.Fatalf("said %q", out.String())
	}
}

func TestListModelsWithNoCredentialSaysWhatToDo(t *testing.T) {
	clearCredentials(t)
	var out bytes.Buffer
	cli.RunListModels(context.Background(), cli.Args{SessionDir: t.TempDir(), ListModels: true},
		cli.Streams{Out: &out, Err: &out}, refuseNetwork{t})
	if !strings.Contains(out.String(), "No models available") {
		t.Fatalf("said %q", out.String())
	}
}

// TestOfflineReachesNoProvider, including through the environment, even for the
// one provider that can list and needs no credential.
func TestOfflineReachesNoProvider(t *testing.T) {
	clearCredentials(t)
	t.Setenv("PI_OFFLINE", "true")
	var out bytes.Buffer
	code := cli.RunListModels(context.Background(), cli.Args{SessionDir: t.TempDir(), ListModels: true, Provider: "ollama"},
		cli.Streams{Out: &out, Err: &out}, refuseNetwork{t})
	if code != 0 || !strings.Contains(out.String(), "ollama") {
		t.Fatalf("exit %d: %s", code, out.String())
	}
}

func TestApprovingOverridesTheRecordForThisRunOnly(t *testing.T) {
	agentDir, work := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(work, ".pi-go"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings.ProjectPath(work), []byte(`{"shellPath":"/bin/sh"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	asked := func(string) bool { t.Fatal("the flag still asked"); return false }

	yes, no := true, false
	approved, err := cli.ResolveConfig(cli.Args{SessionDir: agentDir, ProjectTrust: &yes}, work, asked)
	if err != nil || !approved.ProjectLoaded {
		t.Fatalf("--approve: %+v, %v", approved, err)
	}
	refused, err := cli.ResolveConfig(cli.Args{SessionDir: agentDir, ProjectTrust: &no}, work, asked)
	if err != nil || refused.ProjectLoaded {
		t.Fatalf("--no-approve: %+v, %v", refused, err)
	}
	// Nothing was recorded, so with no flag and nobody to ask it is refused.
	later, err := cli.ResolveConfig(cli.Args{SessionDir: agentDir}, work, nil)
	if err != nil || later.ProjectLoaded {
		t.Fatalf("the flag outlived its run: %+v, %v", later, err)
	}
}

func userMessage(text string) ai.Message {
	return ai.Message{Role: ai.RoleUser, Content: text}
}
