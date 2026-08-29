package cli

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/auth"
	"github.com/iamclancyliang/pi-go/internal/provider/deepseek"
	"github.com/iamclancyliang/pi-go/internal/provider/openai"
	"github.com/iamclancyliang/pi-go/internal/provider/openrouter"
	"github.com/iamclancyliang/pi-go/internal/provider/qwen"
	"github.com/iamclancyliang/pi-go/internal/session"
)

// DefaultMaxOutputTokens caps a reply when nothing else does.
//
// A cap exists at all because an uncapped request is an unbounded bill, and
// this one is generous enough that ordinary answers are never cut.
const DefaultMaxOutputTokens = 8192

// processEnvironment is the injected environment seam, backed by this process.
//
// The seam exists so a credential source can be substituted in a test; this is
// the one real implementation, and it lives at the composition layer rather
// than inside a provider so no provider can read the environment on its own.
type processEnvironment struct{}

func (processEnvironment) Lookup(_ context.Context, name string) (string, error) {
	return os.Getenv(name), nil
}

// Provider describes one model provider this build can reach.
type Provider struct {
	Name string

	// DefaultModel is used when --model is absent. Named per provider because
	// no catalogue exists here to consult, and a request must name a model.
	DefaultModel string

	// EnvVars are the variables a credential may come from, in order.
	EnvVars []string

	build func(model, apiKey string, transport http.RoundTripper) (ai.Port, error)
}

// Providers are the providers this build can reach, by name.
//
// Pi's registry holds forty-two. These three are what pi-go has ports for, and
// naming any other one fails with the list rather than falling back to a
// default — a request silently served by a provider the user did not ask for is
// billed to an account they did not choose.
var Providers = map[string]Provider{
	"deepseek": {
		Name:         "deepseek",
		DefaultModel: "deepseek-chat",
		EnvVars:      deepseek.EnvVars,
		build: func(model, apiKey string, transport http.RoundTripper) (ai.Port, error) {
			var store deepseek.Store
			if apiKey != "" {
				memory := deepseek.NewMemoryStore()
				if _, err := memory.Modify(context.Background(), "deepseek",
					func(deepseek.Stored, bool) (deepseek.Stored, bool, error) {
						return deepseek.NewAPIKey(apiKey), true, nil
					}); err != nil {
					return nil, err
				}
				store = memory
			}
			facts, _ := ai.Facts("deepseek", model)
			return deepseek.New(deepseek.Config{
				Model:       model,
				Transport:   &http.Client{Transport: transport, Timeout: requestTimeout},
				Environment: processEnvironment{},
				Store:       store,
				ProviderID:  "deepseek",
				// Recorded facts where there are any, the caller's defaults
				// where there are none. A zero window leaves the count-based
				// overflow checks off, which is where they already were.
				MaxOutputTokens: outputCap(facts),
				ContextWindow:   facts.ContextWindow,
			})
		},
	},
	"openai": {
		Name:         "openai",
		DefaultModel: "gpt-5",
		EnvVars:      openai.EnvVars,
		build: func(model, apiKey string, transport http.RoundTripper) (ai.Port, error) {
			cred, err := openai.Resolve(context.Background(), processEnvironment{}, apiKey)
			if err != nil {
				return nil, err
			}
			return openai.New(openai.Config{
				Model:           model,
				Transport:       transport,
				Credential:      cred,
				MaxOutputTokens: DefaultMaxOutputTokens,
			})
		},
	},
	"openrouter": {
		Name: "openrouter",
		// An aggregator addresses models as vendor/model, so a default names
		// one — there is no house model to fall back to.
		DefaultModel: "openai/gpt-4o-mini",
		EnvVars:      openrouter.EnvVars,
		build: func(model, apiKey string, transport http.RoundTripper) (ai.Port, error) {
			cred, err := openrouter.Resolve(context.Background(), processEnvironment{}, apiKey)
			if err != nil {
				return nil, err
			}
			facts, _ := ai.Facts("openrouter", model)
			return openrouter.New(openrouter.Config{
				Model:           model,
				Transport:       transport,
				Credential:      cred,
				MaxOutputTokens: outputCap(facts),
				ContextWindow:   facts.ContextWindow,
				// Attribution is opt-in and this build does not opt in: it puts
				// the caller on a public leaderboard, which is not a thing to
				// decide for someone.
			})
		},
	},
	"qwen": {
		Name:         "qwen",
		DefaultModel: "qwen-max",
		EnvVars:      qwen.EnvVars,
		build: func(model, apiKey string, transport http.RoundTripper) (ai.Port, error) {
			cred, err := qwen.Resolve(context.Background(), processEnvironment{}, apiKey)
			if err != nil {
				return nil, err
			}
			return qwen.New(qwen.Config{
				Model:           model,
				Transport:       transport,
				Credential:      cred,
				MaxOutputTokens: DefaultMaxOutputTokens,
			})
		},
	},
}

