package cli

import (
	"context"
	"fmt"
	"sync"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/compaction"
	"github.com/iamclancyliang/pi-go/internal/events"
	"github.com/iamclancyliang/pi-go/internal/runtime"
	"github.com/iamclancyliang/pi-go/internal/session"
	"github.com/iamclancyliang/pi-go/internal/tools"
)

// interactiveSession is a conversation with an agent, and the commands that
// act on them — what the line prompt and the full-screen interface share.
//
// The agent is rebuilt whenever the conversation or the model changes,
// because a loop holds the session and port it was built with: a command
// that opened another conversation while the agent still pointed at the old
// would write the next turn into the conversation the user just left.
type interactiveSession struct {
	// mu guards what a command may swap — the conversation, the model, the
	// agent — against an interface reading them while the command runs.
	mu sync.Mutex

	rt      Runtime
	streams Streams
	ctxt    *commandContext
	agent   *runtime.Agent
	current *Conversation
	// config is copied so commands may update it — /reload re-resolves —
	// without reaching back into the caller's value.
	config Config
	// The model can change mid-session, so what answers is held here rather
	// than read from the configuration each time.
	port     ai.Port
	provider string
	model    string

	// observers and replies are installed on every agent built, so an
	// interface watching the run keeps watching across rebuilds.
	observers []events.Observer
	replies   []runtime.ReplyObserver
}

func newInteractiveSession(ctx context.Context, rt Runtime, streams Streams,
	observers []events.Observer, replies []runtime.ReplyObserver) (*interactiveSession, error) {
	s := &interactiveSession{
		rt: rt, streams: streams, current: rt.Conversation, config: rt.Config,
		port: rt.Model, provider: rt.Provider, model: rt.ModelName,
		observers: observers, replies: replies,
	}
	if err := s.build(); err != nil {
		return nil, err
	}

	s.ctxt = &commandContext{
		ctx:          ctx,
		session:      s.current.Session,
		conversation: s.current,
		out:          streams.Out,
		errOut:       streams.Err,
		args:         rt.Args,
		workingDir:   rt.WorkingDir,
		config:       &s.config,

		tools:         rt.Tools,
		modelProvider: func() string { return s.provider },
		modelName:     func() string { return s.model },
	}
	if registered, ok := rt.Tools.Lookup("bash"); ok {
		if runner, ok := registered.(bashExecutor); ok {
			s.ctxt.bash = func(bashCtx context.Context, command string, exclude bool, onChunk func(string)) (tools.BashResult, error) {
				return executeBash(bashCtx, s.current.Session, runner, command, exclude, onChunk)
			}
		}
	}
	// What the port and the tool set were BUILT from, frozen at startup. The
	// live config moves — /settings edits it — so comparing a reload against
	// the live view would report "nothing changed" for exactly the changes
	// that only a new run can pick up.
	builtFrom := rt.Config.Effective
	s.ctxt.reloadConfig = func() (bool, error) {
		resolved, err := ResolveConfig(s.rt.Args, s.rt.WorkingDir, nil)
		if err != nil {
			return false, err
		}
		changed := resolved.Effective.DefaultProvider != builtFrom.DefaultProvider ||
			resolved.Effective.DefaultModel != builtFrom.DefaultModel ||
			resolved.Effective.ShellPath != builtFrom.ShellPath ||
			resolved.Effective.ShellCommandPrefix != builtFrom.ShellCommandPrefix ||
			resolved.Effective.SessionDir != builtFrom.SessionDir
		s.config = resolved
		return changed, nil
	}
	s.ctxt.compact = func(instructions string) error {
		// Summarised with whatever model is answering now, so a conversation
		// switched to a cheaper model is compacted by it too.
		c := &compaction.Compactor{
			Model: s.port, ModelName: s.model, Instructions: instructions,
			KeepRecentTokens: s.config.Effective.Compaction.KeepRecentTokens,
		}
		summary, kept, err := c.Summarize(ctx, s.current.Session.Truth())
		if err != nil {
			return err
		}
		return s.current.Session.Compact(summary, kept)
	}
	s.ctxt.switchModel = s.switchModel
	s.ctxt.availableModels = func(listCtx context.Context) ([]ai.ModelRef, error) {
		return listAvailableModels(listCtx, s.port, s.provider)
	}
	s.ctxt.cycleModel = func(cycleCtx context.Context) (*ai.ModelCycle, error) {
		return cycleModelsWithPatterns(cycleCtx, s.port, s.provider, s.model, s.rt.ScopedModels, s.rt.ModelPatterns,
			s.rt.Thinking, s.switchModel)
	}
	s.ctxt.reload = func() error {
		// Rebuilt from the store the conversation already has, rather than
		// reopened from its path: reopening would read the leaf back off disk
		// and undo the move that was just made in memory.
		restored, err := session.Restore(context.Background(), s.rt.System, s.current.Store)
		if err != nil {
			return err
		}
		s.mu.Lock()
		s.current.Session = restored
		s.mu.Unlock()
		s.ctxt.session = restored
		return s.build()
	}
	s.ctxt.reopen = s.reopen
	return s, nil
}

