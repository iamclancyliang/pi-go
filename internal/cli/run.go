package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/compaction"
	"github.com/iamclancyliang/pi-go/internal/events"
	"github.com/iamclancyliang/pi-go/internal/jsonstream"
	"github.com/iamclancyliang/pi-go/internal/rpc"
	"github.com/iamclancyliang/pi-go/internal/runtime"
	"github.com/iamclancyliang/pi-go/internal/session"
	"github.com/iamclancyliang/pi-go/internal/settings"
	"github.com/iamclancyliang/pi-go/internal/tools"
)

// DefaultSystemPrompt is what the agent is told it is when nothing else says.
const DefaultSystemPrompt = "You are pi-go, a coding agent. " +
	"Use the tools available to you to inspect and change files in the working directory. " +
	"Prefer the read, ls, find and grep tools over running shell commands to look around."

// Streams are where a run reads from and writes to.
//
// Passed in rather than reached for, so a test drives a whole mode without a
// terminal — and so the two output streams stay distinguishable, which is what
// makes a failing run usable in a pipeline.
type Streams struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer
}

// Runtime is everything a mode needs to talk to a model.
type Runtime struct {
	Model     ai.Port
	ModelName string
	Tools     *tools.Registry
	System    string

	// Provider is shown to the user, never branched on. Which account a
	// session is spending belongs on screen: two providers can serve
	// similarly-named models, and the bill goes to only one of them.
	Provider string

	// Conversation is where this run's history lives. Required: a run with no
	// session has one that keeps nothing, which is a decision made where the
	// flags are read rather than assumed here.
	Conversation *Conversation

	// Args and WorkingDir are what a command needs to open ANOTHER
	// conversation — /new and /resume must land in the same session directory
	// this run was told to use, not in whatever the default happens to be.
	Args       Args
	WorkingDir string

	// Transport is how a newly opened provider reaches the network, so a model
	// switched to mid-session goes through the same one this run was given.
	Transport http.RoundTripper

	// OpenModel replaces provider construction for an embedding or test that
	// supplies its own model factory. Nil uses the normal provider registry.
	OpenModel func(Args, http.RoundTripper) (ai.Port, string, string, error)

	// Config is what this run resolved: effective settings, and how the
	// project's trust question was answered. /settings and /trust read it.
	Config Config

	// Thinking is how much reasoning to ask for on every turn.
	Thinking ai.ThinkingLevel

	// Retry is how a transiently failed model call is tried again. The zero
	// value retries nothing, which is what a Runtime assembled by hand gets;
	// the composition root fills it from settings with RetryPolicy.
	Retry runtime.RetryPolicy

	// SteeringMode and FollowUpMode are how many queued messages each drain
	// point takes; empty is Pi's default, one at a time.
	SteeringMode runtime.QueueMode
	FollowUpMode runtime.QueueMode

	// AutoCompaction is whether and with how much reserve to compact before
	// the context overflows. Its window is filled per model as each agent is
	// built, from what is recorded about that model; zero values compact
	// nothing, and AutoCompactionPolicy fills it from settings.
	AutoCompaction runtime.AutoCompaction

	// ScopedModels limits model cycling when a caller has selected an explicit
	// ordered set. An empty set means cycle through the provider's reported
	// listing. The full-screen scope editor is a separate interface feature.
	ScopedModels []ai.ModelRef

	// ModelPatterns carries the raw --models patterns. They are matched against
	// the provider's on-demand listing when cycling, so startup stays offline.
	ModelPatterns []string

	// ReadLine, when set, replaces the plain line scanner with an editing
	// prompt — the terminal path. Nil reads lines from Streams.In, which is
	// what tests and redirected input use. A seam rather than a TTY check
	// here, so the decision lives where the terminal is actually known.
	ReadLine func(prompt string) (string, bool, error)
}

func (rt Runtime) openModel(args Args) (ai.Port, string, string, error) {
	if rt.OpenModel != nil {
		return rt.OpenModel(args, rt.Transport)
	}
	return Open(args, rt.Transport)
}

