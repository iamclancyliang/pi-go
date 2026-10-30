package interactive

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/session"
)

// fakeSelectors is the host behind the selectors, recording what was chosen.
type fakeSelectors struct {
	smu       sync.Mutex
	model     ai.ModelRef
	models    []ai.ModelRef
	scoped    []ai.ModelRef
	refresh   ModelRefresh
	selected  []ai.ModelRef
	scopes    [][]string
	saved     [][]string
	sessions  []session.Info
	resumed   []string
	renamed   map[string]string
	deleted   []string
	nodes     []session.Node
	leaf      string
	labels    map[string]string
	navigated []navigation
	navErr    error
	skip      bool
	copied    []string
	forks     []string
	forkText  string
	settings  SettingsValues
	changed   map[string]string
	providers []AuthProvider
	stored    []AuthProvider
	creds     map[string]string
	loggedOut []string
	trustCwd  string
	trustOpts []TrustOption
	trusted   bool
}

type navigation struct {
	id           string
	summarize    bool
	instructions string
}

func (f *fakeSelectors) CurrentModel() ai.ModelRef       { return f.model }
func (f *fakeSelectors) ConfiguredModels() []ai.ModelRef { return f.models }
func (f *fakeSelectors) ScopedModels() []ai.ModelRef     { return f.scoped }
func (f *fakeSelectors) ModelRefresh() ModelRefresh      { return f.refresh }
func (f *fakeSelectors) SelectModel(ref ai.ModelRef) error {
	f.model = ref
	f.selected = append(f.selected, ref)
	return nil
}
func (f *fakeSelectors) EnabledModels() []string {
	if len(f.scoped) == 0 {
		return nil
	}
	var ids []string
	for _, ref := range f.scoped {
		ids = append(ids, fullModelID(ref))
	}
	return ids
}
func (f *fakeSelectors) SetScopedModels(enabled []string) { f.scopes = append(f.scopes, enabled) }
func (f *fakeSelectors) SaveEnabledModels(enabled []string) error {
	f.saved = append(f.saved, enabled)
	return nil
}
func (f *fakeSelectors) CycleModel(forward bool) (ai.ModelRef, bool, error) {
	if len(f.models) <= 1 {
		return ai.ModelRef{}, false, nil
	}
	f.model = f.models[1]
	return f.model, true, nil
}
func (f *fakeSelectors) CycleThinking() (string, bool, error) { return "high", true, nil }

func (f *fakeSelectors) SessionLoaders() (SessionLoader, SessionLoader) {
	load := func(onProgress func(int, int)) ([]session.Info, error) {
		onProgress(len(f.sessions), len(f.sessions))
		return f.sessions, nil
	}
	return load, load
}
func (f *fakeSelectors) CurrentSessionPath() string { return "/sessions/current.jsonl" }
func (f *fakeSelectors) ResumeSession(path string) (string, error) {
	f.smu.Lock()
	defer f.smu.Unlock()
	f.resumed = append(f.resumed, path)
	return "", nil
}
func (f *fakeSelectors) RenameSession(path, name string) error {
	f.smu.Lock()
	defer f.smu.Unlock()
	if f.renamed == nil {
		f.renamed = map[string]string{}
	}
	f.renamed[path] = name
	return nil
}
func (f *fakeSelectors) DeleteSession(path string) (bool, error) {
	f.smu.Lock()
	defer f.smu.Unlock()
	f.deleted = append(f.deleted, path)
	return true, nil
}

