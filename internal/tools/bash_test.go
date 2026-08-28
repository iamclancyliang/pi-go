package tools_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/iamclancyliang/pi-go/internal/tools"
)

// TestACommandsOutputComesBack, which is the whole point.
func TestACommandsOutputComesBack(t *testing.T) {
	got := call(t, &tools.Bash{Dir: t.TempDir()}, `{"command":"echo hello"}`)
	// The trailing newline is kept, as Pi keeps it: the result is what the
	// command wrote, not a tidied version of it.
	if got != "hello\n" {
		t.Fatalf("echo returned %q", got)
	}
}

// TestStdoutAndStderrKeepTheirOrder. A diagnostic printed between two lines of
// output belongs where it was printed; separating the streams loses that, and
// the model reads a cause as though it came after its effect.
func TestStdoutAndStderrKeepTheirOrder(t *testing.T) {
	got := call(t, &tools.Bash{Dir: t.TempDir()},
		`{"command":"echo first; echo middle >&2; echo last"}`)
	if got != "first\nmiddle\nlast\n" {
		t.Fatalf("the streams came back as %q", got)
	}
}

// TestANonZeroExitIsAFailure. Returned as success with the code buried in the
// text, a failed build reads to the model as a finished one.
func TestANonZeroExitIsAFailure(t *testing.T) {
	_, err := (&tools.Bash{Dir: t.TempDir()}).Call(context.Background(),
		`{"command":"echo partway; exit 3"}`)
	if err == nil {
		t.Fatal("a command exiting non-zero reported success")
	}
	if !strings.Contains(err.Error(), "Command exited with code 3") {
		t.Fatalf("the failure does not name the code: %v", err)
	}
	// What it managed to print is the reason, and it must survive the failure.
	if !strings.Contains(err.Error(), "partway") {
		t.Fatalf("the output was lost with the failure: %v", err)
	}
}

// TestRunningInTheConfiguredDirectory, so a relative path in a command means
// what the other tools mean by it.
func TestRunningInTheConfiguredDirectory(t *testing.T) {
	dir := tree(t, map[string]string{"marker.txt": "x"})
	got := call(t, &tools.Bash{Dir: dir}, `{"command":"ls"}`)
	if !strings.Contains(got, "marker.txt") {
		t.Fatalf("the command ran somewhere else: %q", got)
	}
}

// TestThereIsNoDefaultTimeout. A build or a test run takes minutes, and a
// default that killed those would make the tool unusable for its actual work.
func TestThereIsNoDefaultTimeout(t *testing.T) {
	start := time.Now()
	got := call(t, &tools.Bash{Dir: t.TempDir()}, `{"command":"sleep 1.2; echo survived"}`)
	if got != "survived\n" {
		t.Fatalf("a command running over a second returned %q", got)
	}
	if time.Since(start) < time.Second {
		t.Fatal("the command did not actually run for a second")
	}
}

// TestATimeoutStopsTheCommandAndKeepsWhatItSaid.
func TestATimeoutStopsTheCommandAndKeepsWhatItSaid(t *testing.T) {
	start := time.Now()
	_, err := (&tools.Bash{Dir: t.TempDir()}).Call(context.Background(),
		`{"command":"echo before; sleep 30","timeout":1}`)
	if err == nil {
		t.Fatal("a command past its timeout reported success")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("the timeout took %s to fire", elapsed)
	}
	if !strings.Contains(err.Error(), "timed out after 1 seconds") {
		t.Fatalf("the failure does not say it timed out: %v", err)
	}
	if !strings.Contains(err.Error(), "before") {
		t.Fatalf("what the command printed before dying was lost: %v", err)
	}
}

// TestAnImpossibleTimeoutIsRefusedRatherThanIgnored. A model that asked for a
// bound and silently got none would wait on a command it believed could not
// outlast it.
func TestAnImpossibleTimeoutIsRefusedRatherThanIgnored(t *testing.T) {
	for name, args := range map[string]string{
		"zero":     `{"command":"echo x","timeout":0}`,
		"negative": `{"command":"echo x","timeout":-5}`,
		"absurd":   `{"command":"echo x","timeout":999999999}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := (&tools.Bash{Dir: t.TempDir()}).Call(context.Background(), args); err == nil {
				t.Fatalf("a %s timeout was accepted", name)
			}
		})
	}
}

// TestKillingACommandKillsWhatItStarted. A kill aimed only at the shell leaves
// its children holding the pipe, and the call waits on output from a process
// nobody is tracking any more.
func TestKillingACommandKillsWhatItStarted(t *testing.T) {
	done := make(chan error, 1)
	go func() {
		_, err := (&tools.Bash{Dir: t.TempDir()}).Call(context.Background(),
			`{"command":"sleep 60 & sleep 60; echo never","timeout":1}`)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("the command outlived its timeout and succeeded")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the call never returned; a child kept the pipe open")
	}
}

// TestACancelledCallStopsTheCommand, and says it was stopped rather than that
// it failed.
func TestACancelledCallStopsTheCommand(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	_, err := (&tools.Bash{Dir: t.TempDir()}).Call(ctx, `{"command":"sleep 30"}`)
	if err == nil {
		t.Fatal("a cancelled command reported success")
	}
	if !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("a cancelled command was reported as something else: %v", err)
	}
}

// TestOutputIsTruncatedFromTheEnd. A command's useful part is where it stopped:
// keeping the first lines of a build log and discarding the failure is exactly
// the wrong half.
func TestOutputIsTruncatedFromTheEnd(t *testing.T) {
	tool := &tools.Bash{Dir: t.TempDir(), Limits: tools.Limits{MaxLines: 5}}
	got := call(t, tool, `{"command":"seq 1 100"}`)

	if !strings.Contains(got, "100") {
		t.Fatalf("the end of the output was discarded: %q", got)
	}
	if strings.Contains(got, "\n1\n") || strings.HasPrefix(got, "1\n") {
		t.Fatalf("the start was kept instead of the end: %q", got)
	}
	if !strings.Contains(got, "of 100") {
		t.Fatalf("the notice does not say how much was withheld: %q", got)
	}
}

// TestNoOutputIsSaidOutright rather than returning an empty string, which reads
// as a tool that failed quietly.
func TestNoOutputIsSaidOutright(t *testing.T) {
	if got := call(t, &tools.Bash{Dir: t.TempDir()}, `{"command":"true"}`); got != "(no output)" {
		t.Fatalf("a silent command returned %q", got)
	}
}

// TestBashIsNotOfferedAsReadOnlyOrRepeatable: a command can do anything, so the
// declaration has to assume it did.
func TestBashIsNotOfferedAsReadOnlyOrRepeatable(t *testing.T) {
	got := (&tools.Bash{Dir: t.TempDir()}).Execution()
	if got.ReadOnly {
		t.Fatal("bash declares itself read-only, and a policy denying mutation would let it through")
	}
	if got.Replay != tools.ReplayNever {
		t.Fatal("bash declares itself repeatable")
	}
}

// TestBashRegisters proves the declared schema survives the registry's check.
func TestBashRegisters(t *testing.T) {
	if err := tools.NewRegistry().Register(&tools.Bash{Dir: t.TempDir()}); err != nil {
		t.Fatalf("bash did not register: %v", err)
	}
}
