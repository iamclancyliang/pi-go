package rpc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
)

// Writer is where a response goes: the JSON stream writer, which numbers it
// from the same counter as the run's events, under the same lock, so a
// response lands in the one order among them.
type Writer interface {
	WriteResponse(resp Response) error
}

// Loop reads commands from a reader and answers each on the writer.
//
// Commands are read and answered in order, but a prompt does not hold the loop:
// it runs on its own goroutine and its ack goes back at once, so the commands
// that only mean something during a run — abort, steer, follow_up — can arrive
// during one. The wire stays in one order because the writer numbers every
// line under its own lock, whichever goroutine wrote it.
//
// It returns when the reader reaches EOF or a read fails, or when ctx is
// cancelled. On EOF a running prompt is allowed to finish first: a client that
// stopped sending has not asked for the work in flight to be thrown away. On
// cancellation it is aborted, because cancellation is exactly that request.
//
// A malformed line is answered, not fatal: a client that sends one line of
// garbage gets a typed refusal and the loop reads the next.
func Loop(ctx context.Context, r io.Reader, w Writer, channel *Channel) error {
	scanner := bufio.NewScanner(r)
	// A command line can be large — a prompt carries whole files pasted in — so
	// the default 64K token limit is lifted well past what a line should hold.
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	for scanner.Scan() {
		if ctx.Err() != nil {
			channel.abort()
			channel.Settle()
			return ctx.Err()
		}
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}

		var cmd Command
		if err := json.Unmarshal(line, &cmd); err != nil {
			// No id could be parsed, so the failure is correlated by position —
			// the one place position is all there is, which is why a command
			// without an id is refused rather than answered blind.
			if err := w.WriteResponse(Response{Family: "response", OK: false,
				Error: &Failure{Kind: FailMalformed, Detail: "the command line is not JSON"}}); err != nil {
				return err
			}
			continue
		}
		if cmd.ID == "" {
			if err := w.WriteResponse(Response{Family: "response", Command: cmd.Command, OK: false,
				Error: &Failure{Kind: FailMalformed, Detail: "every command must carry an id"}}); err != nil {
				return err
			}
			continue
		}

		if err := w.WriteResponse(channel.Dispatch(ctx, cmd)); err != nil {
			return err
		}
	}
	channel.Settle()
	return scanner.Err()
}
