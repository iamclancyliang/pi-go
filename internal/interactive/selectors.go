package interactive

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/session"
	"github.com/iamclancyliang/pi-go/internal/tui"
)

// Selectors is the half of the Host that stands behind a choice: what each selector
// lists, and what choosing does. Everything here is called on the interface's loop
// except where it says otherwise, and none of it is called while a command or a run
// is under way.
type Selectors interface {
	// CurrentModel is what answers now; ConfiguredModels what a credential here
	// can reach, as far as anything records; ScopedModels what cycling goes
	// through, empty for everything.
	CurrentModel() ai.ModelRef
	ConfiguredModels() []ai.ModelRef
	ScopedModels() []ai.ModelRef
	// ModelRefresh is how to ask the providers for their models, off the loop, or
	// nil when no provider here can be asked.
	ModelRefresh() ModelRefresh
	// SelectModel answers from the next turn with a model, and remembers it as the
	// default.
	SelectModel(ai.ModelRef) error
	// EnabledModels are the ids in the scope, nil meaning every model. Ids nothing
	// configured offers are kept, as the selector shows them unavailable.
	EnabledModels() []string
	// SetScopedModels sets the scope for this session; SaveEnabledModels for later
	// ones too.
	SetScopedModels(enabled []string)
	SaveEnabledModels(enabled []string) error
	// CycleModel moves to the next or previous model in the scope, ok false when
	// there is no other.
	CycleModel(forward bool) (ai.ModelRef, bool, error)
	// CycleThinking moves to the next level the model offers, ok false when it
	// cannot reason.
	CycleThinking() (string, bool, error)

	// SessionLoaders read this directory's sessions and everyone's, off the loop.
	SessionLoaders() (current, all SessionLoader)
	CurrentSessionPath() string
	// ResumeSession opens another conversation, off the loop, and may say something
	// about it.
	ResumeSession(path string) (note string, err error)
	// RenameSession and DeleteSession run off the loop.
	RenameSession(path, name string) error
	DeleteSession(path string) (trashed bool, err error)

	// TreeNodes is the whole conversation, and where it stands.
	TreeNodes() ([]session.Node, string, error)
	SetEntryLabel(id, label string) error
	// NavigateTree moves to an entry, off the loop, summarising the branch left
	// behind when asked, and returns the text to put back in the editor. It stops
	// with context.Canceled if ctx is cancelled before it has moved.
	NavigateTree(ctx context.Context, id string, summarize bool, instructions string) (editorText string, err error)
	BranchSummarySkipPrompt() bool
	// CopyToClipboard and ReadClipboard run off the loop.
	CopyToClipboard(text string) error
	ReadClipboard() (string, error)
	// LastAssistantText is the assistant's last words, for copying.
	LastAssistantText() (string, bool)
	// EditExternally hands text to the user's editor and returns what it left,
	// ok false when the editor exited with an error. It runs with the terminal
	// given up, on the loop.
	EditExternally(content string) (edited string, ok bool, err error)

	// ForkMessages is what can be forked from; Fork copies the conversation up to
	// just before one, off the loop, and returns its words.
	ForkMessages() ([]ForkMessage, error)
	Fork(id string) (editorText string, err error)

	SettingsValues() SettingsValues
	// ChangeSetting applies and saves one, by the ids this package names. What only
	// the interface can do about it the mode does itself.
	ChangeSetting(id, value string) error

	// LoginProviders is every provider, sorted by name; SavedProviders those with a
	// stored credential.
	LoginProviders() []AuthProvider
	SavedProviders() ([]AuthProvider, error)
	SaveCredential(provider, key string) (where string, err error)
	Logout(provider string) (message string, err error)

	// TrustState is the working directory in its canonical form, the decision on
	// record that covers it, and whether this session trusts it.
	TrustState() (cwd string, saved *TrustSaved, trusted bool)
	SaveTrust(TrustOption) error
}

