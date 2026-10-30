package interactive

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/session"
	"github.com/iamclancyliang/pi-go/internal/tui"
)

// selectorDivergences are golden cases whose difference from Pi is accepted,
// with why.
var selectorDivergences = map[string]string{}

type goldenSelector struct {
	Kind  string   `json:"kind"`
	Name  string   `json:"name"`
	Width int      `json:"width"`
	Keys  []string `json:"keys"`
	Lines []string `json:"lines"`

	Title   string   `json:"title"`
	Options []string `json:"options"`

	Cwd   string `json:"cwd"`
	Saved *struct {
		Path     string `json:"path"`
		Decision bool   `json:"decision"`
	} `json:"saved"`
	Trusted bool `json:"trusted"`

	Messages []ForkMessage `json:"messages"`
	Initial  string        `json:"initial"`

	Logout    bool   `json:"logout"`
	Search    string `json:"search"`
	Providers []struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Source string `json:"source"`
	} `json:"providers"`
	Provider string `json:"provider"`
	Message  string `json:"message"`

	Current       ai.ModelRef   `json:"current"`
	Models        []ai.ModelRef `json:"models"`
	Scoped        []ai.ModelRef `json:"scoped"`
	Settled       bool          `json:"settled"`
	Enabled       *[]string     `json:"enabled"`
	RefreshStatus string        `json:"refreshStatus"`

	Now      string `json:"now"`
	Sessions []struct {
		Path         string `json:"path"`
		ID           string `json:"id"`
		Cwd          string `json:"cwd"`
		Name         string `json:"name"`
		Parent       string `json:"parentSessionPath"`
		Offset       int64  `json:"offset"`
		MessageCount int    `json:"messageCount"`
		FirstMessage string `json:"firstMessage"`
		AllText      string `json:"allMessagesText"`
	} `json:"sessions"`
	CurrentPath string `json:"currentPath"`

	Entries []goldenEntry `json:"entries"`
	Leaf    string        `json:"leaf"`
	Filter  string        `json:"filter"`

	Items []struct {
		ID           string   `json:"id"`
		Label        string   `json:"label"`
		Description  string   `json:"description"`
		CurrentValue string   `json:"currentValue"`
		Values       []string `json:"values"`
	} `json:"items"`
	SearchEnabled bool `json:"searchEnabled"`

	Value   string `json:"value"`
	Focused bool   `json:"focused"`
}

// goldenEntry is one of Pi's session entries, as the generator wrote it.
type goldenEntry struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	ParentID  string `json:"parentId"`
	Timestamp string `json:"timestamp"`
	Message   *struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
		ToolCallID string          `json:"toolCallId"`
		Command    string          `json:"command"`
	} `json:"message"`
	Summary      string `json:"summary"`
	TokensBefore int    `json:"tokensBefore"`
	FromID       string `json:"fromId"`
	ModelID      string `json:"modelId"`
	Provider     string `json:"provider"`
	Thinking     string `json:"thinkingLevel"`
	Name         string `json:"name"`
	TargetID     string `json:"targetId"`
	Label        string `json:"label"`
}

