package tools

import (
	"os"
	"path/filepath"
	"sync"
)

// mutations serialises changes to one file without serialising changes to
// different ones.
//
// A round of tool calls runs concurrently, so two calls can target the same
// file at once. Declaring the whole tool Sequential would fix that by
// serialising every call in the round, including the ones touching unrelated
// files — a per-file lock is the narrower answer, and the one Pi uses.
//
// Keyed by the path with symlinks resolved, so two names for one file take the
// same lock. A path that does not exist yet cannot be resolved and is keyed by
// its cleaned absolute form instead, which is the same key the file will have
// once it is created.
type mutations struct {
	mu    sync.Mutex
	locks map[string]*mutationLock
}

type mutationLock struct {
	mu sync.Mutex

	// held counts who still needs this lock, so the map does not grow forever
	// and a lock is never removed while another caller is waiting on it.
	held int
}

var fileMutations = &mutations{locks: map[string]*mutationLock{}}

func mutationKey(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		// The usual reason is that the file does not exist yet, which is
		// ordinary for a tool that creates one.
		return abs
	}
	return resolved
}

// do runs fn with this file held against other mutations of the same file.
func (m *mutations) do(path string, fn func() error) error {
	key := mutationKey(path)

	m.mu.Lock()
	lock, found := m.locks[key]
	if !found {
		lock = &mutationLock{}
		m.locks[key] = lock
	}
	lock.held++
	m.mu.Unlock()

	lock.mu.Lock()
	err := fn()
	lock.mu.Unlock()

	m.mu.Lock()
	lock.held--
	if lock.held == 0 {
		delete(m.locks, key)
	}
	m.mu.Unlock()

	return err
}

// resolvePath turns a model's path into one on this machine.
//
// A leading ~ is expanded because a model writes paths the way a person does.
// The macOS filename fallbacks Pi tries when a path does not exist — the narrow
// no-break space before AM/PM, the decomposed and curly-quote variants of
// screenshot names — are NOT ported, so a path that would need one fails as a
// missing file rather than silently resolving to a different one.
func resolvePath(root, path string) (string, error) {
	if path == "~" || len(path) > 1 && path[0] == '~' && path[1] == '/' {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, path[1:]), nil
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	return filepath.Join(root, path), nil
}
