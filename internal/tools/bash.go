package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/iamclancyliang/pi-go/internal/ai"
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

// BashResult is the complete outcome of one command. ExitCode is absent when
// the command was cancelled; otherwise it is the process's code, including
// zero for a successful command.
type BashResult = ai.BashResult

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

	result, err := b.Execute(ctx, in.Command, in.Timeout, nil)
	if err != nil {
		return Result{}, err
	}
	if result.Cancelled {
		return Result{}, fmt.Errorf("bash: %s", appendStatus(result.Output, "Command aborted"))
	}
	if result.ExitCode != nil && *result.ExitCode != 0 {
		return Result{}, fmt.Errorf("bash: %s", appendStatus(result.Output,
			fmt.Sprintf("Command exited with code %d", *result.ExitCode)))
	}
	if result.Output == "" {
		return Result{Content: "(no output)"}, nil
	}
	return Result{Content: result.Output}, nil
}

// Execute runs a command and optionally reports sanitized output chunks while
// the process is alive. It returns nonzero exit codes as data because RPC
// clients need the complete BashResult; Call formats those codes as tool
// failures for model tool-use.
func (b *Bash) Execute(ctx context.Context, command string, timeoutSeconds *float64, onChunk func(string)) (BashResult, error) {
	if strings.TrimSpace(command) == "" {
		return BashResult{}, fmt.Errorf("%w: command is required", ai.ErrBashArgument)
	}
	timeout, err := bashTimeout(timeoutSeconds)
	if err != nil {
		return BashResult{}, err
	}
	if _, err := os.Stat(b.Dir); err != nil {
		return BashResult{}, fmt.Errorf("bash: working directory does not exist: %s", b.Dir)
	}
	if ctx == nil {
		ctx = context.Background()
	}

	// The timeout narrows the caller's context, so cancellation and timeout
	// reach one process-killing path rather than two that can disagree about
	// whether the command stopped.
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
	if b.CommandPrefix != "" {
		command = b.CommandPrefix + "\n" + command
	}
	cmd := exec.CommandContext(runCtx, shell, "-c", command)
	cmd.Dir = b.Dir
	// Its own process group means abort stops children too. Otherwise a
	// background child can keep the output pipe open after the shell dies.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}

	captured := newBashCapture(onChunk, b.Limits)
	cmd.Stdout, cmd.Stderr = captured, captured
	runErr := cmd.Run()
	raw, fullOutputPath, captureErr := captured.Finish()
	if captureErr != nil {
		return BashResult{Output: raw}, captureErr
	}
	cut := TruncateTail(raw, b.Limits)
	result := BashResult{Output: b.render(raw), Truncated: cut.Truncated}
	if fullOutputPath != "" {
		result.FullOutputPath = fullOutputPath
	} else if cut.Truncated {
		path, spillErr := spillBashOutput(raw)
		if spillErr != nil {
			return result, spillErr
		}
		result.FullOutputPath = path
	}

	switch {
	case errors.Is(runCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil:
		return result, fmt.Errorf("bash: %s", appendStatus(result.Output,
			fmt.Sprintf("Command timed out after %s seconds", formatSeconds(timeout))))
	case ctx.Err() != nil:
		result.Cancelled = true
		return result, nil
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		code := exitErr.ExitCode()
		result.ExitCode = &code
		return result, nil
	}
	if runErr != nil {
		return result, fmt.Errorf("bash: %w", runErr)
	}
	code := 0
	result.ExitCode = &code
	return result, nil
}

type bashCapture struct {
	mu             sync.Mutex
	rolling        []byte
	pending        []byte
	inOSC          bool
	totalBytes     int
	spillThreshold int
	maxRolling     int
	spill          *os.File
	spillPath      string
	spillErr       error
	onChunk        func(string)
}

func newBashCapture(onChunk func(string), limits Limits) *bashCapture {
	_, maxBytes := limits.resolve()
	return &bashCapture{
		spillThreshold: maxBytes,
		maxRolling:     maxBytes * 2,
		onChunk:        onChunk,
	}
}

func (c *bashCapture) Write(data []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.spillErr != nil {
		return 0, c.spillErr
	}
	c.totalBytes += len(data)
	text := c.decodeLocked(data)
	if err := c.appendLocked(text); err != nil {
		return 0, err
	}
	if c.onChunk != nil && text != "" {
		c.onChunk(text)
	}
	return len(data), nil
}

func (c *bashCapture) Finish() (string, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.pending) > 0 {
		text, _, _ := sanitizeBashOutputChunkState(c.pending, c.inOSC)
		c.pending = nil
		c.inOSC = false
		if err := c.appendLocked(text); err != nil {
			return string(c.rolling), c.spillPath, err
		}
		if c.onChunk != nil && text != "" {
			c.onChunk(text)
		}
	}
	c.inOSC = false
	if c.spill != nil {
		if err := c.spill.Close(); err != nil && c.spillErr == nil {
			c.spillErr = fmt.Errorf("bash: closing full output file: %w", err)
		}
		c.spill = nil
	}
	return string(c.rolling), c.spillPath, c.spillErr
}