func (s *interactiveSession) build() error {
	if err := recordInUse(s.current.Session, s.provider, s.model, s.rt.Thinking); err != nil {
		return err
	}
	next, err := runtime.New(runtime.Config{
		Model:          s.port,
		ModelName:      s.model,
		Tools:          s.rt.Tools,
		Session:        s.current.Session,
		Thinking:       s.rt.Thinking,
		Retry:          s.rt.Retry,
		AutoCompaction: autoCompactionFor(s.rt.AutoCompaction, s.provider, s.model),
		SteeringMode:   s.rt.SteeringMode,
		FollowUpMode:   s.rt.FollowUpMode,
		Observers:      s.observers,
		ReplyObservers: s.replies,
		// The same summariser /compact uses. Without it an overflow is
		// terminal: the request is refused, and a conversation long enough
		// to overflow once will do it again on every turn after.
		Summarize: (&compaction.Compactor{Model: s.port, ModelName: s.model}).Summarize,
	})
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.agent = next
	s.mu.Unlock()
	return nil
}

// snapshot is what an interface reads while a command may be swapping it.
func (s *interactiveSession) snapshot() (current *Conversation, agent *runtime.Agent, provider, model string, thinking ai.ThinkingLevel) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current, s.agent, s.provider, s.model, s.rt.Thinking
}

func (s *interactiveSession) switchModel(toProvider, toModel string) error {
	next := s.rt.Args
	next.Model = toModel
	if toProvider != "" {
		next.Provider = toProvider
	} else {
		// No provider named means the one already answering, not whichever
		// credential happens to be found first.
		next.Provider = s.provider
	}
	opened, openedProvider, openedModel, err := s.rt.openModel(next)
	if err != nil {
		return err
	}
	oldPort, oldProvider, oldModel := s.port, s.provider, s.model
	s.setModel(opened, openedProvider, openedModel)
	if err := s.build(); err != nil {
		s.setModel(oldPort, oldProvider, oldModel)
		return err
	}
	s.rt.Args = next
	s.ctxt.args = next
	return nil
}

func (s *interactiveSession) setModel(port ai.Port, provider, model string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.port, s.provider, s.model = port, provider, model
}

func (s *interactiveSession) reopen(args Args) error {
	opened, err := OpenConversation(args, s.rt.WorkingDir, s.rt.System)
	if err != nil {
		return err
	}
	// The old one is closed only once the new one is open, so a failure
	// leaves the session the user was in rather than none at all.
	_ = s.current.Close()
	s.mu.Lock()
	s.current = opened
	s.mu.Unlock()
	s.ctxt.session, s.ctxt.conversation = opened.Session, opened
	// Carry on with what the conversation was last using, as Pi's session
	// switch does. A recorded model that cannot be opened leaves the current
	// one answering rather than failing the switch.
	restored, fromRecord := RestoreRecorded(Args{}, opened.Session)
	if fromRecord && (restored.Provider != s.provider || restored.Model != s.model) {
		next := s.rt.Args
		next.Provider, next.Model = restored.Provider, restored.Model
		if reopened, openedProvider, openedModel, err := s.rt.openModel(next); err == nil {
			s.setModel(reopened, openedProvider, openedModel)
			s.rt.Args, s.ctxt.args = next, next
		} else {
			fmt.Fprintf(s.streams.Err, "could not restore %s/%s (%v); keeping %s/%s\n",
				restored.Provider, restored.Model, err, s.provider, s.model)
		}
	}
	if restored.Thinking != "" {
		s.mu.Lock()
		s.rt.Thinking = ai.ClampThinkingLevel(ai.AvailableThinkingLevels(s.provider, s.model), restored.Thinking)
		s.mu.Unlock()
		s.ctxt.args.Thinking = s.rt.Thinking
	}
	return s.build()
}

func (s *interactiveSession) close() { _ = s.current.Close() }

// startNotices are what a session says when it begins: that it continues
// an earlier conversation, or where it is kept.
func (s *interactiveSession) startNotices() []string {
	// Said aloud, because continuing silently cannot be told from starting
	// fresh until the model answers something it should not have known.
	switch {
	case s.current.Resumed:
		return []string{fmt.Sprintf("resumed %d messages · %s",
			len(s.current.Session.Snapshot().Messages), s.current.ID)}
	case s.current.Path != "":
		// The id is shown at the start rather than the end: a session ended
		// by closing the terminal never reaches an ending, and an id the user
		// never saw is one they cannot resume.
		return []string{fmt.Sprintf("session %s · -c to continue it later", s.current.ID)}
	}
	return nil
}
