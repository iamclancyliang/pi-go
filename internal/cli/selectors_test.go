package cli

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/interactive"
	"github.com/iamclancyliang/pi-go/internal/session"
	"github.com/iamclancyliang/pi-go/internal/settings"
	"github.com/iamclancyliang/pi-go/internal/tools"
	"github.com/iamclancyliang/pi-go/internal/trust"
)

// hostFor is a full-screen host over a recorded conversation with one exchange,
// answering with a scripted model.
func hostFor(t *testing.T, model ai.Port) (*fullScreenHost, *Conversation) {
	t.Helper()
	agentDir, work := t.TempDir(), t.TempDir()
	args := Args{SessionDir: agentDir}
	conversation, err := OpenConversation(args, work, DefaultSystemPrompt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conversation.Close() })
	if err := conversation.Session.AppendAll(
		ai.Message{Role: ai.RoleUser, Content: "first question"},
		ai.Message{Role: ai.RoleAssistant, Content: "first answer"},
		ai.Message{Role: ai.RoleUser, Content: "second question"},
		ai.Message{Role: ai.RoleAssistant, Content: "second answer"},
	); err != nil {
		t.Fatal(err)
	}
	registry, err := tools.NewBuiltInRegistry(work)
	if err != nil {
		t.Fatal(err)
	}
	rt := Runtime{
		Model: model, ModelName: "m", Provider: "scripted", Tools: registry, System: DefaultSystemPrompt,
		Conversation: conversation, Args: args, WorkingDir: work,
		Config: Config{AgentDir: agentDir, Global: settings.Settings{}, Effective: settings.Settings{}},
		OpenModel: func(a Args, _ http.RoundTripper) (ai.Port, string, string, error) {
			return model, "scripted", a.Model, nil
		},
	}
	s, err := newInteractiveSession(context.Background(), rt, Streams{Out: &strings.Builder{}, Err: &strings.Builder{}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.close)
	return &fullScreenHost{s: s}, conversation
}

func TestNavigatingTheTreeWritesASummaryWhereTheModelSeesIt(t *testing.T) {
	model := &ai.Scripted{Name: "m", Final: ai.AssistantText("## Goal\nthe second question")}
	h, _ := hostFor(t, model)
	nodes, leaf, err := h.TreeNodes()
	if err != nil || leaf != nodes[len(nodes)-1].ID {
		t.Fatalf("tree %v %s %v", nodes, leaf, err)
	}
	// The record also holds the model the run uses, after the messages.
	find := func(text string) string {
		for _, n := range nodes {
			if m := n.Entry.Message; m != nil && m.Content == text {
				return n.ID
			}
		}
		t.Fatalf("no entry says %q", text)
		return ""
	}
	text, err := h.NavigateTree(context.Background(), find("second question"), true, "what mattered")
	if err != nil {
		t.Fatal(err)
	}
	if text != "second question" {
		t.Fatalf("the editor gets %q", text)
	}
	messages := h.s.current.Session.Snapshot().Messages
	if len(messages) != 3 {
		t.Fatalf("the conversation now has %d messages", len(messages))
	}
	summary, ok := session.BranchSummaryOf(messages[2])
	if !ok || !strings.Contains(summary, "the second question") {
		t.Fatalf("the last message is %+v", messages[2])
	}
	prompt := model.Requests()[0].Messages[1].Content
	if !strings.Contains(prompt, "second answer") || !strings.HasSuffix(prompt, "Additional focus: what mattered") {
		t.Fatalf("the summariser was asked:\n%s", prompt)
	}
	if _, _, err := h.TreeNodes(); err != nil {
		t.Fatal(err)
	}
	// Back to the start, without a summary: nothing is written, and the first
	// question goes back to the editor.
	text, err = h.NavigateTree(context.Background(), find("first question"), false, "")
	if err != nil || text != "first question" {
		t.Fatalf("to the root: %q %v", text, err)
	}
	if n := len(h.s.current.Session.Snapshot().Messages); n != 0 {
		t.Fatalf("at the root the model would see %d messages", n)
	}
	if len(model.Requests()) != 1 {
		t.Fatal("moving without a summary asked the model something")
	}
}