// RunPrint sends each prompt in turn and writes the final answer.
//
// One-shot, and the exit status carries the outcome: a run whose last reply
// ended in a failure exits non-zero with the reason on stderr, because a
// pipeline reading only stdout must not mistake an error for an answer.
func RunPrint(ctx context.Context, rt Runtime, streams Streams, prompts []string) int {
	if len(prompts) == 0 {
		fmt.Fprintln(streams.Err, "pi: no prompt given")
		return 1
	}

	if rt.Conversation == nil || rt.Conversation.Session == nil {
		// A composition root's mistake rather than a user's, but reported
		// rather than dereferenced: a crash here names a line in this file and
		// says nothing about what went wrong.
		fmt.Fprintln(streams.Err, "pi: no conversation was opened for this run")
		return 1
	}
	sess := rt.Conversation.Session
	if err := recordInUse(sess, rt.Provider, rt.ModelName, rt.Thinking); err != nil {
		fmt.Fprintf(streams.Err, "pi: %v\n", err)
		return 1
	}
	agent, err := runtime.New(runtime.Config{
		Model:          rt.Model,
		ModelName:      rt.ModelName,
		Tools:          rt.Tools,
		Session:        sess,
		Thinking:       rt.Thinking,
		Retry:          rt.Retry,
		AutoCompaction: autoCompactionFor(rt.AutoCompaction, rt.Provider, rt.ModelName),
		SteeringMode:   rt.SteeringMode,
		FollowUpMode:   rt.FollowUpMode,
		// A one-shot can overflow too, and a refusal with no recovery is a
		// failed run where a shortened context would have answered.
		Summarize: (&compaction.Compactor{Model: rt.Model, ModelName: rt.ModelName}).Summarize,
	})
	if err != nil {
		fmt.Fprintf(streams.Err, "pi: %v\n", err)
		return 1
	}

	for _, prompt := range prompts {
		if err := agent.Run(ctx, prompt); err != nil {
			// The reason goes to stderr and nothing goes to stdout: a caller
			// piping stdout into another program must receive an answer or
			// nothing, never an apology.
			fmt.Fprintf(streams.Err, "pi: %v\n", err)
			return 1
		}
	}

	answer, ok := lastAnswer(sess)
	if !ok {
		fmt.Fprintln(streams.Err, "pi: the model produced no answer")
		return 1
	}
	fmt.Fprintln(streams.Out, answer)
	return 0
}

// RunJSON sends each prompt in turn and writes the event stream to stdout.
//
// One-shot like RunPrint, but stdout carries the protocol rather than an
// answer: the version line, then every lifecycle and reply event as JSON
// lines. The answer is inside the stream — a model_response carries its text —
// so nothing else is written there, and a consumer parses lines or nothing.
//
// Failures behave as in print mode — the reason on stderr, the exit code
// non-zero — with one addition: the stream itself failing to write is a
// failure of the run, because a consumer that got half a stream with no error
// would read an interrupted run as a quiet one.
func RunJSON(ctx context.Context, rt Runtime, streams Streams, prompts []string) int {
	if len(prompts) == 0 {
		fmt.Fprintln(streams.Err, "pi: no prompt given")
		return 1
	}
	if rt.Conversation == nil || rt.Conversation.Session == nil {
		fmt.Fprintln(streams.Err, "pi: no conversation was opened for this run")
		return 1
	}

	// The writer opens the stream immediately: the version line precedes every
	// event, including agent_start, so a consumer knows what it is reading
	// before there is anything to read.
	writer := jsonstream.NewWriter(streams.Out)

	sess := rt.Conversation.Session
	if err := recordInUse(sess, rt.Provider, rt.ModelName, rt.Thinking); err != nil {
		fmt.Fprintf(streams.Err, "pi: %v\n", err)
		return 1
	}
	agent, err := runtime.New(runtime.Config{
		Model:          rt.Model,
		ModelName:      rt.ModelName,
		Tools:          rt.Tools,
		Session:        sess,
		Thinking:       rt.Thinking,
		Retry:          rt.Retry,
		AutoCompaction: autoCompactionFor(rt.AutoCompaction, rt.Provider, rt.ModelName),
		SteeringMode:   rt.SteeringMode,
		FollowUpMode:   rt.FollowUpMode,
		Summarize:      (&compaction.Compactor{Model: rt.Model, ModelName: rt.ModelName}).Summarize,
		// The writer is both observers, which is the wiring the shared
		// counter exists for: one consumer, two families, one order.
		Observers:      []events.Observer{writer},
		ReplyObservers: []runtime.ReplyObserver{writer},
	})
	if err != nil {
		fmt.Fprintf(streams.Err, "pi: %v\n", err)
		return 1
	}

	code := 0
	for _, prompt := range prompts {
		if err := agent.Run(ctx, prompt); err != nil {
			// The stream already carries the failure — agent_end names it —
			// but stderr and the exit code repeat it for the caller that
			// checks status before parsing anything.
			fmt.Fprintf(streams.Err, "pi: %v\n", err)
			code = 1
			break
		}
	}
	if err := writer.Err(); err != nil {
		fmt.Fprintf(streams.Err, "pi: the event stream broke: %v\n", err)
		return 1
	}
	return code
}

// rpcHost is the command channel's view of a run: the conversation and agent
// as they stand, and the operations that change them. The same abilities the
// interactive loop wires into its commands, with the same reasons — a swapped
// conversation rebuilds the agent, because a loop holds the session it was
// built with, and a swapped model reopens the port before anything is pointed
// at it.
type rpcHost struct {
	mu       sync.Mutex
	rt       Runtime
	writer   *jsonstream.Writer
	current  *Conversation
	agent    *runtime.Agent
	port     ai.Port
	prov     string
	model    string
	scoped   []ai.ModelRef
	patterns []string
}

