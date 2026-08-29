package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/provider/deepseek"
)

// The overflow probe: ONE deliberately oversized request to a real provider, to
// learn how it refuses.
//
// It exists because no checked DeepSeek primary source says how an oversized
// request is rejected — the official error table defines 400 and 422 only as
// generic invalid format and invalid parameters. Without that shape, the
// adapter cannot raise a typed overflow for the up-front rejection path, and
// compact-before-retry never runs for it. Inventing a matcher from an assumed
// message would test the matcher, not the provider.
//
// Authorized by the repository owner on 2026-08-29, under the boundary recorded
// in docs/research/provider-contract-source-audit.md and enforced here:
//
//   - ONE request. Retry is zero and a counting transport asserts the count,
//     because a probe that quietly retried would bill the oversized body twice.
//   - The oversized input is generated locally from a repeated filler string.
//     Nothing real is uploaded — not the repository, not a conversation.
//   - The credential enters through the injected environment seam and has no
//     path to the recorded fixture: the response is scrubbed before it is
//     written, and the request is never recorded at all.
//   - The fixture is committed so this never needs running again. That is the
//     point of a probe: it converts a question about a live service into a file.
//   - A failure is NOT rerun. The gate is a separate variable from the ordinary
//     live one, so a green test run cannot repeat this by accident.
const (
	probeGate = "PI_GO_LIVE_DEEPSEEK_OVERFLOW_PROBE"
	// Named for the question, not the hoped-for answer. The first run recorded
	// an ACCEPTED request, which is a real outcome and is stored under a name
	// that says so — a file called "context-overflow" holding a 200 is how a
	// later reader comes to believe something that never happened.
	probeFixtureDir = "testdata"

	// probeSizeVar sets how much input to send, in megabytes. Explicit rather
	// than a constant, so escalating the probe is an act at the command line
	// and shows up in the shell history of whoever authorized it — not an edit
	// that reads the same in a diff whatever number it carries.
	probeSizeVar     = "PI_GO_PROBE_MEGABYTES"
	probeDefaultSize = 1
)

// recordedProbe is what the probe learns, and all it learns.
type recordedProbe struct {
	Recorded string `json:"recorded"`

	// Outcome is what actually happened, which is not necessarily a rejection:
	// a request large enough to be refused by one model is answered by
	// another, and recording an acceptance as though it were a refusal is the
	// error this field exists to prevent.
	Outcome string `json:"outcome"`

	ModelRequested string `json:"model_requested"`
	ModelServed    string `json:"model_served,omitempty"`

	// PromptChars is how large the request was, so a reader can tell this was
	// an overflow rather than some other refusal.
	PromptChars int `json:"prompt_chars"`

	Status int               `json:"status"`
	Header map[string]string `json:"header,omitempty"`

	// Body is the provider's response, scrubbed. It is the whole reason for
	// the probe: the shape a detector must recognise.
	Body string `json:"body"`

	// Usage is what the provider said the refused request cost, if it said.
	// A refused request that is billed is a fact the ledger needs.
	Usage string `json:"reported_usage"`
}

type probeTransport struct {
	inner  http.RoundTripper
	mu     sync.Mutex
	sent   int
	status int
	header http.Header
	body   []byte
}

func (p *probeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	p.mu.Lock()
	p.sent++
	p.mu.Unlock()

	resp, err := p.inner.RoundTrip(req)
	if err != nil || resp == nil {
		return resp, err
	}
	raw, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(raw))
	if readErr == nil {
		p.mu.Lock()
		p.status, p.header, p.body = resp.StatusCode, resp.Header.Clone(), raw
		p.mu.Unlock()
	}
	return resp, nil
}

