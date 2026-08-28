package tools_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/tools"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}
	return path
}

func read(t *testing.T, tool *tools.Read, args string) string {
	t.Helper()
	got, err := tool.Call(context.Background(), args)
	if err != nil {
		t.Fatalf("read %s: %v", args, err)
	}
	return got.Content
}

// TestASmallFileComesBackWhole: the ordinary case, and the one every other
// behaviour here is a departure from.
func TestASmallFileComesBackWhole(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "one\ntwo\nthree")
	tool := &tools.Read{Root: dir}

	if got := read(t, tool, `{"path":"a.txt"}`); got != "one\ntwo\nthree" {
		t.Fatalf("a file that fits came back as %q", got)
	}
}

// TestOffsetCountsFromOne pins the address the model is told to use. Off by one
// here means every continuation silently repeats or skips a line.
func TestOffsetCountsFromOne(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "one\ntwo\nthree")
	tool := &tools.Read{Root: dir}

	if got := read(t, tool, `{"path":"a.txt","offset":1}`); got != "one\ntwo\nthree" {
		t.Fatalf("offset 1 came back as %q, and it must mean the first line", got)
	}
	if got := read(t, tool, `{"path":"a.txt","offset":2}`); got != "two\nthree" {
		t.Fatalf("offset 2 came back as %q", got)
	}
}

// TestAnOffsetPastTheEndSaysHowLongTheFileIs. A bare failure leaves the model
// guessing; the total is what lets it ask again correctly.
func TestAnOffsetPastTheEndSaysHowLongTheFileIs(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "one\ntwo")
	tool := &tools.Read{Root: dir}

	_, err := tool.Call(context.Background(), `{"path":"a.txt","offset":9}`)
	if err == nil {
		t.Fatal("an offset past the end was accepted")
	}
	if !strings.Contains(err.Error(), "2 lines total") {
		t.Fatalf("the failure does not say how long the file is: %v", err)
	}
}

// TestAModelsOwnLimitStillSaysThereIsMore. The model asked for this, so it is
// not truncation — but without the notice nothing distinguishes "the file ends
// here" from "you asked for ten lines".
func TestAModelsOwnLimitStillSaysThereIsMore(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "one\ntwo\nthree\nfour")
	tool := &tools.Read{Root: dir}

	got := read(t, tool, `{"path":"a.txt","limit":2}`)
	if !strings.HasPrefix(got, "one\ntwo") {
		t.Fatalf("a limit of two returned %q", got)
	}
	if !strings.Contains(got, "2 more lines in file") {
		t.Fatalf("the notice does not say what is left: %q", got)
	}
	if !strings.Contains(got, "offset=3") {
		t.Fatalf("the notice does not say where to continue: %q", got)
	}
}

// TestAskingForTheWholeFileSaysNothingExtra is the other half: a notice that
// appears when there is nothing more teaches the model to make a call that
// returns nothing.
func TestAskingForTheWholeFileSaysNothingExtra(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "one\ntwo")
	tool := &tools.Read{Root: dir}

	if got := read(t, tool, `{"path":"a.txt","limit":2}`); got != "one\ntwo" {
		t.Fatalf("a limit covering the whole file returned %q", got)
	}
}

// TestContinuingFromTheOffsetGivenReassemblesTheFile is the property the
// continuation notice promises. Following it must reach the end, without
// repeating or skipping a line.
func TestContinuingFromTheOffsetGivenReassemblesTheFile(t *testing.T) {
	dir := t.TempDir()
	var want []string
	for i := 1; i <= 250; i++ {
		want = append(want, "line "+strconv.Itoa(i))
	}
	writeFile(t, dir, "big.txt", strings.Join(want, "\n"))
	tool := &tools.Read{Root: dir, Limits: tools.Limits{MaxLines: 40}}

	var seen []string
	offset := 1
	for step := 0; step < 20; step++ {
		out := read(t, tool, fmt.Sprintf(`{"path":"big.txt","offset":%d}`, offset))
		body, notice, truncated := strings.Cut(out, "\n\n[Showing lines ")
		seen = append(seen, strings.Split(body, "\n")...)
		if !truncated {
			break
		}
		_, after, found := strings.Cut(notice, "Use offset=")
		if !found {
			t.Fatalf("a truncated read did not say where to continue: %q", out)
		}
		next, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(after), " to continue.]"))
		if err != nil {
			t.Fatalf("the offset in %q is not a number: %v", notice, err)
		}
		if next <= offset {
			t.Fatalf("offset %d was told to continue at %d, which does not advance", offset, next)
		}
		offset = next
	}

	if len(seen) != len(want) {
		t.Fatalf("following the offsets read %d lines of %d", len(seen), len(want))
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("line %d came back as %q, want %q", i+1, seen[i], want[i])
		}
	}
}

// TestALineTooLongToShowPointsSomewhereElse. There is no offset that helps, so
// repeating the notice about continuing would send the model in a circle.
func TestALineTooLongToShowPointsSomewhereElse(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "wide.txt", strings.Repeat("x", 400)+"\nsecond")
	tool := &tools.Read{Root: dir, Limits: tools.Limits{MaxBytes: 100}}

	got := read(t, tool, `{"path":"wide.txt"}`)
	if strings.Contains(got, "Use offset=") {
		t.Fatalf("a line that cannot be shown offered an offset that would not help: %q", got)
	}
	if !strings.Contains(got, "Line 1 is 400B, exceeds") {
		t.Fatalf("the notice does not say which line or how big: %q", got)
	}
	if !strings.Contains(got, "sed -n '1p'") {
		t.Fatalf("the notice does not name a way through: %q", got)
	}
}

// TestAMissingFileNamesBothPathsItTried. The path the model wrote is what it
// can act on; the resolved one is how a path that quietly landed somewhere
// unexpected becomes visible.
func TestAMissingFileNamesBothPathsItTried(t *testing.T) {
	tool := &tools.Read{Root: t.TempDir()}
	_, err := tool.Call(context.Background(), `{"path":"nope.txt"}`)
	if err == nil {
		t.Fatal("reading a missing file succeeded")
	}
	if !strings.Contains(err.Error(), "nope.txt") {
		t.Fatalf("the failure does not name the path asked for: %v", err)
	}
	if !strings.Contains(err.Error(), tool.Root) {
		t.Fatalf("the failure does not say where it actually looked: %v", err)
	}
}

// TestUnusableArgumentsFailAsArguments keeps a malformed call from reading
// something by accident.
func TestUnusableArgumentsFailAsArguments(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "one")
	tool := &tools.Read{Root: dir}

	for name, args := range map[string]string{
		"not JSON":     `{path: a.txt}`,
		"no path":      `{}`,
		"a blank path": `{"path":"   "}`,
		"a wrong type": `{"path":123}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := tool.Call(context.Background(), args); err == nil {
				t.Fatalf("%s was accepted", name)
			}
		})
	}
}

// TestReadingIsRepeatableAfterACrash: the scheduler needs this to be declared,
// not inferred, and reading changes nothing.
func TestReadingIsRepeatableAfterACrash(t *testing.T) {
	tool := &tools.Read{Root: t.TempDir()}
	if got := tool.Execution(); !got.ReadOnly || got.Replay != tools.ReplaySafe {
		t.Fatalf("read declares %+v", got)
	}
}

// TestReadRegisters proves the declared schema survives the registry's check,
// which is the gate everything else here depends on.
func TestReadRegisters(t *testing.T) {
	r := tools.NewRegistry()
	if err := r.Register(&tools.Read{Root: t.TempDir()}); err != nil {
		t.Fatalf("read did not register: %v", err)
	}
}
