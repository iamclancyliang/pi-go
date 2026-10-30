package interactive

import (
	"context"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/tui"
)

// modelRefreshTimeout bounds asking the providers for their models.
const modelRefreshTimeout = 15 * time.Second

// ModelRefresh asks the providers for their models, returning the whole list as it
// now stands and which providers could not answer.
type ModelRefresh func(ctx context.Context) (models []ai.ModelRef, failed []string, err error)

// ModelSelector is a searchable list of models with the one in use ticked — Pi's
// ModelSelectorComponent. A model here has no name beyond its id: pi-go has no
// catalogue to take a display name from.
type ModelSelector struct {
	tui.Container
	env *Env

	search   tui.Input
	list     tui.Container
	current  ai.ModelRef
	all      []ai.ModelRef
	scoped   []ai.ModelRef
	active   []ai.ModelRef
	filtered []ai.ModelRef
	selected int

	useScoped bool
	scopeText *tui.Text
	scopeHint *tui.Text

	errorMessage   string
	refreshStatus  string
	refreshSuccess bool
	closed         bool
	cancelRefresh  context.CancelFunc

	onSelect func(ai.ModelRef)
	onCancel func()
}

// NewModelSelector lists models with current first, opening on the scoped ones when
// there are any. refresh, when there is one, runs in the background and redraws the
// list with what it finds.
func NewModelSelector(env *Env, current ai.ModelRef, models, scoped []ai.ModelRef, initialSearch string,
	refresh ModelRefresh, onSelect func(ai.ModelRef), onCancel func()) *ModelSelector {

	t := env.Theme
	s := &ModelSelector{env: env, current: current, scoped: scoped, useScoped: len(scoped) > 0,
		onSelect: onSelect, onCancel: onCancel}
	s.search.Keybindings = env.Keys

	s.AddChild(newBorder(env))
	s.AddChild(spacer())
	if len(scoped) > 0 {
		s.scopeText = tui.NewText(s.scopeLine(), 0, 0, nil)
		s.AddChild(s.scopeText)
		s.scopeHint = tui.NewText(env.KeyHint("tui.input.tab", "scope")+t.Fg("muted", " (all/scoped)"), 0, 0, nil)
		s.AddChild(s.scopeHint)
	} else {
		s.AddChild(tui.NewText(t.Fg("warning", "Only showing models from configured providers. Use /login to add providers."), 0, 0, nil))
	}
	s.AddChild(spacer())

	if initialSearch != "" {
		s.search.SetValue(initialSearch)
	}
	s.search.OnSubmit = func(string) {
		if s.selected < len(s.filtered) {
			s.choose(s.filtered[s.selected])
		}
	}
	s.AddChild(&s.search)
	s.AddChild(spacer())
	s.AddChild(&s.list)
	s.AddChild(spacer())
	s.AddChild(newBorder(env))

	s.load(models)
	if initialSearch != "" {
		s.filter(initialSearch)
	} else {
		s.updateList()
	}
	if refresh != nil {
		s.refreshStatus = "Refreshing model catalogs…"
		s.updateList()
		s.startRefresh(refresh)
	}
	return s
}

// Dispose stops a refresh still running.
func (s *ModelSelector) Dispose() {
	if s.closed {
		return
	}
	s.closed = true
	if s.cancelRefresh != nil {
		s.cancelRefresh()
	}
}

// SetFocused passes focus to the search.
func (s *ModelSelector) SetFocused(focused bool) { s.search.SetFocused(focused) }

func (s *ModelSelector) startRefresh(refresh ModelRefresh) {
	ctx, cancel := context.WithTimeout(context.Background(), modelRefreshTimeout)
	s.cancelRefresh = cancel
	go func() {
		models, failed, err := refresh(ctx)
		timedOut := ctx.Err() == context.DeadlineExceeded
		cancel()
		s.env.do(func() {
			if s.closed {
				return
			}
			s.refreshStatus = ""
			switch {
			case timedOut:
				s.errorMessage = "Model refresh timed out; showing cached models."
			case err != nil:
				s.errorMessage = "Could not refresh model catalogs: " + err.Error()
				s.updateList()
				s.env.requestRender()
				return
			case len(failed) == 1:
				s.errorMessage = "Could not refresh " + failed[0] + "; showing cached models."
			case len(failed) > 1:
				s.errorMessage = "Could not refresh " + strconv.Itoa(len(failed)) + " model catalogs (" +
					strings.Join(failed, ", ") + "); showing cached models."
			default:
				s.refreshStatus = "Model catalogs refreshed."
				s.refreshSuccess = true
			}
			s.load(models)
			s.filter(s.search.Value())
			s.env.requestRender()
		})
	}()
}

