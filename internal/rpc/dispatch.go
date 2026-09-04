package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/compaction"
	"github.com/iamclancyliang/pi-go/internal/session"
)

// Host is what the channel drives: the conversation as it stands, the agent
// that answers in it, and the operations that change either. It is the
// interactive session's own set of abilities, narrowed to what a command needs
// and read through methods rather than fields because most of them change —
// switching conversations swaps the session, switching models swaps the port —
// and the channel must always see the current one.
type Host interface {
	// Session is the conversation as it stands now.
	Session() *session.Session
	Provider() string
	ModelName() string

	// Start submits a prompt to the current agent and hands back the live run.
	Start(ctx context.Context, prompt string) (Run, error)

	// Tree is the whole recorded conversation, branches included. ErrNotRecorded
	// when this run keeps nothing.
	Tree() ([]session.Node, error)

	// Fork copies the conversation up to an entry into a new one and moves
	// there; Clone does the same from where it stands. Switch reopens another
	// recorded conversation by id or path; New starts an empty one. Each
	// returns what was opened.
	Fork(entryID string) (Opened, error)
	Clone() (Opened, error)
	Switch(session string) (Opened, error)
	New() (Opened, error)

	// SetModel points the conversation at a different model from the next turn.
	SetModel(provider, model string) error

	// Compact shortens what the model sees. ErrNothingToCompact from the
	// compaction package is the ordinary "too short to bother" answer.
	Compact(instructions string) error

	// Commands lists what the session accepts as commands, and the Pi commands
	// it names as absent.
	Commands() (have []CommandInfo, absent []string)
}

// Run is a prompt in flight: the two ways to add to it, and the way to wait for
// it to settle. Steer and Follow are the runtime's own contract — steer
// interrupts the work at its next safe point, follow-up waits for it — and the
// channel adds nothing to either beyond a typed answer.
type Run interface {
	Steer(text string) error
	Follow(text string) error
	Wait() error
}

// Opened describes a conversation a command just opened.
type Opened struct {
	ID           string `json:"session"`
	Path         string `json:"path,omitempty"`
	MessageCount int    `json:"message_count"`
}

// CommandInfo is one slash command, for get_commands.
type CommandInfo struct {
	Name    string `json:"name"`
	Summary string `json:"summary"`
}

// ErrNotRecorded is what a Host reports when a command needs the durable
// record and this run keeps none.
var ErrNotRecorded = errors.New("this conversation is not recorded")

// Channel dispatches commands, and owns the one prompt that may be running.
//
// A prompt runs on its own goroutine so the channel keeps reading stdin while
// it works — which is what makes abort, steer and follow_up possible at all,
// since each is a command that arrives DURING a run. The channel therefore has
// exactly one piece of state: the active run, or none.
type Channel struct {
	host Host

	mu     sync.Mutex
	active *activeRun
}

// activeRun is a prompt in flight and the means to end it.
type activeRun struct {
	run    Run
	cancel context.CancelFunc
	done   chan struct{}
	err    error
}

// NewChannel builds a channel over a host.
func NewChannel(host Host) *Channel {
	return &Channel{host: host}
}

// stateData is the get_state payload.
type stateData struct {
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	SessionName  string `json:"session_name,omitempty"`
	MessageCount int    `json:"message_count"`
	// Running says whether a prompt is in flight — the fact a client needs
	// before choosing between prompt and steer.
	Running bool `json:"running"`
}

// statsData is the get_session_stats payload. Token counts keep the ledger's
// absent-versus-zero distinction; there is deliberately no cost, because
// pi-go ledgers tokens and computes no currency.
type statsData struct {
	Provider        string `json:"provider"`
	Model           string `json:"model"`
	MessageCount    int    `json:"message_count"`
	InputTokens     int    `json:"input_tokens"`
	OutputTokens    int    `json:"output_tokens"`
	CacheReadTokens *int   `json:"cache_read_tokens,omitempty"`
	ReasoningTokens *int   `json:"reasoning_tokens,omitempty"`
	UsageReported   bool   `json:"usage_reported"`
}

