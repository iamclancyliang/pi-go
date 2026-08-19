package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// The v0 fixture tools.
//
// Tool execution has to be deterministic. Two tools are provided with
// DIFFERENT execution metadata, because the scheduling behaviour only becomes
// observable with a contrast: one batch containing a tool that cannot run
// concurrently, and the same pair of tools run once serialised and once in
// parallel. A single tool cannot express either case.
//
// These are fixtures, not a product tool catalogue. They exist to make the
// contracts observable.

// FileRead is a deterministic read-only tool over an in-memory file map.
//
// Parallel-safe: reading two different paths concurrently is exactly what the
// parallel case needs.
type FileRead struct {
	// Files is the fixed content this tool serves. Nil is valid and yields
	// a not-found error for every path.
	Files map[string]string

	// Delay, if set, is how long each call takes. It exists so a test can
	// observe whether execution intervals overlap. Tests that assert
	// ordering must not depend on it being long enough to win a race — use
	// the emitted start/end events, not timing.
	Delay time.Duration

	// calls records invocation order for assertions.
	mu    sync.Mutex
	calls []string
}

// Name implements Tool.
func (f *FileRead) Name() string { return "file_read" }

// Description implements Tool.
func (f *FileRead) Description() string {
	return "Read the contents of a file at the given path. Read-only."
}

// Execution implements Tool. Parallel-safe and read-only.
func (f *FileRead) Execution() Execution {
	return Execution{Sequential: false, ReadOnly: true}
}

// Call implements Tool.
func (f *FileRead) Call(ctx context.Context, args string) (Result, error) {
	var in struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(args), &in); err != nil {
		return Result{}, fmt.Errorf("file_read: invalid arguments %q: %w", args, err)
	}
	if in.Path == "" {
		return Result{}, fmt.Errorf("file_read: path is required")
	}

	f.mu.Lock()
	f.calls = append(f.calls, in.Path)
	f.mu.Unlock()

	if f.Delay > 0 {
		select {
		case <-ctx.Done():
			// Cancellation is an observable outcome, not a panic:
			// a cancelled tool must still produce an event.
			return Result{}, ctx.Err()
		case <-time.After(f.Delay):
		}
	}

	content, ok := f.Files[in.Path]
	if !ok {
		return Result{}, fmt.Errorf("file_read: no such file: %s", in.Path)
	}
	return Result{Content: content}, nil
}

// Calls returns the paths this tool was asked for, in invocation order.
func (f *FileRead) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// ListFiles is a deterministic read-only tool that is declared SEQUENTIAL.
//
// The sequential flag is the point of this fixture: without a tool that
// declares it, "one tool in a batch cannot run concurrently" is not
// constructible at all.
type ListFiles struct {
	Files map[string]string

	mu    sync.Mutex
	calls int
}

// Name implements Tool.
func (l *ListFiles) Name() string { return "list_files" }

// Description implements Tool.
func (l *ListFiles) Description() string {
	return "List the known file paths, optionally filtered by prefix. Read-only."
}

// Execution implements Tool.
//
// Declared Sequential so a batch containing it cannot overlap. Nothing about
// listing files inherently requires that — it is declared so the scheduling
// contract has a tool that exercises it.
func (l *ListFiles) Execution() Execution {
	return Execution{Sequential: true, ReadOnly: true}
}

// Call implements Tool.
func (l *ListFiles) Call(ctx context.Context, args string) (Result, error) {
	var in struct {
		Prefix string `json:"prefix"`
	}
	// An empty payload is a legitimate "no arguments" call.
	if strings.TrimSpace(args) != "" {
		if err := json.Unmarshal([]byte(args), &in); err != nil {
			return Result{}, fmt.Errorf("list_files: invalid arguments %q: %w", args, err)
		}
	}

	l.mu.Lock()
	l.calls++
	l.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	paths := make([]string, 0, len(l.Files))
	for p := range l.Files {
		if in.Prefix == "" || strings.HasPrefix(p, in.Prefix) {
			paths = append(paths, p)
		}
	}
	// Sorted: map order would make the golden trace nondeterministic.
	sort.Strings(paths)
	return Result{Content: strings.Join(paths, "\n")}, nil
}

// Calls returns how many times this tool ran.
func (l *ListFiles) Calls() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls
}

// FixtureFiles is the shared file set for v0 fixtures, so the golden trace has
// one obvious source of truth.
func FixtureFiles() map[string]string {
	return map[string]string{
		"README.md":  "pi-go tracer bullet fixture\n",
		"config.yml": "mode: tracer\n",
	}
}

// NewFixtureRegistry returns a Registry with both v0 fixture tools registered.
func NewFixtureRegistry() (*Registry, *FileRead, *ListFiles) {
	files := FixtureFiles()
	fr := &FileRead{Files: files}
	lf := &ListFiles{Files: files}

	r := NewRegistry()
	r.MustRegister(fr)
	r.MustRegister(lf)
	return r, fr, lf
}