func (s *ModelSelector) load(models []ai.ModelRef) {
	s.all = s.sorted(models)
	s.active = s.all
	if s.useScoped {
		s.active = s.scoped
	}
	s.filtered = s.active
	if at := slices.Index(s.filtered, s.current); at >= 0 {
		s.selected = at
	} else {
		s.selected = min(s.selected, max(0, len(s.filtered)-1))
	}
}

// sorted puts the model in use first and the rest by provider, keeping the order
// each provider gave.
func (s *ModelSelector) sorted(models []ai.ModelRef) []ai.ModelRef {
	out := slices.Clone(models)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i] == s.current, out[j] == s.current
		if a != b {
			return a
		}
		return out[i].Provider < out[j].Provider
	})
	return out
}

func (s *ModelSelector) scopeLine() string {
	t := s.env.Theme
	allColor, scopedColor := "muted", "accent"
	if !s.useScoped {
		allColor, scopedColor = "accent", "muted"
	}
	return t.Fg("muted", "Scope: ") + t.Fg(allColor, "all") + t.Fg("muted", " | ") + t.Fg(scopedColor, "scoped")
}

func (s *ModelSelector) setScope(scoped bool) {
	s.useScoped = scoped
	s.active = s.all
	if scoped {
		s.active = s.scoped
	}
	s.selected = max(0, slices.Index(s.active, s.current))
	s.filter(s.search.Value())
	if s.scopeText != nil {
		s.scopeText.SetText(s.scopeLine())
	}
}

// modelSearchText puts the provider first so that "openai/gpt-5" ranks the
// provider's own model above a proxy's copy of it — Pi's getModelSelectorSearchText.
func modelSearchText(m ai.ModelRef) string {
	return m.Provider + " " + m.Provider + "/" + m.ID + " " + m.Provider + " " + m.ID + " " + m.ID
}

func (s *ModelSelector) filter(query string) {
	s.filtered = s.active
	if query != "" {
		s.filtered = tui.FuzzyFilter(s.active, query, modelSearchText)
		s.selected = 0
	} else {
		s.selected = min(s.selected, max(0, len(s.filtered)-1))
	}
	s.updateList()
}

func (s *ModelSelector) updateList() {
	t := s.env.Theme
	s.list.Clear()
	start, end := windowOf(s.selected, len(s.filtered), 10)
	for i := start; i < end; i++ {
		item := s.filtered[i]
		badge := t.Fg("muted", "["+item.Provider+"]")
		check := ""
		if item == s.current {
			check = t.Fg("success", " ✓")
		}
		line := "  " + item.ID + " " + badge + check
		if i == s.selected {
			line = t.Fg("accent", "→ ") + t.Fg("accent", item.ID) + " " + badge + check
		}
		s.list.AddChild(tui.NewText(line, 0, 0, nil))
	}
	if start > 0 || end < len(s.filtered) {
		s.list.AddChild(tui.NewText(t.Fg("muted", position(s.selected, len(s.filtered))), 0, 0, nil))
	}
	switch {
	case s.errorMessage != "":
		for _, line := range strings.Split(s.errorMessage, "\n") {
			s.list.AddChild(tui.NewText(t.Fg("error", line), 0, 0, nil))
		}
	case len(s.filtered) == 0:
		s.list.AddChild(tui.NewText(t.Fg("muted", "  No matching models"), 0, 0, nil))
	default:
		s.list.AddChild(spacer())
		s.list.AddChild(tui.NewText(t.Fg("muted", "  Model Name: "+s.filtered[s.selected].ID), 0, 0, nil))
	}
	if s.refreshStatus != "" {
		color := "muted"
		if s.refreshSuccess {
			color = "success"
		}
		s.list.AddChild(spacer())
		s.list.AddChild(tui.NewText(t.Fg(color, "  "+s.refreshStatus), 0, 0, nil))
	}
}