// entryData is one node of the conversation on the wire.
type entryData struct {
	ID       string    `json:"id"`
	ParentID string    `json:"parent_id,omitempty"`
	Kind     string    `json:"kind"`
	At       time.Time `json:"at"`
	Summary  string    `json:"summary"`
	OnPath   bool      `json:"on_path"`
	IsLeaf   bool      `json:"is_leaf"`
}

// Dispatch executes one command and returns its response, minus the sequence
// the writer stamps.
//
// A prompt's response is a RECEIPT: it is returned as soon as the run has
// started, and the outcome — the reply, a failure, an abort — arrives as events,
// with agent_end saying how it ended. A client that reads the ack as completion
// is wrong here for the same reason it is wrong in Pi.
//
// Anything that swaps the conversation or the model — fork, clone, switch,
// new, set_model, compact — is refused as busy while a prompt runs. The agent
// holds the session and port it was built with, and swapping either under a
// live run would write its next turn into a conversation the client just left.
func (c *Channel) Dispatch(ctx context.Context, cmd Command) Response {
	resp := Response{Family: "response", ID: cmd.ID, Command: cmd.Command}
	sess := c.host.Session()

	switch cmd.Command {
	case "prompt":
		return c.prompt(ctx, cmd, resp)

	case "abort":
		return okData(resp, map[string]any{"aborted": c.abort()})

	case "steer", "follow_up":
		if strings.TrimSpace(cmd.Message) == "" {
			return fail(resp, FailBadArgument, cmd.Command+" needs a message")
		}
		run := c.running()
		if run == nil {
			return fail(resp, FailNotRunning, "nothing is running to "+cmd.Command)
		}
		var err error
		if cmd.Command == "steer" {
			err = run.Steer(cmd.Message)
		} else {
			err = run.Follow(cmd.Message)
		}
		if err != nil {
			return fail(resp, FailInternal, err.Error())
		}
		return ok(resp, nil)

	case "get_state":
		return okData(resp, stateData{
			Provider:     c.host.Provider(),
			Model:        c.host.ModelName(),
			SessionName:  sess.Name(),
			MessageCount: len(sess.Snapshot().Messages),
			Running:      c.running() != nil,
		})

	case "get_messages":
		return okData(resp, map[string]any{"messages": sess.Snapshot().Messages})

	case "get_last_assistant_text":
		text, found := lastAssistant(sess)
		var value *string
		if found {
			value = &text
		}
		return okData(resp, map[string]any{"text": value})

	case "get_session_stats":
		return okData(resp, statsFrom(c.host, sess))

	case "set_session_name":
		if err := sess.SetName(cmd.Name); err != nil {
			return fail(resp, FailInternal, err.Error())
		}
		return okData(resp, map[string]any{"name": sess.Name()})

	case "get_tree", "get_entries":
		return c.entries(cmd, resp)

	case "fork":
		if strings.TrimSpace(cmd.EntryID) == "" {
			return fail(resp, FailBadArgument, "fork needs the entry_id of the point to fork at; get_tree lists them")
		}
		return c.reopen(resp, func() (Opened, error) { return c.host.Fork(cmd.EntryID) })

	case "clone":
		return c.reopen(resp, c.host.Clone)

	case "switch_session":
		if strings.TrimSpace(cmd.Session) == "" {
			return fail(resp, FailBadArgument, "switch_session needs a session id or path")
		}
		return c.reopen(resp, func() (Opened, error) { return c.host.Switch(cmd.Session) })

	case "new_session":
		return c.reopen(resp, c.host.New)

	case "set_model":
		if strings.TrimSpace(cmd.Model) == "" {
			return fail(resp, FailBadArgument, "set_model needs a model")
		}
		if c.running() != nil {
			return fail(resp, FailBusy, "a prompt is running; the model changes between turns, not during one")
		}
		if err := c.host.SetModel(cmd.Provider, cmd.Model); err != nil {
			return failFromRun(resp, err)
		}
		return okData(resp, map[string]any{"provider": c.host.Provider(), "model": c.host.ModelName()})

	case "compact":
		if c.running() != nil {
			return fail(resp, FailBusy, "a prompt is running; compact between turns")
		}
		before := len(sess.Snapshot().Messages)
		if err := c.host.Compact(cmd.Instructions); err != nil {
			var nothing *compaction.ErrNothingToCompact
			if errors.As(err, &nothing) {
				// Completing by doing nothing is a success that says so, the
				// same shape as aborting when nothing runs.
				return okData(resp, map[string]any{"compacted": false, "detail": err.Error()})
			}
			return failFromRun(resp, err)
		}
		return okData(resp, map[string]any{"compacted": true, "messages_before": before})

	case "get_commands":
		have, absent := c.host.Commands()
		return okData(resp, map[string]any{"commands": have, "not_here": absent})

	case "":
		return fail(resp, FailMalformed, "a command carried no verb")

	default:
		if pi[cmd.Command] {
			return fail(resp, FailUnimplemented,
				"a Pi command this build has not built; the parity matrix tracks it")
		}
		return fail(resp, FailUnknownCommand, "no such command")
	}
}

