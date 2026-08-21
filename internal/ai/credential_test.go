package ai_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/ai"
)

// fakeEnv answers lookups from a table, and records the order they arrived in.
type fakeEnv struct {
	values map[string]string
	errs   map[string]error
	asked  []string
}

func (f *fakeEnv) Lookup(ctx context.Context, name string) (string, error) {
	f.asked = append(f.asked, name)
	if err := f.errs[name]; err != nil {
		return f.values[name], err
	}
	return f.values[name], nil
}

// TestAStoredCredentialWinsAndABlankVariableIsSkipped: the order is what a user
// reasons about when a key does not take effect, and it is one rule rather than
// one per provider. A variable that is set but blank counts as unset: an empty
// key sent to a provider comes back as an authentication error, which reads as
// a bad credential rather than a missing one.
func TestAStoredCredentialWinsAndABlankVariableIsSkipped(t *testing.T) {
	env := &fakeEnv{values: map[string]string{"FIRST": "  ", "SECOND": "from-second"}}
	vars := []string{"FIRST", "SECOND"}

	stored, err := ai.ResolveCredential(context.Background(), "p", env, "from-store", vars)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Key() != "from-store" || stored.Source != "stored credential" {
		t.Fatalf("resolved %q from %q", stored.Key(), stored.Source)
	}
	if len(env.asked) != 0 {
		t.Fatalf("read the environment despite a stored key: %v", env.asked)
	}

	found, err := ai.ResolveCredential(context.Background(), "p", env, "", vars)
	if err != nil {
		t.Fatal(err)
	}
	if found.Key() != "from-second" || found.Source != "SECOND" {
		t.Fatalf("resolved %q from %q; a blank variable was not skipped", found.Key(), found.Source)
	}
}

// TestAnAbsentCredentialIsTyped: a caller that cannot tell "nothing configured"
// from "the provider rejected the key" tells its user the wrong thing to fix.
func TestAnAbsentCredentialIsTyped(t *testing.T) {
	env := &fakeEnv{values: map[string]string{"ONLY": ""}}
	_, err := ai.ResolveCredential(context.Background(), "p", env, "", []string{"ONLY"})
	if !errors.Is(err, ai.ErrNoCredential) {
		t.Fatalf("an absent credential produced %v", err)
	}
	if !strings.Contains(err.Error(), "ONLY") {
		t.Fatalf("the failure did not name what to set: %v", err)
	}
}

// TestResolutionStopsWhenTheCallerDoes: a lookup may run a command, and a list
// of them should not outlive the caller that asked.
func TestResolutionStopsWhenTheCallerDoes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	env := &fakeEnv{values: map[string]string{"A": "value"}}
	if _, err := ai.ResolveCredential(ctx, "p", env, "", []string{"A", "B"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("resolution continued after cancellation: %v", err)
	}
	if len(env.asked) != 0 {
		t.Fatalf("looked up %v after cancellation", env.asked)
	}
}

// TestALookupErrorDoesNotCarryTheValueOut: a source that reports what it found
// alongside why it could not use it would hand the key to whatever logs the
// failure. The value is removed by identity, which is the only removal that is
// certain — the shape-based pass alone misses a key that does not look like one.
func TestALookupErrorDoesNotCarryTheValueOut(t *testing.T) {
	const secret = "an-arbitrary-shaped-value-6f3a"
	env := &fakeEnv{
		values: map[string]string{"LEAKY": secret},
		errs:   map[string]error{"LEAKY": fmt.Errorf("vault refused while holding %s", secret)},
	}
	_, err := ai.ResolveCredential(context.Background(), "p", env, "", []string{"LEAKY"})
	if err == nil {
		t.Fatal("expected a failure")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("the value reached the error: %v", err)
	}
}

// TestACredentialDoesNotPrintItsKey: %v and %+v are what a log line and a test
// failure reach for, and a key that survives either is a key in a log.
func TestACredentialDoesNotPrintItsKey(t *testing.T) {
	const secret = "sk-should-not-appear"
	c := ai.StoredCredential(secret, "a test")
	for _, rendered := range []string{
		fmt.Sprintf("%v", c), fmt.Sprintf("%+v", c), fmt.Sprintf("%#v", c),
		fmt.Sprintf("%v", struct{ C ai.Credential }{c}),
	} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("formatting printed the key: %s", rendered)
		}
	}
	if c.Key() != secret {
		t.Fatalf("the key did not survive to a deliberate read")
	}
}

// TestScrubbingRemovesBothWhatIsKnownAndWhatIsShaped: neither half is enough on
// its own. A key that does not match the known shapes survives the shape pass,
// and the exact-value pass has nothing to match when the value was never seen.
func TestScrubbingRemovesBothWhatIsKnownAndWhatIsShaped(t *testing.T) {
	const odd = "9f2c-not-shaped-like-a-key"
	text := "sent Authorization: Bearer sk-abcdef1234 and also " + odd
	scrubbed := ai.ScrubSecret(text, odd)
	if strings.Contains(scrubbed, odd) || strings.Contains(scrubbed, "sk-abcdef1234") {
		t.Fatalf("scrubbing left a secret: %s", scrubbed)
	}
}

