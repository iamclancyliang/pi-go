package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/auth"
	"github.com/iamclancyliang/pi-go/internal/compaction"
	"github.com/iamclancyliang/pi-go/internal/interactive"
	"github.com/iamclancyliang/pi-go/internal/runtime"
	"github.com/iamclancyliang/pi-go/internal/session"
	"github.com/iamclancyliang/pi-go/internal/settings"
	"github.com/iamclancyliang/pi-go/internal/trust"
)

// The full-screen host behind the interface's selectors: what each lists, and what
// choosing does. See interactive.Selectors.

// --- models ---

func (h *fullScreenHost) CurrentModel() ai.ModelRef {
	_, _, provider, model, _ := h.s.snapshot()
	return ai.ModelRef{Provider: provider, ID: model}
}

// ConfiguredModels is what a credential here can reach, as far as anything records:
// each configured provider's default and the models with recorded facts, plus the
// one answering now. A provider's own listing joins it when a refresh asks.
func (h *fullScreenHost) ConfiguredModels() []ai.ModelRef {
	current := h.CurrentModel()
	refs := []ai.ModelRef{current}
	seen := map[ai.ModelRef]bool{current: true}
	add := func(ref ai.ModelRef) {
		if ref.ID != "" && !seen[ref] {
			seen[ref] = true
			refs = append(refs, ref)
		}
	}
	for _, name := range ProviderNames() {
		if !hasCredential(h.s.rt.Args, name) && name != current.Provider {
			continue
		}
		add(ai.ModelRef{Provider: name, ID: Providers[name].DefaultModel})
		for _, id := range ai.KnownModels(name) {
			add(ai.ModelRef{Provider: name, ID: id})
		}
		for _, ref := range h.listed[name] {
			add(ref)
		}
	}
	return refs
}

// ModelRefresh asks every configured provider that can list its models for them.
// Nil offline: a selector must not reach the network when the run was told not to.
func (h *fullScreenHost) ModelRefresh() interactive.ModelRefresh {
	if h.s.rt.Args.Offline || truthyEnv(os.Getenv("PI_OFFLINE")) {
		return nil
	}
	return func(ctx context.Context) ([]ai.ModelRef, []string, error) {
		var failed []string
		listed := map[string][]ai.ModelRef{}
		for _, name := range ProviderNames() {
			if !hasCredential(h.s.rt.Args, name) {
				continue
			}
			scoped := h.s.rt.Args
			scoped.Provider = name
			port, _, _, err := h.s.rt.openModel(scoped)
			if err != nil {
				failed = append(failed, name)
				continue
			}
			models, err := listAvailableModels(ctx, port, name)
			if errors.Is(err, ai.ErrModelListingUnsupported) {
				continue
			}
			if err != nil {
				failed = append(failed, name)
				continue
			}
			listed[name] = models
		}
		if err := ctx.Err(); err != nil {
			return nil, failed, err
		}
		h.s.mu.Lock()
		h.listed = listed
		h.s.mu.Unlock()
		return h.ConfiguredModels(), failed, nil
	}
}

// SelectModel answers with a model from the next turn and makes it the default, as
// choosing in Pi's selector does.
func (h *fullScreenHost) SelectModel(ref ai.ModelRef) error {
	if err := h.s.switchModel(ref.Provider, ref.ID); err != nil {
		return err
	}
	cfg := &h.s.config
	cfg.Global.DefaultProvider, cfg.Global.DefaultModel = ref.Provider, ref.ID
	cfg.Effective.DefaultProvider, cfg.Effective.DefaultModel = ref.Provider, ref.ID
	return h.saveGlobal()
}

// saveGlobal writes the global settings, re-merging the project's over them.
func (h *fullScreenHost) saveGlobal() error {
	cfg := &h.s.config
	if cfg.AgentDir == "" {
		return nil
	}
	if err := settings.Save(settings.GlobalPath(cfg.AgentDir), cfg.Global); err != nil {
		return fmt.Errorf("the change is in effect, but saving it failed: %w", err)
	}
	if cfg.ProjectLoaded {
		if project, err := settings.Load(cfg.ProjectPath); err == nil {
			cfg.Effective = settings.Merge(cfg.Global, project)
		}
	}
	return nil
}