// entries answers get_tree and get_entries from one read of the record.
//
// get_tree is everything, branches included; get_entries is the conversation
// as it stands — the entries on the current path — optionally only those after
// a given one. Both name the leaf, which is where the next turn attaches.
func (c *Channel) entries(cmd Command, resp Response) Response {
	nodes, err := c.host.Tree()
	if errors.Is(err, ErrNotRecorded) {
		return fail(resp, FailUnavailable, err.Error())
	}
	if err != nil {
		return fail(resp, FailInternal, err.Error())
	}

	out := make([]entryData, 0, len(nodes))
	leaf := ""
	after := cmd.Command == "get_entries" && cmd.Since != ""
	skipping := after
	for _, n := range nodes {
		if n.OnPath && n.IsLeaf {
			leaf = n.ID
		}
		if cmd.Command == "get_entries" && !n.OnPath {
			continue
		}
		if skipping {
			if n.ID == cmd.Since {
				skipping = false
			}
			continue
		}
		out = append(out, entryData{ID: n.ID, ParentID: n.ParentID, Kind: n.Kind, At: n.At,
			Summary: n.Summary, OnPath: n.OnPath, IsLeaf: n.IsLeaf})
	}
	if after && skipping {
		return fail(resp, FailBadArgument, "no entry on the current path has the id "+cmd.Since)
	}
	var leafValue *string
	if leaf != "" {
		leafValue = &leaf
	}
	return okData(resp, map[string]any{"entries": out, "leaf": leafValue})
}

// reopen runs one of the conversation-swapping operations, refusing it while a
// prompt runs.
func (c *Channel) reopen(resp Response, open func() (Opened, error)) Response {
	if c.running() != nil {
		return fail(resp, FailBusy, "a prompt is running; abort it or wait for it before changing conversation")
	}
	opened, err := open()
	if errors.Is(err, ErrNotRecorded) {
		return fail(resp, FailUnavailable, err.Error())
	}
	if err != nil {
		return fail(resp, FailInternal, err.Error())
	}
	return okData(resp, opened)
}