func (f *fakeSelectors) TreeNodes() ([]session.Node, string, error) { return f.nodes, f.leaf, nil }
func (f *fakeSelectors) SetEntryLabel(id, label string) error {
	if f.labels == nil {
		f.labels = map[string]string{}
	}
	f.labels[id] = label
	return nil
}
func (f *fakeSelectors) NavigateTree(ctx context.Context, id string, summarize bool, instructions string) (string, error) {
	f.smu.Lock()
	defer f.smu.Unlock()
	if f.navErr != nil {
		return "", f.navErr
	}
	if summarize {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	f.navigated = append(f.navigated, navigation{id, summarize, instructions})
	return "restored words", nil
}
func (f *fakeSelectors) BranchSummarySkipPrompt() bool { return f.skip }
func (f *fakeSelectors) CopyToClipboard(text string) error {
	f.smu.Lock()
	defer f.smu.Unlock()
	f.copied = append(f.copied, text)
	return nil
}
func (f *fakeSelectors) ForkMessages() ([]ForkMessage, error) {
	var out []ForkMessage
	for _, n := range f.nodes {
		if m := n.Entry.Message; m != nil && m.Role == ai.RoleUser {
			out = append(out, ForkMessage{ID: n.ID, Text: m.Content})
		}
	}
	return out, nil
}
func (f *fakeSelectors) Fork(id string) (string, error) {
	f.smu.Lock()
	defer f.smu.Unlock()
	f.forks = append(f.forks, id)
	return f.forkText, nil
}
func (f *fakeSelectors) SettingsValues() SettingsValues { return f.settings }
func (f *fakeSelectors) ChangeSetting(id, value string) error {
	if f.changed == nil {
		f.changed = map[string]string{}
	}
	f.changed[id] = value
	return nil
}
func (f *fakeSelectors) LoginProviders() []AuthProvider          { return f.providers }
func (f *fakeSelectors) SavedProviders() ([]AuthProvider, error) { return f.stored, nil }
func (f *fakeSelectors) SaveCredential(provider, key string) (string, error) {
	if f.creds == nil {
		f.creds = map[string]string{}
	}
	f.creds[provider] = key
	return "/home/tester/auth.json", nil
}
func (f *fakeSelectors) Logout(provider string) (string, error) {
	f.loggedOut = append(f.loggedOut, provider)
	return "Removed stored API key for " + provider + ".", nil
}
func (f *fakeSelectors) TrustState() (string, *TrustSaved, bool) { return f.trustCwd, nil, f.trusted }
func (f *fakeSelectors) SaveTrust(option TrustOption) error {
	f.trustOpts = append(f.trustOpts, option)
	return nil
}

func userNode(id, parent, text string) session.Node {
	return session.Node{ID: id, ParentID: parent, Kind: "message", Entry: session.Entry{Message: &ai.Message{Role: ai.RoleUser, Content: text}}}
}

func assistantNode(id, parent, text string) session.Node {
	return session.Node{ID: id, ParentID: parent, Kind: "message", Entry: session.Entry{Message: &ai.Message{Role: ai.RoleAssistant, Content: text}}}
}

func TestTheModelSelectorSwitchesToWhatIsChosen(t *testing.T) {
	r := startMode(t, Options{})
	r.host.model = ai.ModelRef{Provider: "deepseek", ID: "deepseek-chat"}
	r.host.models = []ai.ModelRef{r.host.model, {Provider: "openai", ID: "gpt-5"}, {Provider: "ollama", ID: "llama"}}
	r.typeLine("/model")
	got := r.screen()
	if !strings.Contains(got, "deepseek-chat [deepseek] ✓") || !strings.Contains(got, "Only showing models from configured providers") {
		t.Fatalf("the selector:\n%s", got)
	}
	r.term.send("gpt")
	r.idle()
	if got := r.screen(); !strings.Contains(got, "→ gpt-5 [openai]") || strings.Contains(got, "llama") {
		t.Fatalf("searched:\n%s", got)
	}
	r.term.send("\r")
	r.idle()
	if len(r.host.selected) != 1 || r.host.selected[0].ID != "gpt-5" {
		t.Fatalf("selected %v", r.host.selected)
	}
	if got := r.screen(); !strings.Contains(got, "Model: gpt-5") || strings.Contains(got, "Only showing") {
		t.Fatalf("after choosing:\n%s", got)
	}
}

func TestEscapeLeavesASelectorWithTheEditorBack(t *testing.T) {
	r := startMode(t, Options{})
	r.host.models = []ai.ModelRef{{Provider: "a", ID: "m"}}
	r.term.send("draft")
	r.idle()
	r.typeLine("/model")
	r.term.send("\x1b")
	r.idle()
	time.Sleep(50 * time.Millisecond)
	if got := r.screen(); strings.Contains(got, "Only showing") || len(r.host.selected) != 0 {
		t.Fatalf("escape did not close the selector:\n%s", got)
	}
	r.term.send("typed")
	r.idle()
	if got := r.screen(); !strings.Contains(got, "typed") {
		t.Fatalf("the editor did not get the keys back:\n%s", got)
	}
}

func TestTheModelSelectorRefreshesInTheBackground(t *testing.T) {
	r := startMode(t, Options{})
	r.host.model = ai.ModelRef{Provider: "ollama", ID: "llama"}
	r.host.models = []ai.ModelRef{r.host.model}
	r.host.refresh = func(ctx context.Context) ([]ai.ModelRef, []string, error) {
		return []ai.ModelRef{r.host.model, {Provider: "ollama", ID: "pulled-later"}}, nil, nil
	}
	r.typeLine("/model")
	waitFor(t, "the refresh", func() bool { return strings.Contains(r.screen(), "Model catalogs refreshed.") })
	if got := r.screen(); !strings.Contains(got, "pulled-later") {
		t.Fatalf("the refreshed list:\n%s", got)
	}
}

func TestASelectorWaitsForTheRun(t *testing.T) {
	r := startMode(t, Options{})
	r.typeLine("go")
	waitFor(t, "the run to start", func() bool { return r.host.currentRun() != nil })
	r.idle()
	r.term.send("\x0c") // ctrl+l: the model selector
	r.idle()
	if got := r.screen(); !strings.Contains(got, "Wait for the agent to finish") || strings.Contains(got, "Only showing") {
		t.Fatalf("during a run:\n%s", got)
	}
	r.host.currentRun().done <- nil
}

func TestTheScopeSelectorChangesCyclingAndSavesOnRequest(t *testing.T) {
	r := startMode(t, Options{})
	r.host.models = []ai.ModelRef{{Provider: "a", ID: "one"}, {Provider: "a", ID: "two"}, {Provider: "b", ID: "three"}}
	r.typeLine("/scoped-models")
	if got := r.screen(); !strings.Contains(got, "Model Configuration") || !strings.Contains(got, "all enabled") {
		t.Fatalf("the selector:\n%s", got)
	}
	r.term.send("\r") // toggle the first: only it stays enabled
	r.idle()
	if len(r.host.scopes) != 1 || strings.Join(r.host.scopes[0], ",") != "a/one" {
		t.Fatalf("scoped %v", r.host.scopes)
	}
	if got := r.screen(); !strings.Contains(got, "1/3 enabled") || !strings.Contains(got, "(unsaved)") {
		t.Fatalf("after a toggle:\n%s", got)
	}
	r.term.send("\x10") // ctrl+p: the whole provider
	r.idle()
	if last := r.host.scopes[len(r.host.scopes)-1]; strings.Join(last, ",") != "a/one,a/two" {
		t.Fatalf("the provider toggle gave %v", last)
	}
	r.term.send("\x13") // ctrl+s: save
	r.idle()
	if len(r.host.saved) != 1 || strings.Join(r.host.saved[0], ",") != "a/one,a/two" {
		t.Fatalf("saved %v", r.host.saved)
	}
	if got := r.screen(); strings.Contains(got, "(unsaved)") {
		t.Fatalf("still unsaved after saving:\n%s", got)
	}
	r.term.send("\x01") // ctrl+a: everything again, which is no scope
	r.idle()
	if last := r.host.scopes[len(r.host.scopes)-1]; last != nil {
		t.Fatalf("enabling all gave %v, not nil", last)
	}
}

func TestTheSessionSelectorResumesRenamesAndDeletes(t *testing.T) {
	r := startMode(t, Options{})
	now := time.Now()
	r.host.sessions = []session.Info{
		{Path: "/sessions/b.jsonl", ID: "b", Dir: "/work", Opening: "second talk", MessageCount: 4, Activity: now.Add(-2 * time.Hour)},
		{Path: "/sessions/a.jsonl", ID: "a", Dir: "/work", Name: "named one", MessageCount: 2, Activity: now.Add(-3 * 24 * time.Hour)},
	}
	r.typeLine("/resume")
	waitFor(t, "the list", func() bool { return strings.Contains(r.screen(), "second talk") })
	got := r.screen()
	// The title gives way to the scope and sort at this width, as Pi's does.
	for _, want := range []string{"Resume Session (Current", "◉ Current Folder", "named one", "4 2h", "2 3d"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	r.term.send("\x12") // ctrl+r: rename the selected (first) one
	r.idle()
	if got := r.screen(); !strings.Contains(got, "Rename Session") {
		t.Fatalf("rename:\n%s", got)
	}
	r.term.send(" renamed")
	r.term.send("\r")
	waitFor(t, "the rename", func() bool {
		r.host.smu.Lock()
		defer r.host.smu.Unlock()
		return r.host.renamed["/sessions/b.jsonl"] == "renamed"
	})
	r.idle()
	r.term.send("\x1b[B") // down to the second
	r.term.send("\x04")   // ctrl+d: delete, with confirmation
	r.idle()
	if got := r.screen(); !strings.Contains(got, "Delete session?") {
		t.Fatalf("no confirmation:\n%s", got)
	}
	r.term.send("\r")
	waitFor(t, "the delete", func() bool {
		r.host.smu.Lock()
		defer r.host.smu.Unlock()
		return len(r.host.deleted) == 1 && r.host.deleted[0] == "/sessions/a.jsonl"
	})
	r.idle()
	r.term.send("\r") // resume what is selected
	waitFor(t, "the resume", func() bool {
		r.host.smu.Lock()
		defer r.host.smu.Unlock()
		return len(r.host.resumed) == 1
	})
	waitFor(t, "the status", func() bool { return strings.Contains(r.screen(), "Resumed session") })
}

func TestTheSessionSelectorSearchesAndRefusesToDeleteTheCurrentOne(t *testing.T) {
	r := startMode(t, Options{})
	now := time.Now()
	r.host.sessions = []session.Info{
		{Path: "/sessions/current.jsonl", ID: "c", Opening: "this one", AllText: "this one", MessageCount: 1, Activity: now},
		{Path: "/sessions/other.jsonl", ID: "o", Opening: "parser bug", AllText: "parser bug fix", MessageCount: 1, Activity: now.Add(-time.Minute)},
	}
	r.typeLine("/resume")
	waitFor(t, "the list", func() bool { return strings.Contains(r.screen(), "parser bug") })
	r.term.send("\x04")
	r.idle()
	if got := r.screen(); !strings.Contains(got, "Cannot delete the currently active session") {
		t.Fatalf("deleting the current session:\n%s", got)
	}
	r.term.send("parser")
	r.idle()
	if got := r.screen(); strings.Contains(got, "this one") || !strings.Contains(got, "parser bug") {
		t.Fatalf("searched:\n%s", got)
	}
}

func treeHost(h *fakeHost) {
	h.nodes = []session.Node{
		userNode("n1", "", "first question"),
		assistantNode("n2", "n1", "first answer"),
		userNode("n3", "n2", "second question"),
		assistantNode("n4", "n3", "second answer"),
	}
	h.leaf = "n4"
}

func TestTheTreeSelectorNavigatesAfterAskingAboutASummary(t *testing.T) {
	r := startMode(t, Options{})
	treeHost(r.host)
	r.typeLine("/tree")
	got := r.screen()
	if !strings.Contains(got, "Session Tree") || !strings.Contains(got, "› • assistant: second answer") {
		t.Fatalf("the tree:\n%s", got)
	}
	r.term.send("\x1b[A") // up to n3
	r.term.send("\x1b[A") // up to n2
	r.term.send("\r")
	r.idle()
	if got := r.screen(); !strings.Contains(got, "Summarize branch?") {
		t.Fatalf("no question:\n%s", got)
	}
	r.term.send("\x1b[B") // Summarize
	r.term.send("\r")
	waitFor(t, "the spinner", func() bool { return strings.Contains(r.screen(), "Summarizing branch...") })
	waitFor(t, "the navigation", func() bool {
		r.host.smu.Lock()
		defer r.host.smu.Unlock()
		return len(r.host.navigated) == 1
	})
	r.idle()
	if nav := r.host.navigated[0]; nav.id != "n2" || !nav.summarize {
		t.Fatalf("navigated %+v", nav)
	}
	if got := r.screen(); !strings.Contains(got, "Navigated to selected point") || !strings.Contains(got, "restored words") {
		t.Fatalf("after navigating:\n%s", got)
	}
}

func TestEscapeInTheSummaryQuestionGoesBackToTheTree(t *testing.T) {
	r := startMode(t, Options{})
	treeHost(r.host)
	r.typeLine("/tree")
	r.term.send("\x1b[A")
	r.term.send("\r")
	r.idle()
	r.term.send("\x1b")
	r.idle()
	time.Sleep(50 * time.Millisecond)
	if got := r.screen(); !strings.Contains(got, "Session Tree") || !strings.Contains(got, "› • user: second question") {
		t.Fatalf("not back on the same entry:\n%s", got)
	}
}

func TestEscapeCancelsABranchSummary(t *testing.T) {
	r := startMode(t, Options{})
	treeHost(r.host)
	r.host.skip = false
	r.typeLine("/tree")
	r.term.send("\x1b[A")
	r.term.send("\r")
	r.idle()
	r.host.smu.Lock()
	r.host.navErr = nil
	r.host.smu.Unlock()
	r.term.send("\x1b[B")
	r.term.send("\r")
	waitFor(t, "the spinner", func() bool { return strings.Contains(r.screen(), "Summarizing branch...") })
	r.term.send("\x1b")
	waitFor(t, "the cancellation", func() bool { return strings.Contains(r.screen(), "Branch summarization cancelled") })
	if got := r.screen(); !strings.Contains(got, "Session Tree") {
		t.Fatalf("not back in the tree:\n%s", got)
	}
}

func TestTheTreeSelectorLabelsFiltersAndCopies(t *testing.T) {
	r := startMode(t, Options{})
	treeHost(r.host)
	r.typeLine("/tree")
	r.term.send("L") // shift+l: label
	r.idle()
	if got := r.screen(); !strings.Contains(got, "Label (empty to remove):") {
		t.Fatalf("label input:\n%s", got)
	}
	r.term.send("done here")
	r.term.send("\r")
	r.idle()
	if r.host.labels["n4"] != "done here" {
		t.Fatalf("labels %v", r.host.labels)
	}
	if got := r.screen(); !strings.Contains(got, "[done here]") {
		t.Fatalf("the label is not shown:\n%s", got)
	}
	r.term.send("\x15") // ctrl+u: only user messages
	r.idle()
	got := r.screen()
	if strings.Contains(got, "assistant:") || !strings.Contains(got, "[user]") {
		t.Fatalf("the user filter:\n%s", got)
	}
	r.term.send("\x18") // ctrl+x: copy
	waitFor(t, "the copy", func() bool {
		r.host.smu.Lock()
		defer r.host.smu.Unlock()
		return len(r.host.copied) == 1 && r.host.copied[0] == "second question"
	})
	r.term.send("first")
	r.idle()
	if got := r.screen(); strings.Contains(got, "second question") || !strings.Contains(got, "Type to search: first") {
		t.Fatalf("searched:\n%s", got)
	}
}

func TestDoubleEscapeOpensTheTree(t *testing.T) {
	r := startMode(t, Options{DoubleEscapeAction: "tree"})
	treeHost(r.host)
	r.term.send("\x1b")
	r.idle()
	r.term.send("\x1b")
	r.idle()
	if got := r.screen(); !strings.Contains(got, "Session Tree") {
		t.Fatalf("two escapes:\n%s", got)
	}
}

func TestTheForkSelectorForksBeforeAMessage(t *testing.T) {
	r := startMode(t, Options{})
	treeHost(r.host)
	r.host.forkText = "second question"
	r.typeLine("/fork")
	got := r.screen()
	if !strings.Contains(got, "Fork from Message") || !strings.Contains(got, "› second question") || !strings.Contains(got, "Message 2 of 2") {
		t.Fatalf("the fork list:\n%s", got)
	}
	r.term.send("\r")
	waitFor(t, "the fork", func() bool {
		r.host.smu.Lock()
		defer r.host.smu.Unlock()
		return len(r.host.forks) == 1 && r.host.forks[0] == "n3"
	})
	waitFor(t, "the status", func() bool { return strings.Contains(r.screen(), "Forked to new session") })
	if got := r.screen(); !strings.Contains(got, "second question") {
		t.Fatalf("the words were not put back:\n%s", got)
	}
}

func TestTheSettingsSelectorChangesAndAppliesSettings(t *testing.T) {
	r := startMode(t, Options{})
	r.host.settings = SettingsValues{AutoCompact: true, SteeringMode: "one-at-a-time", FollowUpMode: "one-at-a-time",
		Thinking: "off", AvailableThinking: []string{"off", "low", "high"}, DefaultProjectTrust: "ask",
		DoubleEscapeAction: "tree", TreeFilterMode: "default", OutputPad: 1, AutocompleteMaxVisible: 5}
	r.typeLine("/settings")
	if got := r.screen(); !strings.Contains(got, "Auto-compact") || !strings.Contains(got, "Type to search") {
		t.Fatalf("the settings:\n%s", got)
	}
	r.term.send("\r") // cycle auto-compact to false
	r.idle()
	if r.host.changed[SettingAutoCompact] != "false" {
		t.Fatalf("changed %v", r.host.changed)
	}
	r.term.send("thinking")
	r.idle()
	r.term.send("\r") // open the submenu
	r.idle()
	if got := r.screen(); !strings.Contains(got, "Thinking Level") || !strings.Contains(got, "Light reasoning") {
		t.Fatalf("the submenu:\n%s", got)
	}
	r.term.send("\x1b[B")
	r.term.send("\r")
	r.idle()
	if r.host.changed[SettingThinking] != "low" {
		t.Fatalf("changed %v", r.host.changed)
	}
	if got := r.screen(); strings.Contains(got, "Light reasoning") || !strings.Contains(got, "low") {
		t.Fatalf("after the submenu:\n%s", got)
	}
	r.term.send("\x1b")
	r.idle()
	if got := r.screen(); strings.Contains(got, "Type to search") {
		t.Fatalf("escape did not close it:\n%s", got)
	}
}

func TestLoginAsksForAKeyWithoutShowingIt(t *testing.T) {
	r := startMode(t, Options{})
	r.host.providers = []AuthProvider{{ID: "deepseek", Name: "deepseek"}, {ID: "openai", Name: "openai", Source: "OPENAI_API_KEY"}}
	r.typeLine("/login")
	if got := r.screen(); !strings.Contains(got, "Select provider to configure:") || !strings.Contains(got, "✓ env: OPENAI_API_KEY") {
		t.Fatalf("the providers:\n%s", got)
	}
	r.term.send("\r")
	r.idle()
	if got := r.screen(); !strings.Contains(got, "Login to deepseek") || !strings.Contains(got, "Enter API key") {
		t.Fatalf("the dialog:\n%s", got)
	}
	r.term.send("sk-secret")
	r.idle()
	if got := r.screen(); strings.Contains(got, "sk-secret") || !strings.Contains(got, "*********") {
		t.Fatalf("the key is shown:\n%s", got)
	}
	r.term.send("\r")
	r.idle()
	if r.host.creds["deepseek"] != "sk-secret" {
		t.Fatalf("saved %v", r.host.creds)
	}
	if got := r.screen(); !strings.Contains(got, "Saved API key for deepseek") || strings.Contains(got, "sk-secret") {
		t.Fatalf("after saving:\n%s", got)
	}
}

func TestLoginWithANameSkipsTheList(t *testing.T) {
	r := startMode(t, Options{})
	r.host.providers = []AuthProvider{{ID: "deepseek", Name: "deepseek"}}
	r.typeLine("/login deepseek")
	if got := r.screen(); !strings.Contains(got, "Login to deepseek") {
		t.Fatalf("the dialog:\n%s", got)
	}
}

func TestLogoutListsWhatIsStored(t *testing.T) {
	r := startMode(t, Options{})
	r.typeLine("/logout")
	if got := r.screen(); !strings.Contains(got, "No stored credentials to remove") {
		t.Fatalf("nothing stored:\n%s", got)
	}
	r.host.stored = []AuthProvider{{ID: "deepseek", Name: "deepseek", Source: "stored credential"}}
	r.typeLine("/logout")
	if got := r.screen(); !strings.Contains(got, "Select provider to logout:") || !strings.Contains(got, "✓ configured") {
		t.Fatalf("the list:\n%s", got)
	}
	r.term.send("\r")
	r.idle()
	if len(r.host.loggedOut) != 1 || r.host.loggedOut[0] != "deepseek" {
		t.Fatalf("logged out %v", r.host.loggedOut)
	}
}

func TestTrustSavesTheChoice(t *testing.T) {
	r := startMode(t, Options{})
	r.host.trustCwd = "/home/tester/work/project"
	r.typeLine("/trust")
	got := r.screen()
	for _, want := range []string{"Project trust", "Saved decision: none", "Current session: untrusted", "Trust parent folder (/home/tester/work)", "Do not trust"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	r.term.send("\x1b[B")
	r.term.send("\r")
	r.idle()
	if len(r.host.trustOpts) != 1 || r.host.trustOpts[0].SavedPath != "/home/tester/work" || !r.host.trustOpts[0].Trusted {
		t.Fatalf("saved %+v", r.host.trustOpts)
	}
	if got := r.screen(); !strings.Contains(got, "Saved trust decision: trusted") {
		t.Fatalf("after saving:\n%s", got)
	}
}

func TestTheModelAndThinkingKeysCycle(t *testing.T) {
	r := startMode(t, Options{})
	r.host.models = []ai.ModelRef{{Provider: "a", ID: "one"}, {Provider: "a", ID: "two"}}
	r.term.send("\x10") // ctrl+p
	r.idle()
	if got := r.screen(); !strings.Contains(got, "Switched to two") {
		t.Fatalf("cycling:\n%s", got)
	}
	r.term.send("\x1b[Z") // shift+tab
	r.idle()
	if got := r.screen(); !strings.Contains(got, "Thinking level: high") {
		t.Fatalf("thinking:\n%s", got)
	}
}

func TestANavigationFailureIsShown(t *testing.T) {
	r := startMode(t, Options{})
	treeHost(r.host)
	r.host.skip = true
	r.host.navErr = errors.New("the file is gone")
	r.typeLine("/tree")
	r.term.send("\x1b[A")
	r.term.send("\r")
	waitFor(t, "the error", func() bool { return strings.Contains(r.screen(), "Error: the file is gone") })
}
