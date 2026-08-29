package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// MaxBashTimeout is the largest timeout that may be asked for.
//
// Pi's cap is whatever its timer accepts, which is about 24.8 days. The same
// bound is kept rather than a rounder one, so a command written against Pi is
// not refused here for a limit it never had.
const MaxBashTimeout = 2147483647 * time.Millisecond

// Bash runs a shell command.
//
// There is NO default timeout, which is deliberate and matches Pi: a build or a
// test run legitimately takes minutes, and a default that killed those would
// make the tool unusable for the work it exists for. A command that hangs is
// stopped by the caller's own cancellation instead.
type Bash struct {
	// Dir is the working directory commands run in.
	Dir string

	// Shell overrides the interpreter. Empty means bash.
	Shell string

	// CommandPrefix is prepended to every command, on its own line. It is how
	// a user gets aliases or a required environment into every command the
	// model runs — the model never sees it and cannot drop it.
	CommandPrefix string

	// Limits bound one call's output. The zero value uses the defaults.
	Limits Limits
}

func (b *Bash) Name() string { return "bash" }

func (b *Bash) Description() string {
	return fmt.Sprintf("Execute a bash command in the current working directory. Returns stdout "+
		"and stderr. Output is truncated to the last %d lines or %s (whichever is hit first). "+
		"Optionally provide a timeout in seconds.", DefaultMaxLines, FormatSize(DefaultMaxBytes))
}

func (b *Bash) Execution() Execution {
	// Neither read-only nor repeatable. A command can do anything, so the
	// declaration has to assume it did: a policy denying mutation must stop it,
	// and a call whose outcome was lost must not be run a second time.
	return Execution{}
}

// Prompt is what this tool tells the model about itself.
//
// The wording is Pi's, kept because it is what its models were given: a
// rephrasing is a different instruction, and the difference would show up as
// a behaviour change nobody could trace to a decision.
func (b *Bash) Prompt() Contribution {
	return Contribution{
		Snippet: "Execute bash commands (ls, grep, find, etc.)",
		// Pi's guideline here points the model at PI_* environment variables
		// carrying model and session details. This build exposes none, and a
		// guideline naming what is not there teaches the model to look for it
		// and report its absence as a problem.
		Guidelines: nil,
	}
}

func (b *Bash) Parameters() *Schema {
	return &Schema{Parameters: []Parameter{
		{
			Name:        "command",
			Kind:        KindString,
			Description: "Bash command to execute",
			Required:    true,
		},
		{
			Name:        "timeout",
			Kind:        KindNumber,
			Description: "Timeout in seconds (optional, no default timeout)",
		},
	}}
}

type bashArgs struct {
	Command string   `json:"command"`
	Timeout *float64 `json:"timeout"`
}