func (h *fullScreenHost) ScopedModels() []ai.ModelRef {
	if len(h.s.rt.ScopedModels) > 0 {
		return slices.Clone(h.s.rt.ScopedModels)
	}
	if len(h.s.rt.ModelPatterns) == 0 {
		return nil
	}
	// Patterns are resolved against what is known here; one that matches
	// nothing known is left for the cycle command to diagnose.
	resolved, _ := resolveModelPatterns(h.s.rt.ModelPatterns, h.ConfiguredModels())
	return resolved
}

func (h *fullScreenHost) EnabledModels() []string {
	scoped := h.ScopedModels()
	if len(scoped) == 0 && len(h.s.rt.ModelPatterns) == 0 {
		return nil
	}
	ids := make([]string, 0, len(scoped))
	for _, ref := range scoped {
		ids = append(ids, ref.Provider+"/"+ref.ID)
	}
	// A pattern that resolved to nothing is still part of the scope, and the
	// selector shows it unavailable rather than forgetting it.
	for _, pattern := range h.s.rt.ModelPatterns {
		if !strings.ContainsAny(pattern, "*?[") && !slices.Contains(ids, pattern) && strings.Contains(pattern, "/") {
			ids = append(ids, pattern)
		}
	}
	return ids
}

func parseModelIDs(ids []string) []ai.ModelRef {
	refs := make([]ai.ModelRef, 0, len(ids))
	for _, id := range ids {
		provider, model, found := strings.Cut(id, "/")
		if !found {
			provider, model = "", id
		}
		refs = append(refs, ai.ModelRef{Provider: provider, ID: model})
	}
	return refs
}

// SetScopedModels limits cycling to these models for this session; nil lifts the
// limit.
func (h *fullScreenHost) SetScopedModels(enabled []string) {
	h.s.mu.Lock()
	defer h.s.mu.Unlock()
	h.s.rt.ModelPatterns = nil
	h.s.rt.ScopedModels = nil
	if enabled != nil {
		h.s.rt.ScopedModels = parseModelIDs(enabled)
	}
}

// SaveEnabledModels remembers the scope for later runs; nil forgets it.
func (h *fullScreenHost) SaveEnabledModels(enabled []string) error {
	cfg := &h.s.config
	cfg.Global.EnabledModels = enabled
	cfg.Effective.EnabledModels = enabled
	return h.saveGlobal()
}

// CycleModel moves through the scope, or every configured model, as the model keys
// do — offline, as Pi's cycle reads its cached catalogue.
func (h *fullScreenHost) CycleModel(forward bool) (ai.ModelRef, bool, error) {
	candidates := h.ScopedModels()
	if len(candidates) == 0 {
		candidates = h.ConfiguredModels()
	}
	if len(candidates) <= 1 {
		return ai.ModelRef{}, false, nil
	}
	at := slices.Index(candidates, h.CurrentModel())
	if at < 0 {
		at = 0
	}
	step := 1
	if !forward {
		step = -1
	}
	next := candidates[(at+step+len(candidates))%len(candidates)]
	if err := h.s.switchModel(next.Provider, next.ID); err != nil {
		return ai.ModelRef{}, false, err
	}
	return next, true, nil
}

// CycleThinking moves to the next level the model offers.
func (h *fullScreenHost) CycleThinking() (string, bool, error) {
	_, _, provider, model, thinking := h.s.snapshot()
	available := ai.AvailableThinkingLevels(provider, model)
	if len(available) <= 1 {
		return "", false, nil
	}
	if thinking == "" {
		// Nothing asked for is shown as off, and cycles on from it.
		thinking = ai.ThinkingOff
	}
	next := ai.NextThinkingLevel(available, thinking)
	if err := h.s.setThinking(next); err != nil {
		return "", false, err
	}
	return string(next), true, nil
}