// TestAFailuresSpendCannotBeRewritten: the failure is the record of what was
// spent. Handing out the slice — and the optional counts it points at — lets a
// reader that adjusts what it got change what every later reader sees, and a
// spend that can be rewritten after the fact records nothing.
func TestAFailuresSpendCannotBeRewritten(t *testing.T) {
	cached := 5
	original := []ai.Usage{{InputTokens: 10, OutputTokens: 2, CacheReadTokens: &cached, Reported: true}}

	// One record, read more than once. Rebuilding it between reads would let
	// either half of the ownership — copying in, copying out — stand in for the
	// other, and neither alone is enough.
	for name, record := range map[string]func([]ai.Usage) interface{ Consumed() []ai.Usage }{
		"a classified failure": func(used []ai.Usage) interface{ Consumed() []ai.Usage } {
			// Recorded rather than assigned, and NOT copied here first: the
			// copying is the thing under test, and a test that does it for the
			// code proves only that the test does it.
			failed := &ai.ProviderError{Provider: "p", Failure: ai.FailureQuota}
			failed.Record(used...)
			return failed
		},
		"usage attached to a cause": func(used []ai.Usage) interface{ Consumed() []ai.Usage } {
			var carrier interface{ Consumed() []ai.Usage }
			if !errors.As(ai.WithUsage(errors.New("refused"), used...), &carrier) {
				t.Fatal("the attached usage is unreachable")
			}
			return carrier
		},
	} {
		t.Run(name, func(t *testing.T) {
			held := record(original)

			// What the caller still holds is not the record. A record that can
			// be edited from outside after the fact records nothing.
			original[0].InputTokens = 1
			*cachedRef(t, original) = 1

			first := held.Consumed()
			first[0].InputTokens = 99999
			if first[0].CacheReadTokens != nil {
				*first[0].CacheReadTokens = 99999
			}

			second := held.Consumed()
			if second[0].InputTokens != 10 {
				t.Fatalf("the recorded spend became %d", second[0].InputTokens)
			}
			if second[0].CacheReadTokens == nil || *second[0].CacheReadTokens != 5 {
				t.Fatalf("the recorded cache read became %v", second[0].CacheReadTokens)
			}

			original[0].InputTokens, *cachedRef(t, original) = 10, 5
		})
	}
}

// cachedRef reaches the optional count a usage points at, so a test can change
// it the way a careless caller would.
func cachedRef(t *testing.T, used []ai.Usage) *int {
	t.Helper()
	if used[0].CacheReadTokens == nil {
		t.Fatal("this fixture needs an optional count to point at")
	}
	return used[0].CacheReadTokens
}

// TestTheProvidersInstructionOutranksTheStatusButNotAnExhaustedBalance: the
// classification is an inference drawn from a status code, and the provider
// knows its own state. An exhausted balance is the exception — every attempt
// against it spends more of what is already gone, whoever asked.
func TestTheProvidersInstructionOutranksTheStatusButNotAnExhaustedBalance(t *testing.T) {
	yes, no := true, false
	for _, tc := range []struct {
		name    string
		failure ai.Failure
		advice  *bool
		want    bool
	}{
		{"a transient failure with no instruction", ai.FailureTransient, nil, true},
		{"a transient failure the provider says not to repeat", ai.FailureTransient, &no, false},
		{"a refusal the provider asks to repeat", ai.FailureRefused, &yes, true},
		{"an exhausted balance the provider asks to repeat", ai.FailureQuota, &yes, false},
		{"an unrecognised failure", ai.FailureUnknown, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := &ai.ProviderError{Provider: "p", Failure: tc.failure}
			err.Advise(tc.advice)
			if got := ai.Retryable(err); got != tc.want {
				t.Fatalf("Retryable %v, want %v", got, tc.want)
			}
		})
	}
	if ai.Retryable(errors.New("prose")) {
		t.Fatal("an unclassified error was judged worth repeating")
	}
}

// TestALookupCancellationStaysCancellation: wrapped in a message about reading
// a variable, a cancellation still prints but errors.Is can no longer see it,
// and a caller cannot tell its own stop from a broken credential source.
func TestALookupCancellationStaysCancellation(t *testing.T) {
	for name, cause := range map[string]error{
		"a cancelled caller":  context.Canceled,
		"an expired deadline": context.DeadlineExceeded,
	} {
		t.Run(name, func(t *testing.T) {
			env := &fakeEnv{
				values: map[string]string{"SLOW": ""},
				errs:   map[string]error{"SLOW": fmt.Errorf("looking up: %w", cause)},
			}
			_, err := ai.ResolveCredential(context.Background(), "p", env, "", []string{"SLOW"})
			if !errors.Is(err, cause) {
				t.Fatalf("the caller's own outcome arrived as %v", err)
			}
		})
	}
}

