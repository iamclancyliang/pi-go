package interactive

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/iamclancyliang/pi-go/internal/tui"
)

// ForkMessage is a point the fork selector offers: an entry and what was said in it.
type ForkMessage struct {
	ID   string
	Text string
}

// ForkSelector lists what the user said, to fork the conversation from — Pi's
// UserMessageSelectorComponent.
type ForkSelector struct {
	tui.Container
	list *forkList
}

// NewForkSelector starts on the message whose id is initial, or the newest.
func NewForkSelector(env *Env, messages []ForkMessage, initial string, onSelect func(string), onCancel func()) *ForkSelector {
	t := env.Theme
	s := &ForkSelector{list: &forkList{env: env, messages: messages, selected: max(0, len(messages)-1),
		maxVisible: 10, onSelect: onSelect, onCancel: onCancel}}
	for i, m := range messages {
		if m.ID == initial {
			s.list.selected = i
		}
	}
	s.AddChild(spacer())
	s.AddChild(tui.NewText(Bold("Fork from Message"), 1, 0, nil))
	s.AddChild(tui.NewText(t.Fg("muted", "Select a user message to copy the active path up to that point into a new session"), 1, 0, nil))
	s.AddChild(spacer())
	s.AddChild(newBorder(env))
	s.AddChild(spacer())
	s.AddChild(s.list)
	s.AddChild(spacer())
	s.AddChild(newBorder(env))
	return s
}

// HandleKey goes to the list.
func (s *ForkSelector) HandleKey(k tui.Key) { s.list.HandleKey(k) }

type forkList struct {
	env        *Env
	messages   []ForkMessage
	selected   int
	maxVisible int
	onSelect   func(string)
	onCancel   func()
}

func (l *forkList) Invalidate() {}

func (l *forkList) Render(width int) []string {
	t := l.env.Theme
	if len(l.messages) == 0 {
		return []string{t.Fg("muted", "  No user messages found")}
	}
	var lines []string
	start, end := windowOf(l.selected, len(l.messages), l.maxVisible)
	for i := start; i < end; i++ {
		text := strings.TrimSpace(strings.ReplaceAll(l.messages[i].Text, "\n", " "))
		truncated := tui.TruncateToWidth(text, width-2, "...", false)
		line := "  " + truncated
		if i == l.selected {
			line = t.Fg("accent", "› ") + Bold(truncated)
		}
		lines = append(lines, line,
			t.Fg("muted", "  Message "+strconv.Itoa(i+1)+" of "+strconv.Itoa(len(l.messages))), "")
	}
	if start > 0 || end < len(l.messages) {
		lines = append(lines, t.Fg("muted", position(l.selected, len(l.messages))))
	}
	return lines
}

func (l *forkList) HandleKey(k tui.Key) {
	kb := l.env.Keys
	switch {
	case kb.Matches(k, "tui.select.up"):
		if len(l.messages) > 0 {
			l.selected = wrapUp(l.selected, len(l.messages))
		}
	case kb.Matches(k, "tui.select.down"):
		if len(l.messages) > 0 {
			l.selected = wrapDown(l.selected, len(l.messages))
		}
	case kb.Matches(k, "tui.select.confirm"):
		if l.selected < len(l.messages) {
			l.onSelect(l.messages[l.selected].ID)
		}
	case kb.Matches(k, "tui.select.cancel"):
		l.onCancel()
	}
}

// TrustUpdate is one change to the trust record. Decision is nil to forget the
// directory.
type TrustUpdate struct {
	Path     string
	Decision *bool
}

// TrustOption is a choice the trust selector offers.
type TrustOption struct {
	Label     string
	Trusted   bool
	Updates   []TrustUpdate
	SavedPath string
}

// TrustSaved is the decision on record that covers a directory.
type TrustSaved struct {
	Path     string
	Decision bool
}

// TrustOptions are the choices for a directory, already in its canonical form —
// Pi's getProjectTrustOptions.
func TrustOptions(cwd string) []TrustOption {
	yes, no := true, false
	options := []TrustOption{{Label: "Trust", Trusted: true, SavedPath: cwd,
		Updates: []TrustUpdate{{Path: cwd, Decision: &yes}}}}
	if parent := filepath.Dir(cwd); parent != cwd {
		options = append(options, TrustOption{Label: "Trust parent folder (" + parent + ")", Trusted: true,
			SavedPath: parent, Updates: []TrustUpdate{{Path: parent, Decision: &yes}, {Path: cwd}}})
	}
	return append(options, TrustOption{Label: "Do not trust", SavedPath: cwd,
		Updates: []TrustUpdate{{Path: cwd, Decision: &no}}})
}

// TrustSelector records whether this project may configure the tool — Pi's
// TrustSelectorComponent.
type TrustSelector struct {
	tui.Container
	env      *Env
	options  []TrustOption
	saved    *TrustSaved
	selected int
	list     tui.Container
	onSelect func(TrustOption)
	onCancel func()
}

