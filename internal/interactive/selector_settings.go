package interactive

import (
	"strconv"

	"github.com/iamclancyliang/pi-go/internal/tui"
)

// SettingsValues are what the settings selector shows.
type SettingsValues struct {
	AutoCompact            bool
	SteeringMode           string
	FollowUpMode           string
	Thinking               string
	AvailableThinking      []string
	HideThinking           bool
	QuietStartup           bool
	DefaultProjectTrust    string
	DoubleEscapeAction     string
	TreeFilterMode         string
	ShowHardwareCursor     bool
	EditorPaddingX         int
	OutputPad              int
	AutocompleteMaxVisible int
	ClearOnShrink          bool
}

// Setting ids, as the settings selector and its host name them.
const (
	SettingAutoCompact      = "autocompact"
	SettingHardwareCursor   = "show-hardware-cursor"
	SettingEditorPadding    = "editor-padding"
	SettingOutputPadding    = "output-padding"
	SettingAutocomplete     = "autocomplete-max-visible"
	SettingClearOnShrink    = "clear-on-shrink"
	SettingSteeringMode     = "steering-mode"
	SettingFollowUpMode     = "follow-up-mode"
	SettingHideThinking     = "hide-thinking"
	SettingQuietStartup     = "quiet-startup"
	SettingProjectTrust     = "default-project-trust"
	SettingDoubleEscape     = "double-escape-action"
	SettingTreeFilter       = "tree-filter-mode"
	SettingThinking         = "thinking"
	defaultTrustAsk         = "Ask"
	defaultTrustAlways      = "Always trust"
	defaultTrustNever       = "Never trust"
	settingsSubmenuMinWidth = 12
	settingsSubmenuMaxWidth = 32
)

var thinkingDescriptions = map[string]string{
	"off":     "No reasoning",
	"minimal": "Very brief reasoning (~1k tokens)",
	"low":     "Light reasoning (~2k tokens)",
	"medium":  "Moderate reasoning (~8k tokens)",
	"high":    "Deep reasoning (~16k tokens)",
	"xhigh":   "Extra-high reasoning (~32k tokens)",
	"max":     "Maximum reasoning",
}

// trustLabel and trustValue translate the default project trust setting.
func trustLabel(value string) string {
	switch value {
	case "always":
		return defaultTrustAlways
	case "never":
		return defaultTrustNever
	}
	return defaultTrustAsk
}

func trustValue(label string) string {
	switch label {
	case defaultTrustAlways:
		return "always"
	case defaultTrustNever:
		return "never"
	}
	return "ask"
}

func boolText(b bool) string { return strconv.FormatBool(b) }

// selectSubmenu is a titled list a settings item opens — Pi's SelectSubmenu.
type selectSubmenu struct {
	tui.Container
	list *tui.SelectList
}

func newSelectSubmenu(env *Env, title, description string, options []tui.SelectItem, current string,
	onSelect func(string), onCancel func()) *selectSubmenu {

	t := env.Theme
	s := &selectSubmenu{}
	s.AddChild(tui.NewText(Bold(t.Fg("accent", title)), 0, 0, nil))
	if description != "" {
		s.AddChild(spacer())
		s.AddChild(tui.NewText(t.Fg("muted", description), 0, 0, nil))
	}
	s.AddChild(spacer())
	s.list = tui.NewSelectList(options, min(len(options), 10), t.SelectListTheme(),
		tui.SelectListLayout{MinPrimaryColumnWidth: settingsSubmenuMinWidth, MaxPrimaryColumnWidth: settingsSubmenuMaxWidth})
	s.list.Keybindings = env.Keys
	for i, option := range options {
		if option.Value == current {
			s.list.SetSelectedIndex(i)
		}
	}
	s.list.OnSelect = func(item tui.SelectItem) { onSelect(item.Value) }
	s.list.OnCancel = onCancel
	s.AddChild(s.list)
	s.AddChild(spacer())
	s.AddChild(tui.NewText(t.Fg("dim", "  Enter to select · Esc to go back"), 0, 0, nil))
	return s
}

func (s *selectSubmenu) HandleKey(k tui.Key) { s.list.HandleKey(k) }

// SettingsSelector changes settings — Pi's SettingsSelectorComponent, with the items
// for what this build has. A setting for a subsystem that is not here would be one
// the user believes is on (D-8).
type SettingsSelector struct {
	tui.Container
	list *tui.SettingsList
}

