package deepseek

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
)

// CredentialType tags what a stored credential is.
//
// The tag is part of the stored value, so a reader never has to infer which
// kind it holds from the shape of what it finds.
type CredentialType string

const (
	// TypeAPIKey is a long-lived key. OAuth is not implemented here; the type
	// exists so that adding it later does not have to reinterpret what is
	// already stored.
	TypeAPIKey CredentialType = "api_key"
)

// Stored is one credential, tagged with its kind.
//
// The secret is unexported: a struct with an exported secret is one %+v away
// from a log line, and every holder of it would otherwise have to remember.
type Stored struct {
	Type CredentialType
	key  string
}

// NewAPIKey builds a stored API key.
func NewAPIKey(key string) Stored { return Stored{Type: TypeAPIKey, key: key} }

// Key is the secret. A method rather than a field, so reaching it is always a
// deliberate act at a call site.
func (s Stored) Key() string { return s.key }

// String and GoString keep the secret out of anything that formats this value.
func (s Stored) String() string   { return "deepseek.Stored{Type:" + string(s.Type) + "}" }
func (s Stored) GoString() string { return s.String() }

// Info is non-secret credential metadata.
type Info struct {
	ProviderID string
	Type       CredentialType
}

// ErrNoStoredCredential reports that a provider has no credential stored.
var ErrNoStoredCredential = errors.New("deepseek: no stored credential")

// Store holds credentials, keyed by provider id, one per provider.
//
// Modify is the only path that writes a value, and Delete is serialized against
// it. That pairing is not incidental: a logout racing a refresh, unserialized,
// lets the refresh write the credential back after the delete removed it, and
// the user stays logged in believing otherwise.
type Store interface {
	// Read returns the stored credential for display and status. It is NOT the
	// request authentication path: what a request uses is resolved separately,
	// because a value read here may be stale and reading it skips the refresh
	// that resolution would perform.
	Read(ctx context.Context, providerID string) (Stored, error)

	// Modify is the only write path: a serialized read-modify-write per
	// provider. fn sees the current value because correct writes depend on it.
	// Returning ok=false leaves the entry unchanged.
	Modify(ctx context.Context, providerID string,
		fn func(current Stored, exists bool) (next Stored, ok bool, err error)) (Stored, error)

	// Delete removes a credential, serialized against Modify.
	Delete(ctx context.Context, providerID string) error

	// List returns metadata only, and performs no work beyond reading what is
	// already held: enumerating which providers are configured must not run
	// whatever might produce a credential.
	List(ctx context.Context) ([]Info, error)
}

// MemoryStore is the default store: it holds credentials for the life of the
// process and writes nothing to disk.
//
// Nothing is persisted, so there is no file to leak, no permissions to get
// wrong, and no file to commit by accident. Persistence is what OAuth would
// need; this is the shape to implement against when that arrives.
type MemoryStore struct {
	mu    sync.Mutex
	creds map[string]Stored
	locks map[string]*sync.Mutex
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{creds: map[string]Stored{}, locks: map[string]*sync.Mutex{}}
}

// providerLock returns the per-provider lock, creating it once.
//
// Per provider rather than global: two providers have no reason to serialize
// against each other, and a single lock would make a slow write on one block
// every read on the others.
func (m *MemoryStore) providerLock(providerID string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	lock, ok := m.locks[providerID]
	if !ok {
		lock = &sync.Mutex{}
		m.locks[providerID] = lock
	}
	return lock
}

func (m *MemoryStore) Read(ctx context.Context, providerID string) (Stored, error) {
	if err := ctx.Err(); err != nil {
		return Stored{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cred, ok := m.creds[providerID]
	if !ok {
		return Stored{}, fmt.Errorf("%w for %s", ErrNoStoredCredential, providerID)
	}
	return cred, nil
}

func (m *MemoryStore) Modify(ctx context.Context, providerID string,
	fn func(current Stored, exists bool) (Stored, bool, error)) (Stored, error) {
	if err := ctx.Err(); err != nil {
		return Stored{}, err
	}
	lock := m.providerLock(providerID)
	lock.Lock()
	defer lock.Unlock()

	m.mu.Lock()
	current, exists := m.creds[providerID]
	m.mu.Unlock()

	next, ok, err := fn(current, exists)
	if err != nil {
		return Stored{}, err
	}
	if !ok {
		return current, nil
	}
	m.mu.Lock()
	m.creds[providerID] = next
	m.mu.Unlock()
	return next, nil
}

func (m *MemoryStore) Delete(ctx context.Context, providerID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// The same per-provider lock as Modify: a delete that ran concurrently with
	// a write could be undone by it.
	lock := m.providerLock(providerID)
	lock.Lock()
	defer lock.Unlock()

	m.mu.Lock()
	delete(m.creds, providerID)
	m.mu.Unlock()
	return nil
}

func (m *MemoryStore) List(ctx context.Context) ([]Info, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Info, 0, len(m.creds))
	for id, cred := range m.creds {
		out = append(out, Info{ProviderID: id, Type: cred.Type})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ProviderID < out[j].ProviderID })
	return out, nil
}
