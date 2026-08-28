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
	"github.com/iamclancyliang/pi-go/internal/provider/deepseek"
	"github.com/iamclancyliang/pi-go/internal/provider/openai"
	"github.com/iamclancyliang/pi-go/internal/provider/qwen"
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
			return deepseek.New(deepseek.Config{
				Model:           model,
				Transport:       &http.Client{Transport: transport, Timeout: requestTimeout},
				Environment:     processEnvironment{},
				Store:           store,
				ProviderID:      "deepseek",
				MaxOutputTokens: DefaultMaxOutputTokens,
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

// SelectProvider decides which provider to use.
//
// An explicit --provider wins. Otherwise the first one whose credential is
// present in the environment is chosen, in the order ProviderNames gives, so
// the choice is the same on two machines configured the same way. Guessing
// differently each run would make a bill depend on map iteration order.
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
		for _, v := range p.EnvVars {
			if strings.TrimSpace(os.Getenv(v)) != "" {
				return p, nil
			}
		}
	}
	return Provider{}, fmt.Errorf("no provider credential found; set one of %s, or pass --provider",
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
	port, err := p.build(model, args.APIKey, transport)
	if err != nil {
		return nil, "", "", fmt.Errorf("%s: %w", p.Name, err)
	}
	return port, p.Name, model, nil
}