func TestACancelledSummaryMovesNothing(t *testing.T) {
	model := &ai.Scripted{Name: "m", Final: ai.AssistantText("never")}
	h, _ := hostFor(t, model)
	nodes, before, _ := h.TreeNodes()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.NavigateTree(ctx, nodes[1].ID, true, ""); err == nil {
		t.Fatal("a cancelled navigation succeeded")
	}
	if _, leaf, _ := h.TreeNodes(); leaf != before {
		t.Fatalf("the conversation moved to %s", leaf)
	}
}

func TestForkingFromTheSelectorCopiesUpToTheMessage(t *testing.T) {
	h, before := hostFor(t, &ai.Scripted{Name: "m", Final: ai.AssistantText("x")})
	messages, err := h.ForkMessages()
	if err != nil || len(messages) != 2 {
		t.Fatalf("fork points %v %v", messages, err)
	}
	text, err := h.Fork(messages[1].ID)
	if err != nil || text != "second question" {
		t.Fatalf("forked %q %v", text, err)
	}
	if h.s.current.Path == before.Path || len(h.s.current.Session.Snapshot().Messages) != 2 {
		t.Fatalf("the fork is %+v", h.s.current)
	}
	// Before the first message there is nothing to copy: an empty conversation
	// that still knows where it came from. The copy's entries have ids of their
	// own, so they are listed again.
	messages, _ = h.ForkMessages()
	text, err = h.Fork(messages[0].ID)
	if err != nil || text != "first question" || len(h.s.current.Session.Snapshot().Messages) != 0 {
		t.Fatalf("forked at the start: %q %v %d", text, err, len(h.s.current.Session.Snapshot().Messages))
	}
	if h.s.current.Store.ParentSession() == "" {
		t.Fatal("the empty fork does not say where it came from")
	}
}

func TestSettingsChangedInTheSelectorAreSavedAndInEffect(t *testing.T) {
	h, _ := hostFor(t, &ai.Scripted{Name: "m", Final: ai.AssistantText("x")})
	for id, value := range map[string]string{
		interactive.SettingAutoCompact:   "false",
		interactive.SettingSteeringMode:  "all",
		interactive.SettingHideThinking:  "true",
		interactive.SettingEditorPadding: "2",
		interactive.SettingOutputPadding: "0",
		interactive.SettingDoubleEscape:  "fork",
		interactive.SettingTreeFilter:    "no-tools",
		interactive.SettingProjectTrust:  "never",
	} {
		if err := h.ChangeSetting(id, value); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
	}
	saved, err := settings.Load(settings.GlobalPath(h.s.config.AgentDir))
	if err != nil {
		t.Fatal(err)
	}
	if saved.Compaction.Enabled == nil || *saved.Compaction.Enabled || saved.SteeringMode != "all" || !saved.HideThinkingBlock ||
		*saved.EditorPaddingX != 2 || *saved.OutputPad != 0 || saved.DoubleEscapeAction != "fork" ||
		saved.TreeFilterMode != "no-tools" || saved.DefaultProjectTrust != settings.TrustNever {
		t.Fatalf("saved %+v", saved)
	}
	values := h.SettingsValues()
	if values.AutoCompact || values.SteeringMode != "all" || !values.HideThinking || values.EditorPaddingX != 2 ||
		values.OutputPad != 0 || values.DoubleEscapeAction != "fork" || values.TreeFilterMode != "no-tools" {
		t.Fatalf("in effect %+v", values)
	}
	if h.s.agent.SteeringMode() != "all" || h.s.rt.AutoCompaction.Enabled {
		t.Fatal("the running agent did not take the change")
	}
	if err := h.ChangeSetting("no-such", "x"); err == nil {
		t.Fatal("an unknown setting was accepted")
	}
}