// prompt starts a run and acknowledges it.
//
// One at a time: a second prompt while one runs is refused as busy rather than
// queued, because a queue the client cannot see into is a prompt it believes is
// running. Steer and follow_up are how a client adds to work in flight.
func (c *Channel) prompt(ctx context.Context, cmd Command, resp Response) Response {
	if strings.TrimSpace(cmd.Message) == "" {
		return fail(resp, FailBadArgument, "prompt needs a message")
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active != nil && !c.active.finished() {
		return fail(resp, FailBusy, "a prompt is already running; steer or follow_up to add to it, abort to stop it")
	}

	runCtx, cancel := context.WithCancel(ctx)
	run, err := c.host.Start(runCtx, cmd.Message)
	if err != nil {
		cancel()
		return failFromRun(resp, err)
	}
	active := &activeRun{run: run, cancel: cancel, done: make(chan struct{})}
	c.active = active
	go func() {
		defer cancel()
		defer close(active.done)
		// The outcome is on the stream — agent_end names it — not here: by
		// the time it is known, the receipt has long been sent.
		active.err = run.Wait()
	}()
	return ok(resp, nil)
}

// abort cancels the running prompt, and reports whether there was one.
func (c *Channel) abort() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active == nil || c.active.finished() {
		return false
	}
	c.active.cancel()
	return true
}

// running returns the run in flight, or nil.
func (c *Channel) running() Run {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active == nil || c.active.finished() {
		return nil
	}
	return c.active.run
}

// Settle waits for the running prompt, if any, to finish. The loop calls it
// when stdin closes: a client that stops sending commands has not asked for
// the work in flight to be thrown away.
func (c *Channel) Settle() {
	c.mu.Lock()
	active := c.active
	c.mu.Unlock()
	if active != nil {
		<-active.done
	}
}

func (a *activeRun) finished() bool {
	select {
	case <-a.done:
		return true
	default:
		return false
	}
}

func statsFrom(host Host, sess *session.Session) statsData {
	usage := sess.Usage()
	data := statsData{
		Provider:      host.Provider(),
		Model:         host.ModelName(),
		MessageCount:  len(sess.Snapshot().Messages),
		InputTokens:   usage.InputTokens,
		OutputTokens:  usage.OutputTokens,
		UsageReported: usage.Reported,
	}
	if usage.CacheReadTokens != nil {
		v := *usage.CacheReadTokens
		data.CacheReadTokens = &v
	}
	if usage.ReasoningTokens != nil {
		v := *usage.ReasoningTokens
		data.ReasoningTokens = &v
	}
	return data
}

func lastAssistant(sess *session.Session) (string, bool) {
	messages := sess.Snapshot().Messages
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == ai.RoleAssistant && strings.TrimSpace(messages[i].Content) != "" {
			return messages[i].Content, true
		}
	}
	return "", false
}

func failFromRun(resp Response, err error) Response {
	// A provider failure keeps its classification, which is the whole reason
	// the taxonomy exists: a client learns whether to wait, pay or fix the
	// request without reading prose.
	var pe *ai.ProviderError
	if errors.As(err, &pe) {
		return fail(resp, FailProvider, string(pe.Failure)+": "+err.Error())
	}
	if kind, known := ai.FailureOf(err); known {
		return fail(resp, FailProvider, string(kind)+": "+err.Error())
	}
	return fail(resp, FailInternal, err.Error())
}

func ok(resp Response, data json.RawMessage) Response {
	resp.OK = true
	resp.Data = data
	return resp
}

func okData(resp Response, v any) Response {
	encoded, err := json.Marshal(v)
	if err != nil {
		return fail(resp, FailInternal, "encoding the response: "+err.Error())
	}
	return ok(resp, encoded)
}

func fail(resp Response, kind FailureKind, detail string) Response {
	resp.OK = false
	resp.Data = nil
	resp.Error = &Failure{Kind: kind, Detail: detail}
	return resp
}

// pi is the set of Pi RPC commands pi-go recognises but has not built. A verb
// here fails as unimplemented; one not here fails as unknown. The list is the
// 32-command union from the feature inventory §21.1 minus what Dispatch serves.
var pi = map[string]bool{
	"cycle_model": true, "get_available_models": true,
	"set_thinking_level": true, "cycle_thinking_level": true, "get_available_thinking_levels": true,
	"set_steering_mode": true, "set_follow_up_mode": true,
	"set_auto_compaction": true,
	"set_auto_retry":      true, "abort_retry": true,
	"bash": true, "abort_bash": true,
	"export_html": true, "get_fork_messages": true,
}
