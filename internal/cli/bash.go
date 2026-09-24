package cli

import (
	"context"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/session"
	"github.com/iamclancyliang/pi-go/internal/tools"
)

type bashExecutor interface {
	Execute(context.Context, string, *float64, func(string)) (tools.BashResult, error)
}

func executeBash(ctx context.Context, sess *session.Session, runner bashExecutor,
	command string, excludeFromContext bool, onChunk func(string),
) (tools.BashResult, error) {
	return executeBashWithTimeout(ctx, sess, runner, command, nil, excludeFromContext, onChunk)
}

func executeBashWithTimeout(ctx context.Context, sess *session.Session, runner bashExecutor,
	command string, timeout *float64, excludeFromContext bool, onChunk func(string),
) (tools.BashResult, error) {
	result, err := runner.Execute(ctx, command, timeout, onChunk)
	if err != nil {
		return result, err
	}
	execution := ai.BashExecution{
		Command:            command,
		Output:             result.Output,
		ExitCode:           result.ExitCode,
		Cancelled:          result.Cancelled,
		Truncated:          result.Truncated,
		FullOutputPath:     result.FullOutputPath,
		ExcludeFromContext: excludeFromContext,
		Timestamp:          time.Now(),
	}
	if err := sess.AppendBashExecution(execution); err != nil {
		return result, err
	}
	return result, nil
}

func bashResultText(result tools.BashResult) string {
	if result.Output != "" {
		return result.Output
	}
	if result.Cancelled {
		return "Command aborted"
	}
	return "(no output)"
}
