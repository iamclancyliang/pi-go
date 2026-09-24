package ai

import (
	"errors"
	"fmt"
	"time"
)

// ErrBashArgument marks a shell request that cannot be started because its
// command or timeout is invalid. RPC maps this validation failure to its
// bad-argument kind instead of treating it as an internal execution error.
var ErrBashArgument = errors.New("bash: invalid argument")

// BashResult is the wire-safe outcome of a direct shell command. ExitCode is
// zero for success and absent only when the process was cancelled or killed
// before it returned a code.
type BashResult struct {
	Output         string `json:"output"`
	ExitCode       *int   `json:"exitCode,omitempty"`
	Cancelled      bool   `json:"cancelled"`
	Truncated      bool   `json:"truncated"`
	FullOutputPath string `json:"fullOutputPath,omitempty"`
}

// BashExecution is a direct shell command recorded in conversation truth.
// It is kept as a message payload so session persistence and model projection
// can make the same exclusion decision without a second history store.
type BashExecution struct {
	Command            string    `json:"command"`
	Output             string    `json:"output"`
	ExitCode           *int      `json:"exitCode,omitempty"`
	Cancelled          bool      `json:"cancelled"`
	Truncated          bool      `json:"truncated"`
	FullOutputPath     string    `json:"fullOutputPath,omitempty"`
	ExcludeFromContext bool      `json:"excludeFromContext,omitempty"`
	Timestamp          time.Time `json:"timestamp"`
}

// BashExecutionToText converts a recorded shell result into the user message
// a model sees on the next prompt. An excluded result is filtered by Session's
// projection before this function is called.
func BashExecutionToText(m BashExecution) string {
	text := fmt.Sprintf("Ran `%s`\n", m.Command)
	if m.Output == "" {
		text += "(no output)"
	} else {
		text += "```\n" + m.Output + "\n```"
	}
	if m.Cancelled {
		text += "\n\n(command cancelled)"
	} else if m.ExitCode != nil && *m.ExitCode != 0 {
		text += fmt.Sprintf("\n\nCommand exited with code %d", *m.ExitCode)
	}
	if m.Truncated && m.FullOutputPath != "" {
		text += "\n\n[Output truncated. Full output: " + m.FullOutputPath + "]"
	}
	return text
}
