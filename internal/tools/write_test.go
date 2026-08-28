package tools_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/tools"
)

// TestWritingCreatesTheFileAndItsParents. A model that had to create each
// directory first would spend calls on something the tool can settle.
func TestWritingCreatesTheFileAndItsParents(t *testing.T) {
	dir := t.TempDir()
	tool := &tools.Write{Root: dir}

	got := call(t, tool, `{"path":"a/b/c.txt","content":"hello"}`)
	if !strings.Contains(got, "a/b/c.txt") {
		t.Fatalf("the result does not name what was written: %q", got)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "a", "b", "c.txt"))
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if string(raw) != "hello" {
		t.Fatalf("the file holds %q", raw)
	}
}

// TestWritingReplacesTheWholeFile: this tool owns the case where the previous
// contents do not matter, so anything left behind would be a surprise.
func TestWritingReplacesTheWholeFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "the original, which is longer")
	call(t, &tools.Write{Root: dir}, `{"path":"a.txt","content":"short"}`)

	raw, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if string(raw) != "short" {
		t.Fatalf("the file holds %q, so something of the original survived", raw)
	}
}

// TestAnEmptyStringIsAFileAndAMissingFieldIsAMistake. Truncating a file because
// an argument was absent is not something to infer from silence.
func TestAnEmptyStringIsAFileAndAMissingFieldIsAMistake(t *testing.T) {
	dir := t.TempDir()
	tool := &tools.Write{Root: dir}

	call(t, tool, `{"path":"empty.txt","content":""}`)
	raw, err := os.ReadFile(filepath.Join(dir, "empty.txt"))
	if err != nil || len(raw) != 0 {
		t.Fatalf("writing an empty string produced %q, %v", raw, err)
	}

	writeFile(t, dir, "keep.txt", "precious")
	if _, err := tool.Call(context.Background(), `{"path":"keep.txt"}`); err == nil {
		t.Fatal("a call with no content argument was accepted")
	}
	if raw, _ := os.ReadFile(filepath.Join(dir, "keep.txt")); string(raw) != "precious" {
		t.Fatalf("a rejected call still changed the file: %q", raw)
	}
}

// TestWritingIsNotOfferedAsReadOnlyOrRepeatable is what the policy seam and the
// crash recovery both branch on. Getting either wrong is silent: a mutation
// would pass a read-only gate, or a lost call would be replayed over whatever
// stands there now.
func TestWritingIsNotOfferedAsReadOnlyOrRepeatable(t *testing.T) {
	got := (&tools.Write{Root: t.TempDir()}).Execution()
	if got.ReadOnly {
		t.Fatal("write declares itself read-only, and a policy denying mutation would let it through")
	}
	if got.Replay != tools.ReplayNever {
		t.Fatal("write declares itself repeatable, and a lost call would overwrite whatever stands there now")
	}
}

// TestWritesToDifferentFilesAreNotSerialised covers the tool end of the
// per-file lock. The exclusion itself is proved in mutation_internal_test.go,
// because a whole-file write of a few kilobytes usually completes in one
// syscall — so a test that writes concurrently and finds the file intact passes
// with the lock removed, and is evidence of nothing.
func TestWritesToDifferentFilesAreNotSerialised(t *testing.T) {
	dir := t.TempDir()
	tool := &tools.Write{Root: dir}

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, _ = tool.Call(context.Background(),
				`{"path":"file`+string(rune('a'+n))+`.txt","content":"x"}`)
		}(i)
	}
	wg.Wait()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading the directory: %v", err)
	}
	if len(entries) != 16 {
		t.Fatalf("sixteen concurrent writes produced %d files", len(entries))
	}
}

// TestWriteRegisters proves the declared schema survives the registry's check.
func TestWriteRegisters(t *testing.T) {
	if err := tools.NewRegistry().Register(&tools.Write{Root: t.TempDir()}); err != nil {
		t.Fatalf("write did not register: %v", err)
	}
}
