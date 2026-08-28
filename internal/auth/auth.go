// Package auth keeps provider credentials between runs.
//
// A key given once should not have to be given again, and putting it in the
// environment means every process the agent starts inherits it — including the
// shell commands the model runs. A file this process reads on demand keeps it
// out of that.
package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// FileName is what the credential file is called inside the agent directory.
const FileName = "auth.json"

// Credential is what is stored for one provider.
//
// The key is unexported and the type refuses to format itself, so a credential
// cannot reach a log line, a test failure or a %+v by accident. Getting it out
// takes asking for it by name, which is a thing a reader can find.
type Credential struct {
	kind string
	key  string
}

// APIKey builds a credential from a key the user supplied.
func APIKey(key string) Credential {
	return Credential{kind: "api_key", key: strings.TrimSpace(key)}
}

// Kind names what sort of credential this is, without disclosing it.
func (c Credential) Kind() string { return c.kind }

// Key is the secret. Named so that every place it escapes is greppable.
func (c Credential) Key() string { return c.key }

// String and GoString keep the key out of anything that formats this value.
func (c Credential) String() string   { return "auth.Credential{Kind:" + c.kind + "}" }
func (c Credential) GoString() string { return c.String() }

// MarshalJSON writes the stored form.
func (c Credential) MarshalJSON() ([]byte, error) {
	return json.Marshal(storedCredential{Kind: c.kind, Key: c.key})
}

// UnmarshalJSON reads the stored form.
func (c *Credential) UnmarshalJSON(raw []byte) error {
	var stored storedCredential
	if err := json.Unmarshal(raw, &stored); err != nil {
		return err
	}
	c.kind, c.key = stored.Kind, stored.Key
	return nil
}

type storedCredential struct {
	Kind string `json:"kind"`
	Key  string `json:"key"`
}

// Store is the credential file.
//
// Read on demand rather than cached for the life of the process: a key removed
// in one terminal must stop working in another, and a key added must start.
type Store struct {
	path string
	mu   sync.Mutex
}

// Open returns the store kept in an agent directory.
func Open(agentDir string) *Store {
	return &Store{path: filepath.Join(agentDir, FileName)}
}

// Path is where credentials are kept, for a message that has to name it.
func (s *Store) Path() string { return s.path }

// Get returns the credential for a provider, if there is one.
func (s *Store) Get(provider string) (Credential, bool, error) {
	all, err := s.read()
	if err != nil {
		return Credential{}, false, err
	}
	c, found := all[provider]
	return c, found, nil
}

// Providers lists what has a credential stored, in a stable order.
//
// Names only. A listing that showed keys would put every one of them into the
// terminal scrollback of anyone who ran it.
func (s *Store) Providers() ([]string, error) {
	all, err := s.read()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(all))
	for name := range all {
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

// Set records a credential.
func (s *Store) Set(provider string, c Credential) error {
	return s.update(func(all map[string]Credential) { all[provider] = c })
}

// Remove forgets one. It is not an error to remove what is not there: the
// caller asked for the credential to be gone, and it is.
func (s *Store) Remove(provider string) error {
	return s.update(func(all map[string]Credential) { delete(all, provider) })
}

func (s *Store) read() (map[string]Credential, error) {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]Credential{}, nil
		}
		return nil, fmt.Errorf("auth: %w", err)
	}
	all := map[string]Credential{}
	if err := json.Unmarshal(raw, &all); err != nil {
		return nil, fmt.Errorf("auth: %s is not readable: %w", s.path, err)
	}
	return all, nil
}

// update rewrites the file with a change applied.
//
// Written to a neighbouring file and renamed, so a failure part-way leaves the
// previous credentials rather than a truncated file — losing a key to a full
// disk means being locked out of every provider at once.
//
// The lock is this process's only. Two processes writing at the same instant
// can still lose one change, which is a smaller problem than it looks: adding a
// credential is something a person does, not something that happens in a loop.
func (s *Store) update(change func(map[string]Credential)) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	all, err := s.read()
	if err != nil {
		return err
	}
	change(all)

	encoded, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return fmt.Errorf("auth: %w", err)
	}
	// The directory is the user's alone: a credential file inside a
	// world-readable directory is readable by listing it open.
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("auth: %w", err)
	}
	temporary := s.path + ".writing"
	if err := os.WriteFile(temporary, append(encoded, '\n'), 0o600); err != nil {
		return fmt.Errorf("auth: %w", err)
	}
	if err := os.Rename(temporary, s.path); err != nil {
		os.Remove(temporary)
		return fmt.Errorf("auth: %w", err)
	}
	return nil
}
