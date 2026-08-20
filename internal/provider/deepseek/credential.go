// Package deepseek reaches a real model provider.
//
// It is the first thing in this repository to touch a network, a credential and
// a bill, and it is written so that each of those is visible rather than
// implicit: the transport is injected, the environment is injected, and every
// failure leaves as a typed value rather than as text a caller must interpret.
package deepseek

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Environment is where a credential is read from.
//
// Injected rather than read directly so that a test cannot reach the real
// environment by forgetting to. There is no fallback to os.Getenv: omitting this
// fails, which is the point.
type Environment interface {
	Lookup(ctx context.Context, name string) (string, error)
}

// ErrNoCredential reports that no credential was found.
//
// Pi returns undefined here; this returns a typed failure so the reason survives
// to whatever reports it. A caller that cannot tell "no key configured" from
// "the provider rejected the key" will tell its user the wrong thing to fix.
var ErrNoCredential = errors.New("deepseek: no credential")

// EnvVars is the ordered list of variables a credential may come from. Pi
// declares exactly one for this provider, and so does this.
var EnvVars = []string{"DEEPSEEK_API_KEY"}

// Credential is a resolved key and where it came from.
type Credential struct {
	key string

	// Source names the variable that supplied the key. It is the part that may
	// be logged: it identifies the origin without disclosing the secret.
	Source string
}

// String and GoString keep the key out of anything that formats this value,
// including the %v and %+v that a log line or a test failure reaches for.
func (c Credential) String() string   { return "deepseek.Credential{Source:" + c.Source + "}" }
func (c Credential) GoString() string { return c.String() }

// Key is the resolved secret. Named as a method rather than an exported field so
// that reaching the secret is always a deliberate act at a call site.
func (c Credential) Key() string { return c.key }

// Resolve finds the credential.
//
// Order: a stored key wins, otherwise the first environment variable that is
// set. A variable that is present but blank counts as unset and resolution
// continues, because an empty key sent to a provider fails as an authentication
// error and reads as a bad credential rather than a missing one.
func Resolve(ctx context.Context, env Environment, stored string) (Credential, error) {
	if strings.TrimSpace(stored) != "" {
		return Credential{key: stored, Source: "stored credential"}, nil
	}
	for _, name := range EnvVars {
		if err := ctx.Err(); err != nil {
			return Credential{}, err
		}
		value, err := env.Lookup(ctx, name)
		if err != nil {
			return Credential{}, fmt.Errorf("deepseek: reading %s: %w", name, err)
		}
		if strings.TrimSpace(value) == "" {
			continue
		}
		return Credential{key: value, Source: name}, nil
	}
	return Credential{}, fmt.Errorf("%w: none of %s is set", ErrNoCredential, strings.Join(EnvVars, ", "))
}
