package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"io"

	"github.com/iamclancyliang/pi-go/internal/events"
)

// Writer is where a response goes. It is the JSON stream writer, which stamps
// the sequence from the shared counter and serialises the line — the same path
// the run's events take, so a response lands in the one order among them.
type Writer interface {
	WriteResponse(seq int, resp Response) error
}

// Loop reads commands from a reader and answers each on the writer.
//
// One command at a time, in order, synchronously: a prompt runs to completion —
// its events written — before the next command is read. That is what lets one
// sequence counter order events and responses without a lock between them: the
// response is numbered after the work that precedes it has finished, because
// the loop did not move on until it had.
//
// It returns when the reader reaches EOF or a read fails, or when a command
// asks it to stop. A malformed line is answered, not fatal: a client that sends
// one line of garbage gets a typed refusal and the loop reads the next.
func Loop(ctx context.Context, r io.Reader, w Writer, seq *events.Sequence, run Runner, state State) error {
	scanner := bufio.NewScanner(r)
	// A command line can be large — a prompt carries whole files pasted in — so
	// the default 64K token limit is lifted well past what a line should hold.
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	for scanner.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := scanner.Bytes()
		if len(trim(line)) == 0 {
			continue
		}

		var cmd Command
		if err := json.Unmarshal(line, &cmd); err != nil {
			// No id could be parsed, so the failure is correlated by position —
			// the one place position is all there is, which is why a command
			// without an id is refused rather than answered blind.
			if err := respond(w, seq, Response{Family: "response", OK: false,
				Error: &Failure{Kind: FailMalformed, Detail: "the command line is not JSON"}}); err != nil {
				return err
			}
			continue
		}
		if cmd.ID == "" {
			if err := respond(w, seq, Response{Family: "response", Command: cmd.Command, OK: false,
				Error: &Failure{Kind: FailMalformed, Detail: "every command must carry an id"}}); err != nil {
				return err
			}
			continue
		}

		resp := Dispatch(ctx, cmd, run, state)
		if err := respond(w, seq, resp); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func respond(w Writer, seq *events.Sequence, resp Response) error {
	return w.WriteResponse(seq.Next(), resp)
}

func trim(b []byte) []byte {
	for len(b) > 0 && (b[0] == ' ' || b[0] == '\t' || b[0] == '\r' || b[0] == '\n') {
		b = b[1:]
	}
	for len(b) > 0 {
		if c := b[len(b)-1]; c == ' ' || c == '\t' || c == '\r' || c == '\n' {
			b = b[:len(b)-1]
			continue
		}
		break
	}
	return b
}