func newRPCHost(rt Runtime, writer *jsonstream.Writer) (*rpcHost, error) {
	h := &rpcHost{rt: rt, writer: writer, current: rt.Conversation,
		port: rt.Model, prov: rt.Provider, model: rt.ModelName,
		scoped:   append([]ai.ModelRef(nil), rt.ScopedModels...),
		patterns: append([]string(nil), rt.ModelPatterns...)}
	if err := h.build(); err != nil {
		return nil, err
	}
	return h, nil
}

// build makes the agent for the current conversation and model. The writer is
// wired as both observers, which is how the run's events reach the stream.
func (h *rpcHost) build() error {
	if err := recordInUse(h.current.Session, h.prov, h.model, h.rt.Thinking); err != nil {
		return err
	}
	next, err := runtime.New(runtime.Config{
		Model:          h.port,
		ModelName:      h.model,
		Tools:          h.rt.Tools,
		Session:        h.current.Session,
		Thinking:       requestThinking(h.prov, h.model, h.rt.Thinking),
		Retry:          h.rt.Retry,
		AutoCompaction: autoCompactionFor(h.rt.AutoCompaction, h.prov, h.model),
		SteeringMode:   h.rt.SteeringMode,
		FollowUpMode:   h.rt.FollowUpMode,
		Summarize:      (&compaction.Compactor{Model: h.port, ModelName: h.model}).Summarize,
		Observers:      []events.Observer{h.writer},
		ReplyObservers: []runtime.ReplyObserver{h.writer},
	})
	if err != nil {
		return err
	}
	h.agent = next
	return nil
}

func (h *rpcHost) Session() *session.Session {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.current.Session
}

func (h *rpcHost) Provider() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.prov
}

func (h *rpcHost) ModelName() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.model
}

func (h *rpcHost) Start(ctx context.Context, prompt string) (rpc.Run, error) {
	h.mu.Lock()
	agent := h.agent
	h.mu.Unlock()
	return agent.Start(ctx, prompt)
}

func (h *rpcHost) Tree() ([]session.Node, error) {
	h.mu.Lock()
	store := h.current.Store
	h.mu.Unlock()
	if store == nil {
		return nil, rpc.ErrNotRecorded
	}
	return store.Tree(context.Background())
}

// opened reports the conversation now current.
func (h *rpcHost) opened() rpc.Opened {
	return rpc.Opened{ID: h.current.ID, Path: h.current.Path,
		MessageCount: len(h.current.Session.Snapshot().Messages)}
}

// reopen swaps in another conversation. The old one is closed only once the
// new one is open, so a failure leaves the client in the session it was in
// rather than in none at all.
func (h *rpcHost) reopen(args Args) (rpc.Opened, error) {
	next, err := OpenConversation(args, h.rt.WorkingDir, h.rt.System)
	if err != nil {
		return rpc.Opened{}, err
	}
	_ = h.current.Close()
	h.current = next
	h.restoreFrom(next.Session)
	if err := h.build(); err != nil {
		return rpc.Opened{}, err
	}
	return h.opened(), nil
}

// restoreFrom carries on with what a conversation just switched to was last
// using, as Pi's session switch does: its model, when one is recorded and can
// be opened, and its thinking level, clamped to whichever model answers. A
// recorded model that cannot be opened — its credential gone, say — leaves
// the current one answering rather than failing the switch.
func (h *rpcHost) restoreFrom(sess *session.Session) {
	restored, fromRecord := RestoreRecorded(Args{}, sess)
	if fromRecord && (restored.Provider != h.prov || restored.Model != h.model) {
		next := h.rt.Args
		next.Provider, next.Model = restored.Provider, restored.Model
		if opened, provider, model, err := h.rt.openModel(next); err == nil {
			h.port, h.prov, h.model = opened, provider, model
			h.rt.Args.Provider, h.rt.Args.Model = provider, model
		}
	}
	if restored.Thinking != "" {
		h.rt.Thinking = ai.ClampThinkingLevel(ai.AvailableThinkingLevels(h.prov, h.model), restored.Thinking)
	}
}

func (h *rpcHost) Fork(entryID string) (rpc.Opened, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.current.Store == nil {
		return rpc.Opened{}, rpc.ErrNotRecorded
	}
	full, err := resolveEntryIn(h.current.Store, entryID)
	if err != nil {
		return rpc.Opened{}, err
	}
	return h.branch(full)
}

func (h *rpcHost) Clone() (rpc.Opened, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.current.Store == nil {
		return rpc.Opened{}, rpc.ErrNotRecorded
	}
	return h.branch(h.current.Store.Leaf())
}

// branch copies the conversation up to an entry into a new file and reopens
// there, through the ordinary resume path so a fork lands in the same state a
// resumed conversation does.
func (h *rpcHost) branch(leaf string) (rpc.Opened, error) {
	now := time.Now()
	id := session.NewSessionID(now)
	path := filepath.Join(session.DirFor(h.current.Dir, h.rt.WorkingDir), session.FileName(id, now))
	forked, err := h.current.Store.BranchInto(context.Background(), path, h.rt.WorkingDir, id, leaf)
	if err != nil {
		return rpc.Opened{}, err
	}
	forked.Close()
	next := h.rt.Args
	next.Continue, next.Resume = false, path
	return h.reopen(next)
}