// NewSettingsSelector lists the settings. onChange receives an item's id and new
// value; onCancel closes it.
func NewSettingsSelector(env *Env, values SettingsValues, onChange func(id, value string), onCancel func()) *SettingsSelector {
	t := env.Theme
	onOff := []string{"true", "false"}
	queueModes := []string{"one-at-a-time", "all"}
	followUpKey := env.formatKeyText(joinKeys(env.Keys.Keys("app.message.followUp")), true)

	var thinking []tui.SelectItem
	for _, level := range values.AvailableThinking {
		thinking = append(thinking, tui.SelectItem{Value: level, Label: level, Description: thinkingDescriptions[level]})
	}
	items := []*tui.SettingItem{
		{ID: SettingAutoCompact, Label: "Auto-compact", Description: "Automatically compact context when it gets too large",
			CurrentValue: boolText(values.AutoCompact), Values: onOff},
		{ID: SettingHardwareCursor, Label: "Show hardware cursor", Description: "Show the terminal cursor while still positioning it for IME support",
			CurrentValue: boolText(values.ShowHardwareCursor), Values: onOff},
		{ID: SettingEditorPadding, Label: "Editor padding", Description: "Horizontal padding for input editor (0-3)",
			CurrentValue: strconv.Itoa(values.EditorPaddingX), Values: []string{"0", "1", "2", "3"}},
		{ID: SettingOutputPadding, Label: "Output padding", Description: "Horizontal padding for user messages, assistant messages, and thinking",
			CurrentValue: strconv.Itoa(values.OutputPad), Values: []string{"0", "1"}},
		{ID: SettingAutocomplete, Label: "Autocomplete max items", Description: "Max visible items in autocomplete dropdown (3-20)",
			CurrentValue: strconv.Itoa(values.AutocompleteMaxVisible), Values: []string{"3", "5", "7", "10", "15", "20"}},
		{ID: SettingClearOnShrink, Label: "Clear on shrink", Description: "Clear empty rows when content shrinks (may cause flicker)",
			CurrentValue: boolText(values.ClearOnShrink), Values: onOff},
		{ID: SettingSteeringMode, Label: "Steering mode",
			Description:  "Enter while streaming queues steering messages. 'one-at-a-time': deliver one, wait for response. 'all': deliver all at once.",
			CurrentValue: values.SteeringMode, Values: queueModes},
		{ID: SettingFollowUpMode, Label: "Follow-up mode",
			Description:  followUpKey + " queues follow-up messages until agent stops. 'one-at-a-time': deliver one, wait for response. 'all': deliver all at once.",
			CurrentValue: values.FollowUpMode, Values: queueModes},
		{ID: SettingHideThinking, Label: "Hide thinking", Description: "Hide thinking blocks in assistant responses",
			CurrentValue: boolText(values.HideThinking), Values: onOff},
		{ID: SettingQuietStartup, Label: "Quiet startup", Description: "Disable verbose printing at startup",
			CurrentValue: boolText(values.QuietStartup), Values: onOff},
		{ID: SettingProjectTrust, Label: "Default project trust", Description: "Fallback behavior when no extension or saved trust decision decides project trust",
			CurrentValue: trustLabel(values.DefaultProjectTrust), Values: []string{defaultTrustAsk, defaultTrustAlways, defaultTrustNever}},
		{ID: SettingDoubleEscape, Label: "Double-escape action", Description: "Action when pressing Escape twice with empty editor",
			CurrentValue: values.DoubleEscapeAction, Values: []string{"tree", "fork", "none"}},
		{ID: SettingTreeFilter, Label: "Tree filter mode", Description: "Default filter when opening /tree",
			CurrentValue: values.TreeFilterMode, Values: treeFilterModes},
		{ID: SettingThinking, Label: "Thinking level", Description: "Reasoning depth for thinking-capable models",
			CurrentValue: values.Thinking,
			Submenu: func(current string, done func(string, bool)) tui.Component {
				return newSelectSubmenu(env, "Thinking Level", "Select reasoning depth for thinking-capable models",
					thinking, current, func(value string) { done(value, true) }, func() { done("", false) })
			}},
	}

	s := &SettingsSelector{}
	s.list = tui.NewSettingsList(items, 10, tui.SettingsListTheme{
		Label: func(text string, selected bool) string {
			if selected {
				return t.Fg("accent", text)
			}
			return text
		},
		Value: func(text string, selected bool) string {
			if selected {
				return t.Fg("accent", text)
			}
			return t.Fg("muted", text)
		},
		Description: t.FgStyle("dim"),
		Cursor:      t.Fg("accent", "→ "),
		Hint:        t.FgStyle("dim"),
	}, onChange, onCancel, true)
	s.list.Keybindings = env.Keys
	s.AddChild(newBorder(env))
	s.AddChild(s.list)
	s.AddChild(newBorder(env))
	return s
}

func joinKeys(keys []string) string {
	out := ""
	for i, k := range keys {
		if i > 0 {
			out += "/"
		}
		out += k
	}
	return out
}

// SetFocused passes focus to the list's search.
func (s *SettingsSelector) SetFocused(focused bool) { s.list.SetFocused(focused) }

// HandleKey goes to the list.
func (s *SettingsSelector) HandleKey(k tui.Key) { s.list.HandleKey(k) }