// setThinking makes a level the one every later request asks for, records it in
// the conversation, and remembers it as the default — as Pi does, unless it is off
// on a model that cannot reason, where off is the only level there was.
func (s *interactiveSession) setThinking(level ai.ThinkingLevel) error {
	s.mu.Lock()
	provider, model := s.provider, s.model
	s.mu.Unlock()
	effective := ai.ClampThinkingLevel(ai.AvailableThinkingLevels(provider, model), level)
	if err := s.current.Session.RecordThinking(effective); err != nil {
		return err
	}
	s.mu.Lock()
	s.rt.Thinking = effective
	s.ctxt.args.Thinking = effective
	agent := s.agent
	s.mu.Unlock()
	agent.SetThinking(requestThinking(provider, model, effective))
	if ai.SupportsThinking(provider, model) || effective != ai.ThinkingOff {
		s.config.Global.DefaultThinkingLevel = string(effective)
		s.config.Effective.DefaultThinkingLevel = string(effective)
		if s.config.AgentDir != "" {
			if err := settings.Save(settings.GlobalPath(s.config.AgentDir), s.config.Global); err != nil {
				return fmt.Errorf("the level is in effect, but saving it as the default failed: %w", err)
			}
		}
	}
	return nil
}

// --- sessions ---

// agentDir is where this run's sessions are kept.
func (h *fullScreenHost) agentDir() (string, error) {
	if h.s.current.Dir != "" {
		return h.s.current.Dir, nil
	}
	if dir := h.s.rt.Args.SessionDir; dir != "" {
		return dir, nil
	}
	return session.AgentDir()
}

func (h *fullScreenHost) SessionLoaders() (interactive.SessionLoader, interactive.SessionLoader) {
	current := func(onProgress func(int, int)) ([]session.Info, error) {
		dir, err := h.agentDir()
		if err != nil {
			return nil, err
		}
		return session.ListProgress(dir, h.s.rt.WorkingDir, onProgress)
	}
	all := func(onProgress func(int, int)) ([]session.Info, error) {
		dir, err := h.agentDir()
		if err != nil {
			return nil, err
		}
		return session.ListAllProgress(dir, onProgress)
	}
	return current, all
}

func (h *fullScreenHost) CurrentSessionPath() string { return h.s.current.Path }

// ResumeSession opens a recorded conversation. One from another directory carries
// on here, and says so, since the paths it talks about are relative to where it ran.
func (h *fullScreenHost) ResumeSession(path string) (string, error) {
	info, err := session.Describe(path)
	if err != nil {
		return "", err
	}
	next := h.s.rt.Args.WithoutConversationChoice()
	next.Continue, next.Resume = false, path
	if err := h.s.reopen(next); err != nil {
		return "", err
	}
	if info.Dir != "" && info.Dir != h.s.rt.WorkingDir {
		return "This session was recorded in " + info.Dir + "; continuing it in " + h.s.rt.WorkingDir, nil
	}
	return "", nil
}

func (h *fullScreenHost) RenameSession(path, name string) error {
	if path == h.s.current.Path {
		return h.s.current.Session.SetName(name)
	}
	return session.Rename(path, name)
}

// DeleteSession moves a session file to the trash where a `trash` command exists,
// and deletes it outright where none does, as Pi does.
func (h *fullScreenHost) DeleteSession(path string) (bool, error) {
	args := []string{path}
	if strings.HasPrefix(path, "-") {
		args = []string{"--", path}
	}
	if err := exec.Command("trash", args...).Run(); err == nil {
		return true, nil
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return true, nil
	}
	if err := os.Remove(path); err != nil {
		return false, err
	}
	return false, nil
}

// --- the tree ---

func (h *fullScreenHost) store() (*session.FileStore, error) {
	if h.s.current.Store == nil {
		return nil, fmt.Errorf("this conversation is not recorded, so it has no shape to show")
	}
	return h.s.current.Store, nil
}