// HandleKey moves the selection, switches scope, chooses, cancels, or searches.
func (s *ModelSelector) HandleKey(k tui.Key) {
	kb := s.env.Keys
	switch {
	case kb.Matches(k, "tui.input.tab"):
		if len(s.scoped) > 0 {
			s.setScope(!s.useScoped)
		}
	case kb.Matches(k, "tui.select.up"):
		if len(s.filtered) > 0 {
			s.selected = wrapUp(s.selected, len(s.filtered))
			s.updateList()
		}
	case kb.Matches(k, "tui.select.down"):
		if len(s.filtered) > 0 {
			s.selected = wrapDown(s.selected, len(s.filtered))
			s.updateList()
		}
	case kb.Matches(k, "tui.select.confirm"):
		if s.selected < len(s.filtered) {
			s.choose(s.filtered[s.selected])
		}
	case kb.Matches(k, "tui.select.cancel"):
		s.Dispose()
		s.onCancel()
	default:
		s.search.HandleKey(k)
		s.filter(s.search.Value())
	}
}

func (s *ModelSelector) choose(model ai.ModelRef) {
	s.Dispose()
	s.onSelect(model)
}

// ScopedModelsCallbacks are what a ScopedModelsSelector reports.
type ScopedModelsCallbacks struct {
	// OnChange receives the enabled ids in order whenever they change, nil
	// meaning all of them. It lasts for this session.
	OnChange func(enabled []string)
	// OnPersist is the same, when the user asks for it to be saved.
	OnPersist func(enabled []string)
	OnCancel  func()
}

// ScopedModelsSelector chooses which models cycling goes through, and in what order
// — Pi's ScopedModelsSelectorComponent.
type ScopedModelsSelector struct {
	tui.Container
	env *Env

	known      map[string]ai.ModelRef
	allIDs     []string
	enabled    []string
	allEnabled bool
	filtered   []scopedItem
	selected   int
	maxVisible int
	dirty      bool

	search tui.Input
	list   tui.Container
	footer *tui.Text
	status *tui.Text

	callbacks ScopedModelsCallbacks
}

type scopedItem struct {
	fullID  string
	model   ai.ModelRef
	known   bool
	enabled bool
}

// fullModelID is how a model is named in a scope.
func fullModelID(m ai.ModelRef) string { return m.Provider + "/" + m.ID }

// NewScopedModelsSelector shows models, with enabled nil meaning all of them.
// refreshStatus, when not empty, says a refresh is under way.
func NewScopedModelsSelector(env *Env, models []ai.ModelRef, enabled []string, refreshStatus string,
	callbacks ScopedModelsCallbacks) *ScopedModelsSelector {

	t := env.Theme
	s := &ScopedModelsSelector{env: env, known: map[string]ai.ModelRef{}, maxVisible: 8, callbacks: callbacks}
	s.setModels(models)
	s.enabled, s.allEnabled = slices.Clone(enabled), enabled == nil
	s.filtered = s.buildItems()
	s.search.Keybindings = env.Keys

	s.AddChild(newBorder(env))
	s.AddChild(spacer())
	s.AddChild(tui.NewText(t.Fg("accent", Bold("Model Configuration")), 0, 0, nil))
	s.AddChild(tui.NewText(t.Fg("muted", "Session-only. "+env.KeyText("app.models.save")+" to save to settings."), 0, 0, nil))
	s.AddChild(spacer())
	s.AddChild(&s.search)
	s.AddChild(spacer())
	s.AddChild(&s.list)
	s.AddChild(spacer())
	if refreshStatus != "" {
		s.status = tui.NewText(t.Fg("muted", "  "+refreshStatus), 0, 0, nil)
		s.AddChild(s.status)
	}
	s.footer = tui.NewText(s.footerText(), 0, 0, nil)
	s.AddChild(s.footer)
	s.AddChild(newBorder(env))
	s.updateList()
	return s
}

func (s *ScopedModelsSelector) setModels(models []ai.ModelRef) {
	s.known = map[string]ai.ModelRef{}
	s.allIDs = nil
	for _, m := range models {
		id := fullModelID(m)
		s.known[id] = m
		s.allIDs = append(s.allIDs, id)
	}
}

// UpdateModels replaces the models after a refresh, keeping the selection on the
// same one if it is still there.
func (s *ScopedModelsSelector) UpdateModels(models []ai.ModelRef) {
	selected := ""
	if s.selected < len(s.filtered) {
		selected = s.filtered[s.selected].fullID
	}
	s.setModels(models)
	s.refresh()
	for i, item := range s.filtered {
		if item.fullID == selected {
			s.selected = i
			s.updateList()
			break
		}
	}
}

// SetRefreshStatus says how a refresh went.
func (s *ScopedModelsSelector) SetRefreshStatus(message, color string) {
	if s.status != nil {
		s.status.SetText(s.env.Theme.Fg(color, "  "+message))
	}
}

