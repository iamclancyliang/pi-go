// Package deepseek reaches a real model provider.
//
// It is the first thing in this repository to touch a network, a credential and
// a bill, and it is written so that each of those is visible rather than
// implicit: the transport is injected, the environment is injected, and every
// failure leaves as a typed value rather than as text a caller must interpret.
package deepseek

import (
	"context"

	"github.com/iamclancyliang/pi-go/internal/ai"
)

// The credential rule is shared, not this package's own: which source wins, how
// a blank variable is treated and how a resolved key is kept out of reports are
// answers a user expects to be the same whichever provider they configure.
// What stays here is the list below, because only this package knows what this
// provider's key is called.
type (
	Environment = ai.Environment
	Credential  = ai.Credential
)

// ErrNoCredential reports that no credential was found.
var ErrNoCredential = ai.ErrNoCredential

// EnvVars is the ordered list of variables a credential may come from. Pi
// declares exactly one for this provider, and so does this.
var EnvVars = []string{"DEEPSEEK_API_KEY"}

// Resolve finds the credential.
//
// Order: a stored key wins, otherwise the first environment variable that is
// set. A variable that is present but blank counts as unset and resolution
// continues, because an empty key sent to a provider fails as an authentication
// error and reads as a bad credential rather than a missing one.
func Resolve(ctx context.Context, env Environment, stored string) (Credential, error) {
	return ai.ResolveCredential(ctx, providerName, env, stored, EnvVars)
}