// TestAFailureOwnsWhatTheProviderSaidAboutRetrying: two failures must not share
// the instruction. One of them changing it would rewrite what the other reports
// the provider said, and a caller reads that to decide whether to spend again.
func TestAFailureOwnsWhatTheProviderSaidAboutRetrying(t *testing.T) {
	no := false
	original := &ai.ProviderError{Provider: "p", Failure: ai.FailureTransient}
	original.Advise(&no)

	// The caller still holds the value it advised with.
	no = true
	if ai.Retryable(original) {
		t.Fatal("changing the caller's own value changed what the failure reports")
	}

	copied := original.Clone()
	yes := true
	copied.Advise(&yes)
	if ai.Retryable(original) {
		t.Fatal("advising a copy changed the original")
	}
	if !ai.Retryable(copied) {
		t.Fatal("the copy did not take the new instruction")
	}

	// A copy carries the spend too, and owns that as well.
	cached := 3
	original.Record(ai.Usage{InputTokens: 7, CacheReadTokens: &cached, Reported: true})
	carried := original.Clone().Consumed()
	if len(carried) != 1 || carried[0].InputTokens != 7 {
		t.Fatalf("a copy lost the recorded spend: %v", carried)
	}
	carried[0].InputTokens = 99
	if original.Consumed()[0].InputTokens != 7 {
		t.Fatal("editing a copy's spend changed the original")
	}
}

// TestACopyAndItsOriginalDoNotRecordOverEachOther.
//
// Sharing one slice between two failures is invisible until the shared array
// has room to spare: then the second writer lands on the entry the first just
// wrote, and one call's spend silently becomes another's. Three entries before
// the copy is what leaves that room.
func TestACopyAndItsOriginalDoNotRecordOverEachOther(t *testing.T) {
	original := &ai.ProviderError{Provider: "p", Failure: ai.FailureTransient}
	for _, n := range []int{1, 2, 3} {
		original.Record(ai.Usage{InputTokens: n, Reported: true})
	}

	copied := original.Clone()
	copied.Record(ai.Usage{InputTokens: 100, Reported: true})
	original.Record(ai.Usage{InputTokens: 200, Reported: true})

	if got := copied.Consumed(); got[len(got)-1].InputTokens != 100 {
		t.Fatalf("the original recorded over the copy: %d", got[len(got)-1].InputTokens)
	}
	if got := original.Consumed(); got[len(got)-1].InputTokens != 200 {
		t.Fatalf("the copy recorded over the original: %d", got[len(got)-1].InputTokens)
	}
}

// TestNoEnvironmentIsATypedAbsenceRatherThanAPanic: a caller that configured no
// environment and stored nothing has configured no credential. Reaching for the
// lookup anyway ends the process on the one path where a clear message matters
// most, since what the user has to fix is a setting.
func TestNoEnvironmentIsATypedAbsenceRatherThanAPanic(t *testing.T) {
	_, err := ai.ResolveCredential(context.Background(), "p", nil, "", []string{"ANY"})
	if !errors.Is(err, ai.ErrNoCredential) {
		t.Fatalf("no environment produced %v", err)
	}

	// A stored value still resolves: the environment is only needed when there
	// is nothing to resolve without it.
	found, err := ai.ResolveCredential(context.Background(), "p", nil, "from-store", []string{"ANY"})
	if err != nil || found.Key() != "from-store" {
		t.Fatalf("a stored key needed an environment: %v, %v", found, err)
	}
}

// TestAFailureReadsAsWhatItIs: a failure is read by a person deciding what to
// change. One with no response behind it must not print a status it never had,
// and one with a status must show it — a report that says "status 0" sends that
// person looking for a response that does not exist.
func TestAFailureReadsAsWhatItIs(t *testing.T) {
	withResponse := (&ai.ProviderError{
		Provider: "p", Failure: ai.FailureThrottled, Status: 429, Detail: "slow down",
	}).Error()
	if !strings.Contains(withResponse, "429") || !strings.Contains(withResponse, "slow down") {
		t.Fatalf("a refused request did not report its status: %s", withResponse)
	}

	withNone := (&ai.ProviderError{
		Provider: "p", Failure: ai.FailureAuth, Detail: "no credential",
	}).Error()
	if strings.Contains(withNone, "status") || strings.Contains(withNone, "0") {
		t.Fatalf("a failure with no response invented one: %s", withNone)
	}
	if !strings.Contains(withNone, "no credential") {
		t.Fatalf("the reason was lost: %s", withNone)
	}
}

// TestCopyingNothingIsNothing: Clone is reached through an error value that may
// be absent, and a copy that panics on absence turns a report into a crash at
// the moment something has already gone wrong.
func TestCopyingNothingIsNothing(t *testing.T) {
	var absent *ai.ProviderError
	if absent.Clone() != nil {
		t.Fatal("copying nothing produced something")
	}
}
