package cli

// AppMode is what the application runs as.
//
// Four values against the flag's three, and the difference is the point: there
// is no --mode interactive, yet interactive is reachable. See ResolveAppMode.
type AppMode string

const (
	AppInteractive AppMode = "interactive"
	AppPrint       AppMode = "print"
	AppJSON        AppMode = "json"
	AppRPC         AppMode = "rpc"
)

// ResolveAppMode decides how to run, from the flags AND the terminal.
//
// The terminal is half the decision, which is what makes this worth its own
// function. The same command line runs interactively in a terminal and prints
// when either stream is redirected — so `pi "do a thing" > out.txt` is a
// one-shot, and a port that mapped flags to modes statically would drive a
// full-screen interface into a pipe.
//
// The order matters as much as the conditions: an explicit --mode rpc or json
// wins over redirection, and --print wins over a terminal.
func ResolveAppMode(args Args, stdinIsTTY, stdoutIsTTY bool) AppMode {
	switch args.Mode {
	case ModeRPC:
		return AppRPC
	case ModeJSON:
		return AppJSON
	}
	if args.Print || !stdinIsTTY || !stdoutIsTTY {
		return AppPrint
	}
	// Reached by --mode text in a terminal as well as by no --mode at all:
	// text is not a mode, it is the way to say "decide from the environment".
	return AppInteractive
}

// OutputFormat maps a resolved mode back to how output is written.
func OutputFormat(mode AppMode) string {
	if mode == AppJSON {
		return "json"
	}
	return "text"
}