func (b *Bash) Call(ctx context.Context, args string) (Result, error) {
	var in bashArgs
	if err := json.Unmarshal([]byte(args), &in); err != nil {
		return Result{}, fmt.Errorf("bash: invalid arguments %q: %w", args, err)
	}
	if strings.TrimSpace(in.Command) == "" {
		return Result{}, fmt.Errorf("bash: command is required")
	}

	timeout, err := bashTimeout(in.Timeout)
	if err != nil {
		return Result{}, err
	}
	if _, err := os.Stat(b.Dir); err != nil {
		return Result{}, fmt.Errorf("bash: working directory does not exist: %s", b.Dir)
	}

	// The timeout is the caller's context narrowed, so a cancelled call and an
	// expired timer stop the command by the same path — one place that kills,
	// rather than two that can disagree about whether it died.
	runCtx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	shell := b.Shell
	if shell == "" {
		shell = "bash"
	}
	command := in.Command
	if b.CommandPrefix != "" {
		// Its own line, as Pi runs it: joined onto the command's line it would
		// change the command's first word instead of preceding it.
		command = b.CommandPrefix + "\n" + command
	}
	cmd := exec.CommandContext(runCtx, shell, "-c", command)
	cmd.Dir = b.Dir

	// Its own process group, so stopping the command stops what it started. A
	// kill aimed only at the shell leaves its children running, holding the
	// pipe open and the call waiting on output from a process nobody is
	// tracking — a test with a background sleep hangs for the full wait rather
	// than ending at the timeout.
	//
	// Unix only, which is what this repository builds and tests on. A Windows
	// port needs a job object here; it will not compile until someone writes
	// one, which is the right way for that to surface.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}

	// One buffer for both streams, interleaved as the command wrote them: a
	// diagnostic printed to stderr between two lines of stdout belongs where it
	// was printed, and separating them loses that ordering.
	var captured bytes.Buffer
	cmd.Stdout = &captured
	cmd.Stderr = &captured

	runErr := cmd.Run()
	shown := b.render(captured.String())

	switch {
	case errors.Is(runCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil:
		// What it had produced travels with the failure: a command killed after
		// printing where it got to is far more useful than the fact it was
		// killed.
		return Result{}, fmt.Errorf("bash: %s", appendStatus(shown,
			fmt.Sprintf("Command timed out after %s seconds", formatSeconds(timeout))))
	case ctx.Err() != nil:
		return Result{}, fmt.Errorf("bash: %s", appendStatus(shown, "Command aborted"))
	}

	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		// A non-zero exit is a failure the model must see as one, and the
		// output is the reason. Returning it as success with the code buried in
		// the text lets a model read a failed build as a finished one.
		return Result{}, fmt.Errorf("bash: %s", appendStatus(shown,
			fmt.Sprintf("Command exited with code %d", exitErr.ExitCode())))
	}
	if runErr != nil {
		return Result{}, fmt.Errorf("bash: %w", runErr)
	}
	if shown == "" {
		return Result{Content: "(no output)"}, nil
	}
	return Result{Content: shown}, nil
}

// render bounds what the command said, keeping the end.
func (b *Bash) render(raw string) string {
	cut := TruncateTail(raw, b.Limits)
	if !cut.Truncated {
		return cut.Content
	}
	first := cut.TotalLines - cut.OutputLines + 1
	switch {
	case cut.LastLinePartial:
		return fmt.Sprintf("%s\n\n[Showing the last %s of line %d]",
			cut.Content, FormatSize(cut.OutputBytes), cut.TotalLines)
	case cut.By == TruncatedByBytes:
		return fmt.Sprintf("%s\n\n[Showing lines %d-%d of %d (%s limit)]",
			cut.Content, first, cut.TotalLines, cut.TotalLines, FormatSize(cut.MaxBytes))
	default:
		return fmt.Sprintf("%s\n\n[Showing lines %d-%d of %d]",
			cut.Content, first, cut.TotalLines, cut.TotalLines)
	}
}

// bashTimeout validates what was asked for, in seconds.
func bashTimeout(seconds *float64) (time.Duration, error) {
	if seconds == nil {
		return 0, nil
	}
	v := *seconds
	// Refused rather than ignored. A model that asked for a bound and silently
	// got none would wait on a command it believed could not outlast it.
	if v <= 0 || v != v {
		return 0, fmt.Errorf("bash: invalid timeout: must be a positive number of seconds")
	}
	d := time.Duration(v * float64(time.Second))
	if d > MaxBashTimeout {
		return 0, fmt.Errorf("bash: invalid timeout: maximum is %s seconds",
			formatSeconds(MaxBashTimeout))
	}
	return d, nil
}

func formatSeconds(d time.Duration) string {
	return strings.TrimSuffix(strings.TrimRight(
		fmt.Sprintf("%.3f", d.Seconds()), "0"), ".")
}

// appendStatus puts the status after whatever the command managed to say.
func appendStatus(text, status string) string {
	if text == "" {
		return status
	}
	return text + "\n\n" + status
}