// SetFocused passes focus to the search.
func (s *ScopedModelsSelector) SetFocused(focused bool) { s.search.SetFocused(focused) }

func (s *ScopedModelsSelector) isEnabled(id string) bool {
	return s.allEnabled || slices.Contains(s.enabled, id)
}

func (s *ScopedModelsSelector) buildItems() []scopedItem {
	ids := s.allIDs
	if !s.allEnabled {
		ids = slices.Clone(s.enabled)
		for _, id := range s.allIDs {
			if !slices.Contains(s.enabled, id) {
				ids = append(ids, id)
			}
		}
	}
	items := make([]scopedItem, 0, len(ids))
	for _, id := range ids {
		model, known := s.known[id]
		items = append(items, scopedItem{fullID: id, model: model, known: known, enabled: s.isEnabled(id)})
	}
	return items
}

func (s *ScopedModelsSelector) footerText() string {
	t := s.env.Theme
	count := "all enabled"
	if !s.allEnabled {
		available, unavailable := 0, 0
		for _, id := range s.enabled {
			if _, known := s.known[id]; known {
				available++
			} else {
				unavailable++
			}
		}
		count = strconv.Itoa(available) + "/" + strconv.Itoa(len(s.allIDs)) + " enabled"
		if unavailable > 0 {
			count += " · " + strconv.Itoa(unavailable) + " unavailable"
		}
	}
	key := s.env.KeyText
	parts := strings.Join([]string{
		key("tui.select.confirm") + " toggle",
		key("app.models.enableAll") + " all",
		key("app.models.clearAll") + " clear",
		key("app.models.toggleProvider") + " provider",
		key("app.models.reorderUp") + "/" + key("app.models.reorderDown") + " reorder",
		key("app.models.save") + " save",
		count,
	}, " · ")
	if s.dirty {
		return t.Fg("dim", "  "+parts+" ") + t.Fg("warning", "(unsaved)")
	}
	return t.Fg("dim", "  "+parts)
}

func (s *ScopedModelsSelector) refresh() {
	items := s.buildItems()
	if query := s.search.Value(); query != "" {
		items = tui.FuzzyFilter(items, query, func(item scopedItem) string {
			if item.known {
				m := item.model
				return m.ID + " " + m.Provider + " " + m.Provider + "/" + m.ID + " " + m.Provider + " " + m.ID + " " + m.ID
			}
			return item.fullID
		})
	}
	s.filtered = items
	s.selected = min(s.selected, max(0, len(s.filtered)-1))
	s.updateList()
	s.footer.SetText(s.footerText())
}

func (s *ScopedModelsSelector) current() []string {
	if s.allEnabled {
		return nil
	}
	// Never nil: "no models enabled" is a different answer from "all of them".
	return append([]string{}, s.enabled...)
}

func (s *ScopedModelsSelector) changed() {
	s.dirty = true
	s.refresh()
	s.callbacks.OnChange(s.current())
}

func (s *ScopedModelsSelector) updateList() {
	t := s.env.Theme
	s.list.Clear()
	if len(s.filtered) == 0 {
		s.list.AddChild(tui.NewText(t.Fg("muted", "  No matching models"), 0, 0, nil))
		return
	}
	start, end := windowOf(s.selected, len(s.filtered), s.maxVisible)
	for i := start; i < end; i++ {
		item := s.filtered[i]
		selected := i == s.selected
		prefix, text := "  ", item.fullID
		if item.known {
			text = item.model.ID
		}
		if selected {
			prefix, text = t.Fg("accent", "→ "), t.Fg("accent", text)
		}
		badge := t.Fg("muted", " [unavailable]")
		status := t.Fg("dim", " ✗")
		if item.known {
			badge = t.Fg("muted", " ["+item.model.Provider+"]")
			switch {
			case s.allEnabled:
				status = ""
			case item.enabled:
				status = t.Fg("success", " ✓")
			}
		}
		s.list.AddChild(tui.NewText(prefix+text+badge+status, 0, 0, nil))
	}
	if start > 0 || end < len(s.filtered) {
		s.list.AddChild(tui.NewText(t.Fg("muted", position(s.selected, len(s.filtered))), 0, 0, nil))
	}
	selected := s.filtered[s.selected]
	name := "Model unavailable"
	if selected.known {
		name = "Model Name: " + selected.model.ID
	}
	s.list.AddChild(spacer())
	s.list.AddChild(tui.NewText(t.Fg("muted", "  "+name), 0, 0, nil))
}