func (h *fullScreenHost) TreeNodes() ([]session.Node, string, error) {
	store, err := h.store()
	if err != nil {
		return nil, "", err
	}
	nodes, err := store.Tree(context.Background())
	if err != nil {
		return nil, "", err
	}
	return nodes, store.Leaf(), nil
}

func (h *fullScreenHost) SetEntryLabel(id, label string) error {
	store, err := h.store()
	if err != nil {
		return err
	}
	return store.SetLabel(context.Background(), id, label)
}

func (h *fullScreenHost) BranchSummarySkipPrompt() bool {
	return h.s.config.Effective.BranchSummary.SkipPrompt
}

func (h *fullScreenHost) CopyToClipboard(text string) error { return copyToClipboard(text) }

func (h *fullScreenHost) ReadClipboard() (string, error) { return readClipboard() }

func (h *fullScreenHost) LastAssistantText() (string, bool) {
	current, _, _, _, _ := h.s.snapshot()
	return lastAnswer(current.Session)
}

// EditExternally opens the user's editor on the streams the interface is drawn
// on, which the interface has given up for the purpose.
func (h *fullScreenHost) EditExternally(content string) (string, bool, error) {
	command := ExternalEditorCommand(h.s.config.Effective, os.Getenv, goruntime.GOOS)
	return EditInExternalEditor(command, content, os.Stdin, os.Stdout, os.Stderr)
}