const requestTimeout = 10 * time.Minute

// ProviderNames lists what can be asked for, ordered so a message reads the
// same way twice.
func ProviderNames() []string {
	out := make([]string, 0, len(Providers))
	for name := range Providers {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// AuthStore is where stored credentials live for a run.
//
// Resolved from the same directory sessions use, so a --session-dir aimed
// somewhere else keeps its credentials with it.
func AuthStore(args Args) (*auth.Store, error) {
	dir := args.SessionDir
	if dir == "" {
		resolved, err := session.AgentDir()
		if err != nil {
			return nil, err
		}
		dir = resolved
	}
	return auth.Open(dir), nil
}

// storedKey is the credential kept for a provider, if any.
//
// A failure to read the file is not fatal here: the environment may still carry
// a usable key, and refusing to start because a stored credential is unreadable
// would lock a user out over a file they can simply delete.
func storedKey(args Args, provider string) string {
	store, err := AuthStore(args)
	if err != nil {
		return ""
	}
	credential, found, err := store.Get(provider)
	if err != nil || !found {
		return ""
	}
	return credential.Key()
}

// SelectProvider decides which provider to use.
//
// An explicit --provider wins. Otherwise the first one with a credential is
// chosen, in the order ProviderNames gives, so the choice is the same on two
// machines configured the same way. Guessing differently each run would make a
// bill depend on map iteration order.
func SelectProvider(args Args) (Provider, error) {
	if args.Provider != "" {
		p, known := Providers[args.Provider]
		if !known {
			return Provider{}, fmt.Errorf("unknown provider %q; this build has %s",
				args.Provider, strings.Join(ProviderNames(), ", "))
		}
		return p, nil
	}
	for _, name := range ProviderNames() {
		p := Providers[name]
		// A stored credential counts as much as one in the environment: a user
		// who ran /login expects the next run to use it without also exporting
		// the variable.
		if storedKey(args, name) != "" {
			return p, nil
		}
		for _, v := range p.EnvVars {
			if strings.TrimSpace(os.Getenv(v)) != "" {
				return p, nil
			}
		}
	}
	return Provider{}, fmt.Errorf(
		"no provider credential found; run /login, set one of %s, or pass --provider",
		strings.Join(credentialVars(), ", "))
}

func credentialVars() []string {
	var out []string
	for _, name := range ProviderNames() {
		out = append(out, Providers[name].EnvVars...)
	}
	return out
}

// Open builds the model port for the selected provider.
//
// The transport is a parameter rather than a default so a test can count what
// leaves the machine — the one-request-per-call bound is a property of the
// wire, and asserting it anywhere else asserts a configuration value instead.
func Open(args Args, transport http.RoundTripper) (ai.Port, string, string, error) {
	p, err := SelectProvider(args)
	if err != nil {
		return nil, "", "", err
	}
	model := args.Model
	if model == "" {
		model = p.DefaultModel
	}
	// Order: the flag, then what /login stored, then the environment — which is
	// the provider's own last resort. Most explicit first, so a key given for
	// one run is not silently overridden by one saved earlier.
	key := args.APIKey
	if key == "" {
		key = storedKey(args, p.Name)
	}
	port, err := p.build(model, key, transport)
	if err != nil {
		return nil, "", "", fmt.Errorf("%s: %w", p.Name, err)
	}
	return port, p.Name, model, nil
}

// outputCap is the model's own cap when one is recorded, and this build's
// default otherwise.
func outputCap(facts ai.ModelFacts) int {
	if facts.MaxOutputTokens > 0 {
		return facts.MaxOutputTokens
	}
	return DefaultMaxOutputTokens
}

// ThinkingFor decides what to ask a model for, given what is recorded about it.
//
// A level asked for reaches a model recorded as reasoning, or one nothing is
// recorded about — the second because refusing to try would make every
// unrecorded model unable to reason, and an unrecorded model is the normal case
// for a catalogue this small. A model recorded as NOT reasoning gets nothing,
// which is the case the record exists to catch.
//
// Reported rather than silently dropped: a caller who asked for reasoning and
// got none should be told why, not left reading an ordinary answer as a
// considered one.
func ThinkingFor(provider, model string, asked ai.ThinkingLevel) (ai.ThinkingLevel, string) {
	if asked == "" {
		return "", ""
	}
	facts, recorded := ai.Facts(provider, model)
	if recorded && facts.ReasoningKnown && !facts.Reasoning {
		return "", fmt.Sprintf("%s/%s is recorded as not reasoning; --thinking %s was not sent",
			provider, model, asked)
	}
	return asked, ""
}