// showSelector puts a selector where the editor is, until done is called — Pi's
// showSelector. create is given done and returns what to draw, what takes the keys,
// and what to do when it goes away.
func (m *Mode) showSelector(create func(done func()) (component, focus tui.Component, dispose func())) {
	token := new(int)
	var dispose func()
	done := func() {
		if dispose != nil {
			dispose()
		}
		if m.selectorToken != token {
			return
		}
		m.selectorToken, m.selectorDispose = nil, nil
		m.editorC.Clear()
		m.editorC.AddChild(m.editor)
		m.ui.SetFocus(m.editor)
		m.ui.RequestRender()
	}
	component, focus, created := create(done)
	dispose = created
	if m.selectorDispose != nil {
		m.selectorDispose()
	}
	m.selectorToken, m.selectorDispose = token, created
	m.editorC.Clear()
	m.editorC.AddChild(component)
	m.ui.SetFocus(focus)
	m.ui.RequestRender()
}

// openSelector opens what a bare command means here, and reports whether it did.
// With an argument a command is the command the line prompt has, except /login,
// whose key cannot be asked for on a terminal the interface is drawing on.
func (m *Mode) openSelector(text string) bool {
	name, arg, _ := strings.Cut(strings.TrimPrefix(text, "/"), " ")
	arg = strings.TrimSpace(arg)
	if name == "login" {
		m.handleLogin(arg)
		return true
	}
	if name == "hotkeys" {
		m.showHotkeys()
		return true
	}
	if arg != "" {
		return false
	}
	switch name {
	case "model":
		m.showModelSelector("")
	case "scoped-models":
		m.showScopedModels()
	case "resume":
		m.showSessionSelector()
	case "tree":
		m.showTreeSelector("")
	case "fork":
		m.showForkSelector()
	case "settings":
		m.showSettings()
	case "trust":
		m.showTrust()
	case "logout":
		m.showLogout()
	default:
		return false
	}
	return true
}

// busy says the conversation is being worked on, which a selector must wait for: a
// choice in one acts on what the run or the command is writing.
func (m *Mode) busy() bool {
	if m.running || m.commandBusy {
		m.showStatus("Wait for the agent to finish, or press " + m.env.KeyText("app.interrupt") + " to stop it")
		return true
	}
	return false
}

// conversationChanged draws a conversation that has been replaced.
func (m *Mode) conversationChanged() {
	m.compacted, m.contextTokens, m.contextKnown = false, 0, false
	m.renderHistory()
}

// --- models ---

func (m *Mode) showModelSelector(initialSearch string) {
	if m.busy() {
		return
	}
	m.showSelector(func(done func()) (tui.Component, tui.Component, func()) {
		var selector *ModelSelector
		selector = NewModelSelector(m.env, m.host.CurrentModel(), m.host.ConfiguredModels(), m.host.ScopedModels(),
			initialSearch, m.host.ModelRefresh(),
			func(model ai.ModelRef) {
				err := m.host.SelectModel(model)
				done()
				if err != nil {
					m.showError(err.Error())
					return
				}
				m.showStatus("Model: " + model.ID)
			},
			func() { done() })
		return selector, selector, selector.Dispose
	})
}

func (m *Mode) showScopedModels() {
	if m.busy() {
		return
	}
	m.showSelector(func(done func()) (tui.Component, tui.Component, func()) {
		ctx, cancel := context.WithCancel(context.Background())
		models := m.host.ConfiguredModels()
		refresh := m.host.ModelRefresh()
		status := ""
		if refresh != nil {
			status = "Refreshing model catalogs…"
		}
		selector := NewScopedModelsSelector(m.env, models, m.host.EnabledModels(), status, ScopedModelsCallbacks{
			OnChange: m.host.SetScopedModels,
			OnPersist: func(enabled []string) {
				if err := m.host.SaveEnabledModels(enabled); err != nil {
					m.showError(err.Error())
					return
				}
				m.showStatus("Model selection saved to settings")
			},
			OnCancel: func() { done() },
		})
		if refresh != nil {
			go func() {
				timed, stop := context.WithTimeout(ctx, modelRefreshTimeout)
				defer stop()
				listed, failed, err := refresh(timed)
				timedOut := errors.Is(timed.Err(), context.DeadlineExceeded)
				m.ui.Do(func() {
					if ctx.Err() != nil {
						return
					}
					switch {
					case timedOut:
						selector.SetRefreshStatus("Model refresh timed out; showing cached models.", "warning")
					case err != nil:
						selector.SetRefreshStatus("Could not refresh model catalogs: "+err.Error(), "warning")
					case len(failed) > 0:
						selector.UpdateModels(listed)
						selector.SetRefreshStatus("Could not refresh "+strings.Join(failed, ", ")+"; showing cached models.", "warning")
					default:
						selector.UpdateModels(listed)
						selector.SetRefreshStatus("Model catalogs refreshed.", "success")
					}
					m.ui.RequestRender()
				})
			}()
		}
		return selector, selector, cancel
	})
}

