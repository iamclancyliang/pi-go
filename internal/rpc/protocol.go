// Package rpc is the command channel of --mode rpc.
//
// A client writes one JSON command per line to stdin; the responses join the
// same stdout stream as the run's events, numbered from the same sequence so a
// consumer can put a response back among the events it caused (ADR-0010, over
// the payloads recorded in the feature inventory §21).
//
// Three of pi-go's decisions live in the shapes here. An id is REQUIRED on
// every command and echoed on its response, so a response is never attributable
// only by arrival order — the ambiguity Pi's optional id permits. A failure is
// TYPED, carrying a stable kind rather than prose, so a client can tell "unknown
// command" from "provider quota exhausted" without parsing English. And the
// acknowledgement semantics Pi leans on are kept: a prompt's response confirms
// receipt, and the outcome arrives as events.
package rpc

import "encoding/json"

// Command is one request read from stdin.
//
// The fields beyond id and command are the union of every command's arguments;
// each handler reads only its own. Unknown fields are ignored rather than
// refused, so a newer client talking to an older build fails per command with a
// typed reason rather than at the parser.
type Command struct {
	// ID correlates a response to its command. Required: a command without one
	// is refused, because a response it could only match by position is the bug
	// this protocol declines to reproduce.
	ID string `json:"id"`

	// Command is the verb. Required.
	Command string `json:"command"`

	// Message is the prompt text, on `prompt`.
	Message string `json:"message,omitempty"`

	// Name is the new session name, on `set_session_name`.
	Name string `json:"name,omitempty"`

	// EntryID names a point in the conversation, on `fork`; a prefix is
	// enough, the way /tree shows them, and an ambiguous one is refused.
	EntryID string `json:"entry_id,omitempty"`

	// Session names another recorded conversation, on `switch_session`: an id
	// (a prefix will do) or the path of its file.
	Session string `json:"session,omitempty"`

	// Since, on `get_entries`, returns only the entries after that id.
	Since string `json:"since,omitempty"`

	// Provider and Model select what answers from the next turn, on
	// `set_model`. Provider may be empty: the one already answering.
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`

	// Instructions is what to focus a summary on, on `compact`.
	Instructions string `json:"instructions,omitempty"`
}

// FailureKind is why a command did not succeed. Closed and stable: a client
// switches on it.
type FailureKind string

const (
	// FailMalformed is a command that did not parse, or that arrived without
	// the id or verb every command must carry.
	FailMalformed FailureKind = "malformed_command"

	// FailUnknownCommand is a verb this build does not recognise at all.
	FailUnknownCommand FailureKind = "unknown_command"

	// FailUnimplemented is a verb pi-go recognises and has not built. It is a
	// different answer from unknown: the command is real and will one day work,
	// which is what the detail says.
	FailUnimplemented FailureKind = "unimplemented"

	// FailBadArgument is a recognised command whose arguments it cannot act on.
	FailBadArgument FailureKind = "bad_argument"

	// FailBusy is a prompt arriving while one is already running. One run at a
	// time is the channel's rule, said as a typed answer rather than a queue
	// the client cannot see into; a client that wants to redirect the running
	// work has steer and follow_up for exactly that.
	FailBusy FailureKind = "busy"

	// FailNotRunning is a command that only means something while a prompt is
	// in flight — steer, follow_up — arriving when nothing is.
	FailNotRunning FailureKind = "not_running"

	// FailUnavailable is a command whose subject this run does not have — a
	// conversation kept in memory only has no shape to show and nothing to
	// fork. The command is real and built; this run cannot answer it.
	FailUnavailable FailureKind = "unavailable"

	// FailProvider is a failure that came from the model provider, carrying the
	// provider-independent classification every port already produces.
	FailProvider FailureKind = "provider_failure"

	// FailInternal is a failure inside pi-go itself.
	FailInternal FailureKind = "internal_error"
)

// Response is what goes back for one command.
//
// It carries the family and sequence the wire needs, the id and command echoed
// back, whether it succeeded, and exactly one of Data or Error.
type Response struct {
	Family  string          `json:"family"`
	Seq     int             `json:"seq"`
	ID      string          `json:"id,omitempty"`
	Command string          `json:"command,omitempty"`
	OK      bool            `json:"ok"`
	Data    json.RawMessage `json:"data,omitempty"`
	Error   *Failure        `json:"error,omitempty"`
}

// Failure is a typed error with human detail.
type Failure struct {
	Kind   FailureKind `json:"kind"`
	Detail string      `json:"detail"`
}
