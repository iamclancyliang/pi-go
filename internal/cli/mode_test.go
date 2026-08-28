package cli_test

import (
	"testing"

	"github.com/iamclancyliang/pi-go/internal/cli"
)

// TestTheTerminalIsHalfTheDecision is the rule a static flag-to-mode table gets
// wrong, and gets wrong invisibly: the same command line must run interactively
// in a terminal and print when either stream is redirected. A port that decided
// from flags alone would drive a full-screen interface into a pipe.
func TestTheTerminalIsHalfTheDecision(t *testing.T) {
	none := cli.Args{}
	cases := []struct {
		name                string
		stdinTTY, stdoutTTY bool
		want                cli.AppMode
	}{
		{"both are terminals", true, true, cli.AppInteractive},
		{"output is redirected", true, false, cli.AppPrint},
		{"input is piped in", false, true, cli.AppPrint},
		{"neither is a terminal", false, false, cli.AppPrint},
	}
	for _, c := range cases {
		if got := cli.ResolveAppMode(none, c.stdinTTY, c.stdoutTTY); got != c.want {
			t.Errorf("%s resolved to %q, want %q", c.name, got, c.want)
		}
	}
}

// TestModeTextMeansLetTheEnvironmentDecide. It is the only --mode value that
// does not name a mode, and reading it as one would make it a fourth mode that
// does not exist.
func TestModeTextMeansLetTheEnvironmentDecide(t *testing.T) {
	args := cli.Args{Mode: cli.ModeText}
	if got := cli.ResolveAppMode(args, true, true); got != cli.AppInteractive {
		t.Fatalf("--mode text in a terminal resolved to %q", got)
	}
	if got := cli.ResolveAppMode(args, true, false); got != cli.AppPrint {
		t.Fatalf("--mode text with output redirected resolved to %q", got)
	}
}

// TestAnExplicitModeOutranksTheTerminal: rpc and json are asked for, not
// detected, and a redirected stream must not turn either into print.
func TestAnExplicitModeOutranksTheTerminal(t *testing.T) {
	for mode, want := range map[cli.Mode]cli.AppMode{
		cli.ModeRPC:  cli.AppRPC,
		cli.ModeJSON: cli.AppJSON,
	} {
		args := cli.Args{Mode: mode}
		for _, tty := range []bool{true, false} {
			if got := cli.ResolveAppMode(args, tty, tty); got != want {
				t.Fatalf("--mode %s with tty=%v resolved to %q, want %q", mode, tty, got, want)
			}
		}
		// Even --print does not override an explicitly requested protocol.
		withPrint := cli.Args{Mode: mode, Print: true}
		if got := cli.ResolveAppMode(withPrint, true, true); got != want {
			t.Fatalf("--mode %s with --print resolved to %q, want %q", mode, got, want)
		}
	}
}

// TestPrintOutranksATerminal, which is what makes -p usable interactively.
func TestPrintOutranksATerminal(t *testing.T) {
	if got := cli.ResolveAppMode(cli.Args{Print: true}, true, true); got != cli.AppPrint {
		t.Fatalf("--print in a terminal resolved to %q", got)
	}
}

// TestOutputFormatFollowsTheResolvedMode: only json writes json, and every
// other mode — including rpc — writes text.
func TestOutputFormatFollowsTheResolvedMode(t *testing.T) {
	for mode, want := range map[cli.AppMode]string{
		cli.AppJSON:        "json",
		cli.AppPrint:       "text",
		cli.AppInteractive: "text",
		cli.AppRPC:         "text",
	} {
		if got := cli.OutputFormat(mode); got != want {
			t.Errorf("%q writes %q, want %q", mode, got, want)
		}
	}
}