// cycleModel is the model keys.
func (m *Mode) cycleModel(forward bool) {
	if m.busy() {
		return
	}
	model, ok, err := m.host.CycleModel(forward)
	switch {
	case err != nil:
		m.showError(err.Error())
	case !ok && len(m.host.ScopedModels()) > 0:
		m.showStatus("Only one model in scope")
	case !ok:
		m.showStatus("Only one model available")
	default:
		m.showStatus("Switched to " + model.ID)
	}
}

// cycleThinking is the thinking key.
func (m *Mode) cycleThinking() {
	level, ok, err := m.host.CycleThinking()
	switch {
	case err != nil:
		m.showError(err.Error())
	case !ok:
		m.showStatus("Current model does not support thinking")
	default:
		m.showStatus("Thinking level: " + level)
	}
}

// --- sessions ---

func (m *Mode) showSessionSelector() {
	if m.busy() {
		return
	}
	m.showSelector(func(done func()) (tui.Component, tui.Component, func()) {
		current, all := m.host.SessionLoaders()
		selector := NewSessionSelector(m.env, SessionSelectorOptions{
			Current:     current,
			All:         all,
			CurrentPath: m.host.CurrentSessionPath(),
			OnSelect: func(path string) {
				done()
				m.resumeSession(path)
			},
			OnCancel: func() { done() },
			Rename:   m.host.RenameSession,
			Delete:   m.host.DeleteSession,
		})
		return selector, selector, nil
	})
}

func (m *Mode) resumeSession(path string) {
	m.commandBusy = true
	go func() {
		note, err := m.host.ResumeSession(path)
		m.ui.Do(func() {
			m.commandBusy = false
			if err != nil {
				m.showError(err.Error())
				return
			}
			m.conversationChanged()
			m.showStatus("Resumed session")
			if note != "" {
				m.showWarning(note)
			}
		})
	}()
}

// --- the tree ---

func (m *Mode) showTreeSelector(initial string) {
	if m.busy() {
		return
	}
	nodes, leaf, err := m.host.TreeNodes()
	if err != nil {
		m.showError(err.Error())
		return
	}
	if len(nodes) == 0 {
		m.showStatus("No entries in session")
		return
	}
	m.showSelector(func(done func()) (tui.Component, tui.Component, func()) {
		selector := NewTreeSelector(m.env, nodes, leaf, m.ui.Rows(), initial, m.host.SettingsValues().TreeFilterMode,
			func(id string) {
				done()
				if id == leaf {
					m.showStatus("Already at this point")
					return
				}
				if m.host.BranchSummarySkipPrompt() {
					m.navigate(id, false, "")
					return
				}
				m.askBranchSummary(id)
			},
			func() { done() },
			func(id, label string) {
				if err := m.host.SetEntryLabel(id, label); err != nil {
					m.showError(err.Error())
				}
				m.ui.RequestRender()
			})
		selector.OnCopy = func(text string, ok bool) {
			if !ok {
				m.showError("Selected entry has no text to copy")
				return
			}
			go func() {
				err := m.host.CopyToClipboard(text)
				m.ui.Do(func() {
					if err != nil {
						m.showError(err.Error())
						return
					}
					m.showStatus("Copied selected message to clipboard")
				})
			}()
		}
		return selector, selector, nil
	})
}