func (h *rpcHost) Switch(which string) (rpc.Opened, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	next := h.rt.Args
	next.Continue, next.Resume = false, which
	return h.reopen(next)
}

func (h *rpcHost) New() (rpc.Opened, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	fresh := h.rt.Args
	fresh.Continue, fresh.Resume = false, ""
	return h.reopen(fresh)
}

func (h *rpcHost) SetModel(toProvider, toModel string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	next := h.rt.Args
	next.Model = toModel
	if toProvider != "" {
		next.Provider = toProvider
	} else {
		// No provider named means the one already answering, not whichever
		// credential happens to be found first.
		next.Provider = h.prov
	}
	opened, openedProvider, openedModel, err := h.rt.openModel(next)
	if err != nil {
		return err
	}
	oldPort, oldProvider, oldModel, oldThinking := h.port, h.prov, h.model, h.rt.Thinking
	h.port, h.prov, h.model = opened, openedProvider, openedModel
	// The level carries over to the new model, clamped to what it offers, as
	// Pi's model switch does: a level the new model cannot take is not one
	// the next request should claim to be using.
	if h.rt.Thinking != "" {
		h.rt.Thinking = ai.ClampThinkingLevel(ai.AvailableThinkingLevels(openedProvider, openedModel), h.rt.Thinking)
	}
	if err := h.build(); err != nil {
		h.port, h.prov, h.model, h.rt.Thinking = oldPort, oldProvider, oldModel, oldThinking
		return err
	}
	// Keep later model changes and newly reopened conversations aligned with
	// the model that actually became active. The args are only in-memory run
	// state; Open still resolves credentials through its ordinary path.
	h.rt.Args.Provider = openedProvider
	h.rt.Args.Model = openedModel
	return nil
}

// ThinkingLevel is the level the next model request asks for; empty when
// nothing was asked for and the provider's own default stands.
func (h *rpcHost) ThinkingLevel() ai.ThinkingLevel {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.rt.Thinking
}

// AvailableThinkingLevels are what the current model offers.
func (h *rpcHost) AvailableThinkingLevels() []ai.ThinkingLevel {
	h.mu.Lock()
	defer h.mu.Unlock()
	return ai.AvailableThinkingLevels(h.prov, h.model)
}

// SetThinkingLevel clamps a level to what the current model offers and makes
// it the one the next model request asks for, including the next turn of a
// prompt already running.
//
// A change is remembered as the default for later runs, as Pi's is — unless
// it is off on a model that cannot reason, where off is not a preference but
// the only level there was, and saving it would turn reasoning off for every
// model used afterwards.
func (h *rpcHost) SetThinkingLevel(level ai.ThinkingLevel) (ai.ThinkingLevel, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	effective := ai.ClampThinkingLevel(ai.AvailableThinkingLevels(h.prov, h.model), level)
	if effective == h.rt.Thinking {
		return effective, nil
	}
	if err := h.current.Session.RecordThinking(effective); err != nil {
		return h.rt.Thinking, err
	}
	h.rt.Thinking = effective
	h.agent.SetThinking(requestThinking(h.prov, h.model, effective))

	if h.rt.Config.AgentDir != "" && (ai.SupportsThinking(h.prov, h.model) || effective != ai.ThinkingOff) {
		h.rt.Config.Global.DefaultThinkingLevel = string(effective)
		if err := settings.Save(settings.GlobalPath(h.rt.Config.AgentDir), h.rt.Config.Global); err != nil {
			// The level is in effect either way; only remembering it failed,
			// and that is reported rather than undoing what the client asked.
			return effective, fmt.Errorf("the level is in effect, but saving it as the default failed: %w", err)
		}
	}
	return effective, nil
}

// SetAutoRetry switches retrying from the next failure on, and remembers the
// choice for later runs as Pi does.
func (h *rpcHost) SetAutoRetry(enabled bool) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.rt.Retry.Enabled = enabled
	h.agent.SetAutoRetry(enabled)
	if h.rt.Config.AgentDir == "" {
		return nil
	}
	h.rt.Config.Global.Retry.Enabled = &enabled
	if err := settings.Save(settings.GlobalPath(h.rt.Config.AgentDir), h.rt.Config.Global); err != nil {
		return fmt.Errorf("the change is in effect, but saving it failed: %w", err)
	}
	return nil
}

// AbortRetry ends a retry wait in progress.
func (h *rpcHost) AbortRetry() bool {
	h.mu.Lock()
	agent := h.agent
	h.mu.Unlock()
	return agent.AbortRetry()
}

// AutoCompactionPolicy is the threshold compaction settings describe: on
// unless switched off, as in Pi.
func AutoCompactionPolicy(s settings.Settings) runtime.AutoCompaction {
	enabled := s.Compaction.Enabled == nil || *s.Compaction.Enabled
	return runtime.AutoCompaction{Enabled: enabled, ReserveTokens: s.Compaction.ReserveTokens}
}