func (c *bashCapture) decodeLocked(data []byte) string {
	combined := make([]byte, 0, len(c.pending)+len(data))
	combined = append(combined, c.pending...)
	combined = append(combined, data...)
	c.pending = nil
	complete := len(combined)
	for i := 0; i < len(combined); {
		if !utf8.FullRune(combined[i:]) {
			complete = i
			c.pending = append(c.pending, combined[i:]...)
			break
		}
		_, size := utf8.DecodeRune(combined[i:])
		i += size
	}
	text, pending, inOSC := sanitizeBashOutputChunkState(combined[:complete], c.inOSC)
	c.pending = append(c.pending, pending...)
	c.inOSC = inOSC
	return text
}

func (c *bashCapture) appendLocked(text string) error {
	if c.totalBytes > c.spillThreshold && c.spill == nil {
		f, err := os.CreateTemp("", "pi-bash-*.log")
		if err != nil {
			c.spillErr = fmt.Errorf("bash: creating full output file: %w", err)
			return c.spillErr
		}
		if _, err := f.Write(c.rolling); err != nil {
			_ = f.Close()
			_ = os.Remove(f.Name())
			c.spillErr = fmt.Errorf("bash: writing full output file: %w", err)
			return c.spillErr
		}
		c.spill, c.spillPath = f, f.Name()
	}
	if c.spill != nil && text != "" {
		if _, err := c.spill.WriteString(text); err != nil {
			c.spillErr = fmt.Errorf("bash: writing full output file: %w", err)
			return c.spillErr
		}
	}
	c.rolling = append(c.rolling, text...)
	if len(c.rolling) > c.maxRolling {
		c.rolling = []byte(tailBytes(string(c.rolling), c.maxRolling))
	}
	return nil
}

var ansiEscape = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)

func sanitizeBashOutput(data []byte) string {
	text, _, _ := sanitizeBashOutputChunkState(data, false)
	return text
}

// sanitizeBashOutputChunk removes terminal title and hyperlink payloads before
// the remaining control-byte and CSI sanitization. OSC payloads can contain
// arbitrary URLs, so leaving an unterminated sequence in a streamed chunk
// would leak terminal control data into the model-visible output.
func sanitizeBashOutputChunkState(data []byte, inOSC bool) (string, []byte, bool) {
	clean := make([]byte, 0, len(data))
	for i := 0; i < len(data); {
		if inOSC {
			var pending []byte
			i, pending, inOSC = consumeOSC(data, i)
			if inOSC {
				return sanitizeBashControls(clean), pending, true
			}
			continue
		}

		if data[i] == 0x1b && i+1 == len(data) {
			return sanitizeBashControls(clean), data[i:], false
		}
		prefixSize, ok := oscPrefixSize(data, i)
		if ok {
			i += prefixSize
			inOSC = true
			continue
		}
		r, size := decodeBashRune(data, i)
		if size == 0 {
			size = 1
		}
		if r == '\u009c' {
			i += size
			continue
		}
		clean = append(clean, data[i:i+size]...)
		i += size
	}
	return sanitizeBashControls(clean), nil, inOSC
}

func decodeBashRune(data []byte, offset int) (rune, int) {
	r, size := utf8.DecodeRune(data[offset:])
	if size == 1 && (data[offset] == 0x9c || data[offset] == 0x9d) {
		return rune(data[offset]), 1
	}
	return r, size
}

func oscPrefixSize(data []byte, offset int) (int, bool) {
	if data[offset] == 0x1b && offset+1 < len(data) && data[offset+1] == ']' {
		return 2, true
	}
	r, size := decodeBashRune(data, offset)
	if r == '\u009d' {
		return size, true
	}
	return 0, false
}

// consumeOSC skips an OSC payload without retaining it. Only a trailing ESC
// needs to be carried across writes to decide whether the next byte is ST;
// this keeps an unterminated payload's memory bounded while it is streamed.
func consumeOSC(data []byte, offset int) (int, []byte, bool) {
	for offset < len(data) {
		r, size := decodeBashRune(data, offset)
		if r == '\a' || r == '\u009c' {
			return offset + size, nil, false
		}
		if data[offset] == 0x1b {
			if offset+1 == len(data) {
				return len(data), data[offset:], true
			}
			if data[offset+1] == '\\' {
				return offset + 2, nil, false
			}
			offset++
			continue
		}
		if size == 0 {
			size = 1
		}
		offset += size
	}
	return len(data), nil, true
}

func sanitizeBashControls(data []byte) string {
	text := ansiEscape.ReplaceAllString(strings.ToValidUTF8(string(data), "�"), "")
	text = strings.ReplaceAll(text, "\r", "")
	return strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || (r >= 0x20 && !(r >= 0x80 && r <= 0x9f)) {
			return r
		}
		return -1
	}, text)
}

func spillBashOutput(content string) (string, error) {
	f, err := os.CreateTemp("", "pi-bash-*.log")
	if err != nil {
		return "", fmt.Errorf("bash: creating full output file: %w", err)
	}
	name := f.Name()
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		_ = os.Remove(name)
		return "", fmt.Errorf("bash: writing full output file: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(name)
		return "", fmt.Errorf("bash: closing full output file: %w", err)
	}
	return name, nil
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
		return 0, fmt.Errorf("%w: timeout must be a positive number of seconds", ai.ErrBashArgument)
	}
	d := time.Duration(v * float64(time.Second))
	if d > MaxBashTimeout {
		return 0, fmt.Errorf("%w: timeout maximum is %s seconds", ai.ErrBashArgument,
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