// askBranchSummary asks what to do about the branch being left. Backing out of the
// question goes back to the tree, on the same entry.
func (m *Mode) askBranchSummary(id string) {
	const (
		noSummary = "No summary"
		summarize = "Summarize"
		custom    = "Summarize with custom prompt"
	)
	m.showSelector(func(done func()) (tui.Component, tui.Component, func()) {
		dialog := NewChoiceDialog(m.env, "Summarize branch?", []string{noSummary, summarize, custom},
			func(choice string) {
				done()
				switch choice {
				case noSummary:
					m.navigate(id, false, "")
				case summarize:
					m.navigate(id, true, "")
				default:
					m.askBranchInstructions(id)
				}
			},
			func() {
				done()
				m.showTreeSelector(id)
			})
		return dialog, dialog, nil
	})
}

func (m *Mode) askBranchInstructions(id string) {
	m.showSelector(func(done func()) (tui.Component, tui.Component, func()) {
		dialog := NewEditorDialog(m.env, m.ui, "Custom summarization instructions",
			func(text string) {
				done()
				m.navigate(id, true, text)
			},
			func() {
				done()
				m.askBranchSummary(id)
			})
		return dialog, dialog, nil
	})
}

// navigate moves within the conversation. A summary takes a model call, so it runs
// with a spinner, and escape cancels it and goes back to the tree.
func (m *Mode) navigate(id string, summarize bool, instructions string) {
	ctx, cancel := context.WithCancel(context.Background())
	m.commandBusy = true
	if summarize {
		m.chat.AddChild(spacer())
		m.setStatus(branchSummaryStatus(m.env))
		m.cancelNavigation = cancel
		m.ui.RequestRender()
	}
	go func() {
		text, err := m.host.NavigateTree(ctx, id, summarize, instructions)
		cancelled := ctx.Err() != nil
		cancel()
		m.ui.Do(func() {
			m.commandBusy = false
			m.cancelNavigation = nil
			m.clearStatus("branchSummary")
			switch {
			case err != nil && cancelled:
				m.showStatus("Branch summarization cancelled")
				m.showTreeSelector(id)
			case err != nil:
				m.showError(err.Error())
			default:
				m.conversationChanged()
				if text != "" && strings.TrimSpace(m.editor.Text()) == "" {
					m.editor.SetText(text)
				}
				m.showStatus("Navigated to selected point")
			}
		})
	}()
}

// --- fork ---

func (m *Mode) showForkSelector() {
	if m.busy() {
		return
	}
	messages, err := m.host.ForkMessages()
	if err != nil {
		m.showError(err.Error())
		return
	}
	if len(messages) == 0 {
		m.showStatus("No messages to fork from")
		return
	}
	initial := messages[len(messages)-1].ID
	m.showSelector(func(done func()) (tui.Component, tui.Component, func()) {
		selector := NewForkSelector(m.env, messages, initial, func(id string) {
			done()
			m.commandBusy = true
			go func() {
				text, err := m.host.Fork(id)
				m.ui.Do(func() {
					m.commandBusy = false
					if err != nil {
						m.showError(err.Error())
						return
					}
					m.conversationChanged()
					m.editor.SetText(text)
					m.showStatus("Forked to new session")
				})
			}()
		}, func() { done() })
		return selector, selector, nil
	})
}

// --- settings ---

func (m *Mode) showSettings() {
	if m.busy() {
		return
	}
	m.showSelector(func(done func()) (tui.Component, tui.Component, func()) {
		selector := NewSettingsSelector(m.env, m.host.SettingsValues(), m.changeSetting, func() { done() })
		return selector, selector, nil
	})
}

func (m *Mode) changeSetting(id, value string) {
	if err := m.host.ChangeSetting(id, value); err != nil {
		m.showError(err.Error())
	}
	m.applySetting(id, value)
	m.ui.RequestRender()
}

