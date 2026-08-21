package ai

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Environment is where a credential is read from.
//
// Injected rather than read directly so that a test cannot reach the real
// environment by forgetting to. There is no fallback to the process
// environment: omitting this fails, which is the point.
type Environment interface {
	Lookup(ctx context.Context, name string) (string, error)
}

// ErrNoCredential reports that no credential was found.
//
// Typed rather than an absent value so the reason survives to whatever reports
// it. A caller that cannot tell "no key configured" from "the provider rejected
// the key" will tell its user the wrong thing to fix.
var ErrNoCredential = errors.New("ai: no credential")

// Credential is a resolved key and where it came from.
//
// Provider-independent because the rule that produces it is: a stored value
// wins, otherwise the first variable that is set. A provider that resolved its
// own would be free to disagree with that order, and the order is the part a
// user reasons about when a key does not take effect.
type Credential struct {
	key string

	// Source names what supplied the key. It is the part that may be logged: it
	// identifies the origin without disclosing the secret.
	Source string
}

// String and GoString keep the key out of anything that formats this value,
// including the %v and %+v that a log line or a test failure reaches for.
func (c Credential) String() string   { return "ai.Credential{Source:" + c.Source + "}" }
func (c Credential) GoString() string { return c.String() }

// Key is the resolved secret. Named as a method rather than an exported field
// so that reaching the secret is always a deliberate act at a call site.
func (c Credential) Key() string { return c.key }

// StoredCredential wraps a value that came from somewhere other than the
// environment, so a caller can hand a port a credential it already holds.
func StoredCredential(key, source string) Credential {
	return Credential{key: key, Source: source}
}

// ResolveCredential finds the credential a provider authenticates with.
//
// Order: a stored key wins, otherwise the first variable in the declared list
// that is set. A variable that is present but blank counts as unset and
// resolution continues, because an empty key sent to a provider fails as an
// authentication error and reads as a bad credential rather than a missing one.
//
// Cancellation is checked between lookups rather than only at the start: a
// lookup may run a command, and a list of them should not outlive the caller.
func ResolveCredential(ctx context.Context, provider string, env Environment,
	stored string, vars []string) (Credential, error) {

	if strings.TrimSpace(stored) != "" {
		return Credential{key: stored, Source: "stored credential"}, nil
	}
	if env == nil {
		return Credential{}, fmt.Errorf("%w: %s has no environment to read from", ErrNoCredential, provider)
	}
	for _, name := range vars {
		if err := ctx.Err(); err != nil {
			return Credential{}, err
		}
		value, err := env.Lookup(ctx, name)
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			// The caller's own outcome, returned unchanged. Wrapped in a
			// message about reading a variable it would still print, but
			// errors.Is could no longer see it — and a caller that cannot tell
			// its own cancellation from a broken credential source will report
			// the wrong thing and may retry what it just stopped.
			return Credential{}, err
		}
		if err != nil {
			// The value is scrubbed out of the error even though the lookup
			// failed: a source that reports what it found alongside why it
			// could not use it would hand the key to whatever logs this.
			return Credential{}, fmt.Errorf("%s: reading %s: %s", provider, name,
				ScrubSecret(err.Error(), value))
		}
		if strings.TrimSpace(value) == "" {
			continue
		}
		return Credential{key: value, Source: name}, nil
	}
	return Credential{}, fmt.Errorf("%w: none of %s is set for %s",
		ErrNoCredential, strings.Join(vars, ", "), provider)
}

// ScrubSecret removes a secret from text that is about to become an error.
//
// Both halves matter. The exact value is removed when it is known, which is the
// only removal that is certain; the shape-based pass then catches a key that
// arrived from somewhere this call cannot see. Neither alone is enough: a
// credential that does not match the shapes below survives the second, and the
// first has nothing to match when the value was never returned.
func ScrubSecret(text, secret string) string {
	if strings.TrimSpace(secret) != "" {
		text = strings.ReplaceAll(text, secret, "<redacted>")
	}
	return credentialShape.ReplaceAllString(text, "<redacted>")
}

// credentialShape matches common key formats and bearer headers, so a value
// this code never saw still does not survive into a report.
var credentialShape = regexp.MustCompile(`(?i)(sk-[A-Za-z0-9_-]{4,}|bearer\s+\S+)`)