func TestTrustFromTheSelectorIsRecordedForTheRightDirectory(t *testing.T) {
	h, _ := hostFor(t, &ai.Scripted{Name: "m", Final: ai.AssistantText("x")})
	cwd, saved, trusted := h.TrustState()
	if saved != nil || trusted || cwd != trust.Canonical(h.s.rt.WorkingDir) {
		t.Fatalf("state %s %v %v", cwd, saved, trusted)
	}
	options := interactive.TrustOptions(cwd)
	if err := h.SaveTrust(options[1]); err != nil {
		t.Fatal(err)
	}
	_, saved, _ = h.TrustState()
	if saved == nil || !saved.Decision || saved.Path != filepath.Dir(cwd) {
		t.Fatalf("after trusting the parent: %+v", saved)
	}
	if err := h.SaveTrust(options[len(options)-1]); err != nil {
		t.Fatal(err)
	}
	_, saved, _ = h.TrustState()
	if saved == nil || saved.Decision || saved.Path != cwd {
		t.Fatalf("after refusing: %+v", saved)
	}
}

// clearEnvCredentials leaves no provider configured by the environment.
func clearEnvCredentials(t *testing.T) {
	t.Helper()
	for _, name := range ProviderNames() {
		for _, v := range Providers[name].EnvVars {
			t.Setenv(v, "")
		}
	}
	t.Setenv("PI_OFFLINE", "")
}

func TestCredentialsSavedFromTheDialogAreListedAndForgotten(t *testing.T) {
	clearEnvCredentials(t)
	t.Setenv("OPENAI_API_KEY", "env-key")
	h, _ := hostFor(t, &ai.Scripted{Name: "m", Final: ai.AssistantText("x")})
	providers := h.LoginProviders()
	sources := map[string]string{}
	for _, p := range providers {
		sources[p.ID] = p.Source
	}
	if sources["openai"] != "OPENAI_API_KEY" || sources["deepseek"] != "" {
		t.Fatalf("sources %v", sources)
	}
	where, err := h.SaveCredential("deepseek", "sk-1")
	if err != nil || !strings.HasPrefix(where, h.s.config.AgentDir) {
		t.Fatalf("saved at %q, %v", where, err)
	}
	if _, err := h.SaveCredential("nowhere", "k"); err == nil {
		t.Fatal("an unknown provider was accepted")
	}
	stored, err := h.SavedProviders()
	if err != nil || len(stored) != 1 || stored[0].ID != "deepseek" || stored[0].Source != "stored credential" {
		t.Fatalf("stored %v %v", stored, err)
	}
	if h.providers != 2 {
		t.Fatalf("%d providers counted after a login", h.providers)
	}
	message, err := h.Logout("deepseek")
	if err != nil || !strings.Contains(message, "Removed stored API key for deepseek") {
		t.Fatalf("logout: %q %v", message, err)
	}
	if stored, _ := h.SavedProviders(); len(stored) != 0 {
		t.Fatalf("still stored: %v", stored)
	}
	if _, err := os.Stat(where); err != nil {
		t.Fatalf("the store file: %v", err)
	}
}

func TestConfiguredModelsAndTheScope(t *testing.T) {
	clearEnvCredentials(t)
	t.Setenv("DEEPSEEK_API_KEY", "k")
	h, _ := hostFor(t, &ai.Scripted{Name: "m", Final: ai.AssistantText("x")})
	models := h.ConfiguredModels()
	if models[0] != (ai.ModelRef{Provider: "scripted", ID: "m"}) {
		t.Fatalf("the current model is not first: %v", models)
	}
	var ids []string
	for _, m := range models {
		ids = append(ids, m.Provider+"/"+m.ID)
	}
	if !strings.Contains(strings.Join(ids, " "), "deepseek/deepseek-chat") || strings.Contains(strings.Join(ids, " "), "openai/") {
		t.Fatalf("configured %v", ids)
	}
	if h.EnabledModels() != nil {
		t.Fatalf("a scope with nothing in it: %v", h.EnabledModels())
	}
	h.SetScopedModels([]string{"deepseek/deepseek-chat", "scripted/m"})
	if got := h.EnabledModels(); strings.Join(got, ",") != "deepseek/deepseek-chat,scripted/m" {
		t.Fatalf("scoped %v", got)
	}
	next, ok, err := h.CycleModel(true)
	if err != nil || !ok || next.ID != "deepseek-chat" {
		t.Fatalf("cycled to %v %v %v", next, ok, err)
	}
	if err := h.SaveEnabledModels([]string{"deepseek/deepseek-chat"}); err != nil {
		t.Fatal(err)
	}
	saved, _ := settings.Load(settings.GlobalPath(h.s.config.AgentDir))
	if strings.Join(saved.EnabledModels, ",") != "deepseek/deepseek-chat" {
		t.Fatalf("saved %v", saved.EnabledModels)
	}
	h.SetScopedModels(nil)
	if h.EnabledModels() != nil {
		t.Fatal("lifting the scope left one")
	}
	if h.ModelRefresh() == nil {
		t.Fatal("no refresh with a credential configured")
	}
	h.s.rt.Args.Offline = true
	if h.ModelRefresh() != nil {
		t.Fatal("offline still refreshes")
	}
}