// applySetting does what only the interface can about a setting.
func (m *Mode) applySetting(id, value string) {
	number, _ := strconv.Atoi(value)
	switch id {
	case SettingHideThinking:
		m.setHideThinking(value == "true")
	case SettingHardwareCursor:
		m.ui.SetShowHardwareCursor(value == "true")
	case SettingClearOnShrink:
		m.ui.SetClearOnShrink(value == "true")
	case SettingEditorPadding:
		m.editor.SetPaddingX(number)
	case SettingAutocomplete:
		m.editor.SetAutocompleteMaxVisible(number)
	case SettingOutputPadding:
		m.outputPad = number
		m.renderHistory()
	case SettingDoubleEscape:
		m.opts.DoubleEscapeAction = value
	}
}

// --- login ---

// handleLogin takes a provider's name or id, going straight to the dialog when it
// names one and to the list, searched for it, when it does not.
func (m *Mode) handleLogin(ref string) {
	if m.busy() {
		return
	}
	providers := m.host.LoginProviders()
	wanted := strings.ToLower(strings.TrimSpace(ref))
	if wanted != "" {
		for _, p := range providers {
			if strings.ToLower(p.ID) == wanted || strings.ToLower(p.Name) == wanted {
				m.showLoginDialog(p)
				return
			}
		}
	}
	if len(providers) == 0 {
		m.showStatus("No login providers available.")
		return
	}
	m.showSelector(func(done func()) (tui.Component, tui.Component, func()) {
		selector := NewAuthSelector(m.env, false, providers, ref, func(p AuthProvider) {
			done()
			m.showLoginDialog(p)
		}, func() { done() })
		return selector, selector, nil
	})
}

func (m *Mode) showLoginDialog(p AuthProvider) {
	m.showSelector(func(done func()) (tui.Component, tui.Component, func()) {
		dialog := NewLoginDialog(m.env, p.Name, "Enter API key", func(key string) {
			done()
			if strings.TrimSpace(key) == "" {
				// A saved empty key would look like being logged in while every
				// request failed as an authentication error.
				m.showStatus("Nothing entered; no credential was saved")
				return
			}
			where, err := m.host.SaveCredential(p.ID, key)
			if err != nil {
				m.showError("Failed to save API key for " + p.Name + ": " + err.Error())
				return
			}
			m.showStatus("Saved API key for " + p.Name + ". Credentials saved to " + where)
		}, func() { done() })
		return dialog, dialog, nil
	})
}

func (m *Mode) showLogout() {
	if m.busy() {
		return
	}
	providers, err := m.host.SavedProviders()
	if err != nil {
		m.showError("Could not read stored credentials: " + err.Error())
		return
	}
	if len(providers) == 0 {
		m.showStatus("No stored credentials to remove. /logout only removes credentials saved by /login; environment variables are unchanged.")
		return
	}
	m.showSelector(func(done func()) (tui.Component, tui.Component, func()) {
		selector := NewAuthSelector(m.env, true, providers, "", func(p AuthProvider) {
			done()
			message, err := m.host.Logout(p.ID)
			if err != nil {
				m.showError("Logout failed: " + err.Error())
				return
			}
			m.showStatus(message)
		}, func() { done() })
		return selector, selector, nil
	})
}

// --- trust ---

func (m *Mode) showTrust() {
	if m.busy() {
		return
	}
	cwd, saved, trusted := m.host.TrustState()
	m.showSelector(func(done func()) (tui.Component, tui.Component, func()) {
		selector := NewTrustSelector(m.env, cwd, saved, trusted, func(option TrustOption) {
			err := m.host.SaveTrust(option)
			done()
			if err != nil {
				m.showError(err.Error())
				return
			}
			word := "untrusted"
			if option.Trusted {
				word = "trusted"
			}
			m.showStatus("Saved trust decision: " + word + ". /reload applies it to this session.")
		}, func() { done() })
		return selector, selector, nil
	})
}