// autoCompactionFor is a policy with the window of the model it is for. A
// model with no recorded window gets none, which leaves the threshold off for
// it: guessing would compact conversations nowhere near a limit.
func autoCompactionFor(policy runtime.AutoCompaction, provider, model string) runtime.AutoCompaction {
	facts, _ := ai.Facts(provider, model)
	policy.ContextWindow = facts.ContextWindow
	return policy
}

// SetAutoCompaction switches compacting before an overflow, from the next
// run's end, and remembers the choice for later runs as Pi does. Recovering
// from an overflow stays on either way.
func (h *rpcHost) SetAutoCompaction(enabled bool) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.rt.AutoCompaction.Enabled = enabled
	h.agent.SetAutoCompaction(enabled)
	if h.rt.Config.AgentDir == "" {
		return nil
	}
	h.rt.Config.Global.Compaction.Enabled = &enabled
	if err := settings.Save(settings.GlobalPath(h.rt.Config.AgentDir), h.rt.Config.Global); err != nil {
		return fmt.Errorf("the change is in effect, but saving it failed: %w", err)
	}
	return nil
}

// ExportHTML writes the conversation as a page, at path or under the
// session's own name in the working directory.
func (h *rpcHost) ExportHTML(path string) (string, error) {
	h.mu.Lock()
	source := exportSource{
		conversation: h.current, session: h.current.Session,
		provider: h.prov, model: h.model, thinking: h.rt.Thinking,
		tools: h.rt.Tools, workingDir: h.rt.WorkingDir,
	}
	h.mu.Unlock()
	return exportTo(source, path)
}

// QueueModes reports how many queued messages each drain point takes.
func (h *rpcHost) QueueModes() (steering, followUp string) {
	h.mu.Lock()
	agent := h.agent
	h.mu.Unlock()
	return string(agent.SteeringMode()), string(agent.FollowUpMode())
}

// SetQueueMode changes one queue's mode from the next drain point, and
// remembers it for later runs as Pi does.
func (h *rpcHost) SetQueueMode(which, raw string) error {
	mode, err := runtime.ParseQueueMode(raw)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if which == "steering" {
		h.rt.SteeringMode = mode
		h.agent.SetSteeringMode(mode)
		h.rt.Config.Global.SteeringMode = raw
	} else {
		h.rt.FollowUpMode = mode
		h.agent.SetFollowUpMode(mode)
		h.rt.Config.Global.FollowUpMode = raw
	}
	if h.rt.Config.AgentDir == "" {
		return nil
	}
	if err := settings.Save(settings.GlobalPath(h.rt.Config.AgentDir), h.rt.Config.Global); err != nil {
		return fmt.Errorf("the change is in effect, but saving it failed: %w", err)
	}
	return nil
}

// AutoCompactionEnabled reports whether compacting before an overflow is on.
func (h *rpcHost) AutoCompactionEnabled() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.rt.AutoCompaction.Enabled
}

// RetryPolicy is the retry policy settings describe, Pi's defaults filling
// whatever is unset.
func RetryPolicy(s settings.Settings) runtime.RetryPolicy {
	enabled, maxRetries, baseDelayMs := s.Retry.Resolved()
	return runtime.RetryPolicy{
		Enabled:    enabled,
		MaxRetries: maxRetries,
		BaseDelay:  time.Duration(baseDelayMs) * time.Millisecond,
	}
}

// requestThinking is what a request asks for at a level: nothing, for a
// model recorded as not reasoning, whose provider may refuse the field
// outright, and the level itself otherwise.
func requestThinking(provider, model string, level ai.ThinkingLevel) ai.ThinkingLevel {
	if !ai.SupportsThinking(provider, model) {
		return ""
	}
	return level
}

// AvailableModels asks the currently selected provider for its identifiers.
// No listing request occurs while the host is constructed or a prompt runs;
// the RPC command invokes this method explicitly.
func (h *rpcHost) AvailableModels(ctx context.Context) ([]ai.ModelRef, error) {
	h.mu.Lock()
	port, provider := h.port, h.prov
	h.mu.Unlock()
	return listAvailableModels(ctx, port, provider)
}

// CycleModel lists the current provider on demand, then applies the shared
// cycle semantics and changes the model used from the next turn onward.
func (h *rpcHost) CycleModel(ctx context.Context) (*ai.ModelCycle, error) {
	h.mu.Lock()
	port, provider, current, thinking := h.port, h.prov, h.model, h.rt.Thinking
	scoped, patterns := append([]ai.ModelRef(nil), h.scoped...), append([]string(nil), h.patterns...)
	h.mu.Unlock()
	return cycleModelsWithPatterns(ctx, port, provider, current, scoped, patterns, thinking, h.SetModel)
}