func formatTrustDecision(trustPath string, saved *TrustSaved) string {
	if saved == nil {
		return "none"
	}
	label := "untrusted"
	if saved.Decision {
		label = "trusted"
	}
	if saved.Path != trustPath {
		return label + " (inherited from " + saved.Path + ")"
	}
	return label + " (" + saved.Path + ")"
}

// NewTrustSelector shows the options for cwd, starting on the one on record.
func NewTrustSelector(env *Env, cwd string, saved *TrustSaved, projectTrusted bool,
	onSelect func(TrustOption), onCancel func()) *TrustSelector {

	t := env.Theme
	s := &TrustSelector{env: env, options: TrustOptions(cwd), saved: saved, onSelect: onSelect, onCancel: onCancel}
	for i, option := range s.options {
		if s.isSaved(option) {
			s.selected = i
			break
		}
	}
	session := "untrusted"
	if projectTrusted {
		session = "trusted"
	}
	s.AddChild(newBorder(env))
	s.AddChild(spacer())
	s.AddChild(tui.NewText(t.Fg("accent", Bold("Project trust")), 1, 0, nil))
	s.AddChild(tui.NewText(t.Fg("muted", cwd), 1, 0, nil))
	s.AddChild(spacer())
	s.AddChild(tui.NewText(t.Fg("muted", "Saved decision: "+formatTrustDecision(s.options[0].SavedPath, saved)), 1, 0, nil))
	s.AddChild(tui.NewText(t.Fg("muted", "Current session: "+session), 1, 0, nil))
	s.AddChild(spacer())
	s.AddChild(&s.list)
	s.AddChild(spacer())
	s.AddChild(tui.NewText(env.rawKeyHint("↑↓", "navigate")+"  "+
		env.KeyHint("tui.select.confirm", "save")+"  "+
		env.KeyHint("tui.select.cancel", "cancel"), 1, 0, nil))
	s.AddChild(spacer())
	s.AddChild(newBorder(env))
	s.updateList()
	return s
}

func (s *TrustSelector) isSaved(option TrustOption) bool {
	return s.saved != nil && s.saved.Decision == option.Trusted && s.saved.Path == option.SavedPath
}

func (s *TrustSelector) updateList() {
	t := s.env.Theme
	s.list.Clear()
	for i, option := range s.options {
		check := ""
		if s.isSaved(option) {
			check = t.Fg("success", " ✓")
		}
		prefix, label := "  ", t.Fg("text", option.Label)
		if i == s.selected {
			prefix, label = t.Fg("accent", "→ "), t.Fg("accent", option.Label)
		}
		s.list.AddChild(tui.NewText(prefix+label+check, 1, 0, nil))
	}
}

// HandleKey moves the selection, saves or cancels.
func (s *TrustSelector) HandleKey(k tui.Key) {
	kb := s.env.Keys
	switch {
	case kb.Matches(k, "tui.select.up"), k.Text == 'k':
		s.selected = max(0, s.selected-1)
		s.updateList()
	case kb.Matches(k, "tui.select.down"), k.Text == 'j':
		s.selected = min(len(s.options)-1, s.selected+1)
		s.updateList()
	case kb.Matches(k, "tui.select.confirm"):
		s.onSelect(s.options[s.selected])
	case kb.Matches(k, "tui.select.cancel"):
		s.onCancel()
	}
}

// AuthProvider is a provider the login and logout selectors offer. Every credential
// here is an API key: Pi's subscription logins are not ported.
type AuthProvider struct {
	ID   string
	Name string
	// Source says where its credential comes from, empty when it has none:
	// "stored credential", or the variables that hold one.
	Source string
}

// AuthSelector is a searchable list of providers — Pi's OAuthSelectorComponent.
type AuthSelector struct {
	tui.Container
	env      *Env
	logout   bool
	search   tui.Input
	list     tui.Container
	all      []AuthProvider
	filtered []AuthProvider
	selected int
	onSelect func(AuthProvider)
	onCancel func()
}

// NewAuthSelector lists providers to log in to, or out of.
func NewAuthSelector(env *Env, logout bool, providers []AuthProvider, initialSearch string,
	onSelect func(AuthProvider), onCancel func()) *AuthSelector {

	t := env.Theme
	s := &AuthSelector{env: env, logout: logout, all: providers, filtered: providers, onSelect: onSelect, onCancel: onCancel}
	s.search.Keybindings = env.Keys
	s.AddChild(newBorder(env))
	s.AddChild(spacer())
	title := "Select provider to configure:"
	if logout {
		title = "Select provider to logout:"
	}
	s.AddChild(&tui.TruncatedText{Text: t.Fg("accent", Bold(title)), PaddingX: 1})
	s.AddChild(spacer())
	if initialSearch != "" {
		s.search.SetValue(initialSearch)
	}
	s.search.OnSubmit = func(string) {
		if s.selected < len(s.filtered) {
			s.onSelect(s.filtered[s.selected])
		}
	}
	s.AddChild(&s.search)
	s.AddChild(spacer())
	s.AddChild(&s.list)
	s.AddChild(spacer())
	s.AddChild(newBorder(env))
	s.filter(initialSearch)
	return s
}