func TestProbeDeepSeekContextOverflow(t *testing.T) {
	if os.Getenv(probeGate) == "" {
		t.Skipf("the overflow probe is off; it sends one deliberately oversized billed request. "+
			"Set %s=1 to run it, and only when the recorded fixture needs replacing.", probeGate)
	}
	// Stops when the question is ANSWERED, which a rejection is and an
	// acceptance is not. The first probe was accepted; that bounded the window
	// and left the question open, so it must not stand in the way of the
	// larger request that could close it. The gate variable still has to be set
	// either way, so this can never happen by accident.
	if answered, _ := filepath.Glob(filepath.Join(probeFixtureDir, "deepseek-*-rejected.json")); len(answered) > 0 {
		t.Skipf("%v already records a rejection; delete it to probe again", answered)
	}

	key := strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY"))
	if key == "" {
		t.Fatal("the probe needs DEEPSEEK_API_KEY")
	}

	transport := &probeTransport{inner: http.DefaultTransport}
	port, err := deepseek.New(deepseek.Config{
		Model:       "deepseek-chat",
		Transport:   &http.Client{Transport: transport, Timeout: 5 * time.Minute},
		Environment: processEnvironment{},
		// Retry stays at its zero value: one request, no repeat.
		MaxOutputTokens: 16,
	})
	if err != nil {
		t.Fatalf("configuring the provider: %v", err)
	}

	megabytes := probeDefaultSize
	if given := strings.TrimSpace(os.Getenv(probeSizeVar)); given != "" {
		parsed, err := strconv.Atoi(given)
		if err != nil || parsed < 1 {
			t.Fatalf("%s is %q; it takes a positive number of megabytes", probeSizeVar, given)
		}
		megabytes = parsed
	}

	// Generated here, from nothing: no file, conversation or environment value
	// goes into it.
	//
	// Random-looking rather than repeated, and that is the lesson of the first
	// probe. Repeated text tokenises efficiently — 1MB of one phrase came to
	// 182,365 tokens, about 5.75 characters each — so reaching a large context
	// window that way means uploading megabytes for tokens the provider merges
	// anyway. Unpredictable text costs the tokeniser far more per character, so
	// the same bytes buy several times the tokens, and the request that has to
	// travel stays smaller.
	oversized := randomFiller(megabytes << 20)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	_, callErr := port.Generate(ctx, ai.Request{
		Model:    "deepseek-chat",
		Messages: []ai.Message{{Role: ai.RoleUser, Content: oversized}},
	})

	if sent := transport.sent; sent != 1 {
		t.Fatalf("the probe sent %d requests; its whole boundary is one", sent)
	}
	if transport.status == 0 {
		t.Fatalf("no response was recorded; the call failed as %v", callErr)
	}

	outcome := "rejected"
	if transport.status == http.StatusOK {
		outcome = "accepted"
	}
	body := scrub(string(transport.body), key)
	recorded := recordedProbe{
		Recorded:       time.Now().UTC().Format(time.RFC3339),
		Outcome:        outcome,
		ModelRequested: "deepseek-chat",
		ModelServed:    servedModel(body),
		PromptChars:    len(oversized),
		Status:         transport.status,
		Header:         interestingHeaders(transport.header),
		Body:           body,
		Usage:          reportedUsage(transport.body),
	}
	fixture := filepath.Join(probeFixtureDir, "deepseek-large-request-"+outcome+".json")
	encoded, err := json.MarshalIndent(recorded, "", "  ")
	if err != nil {
		t.Fatalf("encoding the fixture: %v", err)
	}
	if strings.Contains(string(encoded), key) {
		t.Fatal("the credential survived into the fixture; nothing was written")
	}
	if err := os.MkdirAll(probeFixtureDir, 0o755); err != nil {
		t.Fatalf("preparing testdata: %v", err)
	}
	if err := os.WriteFile(fixture, append(encoded, '\n'), 0o644); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}

	t.Logf("sent %d MB as %d prompt tokens", megabytes, promptTokens(recorded.Usage))
	t.Logf("outcome: %s, status %d", recorded.Outcome, recorded.Status)
	t.Logf("body: %s", recorded.Body)
	t.Logf("reported usage: %s", recorded.Usage)
	t.Logf("recorded to %s; the probe will not run again while it exists", fixture)
}

// interestingHeaders keeps the few a detector might branch on, and drops the
// rest — a full header dump carries request ids and rate-limit state that say
// more about the account than about the rejection.
func interestingHeaders(h http.Header) map[string]string {
	out := map[string]string{}
	for _, name := range []string{"Content-Type"} {
		if v := h.Get(name); v != "" {
			out[name] = v
		}
	}
	return out
}

// reportedUsage extracts what the provider said the request cost.
//
// A refusal arrives as one JSON object; an ACCEPTED request arrives as a
// stream, and its usage is in the last event rather than at the top level. The
// first version of this only handled the first case and recorded nothing for
// the second — losing the token count that was the most useful number the probe
// produced.
func reportedUsage(body []byte) string {
	var whole struct {
		Usage json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(body, &whole); err == nil && len(whole.Usage) > 0 {
		return string(whole.Usage)
	}
	last := ""
	for _, line := range strings.Split(string(body), "\n") {
		payload, isData := strings.CutPrefix(strings.TrimSpace(line), "data: ")
		if !isData || payload == "[DONE]" {
			continue
		}
		var chunk struct {
			Usage json.RawMessage `json:"usage"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		if len(chunk.Usage) > 0 && string(chunk.Usage) != "null" {
			last = string(chunk.Usage)
		}
	}
	return last
}

// servedModel is which model actually answered, which need not be the one
// asked for — and a window is a property of the model that served.
func servedModel(body string) string {
	if m := regexp.MustCompile(`"model":"([^"]+)"`).FindStringSubmatch(body); m != nil {
		return m[1]
	}
	return ""
}

// scrub removes the credential by identity and anything shaped like one.
var keyShaped = regexp.MustCompile(`sk-[A-Za-z0-9_-]{8,}`)

func scrub(text, key string) string {
	if key != "" {
		text = strings.ReplaceAll(text, key, "[redacted]")
	}
	return keyShaped.ReplaceAllString(text, "[redacted]")
}

// randomFiller is n bytes of locally generated, unpredictable text.
//
// Seeded from the clock rather than a fixed value: a probe is not reproducing
// anything, and a fixed seed would only invite someone to believe two runs sent
// the same thing when the size can differ.
func randomFiller(n int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789 "
	source := rand.New(rand.NewSource(time.Now().UnixNano()))
	out := make([]byte, n)
	for i := range out {
		out[i] = alphabet[source.Intn(len(alphabet))]
	}
	return string(out)
}

// promptTokens reads the count back out of the recorded usage, for the log line
// that tells the operator what the request actually cost.
func promptTokens(usage string) int {
	var parsed struct {
		PromptTokens int `json:"prompt_tokens"`
	}
	if err := json.Unmarshal([]byte(usage), &parsed); err != nil {
		return 0
	}
	return parsed.PromptTokens
}
