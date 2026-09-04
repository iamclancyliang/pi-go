package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/session"
)

// Runner is what the channel drives: submitting a prompt and reporting model
// facts. It is the runtime's Agent, narrowed to what a command needs, so the
// channel can be tested without building one.
type Runner interface {
	Run(ctx context.Context, prompt string) error
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

// stateData is the get_state payload.
type stateData struct {
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	SessionName  string `json:"session_name,omitempty"`
	MessageCount int    `json:"message_count"`
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

// Dispatch executes one command and returns its response, minus the Seq and
// family the writer stamps. The Runner may be nil for a read-only channel; a
// command that needs it then fails as unimplemented rather than panicking.
//
// A prompt runs to completion here: its response is the receipt, and the events
// it produced have already been written by the time this returns. That is what
// keeps the one sequence honest without a lock — the response is allocated
// after the last event, because the work finished before the response was made.
func Dispatch(ctx context.Context, cmd Command, run Runner, state State) Response {
	resp := Response{Family: "response", ID: cmd.ID, Command: cmd.Command}

	switch cmd.Command {
	case "prompt":
		if run == nil {
			return fail(resp, FailUnimplemented, "this channel has no agent to prompt")
		}
		if strings.TrimSpace(cmd.Message) == "" {
			return fail(resp, FailBadArgument, "prompt needs a message")
		}
		if err := run.Run(ctx, cmd.Message); err != nil {
			return failFromRun(resp, err)
		}
		return ok(resp, nil)

	case "get_state":
		return okData(resp, stateData{
			Provider:     state.provider,
			Model:        state.modelName,
			SessionName:  state.sess.Name(),
			MessageCount: len(state.sess.Snapshot().Messages),
		})

	case "get_messages":
		return okData(resp, map[string]any{"messages": state.sess.Snapshot().Messages})

	case "get_last_assistant_text":
		text, ok := lastAssistant(state.sess)
		var value *string
		if ok {
			value = &text
		}
		return okData(resp, map[string]any{"text": value})

	case "get_session_stats":
		return okData(resp, statsFrom(state))

	case "set_session_name":
		if err := state.sess.SetName(cmd.Name); err != nil {
			return fail(resp, FailInternal, err.Error())
		}
		return okData(resp, map[string]any{"name": state.sess.Name()})

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
		return Response{Family: resp.Family, ID: resp.ID, Command: resp.Command,
			Error: &Failure{Kind: FailProvider, Detail: string(pe.Failure) + ": " + err.Error()}}
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
	"steer": true, "follow_up": true, "abort": true, "new_session": true,
	"set_model": true, "cycle_model": true, "get_available_models": true,
	"set_thinking_level": true, "cycle_thinking_level": true, "get_available_thinking_levels": true,
	"set_steering_mode": true, "set_follow_up_mode": true,
	"compact": true, "set_auto_compaction": true,
	"set_auto_retry": true, "abort_retry": true,
	"bash": true, "abort_bash": true,
	"export_html": true, "switch_session": true, "fork": true, "clone": true,
	"get_fork_messages": true, "get_entries": true, "get_tree": true,
	"get_commands": true,
}
