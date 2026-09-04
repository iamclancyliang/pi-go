package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/session"
)

// Runner starts a prompt and hands back the live run. It is the runtime's
// Agent narrowed to what the channel drives, so the channel is tested against
// a fake rather than a model.
type Runner interface {
	Start(ctx context.Context, prompt string) (Run, error)
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

// State reports what a session and its model are, for a client that asks
// before or between prompts. It is the read side of the channel.
type State struct {
	sess      *session.Session
	modelName string
	provider  string
}

// NewState pairs a session with the model facts the runtime holds outside it.
func NewState(sess *session.Session, provider, modelName string) State {
	return State{sess: sess, modelName: modelName, provider: provider}
}

// Channel dispatches commands, and owns the one prompt that may be running.
//
// A prompt runs on its own goroutine so the channel keeps reading stdin while
// it works — which is what makes abort, steer and follow_up possible at all,
// since each is a command that arrives DURING a run. The channel therefore has
// exactly one piece of state: the active run, or none.
type Channel struct {
	runner Runner
	state  State

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

// NewChannel builds a channel over a runner. The runner may be nil for a
// read-only channel; prompt then fails as unimplemented rather than panicking.
func NewChannel(runner Runner, state State) *Channel {
	return &Channel{runner: runner, state: state}
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

// Dispatch executes one command and returns its response, minus the sequence
// the writer stamps.
//
// A prompt's response is a RECEIPT: it is returned as soon as the run has
// started, and the outcome — the reply, a failure, an abort — arrives as events,
// with agent_end saying how it ended. A client that reads the ack as completion
// is wrong here for the same reason it is wrong in Pi.
func (c *Channel) Dispatch(ctx context.Context, cmd Command) Response {
	resp := Response{Family: "response", ID: cmd.ID, Command: cmd.Command}

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
			Provider:     c.state.provider,
			Model:        c.state.modelName,
			SessionName:  c.state.sess.Name(),
			MessageCount: len(c.state.sess.Snapshot().Messages),
			Running:      c.running() != nil,
		})

	case "get_messages":
		return okData(resp, map[string]any{"messages": c.state.sess.Snapshot().Messages})

	case "get_last_assistant_text":
		text, found := lastAssistant(c.state.sess)
		var value *string
		if found {
			value = &text
		}
		return okData(resp, map[string]any{"text": value})

	case "get_session_stats":
		return okData(resp, statsFrom(c.state))

	case "set_session_name":
		if err := c.state.sess.SetName(cmd.Name); err != nil {
			return fail(resp, FailInternal, err.Error())
		}
		return okData(resp, map[string]any{"name": c.state.sess.Name()})

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

// prompt starts a run and acknowledges it.
//
// One at a time: a second prompt while one runs is refused as busy rather than
// queued, because a queue the client cannot see into is a prompt it believes is
// running. Steer and follow_up are how a client adds to work in flight.
func (c *Channel) prompt(ctx context.Context, cmd Command, resp Response) Response {
	if c.runner == nil {
		return fail(resp, FailUnimplemented, "this channel has no agent to prompt")
	}
	if strings.TrimSpace(cmd.Message) == "" {
		return fail(resp, FailBadArgument, "prompt needs a message")
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active != nil && !c.active.finished() {
		return fail(resp, FailBusy, "a prompt is already running; steer or follow_up to add to it, abort to stop it")
	}

	runCtx, cancel := context.WithCancel(ctx)
	run, err := c.runner.Start(runCtx, cmd.Message)
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

func statsFrom(state State) statsData {
	usage := state.sess.Usage()
	data := statsData{
		Provider:      state.provider,
		Model:         state.modelName,
		MessageCount:  len(state.sess.Snapshot().Messages),
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
	"new_session": true,
	"set_model":   true, "cycle_model": true, "get_available_models": true,
	"set_thinking_level": true, "cycle_thinking_level": true, "get_available_thinking_levels": true,
	"set_steering_mode": true, "set_follow_up_mode": true,
	"compact": true, "set_auto_compaction": true,
	"set_auto_retry": true, "abort_retry": true,
	"bash": true, "abort_bash": true,
	"export_html": true, "switch_session": true, "fork": true, "clone": true,
	"get_fork_messages": true, "get_entries": true, "get_tree": true,
	"get_commands": true,
}