func (s *ScopedModelsSelector) toggle(id string) {
	if s.allEnabled {
		// The first toggle starts from only this one.
		s.allEnabled, s.enabled = false, []string{id}
		return
	}
	if at := slices.Index(s.enabled, id); at >= 0 {
		s.enabled = slices.Delete(s.enabled, at, at+1)
		return
	}
	s.enabled = append(s.enabled, id)
}

func (s *ScopedModelsSelector) enableAll(targets []string) {
	if s.allEnabled {
		return
	}
	if targets == nil {
		targets = s.allIDs
	}
	for _, id := range targets {
		if !slices.Contains(s.enabled, id) {
			s.enabled = append(s.enabled, id)
		}
	}
	if len(s.enabled) == len(s.allIDs) && !slices.ContainsFunc(s.enabled, func(id string) bool {
		return !slices.Contains(s.allIDs, id)
	}) {
		s.allEnabled, s.enabled = true, nil
	}
}

func (s *ScopedModelsSelector) clearAll(targets []string) {
	if s.allEnabled {
		s.allEnabled = false
		s.enabled = []string{}
		if targets != nil {
			for _, id := range s.allIDs {
				if !slices.Contains(targets, id) {
					s.enabled = append(s.enabled, id)
				}
			}
		}
		return
	}
	if targets == nil {
		s.enabled = []string{}
		return
	}
	s.enabled = slices.DeleteFunc(s.enabled, func(id string) bool { return slices.Contains(targets, id) })
}

// searchTargets is what "all" and "clear" apply to: what a search left showing, or
// nil for everything.
func (s *ScopedModelsSelector) searchTargets() []string {
	if s.search.Value() == "" {
		return nil
	}
	targets := make([]string, 0, len(s.filtered))
	for _, item := range s.filtered {
		targets = append(targets, item.fullID)
	}
	return targets
}

// HandleKey moves, reorders, toggles, saves, cancels or searches.
func (s *ScopedModelsSelector) HandleKey(k tui.Key) {
	kb := s.env.Keys
	up, down := kb.Matches(k, "app.models.reorderUp"), kb.Matches(k, "app.models.reorderDown")
	switch {
	case kb.Matches(k, "tui.select.up"):
		if len(s.filtered) > 0 {
			s.selected = wrapUp(s.selected, len(s.filtered))
			s.updateList()
		}
	case kb.Matches(k, "tui.select.down"):
		if len(s.filtered) > 0 {
			s.selected = wrapDown(s.selected, len(s.filtered))
			s.updateList()
		}
	case up, down:
		if s.allEnabled || s.selected >= len(s.filtered) {
			return
		}
		item := s.filtered[s.selected]
		if !s.isEnabled(item.fullID) {
			return
		}
		delta := 1
		if up {
			delta = -1
		}
		at := slices.Index(s.enabled, item.fullID)
		next := at + delta
		if next >= 0 && next < len(s.enabled) {
			s.enabled[at], s.enabled[next] = s.enabled[next], s.enabled[at]
			s.selected += delta
			s.changed()
		}
	case kb.Matches(k, "tui.select.confirm"):
		if s.selected < len(s.filtered) {
			s.toggle(s.filtered[s.selected].fullID)
			s.changed()
		}
	case kb.Matches(k, "app.models.enableAll"):
		s.enableAll(s.searchTargets())
		s.changed()
	case kb.Matches(k, "app.models.clearAll"):
		s.clearAll(s.searchTargets())
		s.changed()
	case kb.Matches(k, "app.models.toggleProvider"):
		if s.selected >= len(s.filtered) || !s.filtered[s.selected].known {
			return
		}
		provider := s.filtered[s.selected].model.Provider
		var ids []string
		for _, id := range s.allIDs {
			if s.known[id].Provider == provider {
				ids = append(ids, id)
			}
		}
		if !slices.ContainsFunc(ids, func(id string) bool { return !s.isEnabled(id) }) {
			s.clearAll(ids)
		} else {
			s.enableAll(ids)
		}
		s.changed()
	case kb.Matches(k, "app.models.save"):
		s.callbacks.OnPersist(s.current())
		s.dirty = false
		s.footer.SetText(s.footerText())
	case tui.KeyMatches(k, "ctrl+c"):
		if s.search.Value() != "" {
			s.search.SetValue("")
			s.refresh()
		} else {
			s.callbacks.OnCancel()
		}
	case tui.KeyMatches(k, "escape"):
		s.callbacks.OnCancel()
	default:
		s.search.HandleKey(k)
		s.refresh()
	}
}