// NavigateTree moves the conversation to an entry — Pi's navigateTree. Choosing
// something the user said puts its words back in the editor and stands before it;
// anything else is stood on. A summary of the branch left behind is written at the
// new position first, where the model will see it.
func (h *fullScreenHost) NavigateTree(ctx context.Context, id string, summarize bool, instructions string) (string, error) {
	store, err := h.store()
	if err != nil {
		return "", err
	}
	if id == store.Leaf() {
		return "", nil
	}
	nodes, err := store.Tree(ctx)
	if err != nil {
		return "", err
	}
	at := slices.IndexFunc(nodes, func(n session.Node) bool { return n.ID == id })
	if at < 0 {
		return "", fmt.Errorf("no entry %q in this conversation", id)
	}
	target := nodes[at]

	var summary *session.BranchSummary
	if summarize {
		left, _, err := store.LeftBehind(ctx, id)
		if err != nil {
			return "", err
		}
		if len(left) > 0 {
			result, err := h.summarizeBranch(ctx, left, instructions)
			if err != nil {
				return "", err
			}
			summary = &session.BranchSummary{Summary: result.Summary,
				ReadFiles: result.ReadFiles, ModifiedFiles: result.ModifiedFiles}
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	newLeaf, editorText := id, ""
	if m := target.Entry.Message; m != nil && m.Role == ai.RoleUser && m.BashExecution == nil {
		newLeaf, editorText = target.ParentID, m.Content
	}
	if newLeaf == "" {
		store.ResetLeaf()
	} else if err := store.MoveTo(ctx, newLeaf); err != nil {
		return "", err
	}
	if summary != nil {
		summary.FromID = newLeaf
		if summary.FromID == "" {
			summary.FromID = "root"
		}
		if err := store.Append(ctx, session.Entry{BranchSummary: summary}); err != nil {
			return "", err
		}
	}
	if err := h.s.ctxt.reload(); err != nil {
		return "", err
	}
	return editorText, nil
}

// summarizeBranch says what the entries being left were about, with the model
// answering now.
func (h *fullScreenHost) summarizeBranch(ctx context.Context, left []session.Node, instructions string) (compaction.BranchResult, error) {
	var entries []compaction.BranchEntry
	for _, n := range left {
		e := n.Entry
		switch {
		case e.Message != nil:
			if e.Message.BashExecution != nil && e.Message.BashExecution.ExcludeFromContext {
				continue
			}
			entries = append(entries, compaction.BranchEntry{Message: *e.Message})
		case e.Checkpoint != nil:
			entries = append(entries, compaction.BranchEntry{Message: session.SummaryMessage(e.Checkpoint.Summary), IsSummary: true})
		case e.BranchSummary != nil:
			entries = append(entries, compaction.BranchEntry{Message: session.BranchSummaryMessage(e.BranchSummary.Summary),
				IsSummary: true, ReadFiles: e.BranchSummary.ReadFiles, ModifiedFiles: e.BranchSummary.ModifiedFiles})
		}
	}
	h.s.mu.Lock()
	port, provider, model := h.s.port, h.s.provider, h.s.model
	h.s.mu.Unlock()
	facts, _ := ai.Facts(provider, model)
	c := &compaction.Compactor{Model: port, ModelName: model}
	return c.SummarizeBranch(ctx, entries, instructions, facts.ContextWindow, h.s.config.Effective.BranchSummary.ReserveTokens)
}

// --- fork ---

func (h *fullScreenHost) ForkMessages() ([]interactive.ForkMessage, error) {
	store, err := h.store()
	if err != nil {
		return nil, err
	}
	messages, err := store.ForkMessages(context.Background())
	if err != nil {
		return nil, err
	}
	out := make([]interactive.ForkMessage, len(messages))
	for i, m := range messages {
		out[i] = interactive.ForkMessage{ID: m.EntryID, Text: m.Text}
	}
	return out, nil
}

// Fork copies the conversation up to just before what the user said into a new
// one, and hands the words back for the editor.
func (h *fullScreenHost) Fork(id string) (string, error) {
	store, err := h.store()
	if err != nil {
		return "", err
	}
	leaf, text, err := store.Forked(context.Background(), id)
	if err != nil {
		return "", err
	}
	return text, h.s.forkAt(leaf)
}

// forkAt copies the conversation up to leaf into a new file beside it, or starts an
// empty one that records where it came from when there is nothing to copy, and
// opens it through the ordinary resume path.
func (s *interactiveSession) forkAt(leaf string) error {
	now := time.Now()
	id := session.NewSessionID(now)
	path := filepath.Join(session.DirFor(s.current.Dir, s.rt.WorkingDir), session.FileName(id, now))
	var forked *session.FileStore
	var err error
	if leaf == "" {
		forked, err = s.current.Store.EmptyBranch(path, s.rt.WorkingDir, id)
	} else {
		forked, err = s.current.Store.BranchInto(context.Background(), path, s.rt.WorkingDir, id, leaf)
	}
	if err != nil {
		return err
	}
	forked.Close()
	next := s.rt.Args.WithoutConversationChoice()
	next.Continue, next.Resume = false, path
	return s.reopen(next)
}

// --- settings ---

func (h *fullScreenHost) SettingsValues() interactive.SettingsValues {
	_, agent, provider, model, thinking := h.s.snapshot()
	effective := h.s.config.Effective
	available := ai.AvailableThinkingLevels(provider, model)
	levels := make([]string, len(available))
	for i, level := range available {
		levels[i] = string(level)
	}
	v := interactive.SettingsValues{
		AutoCompact:            h.s.rt.AutoCompaction.Enabled,
		SteeringMode:           string(agent.SteeringMode()),
		FollowUpMode:           string(agent.FollowUpMode()),
		Thinking:               string(thinking),
		AvailableThinking:      levels,
		HideThinking:           effective.HideThinkingBlock,
		QuietStartup:           effective.QuietStartup,
		DefaultProjectTrust:    string(h.s.config.Global.DefaultProjectTrust),
		DoubleEscapeAction:     DoubleEscapeAction(effective),
		TreeFilterMode:         TreeFilterMode(effective),
		ShowHardwareCursor:     ShowHardwareCursor(effective, os.Getenv),
		EditorPaddingX:         EditorPaddingX(effective),
		OutputPad:              OutputPad(effective),
		AutocompleteMaxVisible: AutocompleteMaxVisible(effective),
		ClearOnShrink:          ClearOnShrink(effective, os.Getenv),
	}
	if v.Thinking == "" {
		v.Thinking = "off"
	}
	if v.DefaultProjectTrust == "" {
		v.DefaultProjectTrust = "ask"
	}
	return v
}

// The interface's settings, with Pi's defaults where nothing is set.

func DoubleEscapeAction(s settings.Settings) string {
	if s.DoubleEscapeAction == "" {
		return "tree"
	}
	return s.DoubleEscapeAction
}

func TreeFilterMode(s settings.Settings) string {
	if s.TreeFilterMode == "" {
		return "default"
	}
	return s.TreeFilterMode
}

func ShowHardwareCursor(s settings.Settings, getenv func(string) string) bool {
	if s.ShowHardwareCursor != nil {
		return *s.ShowHardwareCursor
	}
	return getenv("PI_HARDWARE_CURSOR") == "1"
}

func ClearOnShrink(s settings.Settings, getenv func(string) string) bool {
	if s.Terminal.ClearOnShrink != nil {
		return *s.Terminal.ClearOnShrink
	}
	return getenv("PI_CLEAR_ON_SHRINK") == "1"
}

func EditorPaddingX(s settings.Settings) int {
	if s.EditorPaddingX != nil {
		return *s.EditorPaddingX
	}
	return 0
}

func OutputPad(s settings.Settings) int {
	if s.OutputPad != nil && *s.OutputPad == 0 {
		return 0
	}
	return 1
}

func AutocompleteMaxVisible(s settings.Settings) int {
	if s.AutocompleteMaxVisible == 0 {
		return 5
	}
	return s.AutocompleteMaxVisible
}

// ChangeSetting applies a setting to the running session where it can, and saves
// it. Settings without a running effect — the interface applies those itself.
func (h *fullScreenHost) ChangeSetting(id, value string) error {
	cfg := &h.s.config
	on := value == "true"
	number, _ := strconv.Atoi(value)
	set := func(apply func(*settings.Settings)) {
		apply(&cfg.Global)
		apply(&cfg.Effective)
	}
	switch id {
	case interactive.SettingAutoCompact:
		h.s.mu.Lock()
		h.s.rt.AutoCompaction.Enabled = on
		h.s.agent.SetAutoCompaction(on)
		h.s.mu.Unlock()
		set(func(s *settings.Settings) { s.Compaction.Enabled = &on })
	case interactive.SettingSteeringMode, interactive.SettingFollowUpMode:
		mode, err := runtime.ParseQueueMode(value)
		if err != nil {
			return err
		}
		h.s.mu.Lock()
		if id == interactive.SettingSteeringMode {
			h.s.rt.SteeringMode = mode
			h.s.agent.SetSteeringMode(mode)
			set(func(s *settings.Settings) { s.SteeringMode = value })
		} else {
			h.s.rt.FollowUpMode = mode
			h.s.agent.SetFollowUpMode(mode)
			set(func(s *settings.Settings) { s.FollowUpMode = value })
		}
		h.s.mu.Unlock()
	case interactive.SettingThinking:
		level, err := ai.ParseThinkingLevel(value)
		if err != nil {
			return err
		}
		return h.s.setThinking(level)
	case interactive.SettingHideThinking:
		set(func(s *settings.Settings) { s.HideThinkingBlock = on })
	case interactive.SettingQuietStartup:
		set(func(s *settings.Settings) { s.QuietStartup = on })
	case interactive.SettingProjectTrust:
		// Global only: a project choosing its own trust default is the project
		// trusting itself.
		cfg.Global.DefaultProjectTrust = settings.DefaultProjectTrust(value)
		cfg.Effective.DefaultProjectTrust = cfg.Global.DefaultProjectTrust
	case interactive.SettingDoubleEscape:
		set(func(s *settings.Settings) { s.DoubleEscapeAction = value })
	case interactive.SettingTreeFilter:
		set(func(s *settings.Settings) { s.TreeFilterMode = value })
	case interactive.SettingHardwareCursor:
		set(func(s *settings.Settings) { s.ShowHardwareCursor = &on })
	case interactive.SettingClearOnShrink:
		set(func(s *settings.Settings) { s.Terminal.ClearOnShrink = &on })
	case interactive.SettingEditorPadding:
		set(func(s *settings.Settings) { s.EditorPaddingX = &number })
	case interactive.SettingOutputPadding:
		set(func(s *settings.Settings) { s.OutputPad = &number })
	case interactive.SettingAutocomplete:
		set(func(s *settings.Settings) { s.AutocompleteMaxVisible = number })
	default:
		return fmt.Errorf("no setting called %q", id)
	}
	return h.saveGlobal()
}

// --- credentials ---

func (h *fullScreenHost) LoginProviders() []interactive.AuthProvider {
	var out []interactive.AuthProvider
	for _, name := range ProviderNames() {
		out = append(out, interactive.AuthProvider{ID: name, Name: name, Source: credentialSource(h.s.rt.Args, name)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// credentialSource says where a provider's credential comes from, or nothing.
func credentialSource(args Args, name string) string {
	if storedKey(args, name) != "" {
		return "stored credential"
	}
	var set []string
	for _, v := range Providers[name].EnvVars {
		if strings.TrimSpace(os.Getenv(v)) != "" {
			set = append(set, v)
		}
	}
	return strings.Join(set, ", ")
}

func (h *fullScreenHost) SavedProviders() ([]interactive.AuthProvider, error) {
	store, err := AuthStore(h.s.rt.Args)
	if err != nil {
		return nil, err
	}
	saved, err := store.Providers()
	if err != nil {
		return nil, err
	}
	out := make([]interactive.AuthProvider, 0, len(saved))
	for _, name := range saved {
		out = append(out, interactive.AuthProvider{ID: name, Name: name, Source: "stored credential"})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (h *fullScreenHost) SaveCredential(provider, key string) (string, error) {
	if _, known := Providers[provider]; !known {
		return "", fmt.Errorf("unknown provider %q", provider)
	}
	store, err := AuthStore(h.s.rt.Args)
	if err != nil {
		return "", err
	}
	if err := store.Set(provider, auth.APIKey(key)); err != nil {
		return "", err
	}
	h.providers = AvailableProviders(h.s.rt.Args)
	return store.Path(), nil
}

// Logout forgets a stored credential, saying so when the environment still carries
// one: a user told they are logged out while requests keep succeeding has been told
// something false.
func (h *fullScreenHost) Logout(provider string) (string, error) {
	store, err := AuthStore(h.s.rt.Args)
	if err != nil {
		return "", err
	}
	if err := store.Remove(provider); err != nil {
		return "", err
	}
	h.providers = AvailableProviders(h.s.rt.Args)
	message := "Removed stored API key for " + provider + ". Environment variables are unchanged."
	if p, known := Providers[provider]; known {
		for _, v := range p.EnvVars {
			if strings.TrimSpace(os.Getenv(v)) != "" {
				return message + " " + v + " is still set.", nil
			}
		}
	}
	return message, nil
}

// --- trust ---

func (h *fullScreenHost) TrustState() (string, *interactive.TrustSaved, bool) {
	cwd := trust.Canonical(h.s.rt.WorkingDir)
	store := trust.Open(h.s.config.AgentDir)
	var saved *interactive.TrustSaved
	if decision, from, err := store.Get(cwd); err == nil && decision != trust.Undecided {
		saved = &interactive.TrustSaved{Path: from, Decision: decision == trust.Trusted}
	}
	return cwd, saved, h.s.config.Trust == trust.Trusted
}

func (h *fullScreenHost) SaveTrust(option interactive.TrustOption) error {
	store := trust.Open(h.s.config.AgentDir)
	for _, update := range option.Updates {
		decision := trust.Undecided
		switch {
		case update.Decision == nil:
		case *update.Decision:
			decision = trust.Trusted
		default:
			decision = trust.Refused
		}
		if err := store.Set(update.Path, decision); err != nil {
			return err
		}
	}
	return nil
}