func TestSelectingAModelMakesItTheDefault(t *testing.T) {
	h, _ := hostFor(t, &ai.Scripted{Name: "m", Final: ai.AssistantText("x")})
	if err := h.SelectModel(ai.ModelRef{Provider: "scripted", ID: "other"}); err != nil {
		t.Fatal(err)
	}
	if h.CurrentModel().ID != "other" {
		t.Fatalf("answering with %v", h.CurrentModel())
	}
	saved, _ := settings.Load(settings.GlobalPath(h.s.config.AgentDir))
	if saved.DefaultProvider != "scripted" || saved.DefaultModel != "other" {
		t.Fatalf("saved %+v", saved)
	}
}

func TestCyclingThinkingRecordsAndRemembersTheLevel(t *testing.T) {
	h, _ := hostFor(t, &ai.Scripted{Name: "m", Final: ai.AssistantText("x")})
	level, ok, err := h.CycleThinking()
	if err != nil || !ok || level != "minimal" {
		t.Fatalf("cycled to %q %v %v", level, ok, err)
	}
	if recorded, ok := h.s.current.Session.RecordedThinking(); !ok || recorded != "minimal" {
		t.Fatalf("recorded %q %v", recorded, ok)
	}
	saved, _ := settings.Load(settings.GlobalPath(h.s.config.AgentDir))
	if saved.DefaultThinkingLevel != "minimal" {
		t.Fatalf("saved %+v", saved)
	}
	if h.SettingsValues().Thinking != "minimal" {
		t.Fatalf("values %+v", h.SettingsValues())
	}
}

func TestSessionsAreListedRenamedAndResumedFromTheSelector(t *testing.T) {
	// No trash command: a test's file must not land in the user's bin.
	t.Setenv("PATH", "")
	h, before := hostFor(t, &ai.Scripted{Name: "m", Final: ai.AssistantText("x")})
	other, err := OpenConversation(Args{SessionDir: h.s.config.AgentDir}, h.s.rt.WorkingDir, "s")
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Session.Append(ai.Message{Role: ai.RoleUser, Content: "elsewhere"}); err != nil {
		t.Fatal(err)
	}
	other.Close()

	current, all := h.SessionLoaders()
	here, err := current(nil)
	if err != nil || len(here) != 2 {
		t.Fatalf("here %v %v", here, err)
	}
	everywhere, err := all(func(int, int) {})
	if err != nil || len(everywhere) != 2 {
		t.Fatalf("everywhere %v %v", everywhere, err)
	}
	if err := h.RenameSession(other.Path, "the other"); err != nil {
		t.Fatal(err)
	}
	if err := h.RenameSession(before.Path, "this one"); err != nil {
		t.Fatal(err)
	}
	if before.Session.Name() != "this one" {
		t.Fatalf("the current session is called %q", before.Session.Name())
	}
	if h.CurrentSessionPath() != before.Path {
		t.Fatalf("current path %q", h.CurrentSessionPath())
	}
	note, err := h.ResumeSession(other.Path)
	if err != nil || note != "" {
		t.Fatalf("resumed: %q %v", note, err)
	}
	if h.s.current.Session.Name() != "the other" || len(h.s.current.Session.Snapshot().Messages) != 1 {
		t.Fatalf("resumed %+v", h.s.current.Session.Name())
	}
	trashed, err := h.DeleteSession(before.Path)
	if err != nil {
		t.Fatal(err)
	}
	_ = trashed
	if _, err := os.Stat(before.Path); err == nil {
		t.Fatal("the deleted session is still there")
	}
}