// contentBlock is one block of a Pi message's content.
type contentBlock struct {
	Type      string         `json:"type"`
	Text      string         `json:"text"`
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// messageOf is the pi-go message for one of Pi's.
func (e goldenEntry) messageOf() ai.Message {
	m := e.Message
	switch m.Role {
	case "bashExecution":
		return ai.Message{Role: ai.RoleUser, BashExecution: &ai.BashExecution{Command: m.Command}}
	case "toolResult":
		var blocks []contentBlock
		_ = json.Unmarshal(m.Content, &blocks)
		text := ""
		for _, b := range blocks {
			text += b.Text
		}
		return ai.Message{Role: ai.RoleTool, ToolCallID: m.ToolCallID, Content: text}
	}
	role := ai.RoleUser
	if m.Role == "assistant" {
		role = ai.RoleAssistant
	}
	var text string
	if err := json.Unmarshal(m.Content, &text); err == nil {
		return ai.Message{Role: role, Content: text}
	}
	var blocks []contentBlock
	_ = json.Unmarshal(m.Content, &blocks)
	out := ai.Message{Role: role}
	for _, b := range blocks {
		switch b.Type {
		case "text":
			out.Content += b.Text
		case "toolCall":
			args, _ := json.Marshal(b.Arguments)
			out.ToolCalls = append(out.ToolCalls, ai.ToolCall{ID: b.ID, Name: b.Name, Args: string(args)})
		}
	}
	return out
}

// nodesOf turns Pi's entries into what a store's Tree gives, labels resolved.
func nodesOf(entries []goldenEntry, leaf string) []session.Node {
	labels := map[string]goldenEntry{}
	for _, e := range entries {
		if e.Type != "label" {
			continue
		}
		if e.Label == "" {
			delete(labels, e.TargetID)
		} else {
			labels[e.TargetID] = e
		}
	}
	onPath := map[string]bool{}
	byID := map[string]goldenEntry{}
	for _, e := range entries {
		byID[e.ID] = e
	}
	for current := leaf; current != ""; current = byID[current].ParentID {
		onPath[current] = true
	}
	var nodes []session.Node
	for _, e := range entries {
		n := session.Node{ID: e.ID, ParentID: e.ParentID, Kind: e.Type, OnPath: onPath[e.ID]}
		n.At, _ = time.Parse(time.RFC3339Nano, e.Timestamp)
		if l, ok := labels[e.ID]; ok {
			n.Label = l.Label
			n.LabelAt, _ = time.Parse(time.RFC3339Nano, l.Timestamp)
		}
		switch e.Type {
		case "message":
			m := e.messageOf()
			n.Entry.Message = &m
		case "compaction":
			n.Entry.Checkpoint = &session.Checkpoint{Summary: e.Summary, TokensBefore: e.TokensBefore}
		case "branch_summary":
			n.Entry.BranchSummary = &session.BranchSummary{FromID: e.FromID, Summary: e.Summary}
		case "model_change":
			n.Entry.Model = &session.ModelChange{Provider: e.Provider, Model: e.ModelID}
		case "thinking_level_change":
			n.Entry.Thinking = &session.ThinkingChange{Level: ai.ThinkingLevel(e.Thinking)}
		case "session_info":
			n.Entry.Info = &session.SessionInfo{Name: e.Name}
		case "label":
			n.Entry.Label = &session.LabelChange{TargetID: e.TargetID, Label: e.Label}
		}
		nodes = append(nodes, n)
	}
	return nodes
}

// sendKeys delivers raw terminal bytes to a component, as the interface would.
func sendKeys(t *testing.T, to tui.KeyHandler, keys []string) {
	t.Helper()
	for _, raw := range keys {
		decoded, rest := tui.Decode([]byte(raw))
		if len(rest) == 1 && rest[0] == 0x1b {
			decoded = append(decoded, tui.Key{Name: "escape"})
		} else if len(rest) > 0 {
			t.Fatalf("key %q did not decode", raw)
		}
		for _, k := range decoded {
			to.HandleKey(k)
		}
	}
}

func (c goldenSelector) render(t *testing.T, shared *Env) []string {
	t.Helper()
	// Each case gets its own environment: a refresh left running by one case
	// reports through its environment after the next case has begun.
	own := *shared
	env := &own
	switch c.Kind {
	case "choice":
		d := NewChoiceDialog(env, c.Title, c.Options, func(string) {}, func() {})
		sendKeys(t, d, c.Keys)
		return d.Render(c.Width)
	case "trust":
		var saved *TrustSaved
		if c.Saved != nil {
			saved = &TrustSaved{Path: c.Saved.Path, Decision: c.Saved.Decision}
		}
		s := NewTrustSelector(env, c.Cwd, saved, c.Trusted, func(TrustOption) {}, func() {})
		sendKeys(t, s, c.Keys)
		return s.Render(c.Width)
	case "fork":
		s := NewForkSelector(env, c.Messages, c.Initial, func(string) {}, func() {})
		sendKeys(t, s, c.Keys)
		return s.Render(c.Width)
	case "auth":
		var providers []AuthProvider
		for _, p := range c.Providers {
			providers = append(providers, AuthProvider{ID: p.ID, Name: p.Name, Source: p.Source})
		}
		s := NewAuthSelector(env, c.Logout, providers, c.Search, func(AuthProvider) {}, func() {})
		sendKeys(t, s, c.Keys)
		return s.Render(c.Width)
	case "login":
		d := NewLoginDialog(env, c.Provider, c.Message, func(string) {}, func() {})
		return d.Render(c.Width)
	case "model":
		var refresh ModelRefresh
		applied := make(chan func(), 1)
		if c.Settled {
			refresh = func(ctx context.Context) ([]ai.ModelRef, []string, error) { return c.Models, nil, nil }
			env.Do = func(fn func()) { applied <- fn }
		} else {
			refresh = func(ctx context.Context) ([]ai.ModelRef, []string, error) {
				<-ctx.Done()
				return nil, nil, ctx.Err()
			}
		}
		s := NewModelSelector(env, c.Current, c.Models, c.Scoped, c.Search, refresh, func(ai.ModelRef) {}, func() {})
		if c.Settled {
			select {
			case fn := <-applied:
				fn()
			case <-time.After(5 * time.Second):
				t.Fatal("the refresh never came back")
			}
		}
		sendKeys(t, s, c.Keys)
		lines := s.Render(c.Width)
		s.Dispose()
		return lines
	case "scoped":
		var enabled []string
		if c.Enabled != nil {
			enabled = append([]string{}, (*c.Enabled)...)
		}
		s := NewScopedModelsSelector(env, c.Models, enabled, c.RefreshStatus, ScopedModelsCallbacks{
			OnChange: func([]string) {}, OnPersist: func([]string) {}, OnCancel: func() {}})
		sendKeys(t, s, c.Keys)
		return s.Render(c.Width)
	case "session":
		now, _ := time.Parse(time.RFC3339Nano, c.Now)
		var infos []session.Info
		for _, s := range c.Sessions {
			infos = append(infos, session.Info{Path: s.Path, ID: s.ID, Dir: s.Cwd, Name: s.Name, ParentSession: s.Parent,
				Activity: now.Add(-time.Duration(s.Offset) * time.Millisecond), MessageCount: s.MessageCount,
				Opening: s.FirstMessage, AllText: s.AllText})
		}
		loaded := make(chan func(), 8)
		env.Do = func(fn func()) { loaded <- fn }
		load := func(onProgress func(int, int)) ([]session.Info, error) { return infos, nil }
		s := NewSessionSelector(env, SessionSelectorOptions{Current: load, All: load, CurrentPath: c.CurrentPath,
			OnSelect: func(string) {}, OnCancel: func() {},
			Rename: func(string, string) error { return nil }, Delete: func(string) (bool, error) { return true, nil },
			Now: func() time.Time { return now }})
		select {
		case fn := <-loaded:
			fn()
		case <-time.After(5 * time.Second):
			t.Fatal("the sessions never loaded")
		}
		sendKeys(t, s, c.Keys)
		return s.Render(c.Width)
	case "tree":
		s := NewTreeSelector(env, nodesOf(c.Entries, c.Leaf), c.Leaf, 30, c.Initial, c.Filter,
			func(string) {}, func() {}, func(string, string) {})
		now, _ := time.Parse(time.RFC3339Nano, c.Now)
		s.list.now = func() time.Time { return now }
		sendKeys(t, s, c.Keys)
		return s.Render(c.Width)
	case "settingsList":
		var items []*tui.SettingItem
		for _, i := range c.Items {
			items = append(items, &tui.SettingItem{ID: i.ID, Label: i.Label, Description: i.Description,
				CurrentValue: i.CurrentValue, Values: i.Values})
		}
		th := env.Theme
		l := tui.NewSettingsList(items, 10, tui.SettingsListTheme{
			Label: func(text string, selected bool) string {
				if selected {
					return th.Fg("accent", text)
				}
				return text
			},
			Value: func(text string, selected bool) string {
				if selected {
					return th.Fg("accent", text)
				}
				return th.Fg("muted", text)
			},
			Description: th.FgStyle("dim"), Cursor: th.Fg("accent", "→ "), Hint: th.FgStyle("dim"),
		}, func(string, string) {}, func() {}, c.SearchEnabled)
		l.Keybindings = env.Keys
		sendKeys(t, l, c.Keys)
		return l.Render(c.Width)
	case "input":
		i := &tui.Input{Keybindings: env.Keys}
		i.SetValue(c.Value)
		i.SetFocused(c.Focused)
		sendKeys(t, i, c.Keys)
		return i.Render(c.Width)
	}
	t.Fatalf("unknown kind %q", c.Kind)
	return nil
}

// TestSelectorsMatchPi draws each selector as Pi's own components drew it
// (testdata/selectors/generate.ts).
func TestSelectorsMatchPi(t *testing.T) {
	data, err := os.ReadFile("testdata/selectors/golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden struct {
		Cases []goldenSelector `json:"cases"`
	}
	if err := json.Unmarshal(data, &golden); err != nil {
		t.Fatal(err)
	}
	tui.SetCapabilities(tui.TerminalCapabilities{TrueColor: true})
	defer tui.SetCapabilities(tui.TerminalCapabilities{})
	env := goldenEnv(t)
	failures := 0
	for _, c := range golden.Cases {
		label := c.Kind + " " + c.Name
		if _, skip := selectorDivergences[label]; skip {
			continue
		}
		got := c.render(t, env)
		if strings.Join(got, "\n") != strings.Join(c.Lines, "\n") {
			failures++
			if failures <= 8 {
				t.Errorf("%s\n got:\n%s\nwant:\n%s", label, showLines(got), showLines(c.Lines))
			}
		}
	}
	if failures > 0 {
		t.Logf("%d of %d cases differ", failures, len(golden.Cases))
	}
}
