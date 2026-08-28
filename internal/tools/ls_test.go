package tools_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/tools"
)

func call(t *testing.T, tool tools.Tool, args string) string {
	t.Helper()
	got, err := tool.Call(context.Background(), args)
	if err != nil {
		t.Fatalf("%s %s: %v", tool.Name(), args, err)
	}
	return got.Content
}

// TestListingIsSortedTheWayAPersonReads: a listing grouped by case puts every
// capitalised name ahead of the lowercase ones, which is not the order anyone
// scans for a filename.
func TestListingIsSortedTheWayAPersonReads(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"zeta.txt", "Alpha.txt", "beta.txt", "README.md"} {
		writeFile(t, dir, name, "x")
	}
	got := call(t, &tools.Ls{Root: dir}, `{}`)

	want := []string{"Alpha.txt", "beta.txt", "README.md", "zeta.txt"}
	if lines := strings.Split(got, "\n"); !equalStrings(lines, want) {
		t.Fatalf("listing came back as %v, want %v", lines, want)
	}
}

// TestADirectoryIsMarkedAsOne. The suffix is what tells the model where it can
// descend; without it a directory is indistinguishable from a file it would try
// to read.
func TestADirectoryIsMarkedAsOne(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "file.txt", "x")
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatalf("making the directory: %v", err)
	}
	got := call(t, &tools.Ls{Root: dir}, `{}`)
	if !strings.Contains(got, "sub/") {
		t.Fatalf("a directory came back unmarked: %q", got)
	}
	if strings.Contains(got, "file.txt/") {
		t.Fatalf("a file was marked as a directory: %q", got)
	}
}

// TestDotfilesAreListed: they are exactly what a coding agent needs to see, and
// a shell's default of hiding them is a display convention, not a rule about
// what exists.
func TestDotfilesAreListed(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".gitignore", "x")
	if got := call(t, &tools.Ls{Root: dir}, `{}`); !strings.Contains(got, ".gitignore") {
		t.Fatalf("a dotfile was hidden: %q", got)
	}
}

// TestAnEmptyDirectorySaysSo. An empty result reads as a tool that failed
// quietly rather than a directory with nothing in it.
func TestAnEmptyDirectorySaysSo(t *testing.T) {
	if got := call(t, &tools.Ls{Root: t.TempDir()}, `{}`); got != "(empty directory)" {
		t.Fatalf("an empty directory came back as %q", got)
	}
}

// TestTheEntryLimitSaysHowToSeeMore. A bound with no way past it makes the
// model retry the same call.
func TestTheEntryLimitSaysHowToSeeMore(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		writeFile(t, dir, name, "x")
	}
	got := call(t, &tools.Ls{Root: dir}, `{"limit":2}`)

	if lines := strings.Split(strings.SplitN(got, "\n\n[", 2)[0], "\n"); len(lines) != 2 {
		t.Fatalf("a limit of two returned %d entries: %q", len(lines), got)
	}
	if !strings.Contains(got, "2 entries limit reached") {
		t.Fatalf("the notice does not say the limit was reached: %q", got)
	}
	if !strings.Contains(got, "limit=4") {
		t.Fatalf("the notice does not say how to see more: %q", got)
	}
}

// TestListingTheWholeDirectorySaysNothingExtra is the other half: a notice that
// fires when nothing was withheld teaches the model to make a pointless call.
func TestListingTheWholeDirectorySaysNothingExtra(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "only.txt", "x")
	if got := call(t, &tools.Ls{Root: dir}, `{}`); got != "only.txt" {
		t.Fatalf("a complete listing came back as %q", got)
	}
}

// TestTheByteBudgetAppliesToListingsToo, and it is the only other bound: the
// entry count is already capped, so a line limit would cut the output somewhere
// the notice could not explain.
func TestTheByteBudgetAppliesToListingsToo(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 40; i++ {
		writeFile(t, dir, strings.Repeat("n", 20)+string(rune('a'+i)), "x")
	}
	got := call(t, &tools.Ls{Root: dir, Limits: tools.Limits{MaxBytes: 100}}, `{}`)
	if !strings.Contains(got, "limit reached") {
		t.Fatalf("a listing over the byte budget carried no notice: %q", got)
	}
	body := strings.SplitN(got, "\n\n[", 2)[0]
	if len(body) > 100 {
		t.Fatalf("the listing is %d bytes, over the 100-byte budget", len(body))
	}
}

// TestListingSomethingThatIsNotADirectoryFailsAsThat, rather than as an
// unreadable directory, which would send the model looking at permissions.
func TestListingSomethingThatIsNotADirectoryFailsAsThat(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "file.txt", "x")
	_, err := (&tools.Ls{Root: dir}).Call(context.Background(), `{"path":"file.txt"}`)
	if err == nil {
		t.Fatal("listing a file succeeded")
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("the failure does not say what was wrong: %v", err)
	}
}

// TestListingDefaultsToTheRoot, so a model that has no path yet can still look
// around.
func TestListingDefaultsToTheRoot(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "here.txt", "x")
	for _, args := range []string{`{}`, ``, `{"path":""}`} {
		if got := call(t, &tools.Ls{Root: dir}, args); !strings.Contains(got, "here.txt") {
			t.Fatalf("args %q listed %q", args, got)
		}
	}
}

// TestLsRegisters proves the declared schema survives the registry's check.
func TestLsRegisters(t *testing.T) {
	if err := tools.NewRegistry().Register(&tools.Ls{Root: t.TempDir()}); err != nil {
		t.Fatalf("ls did not register: %v", err)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
