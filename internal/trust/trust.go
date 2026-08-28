// Package trust records whether a project may configure this tool.
//
// The question exists because a project directory is somebody else's input: a
// cloned repository can carry a .pi-go/settings.json, and reading it means the
// repository configures the tool that is about to run shell commands on its
// contents. Trust is the user's answer, remembered so it is asked once per
// project rather than once per run.
package trust

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// FileName is what the trust file is called inside the agent directory.
const FileName = "trust.json"

// Decision is one recorded answer.
type Decision int

const (
	// Undecided means nobody has said. It is a real state, distinct from
	// refused: undecided asks, refused does not.
	Undecided Decision = iota
	Trusted
	Refused
)

func (d Decision) String() string {
	switch d {
	case Trusted:
		return "trusted"
	case Refused:
		return "refused"
	default:
		return "undecided"
	}
}

// Store is the trust file: canonical path to decision.
type Store struct {
	path string
	mu   sync.Mutex
}

// Open returns the store kept in an agent directory.
func Open(agentDir string) *Store {
	return &Store{path: filepath.Join(agentDir, FileName)}
}

// Path is where decisions are kept, for a message that has to name it.
func (s *Store) Path() string { return s.path }

// Get answers for a directory, honouring the NEAREST recorded ancestor.
//
// Walking up is what makes "trust the parent folder" mean anything: a decision
// recorded for ~/work covers every project under it, until a more specific
// entry says otherwise. Nearest wins over any ancestor, so one refused
// subdirectory inside a trusted tree stays refused.
func (s *Store) Get(dir string) (Decision, string, error) {
	all, err := s.read()
	if err != nil {
		return Undecided, "", err
	}
	current, err := canonical(dir)
	if err != nil {
		return Undecided, "", err
	}
	for {
		if decision, found := all[current]; found {
			return decision, current, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return Undecided, "", nil
		}
		current = parent
	}
}

// Set records a decision for a directory.
func (s *Store) Set(dir string, d Decision) error {
	path, err := canonical(dir)
	if err != nil {
		return err
	}
	return s.update(func(all map[string]Decision) {
		if d == Undecided {
			// Recording "undecided" is forgetting: the entry's absence is what
			// makes the question be asked again.
			delete(all, path)
			return
		}
		all[path] = d
	})
}

// Entries lists every recorded decision, in a stable order.
func (s *Store) Entries() ([]string, map[string]Decision, error) {
	all, err := s.read()
	if err != nil {
		return nil, nil, err
	}
	paths := make([]string, 0, len(all))
	for path := range all {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths, all, nil
}

// canonical resolves a path the way decisions are keyed, symlinks included, so
// two names for one directory share one answer.
func canonical(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("trust: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		// The directory may not exist yet; the absolute form is then the best
		// available key, and matches what it will resolve to once created.
		return abs, nil
	}
	return resolved, nil
}

func (s *Store) read() (map[string]Decision, error) {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]Decision{}, nil
		}
		return nil, fmt.Errorf("trust: %w", err)
	}
	// The file holds booleans, as Pi's does: readable and editable by hand.
	var stored map[string]bool
	if err := json.Unmarshal(raw, &stored); err != nil {
		return nil, fmt.Errorf("trust: %s is not readable: %w", s.path, err)
	}
	all := make(map[string]Decision, len(stored))
	for path, trusted := range stored {
		if trusted {
			all[path] = Trusted
		} else {
			all[path] = Refused
		}
	}
	return all, nil
}

func (s *Store) update(change func(map[string]Decision)) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	all, err := s.read()
	if err != nil {
		return err
	}
	change(all)

	stored := make(map[string]bool, len(all))
	for path, d := range all {
		stored[path] = d == Trusted
	}
	encoded, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return fmt.Errorf("trust: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("trust: %w", err)
	}
	temporary := s.path + ".writing"
	if err := os.WriteFile(temporary, append(encoded, '\n'), 0o600); err != nil {
		return fmt.Errorf("trust: %w", err)
	}
	if err := os.Rename(temporary, s.path); err != nil {
		os.Remove(temporary)
		return fmt.Errorf("trust: %w", err)
	}
	return nil
}