// ExecuteBash runs a direct shell command independently of the model loop.
// Session queues its completed message when a prompt is still active so the
// command cannot split an assistant tool call from its result.
func (h *rpcHost) ExecuteBash(ctx context.Context, command string, timeout *float64,
	excludeFromContext bool, _ string, onChunk func(string),
) (ai.BashResult, error) {
	h.mu.Lock()
	tool, ok := h.rt.Tools.Lookup("bash")
	sess := h.current.Session
	h.mu.Unlock()
	if !ok {
		return ai.BashResult{}, fmt.Errorf("shell execution is not configured")
	}
	runner, ok := tool.(bashExecutor)
	if !ok {
		return ai.BashResult{}, fmt.Errorf("the configured bash tool cannot run direct commands")
	}
	return executeBashWithTimeout(ctx, sess, runner, command, timeout, excludeFromContext, onChunk)
}

// AbortBash is a host capability marker. The channel owns the cancellation
// contexts for each command, so aborting there is what makes this operation
// safe when several commands are active at once.
func (h *rpcHost) AbortBash() {}

func (h *rpcHost) Compact(instructions string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := &compaction.Compactor{
		Model: h.port, ModelName: h.model, Instructions: instructions,
		KeepRecentTokens: h.rt.Config.Effective.Compaction.KeepRecentTokens,
	}
	summary, kept, err := c.Summarize(context.Background(), h.current.Session.Truth())
	if err != nil {
		return err
	}
	return h.current.Session.Compact(summary, kept)
}

func (h *rpcHost) Commands() ([]rpc.CommandInfo, []string) {
	have := make([]rpc.CommandInfo, 0, len(commands))
	for name, cmd := range commands {
		have = append(have, rpc.CommandInfo{Name: name, Summary: cmd.Summary})
	}
	sort.Slice(have, func(i, j int) bool { return have[i].Name < have[j].Name })
	absent := make([]string, 0, len(notImplemented))
	for name := range notImplemented {
		absent = append(absent, "/"+name)
	}
	sort.Strings(absent)
	return have, absent
}

// resolveEntryIn turns a prefix into an entry id, refusing ambiguity: standing
// at the wrong point is not something to discover from the conversation
// afterwards.
func resolveEntryIn(store *session.FileStore, prefix string) (string, error) {
	nodes, err := store.Tree(context.Background())
	if err != nil {
		return "", err
	}
	var matched []string
	for _, n := range nodes {
		if strings.HasPrefix(n.ID, prefix) {
			matched = append(matched, n.ID)
		}
	}
	switch len(matched) {
	case 0:
		return "", fmt.Errorf("no entry here starts with %q; get_tree lists them", prefix)
	case 1:
		return matched[0], nil
	default:
		return "", fmt.Errorf("%q matches more than one entry", prefix)
	}
}

// RunRPC drives the command channel: commands on stdin, responses and events on
// stdout, one JSON object per line.
//
// A prompt runs on its own goroutine while stdin keeps being read, which is
// what lets abort, steer and follow_up arrive during one. Everything on stdout
// is numbered by the writer under its own lock, so the wire's order is the
// order things were written whichever goroutine wrote them.
//
// It returns when stdin reaches EOF, after letting a running prompt finish. A
// command that fails is answered with a typed failure and the loop continues;
// only a broken stdout stream, or stdin itself failing, ends the run.
func RunRPC(ctx context.Context, rt Runtime, streams Streams) int {
	if rt.Conversation == nil || rt.Conversation.Session == nil {
		fmt.Fprintln(streams.Err, "pi: no conversation was opened for this run")
		return 1
	}

	writer := jsonstream.NewWriter(streams.Out)
	host, err := newRPCHost(rt, writer)
	if err != nil {
		fmt.Fprintf(streams.Err, "pi: %v\n", err)
		return 1
	}
	defer func() { _ = host.current.Close() }()

	if err := rpc.Loop(ctx, streams.In, writer, rpc.NewChannelWithWriter(host, writer)); err != nil {
		if err == context.Canceled {
			return 0
		}
		fmt.Fprintf(streams.Err, "pi: %v\n", err)
		return 1
	}
	return 0
}