// SetFocused passes focus to the search.
func (s *AuthSelector) SetFocused(focused bool) { s.search.SetFocused(focused) }

func (s *AuthSelector) filter(query string) {
	s.filtered = s.all
	if query != "" {
		s.filtered = tui.FuzzyFilter(s.all, query, func(p AuthProvider) string {
			return p.Name + " " + p.ID + " api_key "
		})
	}
	s.selected = max(0, min(s.selected, max(0, len(s.filtered)-1)))
	s.updateList()
}

var authEnvNames = regexp.MustCompile(`^[A-Z][A-Z0-9_]*(?:, [A-Z][A-Z0-9_]*)*$`)

func (s *AuthSelector) statusOf(p AuthProvider) string {
	t := s.env.Theme
	switch {
	case p.Source == "":
		return t.Fg("muted", " • unconfigured")
	case p.Source == "stored credential":
		return t.Fg("success", " ✓ configured")
	case authEnvNames.MatchString(p.Source):
		return t.Fg("success", " ✓ env: "+p.Source)
	}
	return t.Fg("success", " ✓ "+p.Source)
}

func (s *AuthSelector) updateList() {
	t := s.env.Theme
	s.list.Clear()
	start, end := windowOf(s.selected, len(s.filtered), 8)
	for i := start; i < end; i++ {
		p := s.filtered[i]
		line := "  " + t.Fg("text", p.Name) + s.statusOf(p)
		if i == s.selected {
			line = t.Fg("accent", "→ ") + t.Fg("accent", p.Name) + s.statusOf(p)
		}
		s.list.AddChild(&tui.TruncatedText{Text: line, PaddingX: 1})
	}
	if start > 0 || end < len(s.filtered) {
		s.list.AddChild(&tui.TruncatedText{Text: t.Fg("muted", position(s.selected, len(s.filtered))), PaddingX: 1})
	}
	if len(s.filtered) == 0 {
		message := "No matching providers"
		if len(s.all) == 0 {
			message = "No providers available"
			if s.logout {
				message = "No providers logged in. Use /login first."
			}
		}
		s.list.AddChild(&tui.TruncatedText{Text: t.Fg("muted", "  "+message), PaddingX: 1})
	}
}

// HandleKey moves the selection, chooses, cancels or searches.
func (s *AuthSelector) HandleKey(k tui.Key) {
	kb := s.env.Keys
	switch {
	case kb.Matches(k, "tui.select.up"):
		if len(s.filtered) > 0 {
			s.selected = max(0, s.selected-1)
			s.updateList()
		}
	case kb.Matches(k, "tui.select.down"):
		if len(s.filtered) > 0 {
			s.selected = min(len(s.filtered)-1, s.selected+1)
			s.updateList()
		}
	case kb.Matches(k, "tui.select.confirm"):
		if s.selected < len(s.filtered) {
			s.onSelect(s.filtered[s.selected])
		}
	case kb.Matches(k, "tui.select.cancel"):
		s.onCancel()
	default:
		s.search.HandleKey(k)
		s.filter(s.search.Value())
	}
}

// LoginDialog asks for a provider's API key — Pi's LoginDialogComponent in its
// prompt form. What is typed is masked, and is not left on the screen afterwards,
// where Pi shows it and then prints it above: a key in the scrollback outlives the
// terminal it was typed into, which is why the line prompt's /login turns echo off.
type LoginDialog struct {
	tui.Container
	env      *Env
	input    tui.Input
	onSubmit func(string)
	onCancel func()
}

// NewLoginDialog is a dialog for providerName, asking with message.
func NewLoginDialog(env *Env, providerName, message string, onSubmit func(string), onCancel func()) *LoginDialog {
	t := env.Theme
	d := &LoginDialog{env: env, onSubmit: onSubmit, onCancel: onCancel}
	d.input.Keybindings = env.Keys
	d.input.Mask = "*"
	d.input.OnSubmit = func(value string) { d.onSubmit(value) }
	d.input.OnEscape = func() { d.onCancel() }
	d.AddChild(newBorder(env))
	d.AddChild(tui.NewText(t.Fg("accent", Bold("Login to "+providerName)), 1, 0, nil))
	d.AddChild(spacer())
	d.AddChild(tui.NewText(t.Fg("text", message), 1, 0, nil))
	d.AddChild(&d.input)
	d.AddChild(tui.NewText("("+env.KeyHint("tui.select.cancel", "to cancel,")+" "+
		env.KeyHint("tui.select.confirm", "to submit")+")", 1, 0, nil))
	d.AddChild(newBorder(env))
	return d
}

// SetFocused passes focus to the input.
func (d *LoginDialog) SetFocused(focused bool) { d.input.SetFocused(focused) }

// HandleKey cancels or types.
func (d *LoginDialog) HandleKey(k tui.Key) {
	if d.env.Keys.Matches(k, "tui.select.cancel") {
		d.onCancel()
		return
	}
	d.input.HandleKey(k)
}