// RunInteractive reads prompts a line at a time and answers each in turn.
//
// A line-based loop, NOT Pi's full-screen interface. The two are different
// features: this is the conversation, and the interface that renders it — the
// components, the key handling, the live redraw — is its own body of work that
// has not been ported. Said plainly rather than approximated, because a
// half-drawn interface is worse than an honest prompt.
func RunInteractive(ctx context.Context, rt Runtime, streams Streams) int {
	if rt.Conversation == nil || rt.Conversation.Session == nil {
		// A composition root's mistake rather than a user's, but reported
		// rather than dereferenced: a crash here names a line in this file and
		// says nothing about what went wrong.
		fmt.Fprintln(streams.Err, "pi: no conversation was opened for this run")
		return 1
	}
	// The agent is rebuilt whenever the conversation changes, because a loop
	// holds the session it was built with: a command that opened another one
	// while the agent still pointed at the old would write the next turn into
	// the conversation the user just left.
	current := rt.Conversation
	// Copied so the commands may update it — /reload re-resolves — without
	// reaching back into the caller's value.
	config := rt.Config
	// The model can change mid-session, so what answers is held here rather
	// than read from the configuration each time.
	port, provider, modelName := rt.Model, rt.Provider, rt.ModelName
	var agent *runtime.Agent
	build := func() error {
		if err := recordInUse(current.Session, provider, modelName, rt.Thinking); err != nil {
			return err
		}
		next, err := runtime.New(runtime.Config{
			Model:          port,
			ModelName:      modelName,
			Tools:          rt.Tools,
			Session:        current.Session,
			Thinking:       rt.Thinking,
			Retry:          rt.Retry,
			AutoCompaction: autoCompactionFor(rt.AutoCompaction, provider, modelName),
			SteeringMode:   rt.SteeringMode,
			FollowUpMode:   rt.FollowUpMode,
			// The same summariser /compact uses. Without it an overflow is
			// terminal: the request is refused, and a conversation long enough
			// to overflow once will do it again on every turn after.
			Summarize: (&compaction.Compactor{Model: port, ModelName: modelName}).Summarize,
		})
		if err != nil {
			return err
		}
		agent = next
		return nil
	}
	if err := build(); err != nil {
		fmt.Fprintf(streams.Err, "pi: %v\n", err)
		return 1
	}

	ctxt := &commandContext{
		ctx:          ctx,
		session:      current.Session,
		conversation: current,
		out:          streams.Out,
		errOut:       streams.Err,
		args:         rt.Args,
		workingDir:   rt.WorkingDir,
		config:       &config,

		tools:         rt.Tools,
		modelProvider: func() string { return provider },
		modelName:     func() string { return modelName },
	}
	if registered, ok := rt.Tools.Lookup("bash"); ok {
		if runner, ok := registered.(bashExecutor); ok {
			ctxt.bash = func(bashCtx context.Context, command string, exclude bool, onChunk func(string)) (tools.BashResult, error) {
				return executeBash(bashCtx, current.Session, runner, command, exclude, onChunk)
			}
		}
	}
	// The scanner is shared with the prompt loop, so a confirmation reads the
	// next line the user types rather than opening a second reader that would
	// race the first for the same input.
	var lines *bufio.Scanner
	ctxt.confirm = func() bool {
		if lines == nil || !lines.Scan() {
			return false
		}
		answer := strings.ToLower(strings.TrimSpace(lines.Text()))
		// Only an explicit yes. Anything else — including end of input — leaves
		// the outward-facing thing undone, which is the safe way to be wrong.
		return answer == "y" || answer == "yes"
	}
	// What the port and the tool set were BUILT from, frozen at startup. The
	// live config moves — /settings edits it — so comparing a reload against
	// the live view would report "nothing changed" for exactly the changes
	// that only a new run can pick up.
	builtFrom := rt.Config.Effective
	ctxt.reloadConfig = func() (bool, error) {
		resolved, err := ResolveConfig(rt.Args, rt.WorkingDir, nil)
		if err != nil {
			return false, err
		}
		changed := resolved.Effective.DefaultProvider != builtFrom.DefaultProvider ||
			resolved.Effective.DefaultModel != builtFrom.DefaultModel ||
			resolved.Effective.ShellPath != builtFrom.ShellPath ||
			resolved.Effective.ShellCommandPrefix != builtFrom.ShellCommandPrefix ||
			resolved.Effective.SessionDir != builtFrom.SessionDir
		config = resolved
		return changed, nil
	}
	ctxt.compact = func(instructions string) error {
		// Summarised with whatever model is answering now, so a conversation
		// switched to a cheaper model is compacted by it too.
		c := &compaction.Compactor{
			Model: port, ModelName: modelName, Instructions: instructions,
			KeepRecentTokens: config.Effective.Compaction.KeepRecentTokens,
		}
		summary, kept, err := c.Summarize(ctx, current.Session.Truth())
		if err != nil {
			return err
		}
		return current.Session.Compact(summary, kept)
	}
	ctxt.switchModel = func(toProvider, toModel string) error {
		next := rt.Args
		next.Model = toModel
		if toProvider != "" {
			next.Provider = toProvider
		} else {
			// No provider named means the one already answering, not whichever
			// credential happens to be found first.
			next.Provider = provider
		}
		opened, openedProvider, openedModel, err := rt.openModel(next)
		if err != nil {
			return err
		}
		oldPort, oldProvider, oldModel := port, provider, modelName
		port, provider, modelName = opened, openedProvider, openedModel
		if err := build(); err != nil {
			port, provider, modelName = oldPort, oldProvider, oldModel
			return err
		}
		rt.Args = next
		ctxt.args = next
		return nil
	}
	ctxt.availableModels = func(listCtx context.Context) ([]ai.ModelRef, error) {
		return listAvailableModels(listCtx, port, provider)
	}
	ctxt.cycleModel = func(cycleCtx context.Context) (*ai.ModelCycle, error) {
		return cycleModelsWithPatterns(cycleCtx, port, provider, modelName, rt.ScopedModels, rt.ModelPatterns,
			rt.Thinking, ctxt.switchModel)
	}
	ctxt.reload = func() error {
		// Rebuilt from the store the conversation already has, rather than
		// reopened from its path: reopening would read the leaf back off disk
		// and undo the move that was just made in memory.
		restored, err := session.Restore(context.Background(), rt.System, current.Store)
		if err != nil {
			return err
		}
		current.Session = restored
		ctxt.session = restored
		return build()
	}
	ctxt.reopen = func(args Args) error {
		opened, err := OpenConversation(args, rt.WorkingDir, rt.System)
		if err != nil {
			return err
		}
		// The old one is closed only once the new one is open, so a failure
		// leaves the session the user was in rather than none at all.
		_ = current.Close()
		current = opened
		ctxt.session, ctxt.conversation = opened.Session, opened
		// Carry on with what the conversation was last using, as Pi's
		// session switch does. A recorded model that cannot be opened leaves
		// the current one answering rather than failing the switch.
		restored, fromRecord := RestoreRecorded(Args{}, opened.Session)
		if fromRecord && (restored.Provider != provider || restored.Model != modelName) {
			next := rt.Args
			next.Provider, next.Model = restored.Provider, restored.Model
			if reopened, openedProvider, openedModel, err := rt.openModel(next); err == nil {
				port, provider, modelName = reopened, openedProvider, openedModel
				rt.Args, ctxt.args = next, next
			} else {
				fmt.Fprintf(streams.Err, "could not restore %s/%s (%v); keeping %s/%s\n",
					restored.Provider, restored.Model, err, provider, modelName)
			}
		}
		if restored.Thinking != "" {
			rt.Thinking = ai.ClampThinkingLevel(ai.AvailableThinkingLevels(provider, modelName), restored.Thinking)
			ctxt.args.Thinking = rt.Thinking
		}
		return build()
	}
	defer func() { _ = current.Close() }()

	if !config.Effective.QuietStartup {
		fmt.Fprintf(streams.Out, "pi-go · %s/%s · %d tools · /help for commands\n",
			provider, modelName, len(rt.Tools.All()))
	}
	// Said aloud, because continuing silently cannot be told from starting
	// fresh until the model answers something it should not have known.
	switch {
	case current.Resumed:
		fmt.Fprintf(streams.Out, "resumed %d messages · %s\n",
			len(current.Session.Snapshot().Messages), current.ID)
	case current.Path != "":
		// The id is shown at the start rather than the end: a session ended by
		// closing the terminal never reaches an ending, and an id the user
		// never saw is one they cannot resume.
		fmt.Fprintf(streams.Out, "session %s · -c to continue it later\n", current.ID)
	}

	lines = bufio.NewScanner(streams.In)
	// A prompt can be long; the default limit would cut one mid-sentence and
	// send the fragment.
	lines.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	readLine := func() (string, bool) {
		if rt.ReadLine != nil {
			line, ok, err := rt.ReadLine("> ")
			if err != nil {
				fmt.Fprintf(streams.Err, "pi: %v\n", err)
				return "", false
			}
			return line, ok
		}
		fmt.Fprint(streams.Out, "\n> ")
		if !lines.Scan() {
			return "", false
		}
		return lines.Text(), true
	}

	for {
		line, more := readLine()
		if !more {
			break
		}
		prompt := strings.TrimSpace(line)
		if prompt == "" {
			continue
		}
		if handled, stop := dispatch(ctxt, prompt); handled {
			if stop {
				break
			}
			continue
		}

		// The session carries across turns, so this is one conversation rather
		// than a series of unrelated questions.
		if err := agent.Run(ctx, prompt); err != nil {
			if ctx.Err() != nil {
				// The user stopped it. Ending the loop as well would exit on a
				// keystroke meant to interrupt one answer.
				fmt.Fprintln(streams.Err, "\npi: stopped")
				return 130
			}
			fmt.Fprintf(streams.Err, "pi: %v\n", err)
			continue
		}
		if answer, ok := lastAnswer(current.Session); ok {
			fmt.Fprintln(streams.Out, answer)
		}
	}

	if err := lines.Err(); err != nil && !errors.Is(err, io.EOF) {
		fmt.Fprintf(streams.Err, "pi: %v\n", err)
		return 1
	}
	fmt.Fprintln(streams.Out)
	return 0
}

// lastAnswer is the most recent thing the assistant actually said.
//
// Read from durable truth rather than accumulated as events arrive: the session
// is what the next turn is built from, so an answer taken from anywhere else
// could differ from what the model will be shown it said.
func lastAnswer(sess *session.Session) (string, bool) {
	messages := sess.Snapshot().Messages
	for i := len(messages) - 1; i >= 0; i-- {
		m := messages[i]
		if m.Role == ai.RoleAssistant && strings.TrimSpace(m.Content) != "" {
			return m.Content, true
		}
	}
	return "", false
}
