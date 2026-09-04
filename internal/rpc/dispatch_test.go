package rpc

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/compaction"
	"github.com/iamclancyliang/pi-go/internal/session"
)

// fakeRun is a prompt in flight that ends when its context does, or when
// released. It records what was steered and followed into it.
type fakeRun struct {
	ctx      context.Context
	release  chan struct{}
	mu       sync.Mutex
	steered  []string
	followed []string
}

func (r *fakeRun) Steer(text string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.steered = append(r.steered, text)
	return nil
}

func (r *fakeRun) Follow(text string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.followed = append(r.followed, text)
	return nil
}

func (r *fakeRun) Wait() error {
	select {
	case <-r.ctx.Done():
		return r.ctx.Err()
	case <-r.release:
		return nil
	}
}

// fakeHost is a session in memory plus a record of every operation asked of
// it. Recorded is whether it pretends to have a durable record.
type fakeHost struct {
	sess     *session.Session
	provider string
	model    string
	recorded bool
	tree     []session.Node

	mu       sync.Mutex
	last     *fakeRun
	prompts  []string
	startErr error
	ops      []string
	tooShort bool
}

func (h *fakeHost) Session() *session.Session { return h.sess }
func (h *fakeHost) Provider() string          { return h.provider }
func (h *fakeHost) ModelName() string         { return h.model }

func (h *fakeHost) Start(ctx context.Context, prompt string) (Run, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.startErr != nil {
		return nil, h.startErr
	}
	h.prompts = append(h.prompts, prompt)
	h.last = &fakeRun{ctx: ctx, release: make(chan struct{})}
	return h.last, nil
}

func (h *fakeHost) Tree() ([]session.Node, error) {
	if !h.recorded {
		return nil, ErrNotRecorded
	}
	return h.tree, nil
}

func (h *fakeHost) note(op string) (Opened, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ops = append(h.ops, op)
	if !h.recorded && op != "new" {
		return Opened{}, ErrNotRecorded
	}
	return Opened{ID: op + "-id", MessageCount: 0}, nil
}

func (h *fakeHost) Fork(id string) (Opened, error) { return h.note("fork:" + id) }
func (h *fakeHost) Clone() (Opened, error)         { return h.note("clone") }
func (h *fakeHost) Switch(s string) (Opened, error) {
	return h.note("switch:" + s)
}
func (h *fakeHost) New() (Opened, error) { return h.note("new") }

func (h *fakeHost) SetModel(provider, model string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ops = append(h.ops, "model:"+provider+"/"+model)
	h.provider, h.model = provider, model
	return nil
}

func (h *fakeHost) Compact(instructions string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ops = append(h.ops, "compact:"+instructions)
	if h.tooShort {
		return &compaction.ErrNothingToCompact{}
	}
	return nil
}

func (h *fakeHost) Commands() ([]CommandInfo, []string) {
	return []CommandInfo{{Name: "help", Summary: "list these commands"}}, []string{"/scoped-models"}
}

func newChannel(t *testing.T) (*Channel, *fakeHost) {
	t.Helper()
	host := &fakeHost{sess: session.New("You are pi-go."), provider: "scripted", model: "scripted-1", recorded: true}
	return NewChannel(host), host
}

func dispatch(c *Channel, cmd Command) Response {
	return c.Dispatch(context.Background(), cmd)
}

func decode(t *testing.T, resp Response, into any) {
	t.Helper()
	if !resp.OK {
		t.Fatalf("%s failed: %+v", resp.Command, resp.Error)
	}
	if err := json.Unmarshal(resp.Data, into); err != nil {
		t.Fatalf("%s data is not JSON: %v", resp.Command, err)
	}
}

// TestAnUnknownVerbAndAnUnbuiltOneAreDifferentAnswers. A client must be able to
// tell "no such command" from "a real command not built yet": the first is its
// own bug, the second is a gap this repository tracks.
func TestAnUnknownVerbAndAnUnbuiltOneAreDifferentAnswers(t *testing.T) {
	c, _ := newChannel(t)
	unknown := dispatch(c, Command{ID: "1", Command: "flibbertigibbet"})
	if unknown.OK || unknown.Error.Kind != FailUnknownCommand {
		t.Fatalf("an invented command was not unknown: %+v", unknown.Error)
	}
	unbuilt := dispatch(c, Command{ID: "2", Command: "cycle_model"})
	if unbuilt.OK || unbuilt.Error.Kind != FailUnimplemented {
		t.Fatalf("a real Pi command was not unimplemented: %+v", unbuilt.Error)
	}
}

// TestEveryResponseEchoesTheIdItAnswers, because a response attributable only
// by arrival order is the ambiguity this protocol exists to remove.
func TestEveryResponseEchoesTheIdItAnswers(t *testing.T) {
	c, _ := newChannel(t)
	resp := dispatch(c, Command{ID: "abc-123", Command: "get_state"})
	if resp.ID != "abc-123" || resp.Command != "get_state" || !resp.OK {
		t.Fatalf("the response did not echo its command: %+v", resp)
	}
}

// TestAPromptIsAcknowledgedBeforeItFinishes is the ack-then-events contract:
// the response is a receipt, returned while the run is still in flight, and
// get_state says so.
func TestAPromptIsAcknowledgedBeforeItFinishes(t *testing.T) {
	c, host := newChannel(t)
	resp := dispatch(c, Command{ID: "1", Command: "prompt", Message: "hello"})
	if !resp.OK || len(resp.Data) != 0 {
		t.Fatalf("the ack is wrong: %+v", resp)
	}
	var state stateData
	decode(t, dispatch(c, Command{ID: "2", Command: "get_state"}), &state)
	if !state.Running {
		t.Fatal("the run was acknowledged but get_state says nothing is running")
	}
	close(host.last.release)
	c.Settle()
	decode(t, dispatch(c, Command{ID: "3", Command: "get_state"}), &state)
	if state.Running {
		t.Fatal("the run finished but get_state still says it is running")
	}
}

// TestASecondPromptWhileOneRunsIsBusy, not queued: a queue the client cannot
// see into is a prompt it believes is running.
func TestASecondPromptWhileOneRunsIsBusy(t *testing.T) {
	c, host := newChannel(t)
	dispatch(c, Command{ID: "1", Command: "prompt", Message: "first"})
	second := dispatch(c, Command{ID: "2", Command: "prompt", Message: "second"})
	if second.OK || second.Error.Kind != FailBusy {
		t.Fatalf("a second prompt was not refused as busy: %+v", second)
	}
	if len(host.prompts) != 1 {
		t.Fatalf("the second prompt reached the host: %v", host.prompts)
	}
	close(host.last.release)
	c.Settle()
	third := dispatch(c, Command{ID: "3", Command: "prompt", Message: "third"})
	if !third.OK {
		t.Fatalf("a prompt after the first finished was refused: %+v", third)
	}
	close(host.last.release)
	c.Settle()
}

// TestAbortCancelsTheRunAndSaysWhetherThereWasOne.
func TestAbortCancelsTheRunAndSaysWhetherThereWasOne(t *testing.T) {
	c, host := newChannel(t)
	idle := dispatch(c, Command{ID: "0", Command: "abort"})
	if !idle.OK || !strings.Contains(string(idle.Data), `"aborted":false`) {
		t.Fatalf("aborting nothing did not say so: %+v", idle)
	}
	dispatch(c, Command{ID: "1", Command: "prompt", Message: "work"})
	aborted := dispatch(c, Command{ID: "2", Command: "abort"})
	if !aborted.OK || !strings.Contains(string(aborted.Data), `"aborted":true`) {
		t.Fatalf("abort did not report the run it cancelled: %+v", aborted)
	}
	done := make(chan struct{})
	go func() { c.Settle(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the aborted run never settled: abort did not cancel its context")
	}
	if host.last.ctx.Err() == nil {
		t.Fatal("the run's context was not cancelled")
	}
}

// TestSteerAndFollowUpReachTheRunInFlightAndNothingElse. During a run they are
// forwarded to it; with nothing running they are a typed not_running rather
// than a silent drop or a queued prompt.
func TestSteerAndFollowUpReachTheRunInFlightAndNothingElse(t *testing.T) {
	c, host := newChannel(t)
	idle := dispatch(c, Command{ID: "0", Command: "steer", Message: "now"})
	if idle.OK || idle.Error.Kind != FailNotRunning {
		t.Fatalf("steering nothing was not not_running: %+v", idle)
	}
	dispatch(c, Command{ID: "1", Command: "prompt", Message: "work"})
	if r := dispatch(c, Command{ID: "2", Command: "steer", Message: "turn left"}); !r.OK {
		t.Fatalf("steer during a run failed: %+v", r)
	}
	if r := dispatch(c, Command{ID: "3", Command: "follow_up", Message: "then stop"}); !r.OK {
		t.Fatalf("follow_up during a run failed: %+v", r)
	}
	empty := dispatch(c, Command{ID: "4", Command: "steer", Message: " "})
	if empty.OK || empty.Error.Kind != FailBadArgument {
		t.Fatalf("an empty steer was not a bad argument: %+v", empty)
	}
	close(host.last.release)
	c.Settle()
	if strings.Join(host.last.steered, ",") != "turn left" || strings.Join(host.last.followed, ",") != "then stop" {
		t.Fatalf("the messages did not reach the run as what they were: steered=%v followed=%v",
			host.last.steered, host.last.followed)
	}
}

// TestAnEmptyPromptIsABadArgument, not an internal error and not a run.
func TestAnEmptyPromptIsABadArgument(t *testing.T) {
	c, host := newChannel(t)
	resp := dispatch(c, Command{ID: "1", Command: "prompt", Message: "   "})
	if resp.OK || resp.Error.Kind != FailBadArgument {
		t.Fatalf("an empty prompt was not a bad argument: %+v", resp.Error)
	}
	if len(host.prompts) != 0 {
		t.Fatal("an empty prompt reached the host")
	}
}

// TestAProviderFailureAtStartKeepsItsClassification is the reason the taxonomy
// is on the wire at all: a client learns whether to wait, pay or fix without
// prose. (A failure after the run starts is on the stream, in agent_end.)
func TestAProviderFailureAtStartKeepsItsClassification(t *testing.T) {
	c, host := newChannel(t)
	host.startErr = &ai.ProviderError{Provider: "scripted", Failure: ai.FailureQuota, Detail: "gone"}
	resp := dispatch(c, Command{ID: "1", Command: "prompt", Message: "x"})
	if resp.OK || resp.Error.Kind != FailProvider {
		t.Fatalf("a provider failure was misclassified: %+v", resp.Error)
	}
	if !strings.Contains(resp.Error.Detail, string(ai.FailureQuota)) {
		t.Fatalf("the classification was flattened away: %q", resp.Error.Detail)
	}
}

// TestGetStateReportsWhatTheSessionIs.
func TestGetStateReportsWhatTheSessionIs(t *testing.T) {
	c, host := newChannel(t)
	_ = host.sess.SetName("my work")
	var data stateData
	decode(t, dispatch(c, Command{ID: "1", Command: "get_state"}), &data)
	if data.Provider != "scripted" || data.Model != "scripted-1" || data.SessionName != "my work" {
		t.Fatalf("state does not describe the session: %+v", data)
	}
}

// TestSessionStatsHasNoCurrency: pi-go ledgers tokens and computes no cost, so
// the payload must not grow a money field a client would trust.
func TestSessionStatsHasNoCurrency(t *testing.T) {
	c, _ := newChannel(t)
	resp := dispatch(c, Command{ID: "1", Command: "get_session_stats"})
	if strings.Contains(string(resp.Data), "cost") || strings.Contains(string(resp.Data), "currency") {
		t.Fatalf("stats claimed a cost pi-go does not compute: %s", resp.Data)
	}
}

// TestTheTreeAndTheEntriesAreTwoViewsOfOneRecord: get_tree is everything,
// branches included; get_entries is the path as it stands, and `since` cuts
// it. Both name the leaf, where the next turn attaches.
func TestTheTreeAndTheEntriesAreTwoViewsOfOneRecord(t *testing.T) {
	c, host := newChannel(t)
	host.tree = []session.Node{
		{ID: "a", Kind: "message", OnPath: true},
		{ID: "b", ParentID: "a", Kind: "message", OnPath: true},
		{ID: "x", ParentID: "a", Kind: "message", OnPath: false, IsLeaf: true},
		{ID: "c", ParentID: "b", Kind: "message", OnPath: true, IsLeaf: true},
	}
	var tree struct {
		Entries []entryData `json:"entries"`
		Leaf    *string     `json:"leaf"`
	}
	decode(t, dispatch(c, Command{ID: "1", Command: "get_tree"}), &tree)
	if len(tree.Entries) != 4 || tree.Leaf == nil || *tree.Leaf != "c" {
		t.Fatalf("the tree is not the whole record with its leaf: %+v", tree)
	}
	var path struct {
		Entries []entryData `json:"entries"`
	}
	decode(t, dispatch(c, Command{ID: "2", Command: "get_entries"}), &path)
	if len(path.Entries) != 3 || path.Entries[2].ID != "c" {
		t.Fatalf("the entries are not the current path: %+v", path.Entries)
	}
	decode(t, dispatch(c, Command{ID: "3", Command: "get_entries", Since: "a"}), &path)
	if len(path.Entries) != 2 || path.Entries[0].ID != "b" {
		t.Fatalf("since did not cut the path after the named entry: %+v", path.Entries)
	}
	bad := dispatch(c, Command{ID: "4", Command: "get_entries", Since: "x"})
	if bad.OK || bad.Error.Kind != FailBadArgument {
		t.Fatalf("since naming an entry off the path was accepted: %+v", bad)
	}
}

// TestAnUnrecordedConversationSaysSoRatherThanFailingInternally: a run that
// keeps nothing has no shape to show and nothing to fork, and that is a state
// of this run rather than a bug in it.
func TestAnUnrecordedConversationSaysSoRatherThanFailingInternally(t *testing.T) {
	c, host := newChannel(t)
	host.recorded = false
	for _, verb := range []string{"get_tree", "clone"} {
		resp := dispatch(c, Command{ID: "1", Command: verb})
		if resp.OK || resp.Error.Kind != FailUnavailable {
			t.Fatalf("%s on an unrecorded conversation was not unavailable: %+v", verb, resp)
		}
	}
	// A new conversation needs no record to start from.
	if resp := dispatch(c, Command{ID: "2", Command: "new_session"}); !resp.OK {
		t.Fatalf("new_session needed a record: %+v", resp)
	}
}

// TestSwappingTheConversationOrModelWaitsForTheRun. The agent holds the session
// and port it was built with; swapping either under a live prompt would write
// the next turn into a conversation the client just left.
func TestSwappingTheConversationOrModelWaitsForTheRun(t *testing.T) {
	c, host := newChannel(t)
	dispatch(c, Command{ID: "1", Command: "prompt", Message: "work"})
	for _, cmd := range []Command{
		{Command: "fork", EntryID: "a"}, {Command: "clone"}, {Command: "switch_session", Session: "s"},
		{Command: "new_session"}, {Command: "set_model", Model: "m"}, {Command: "compact"},
	} {
		cmd.ID = "x"
		resp := dispatch(c, cmd)
		if resp.OK || resp.Error.Kind != FailBusy {
			t.Fatalf("%s during a run was not busy: %+v", cmd.Command, resp)
		}
	}
	if len(host.ops) != 0 {
		t.Fatalf("an operation reached the host during a run: %v", host.ops)
	}
	close(host.last.release)
	c.Settle()

	// Free again: each reaches the host as what it was.
	dispatch(c, Command{ID: "2", Command: "fork", EntryID: "a"})
	dispatch(c, Command{ID: "3", Command: "clone"})
	dispatch(c, Command{ID: "4", Command: "switch_session", Session: "s"})
	dispatch(c, Command{ID: "5", Command: "new_session"})
	var opened Opened
	decode(t, dispatch(c, Command{ID: "6", Command: "set_model", Provider: "p", Model: "m"}), &map[string]any{})
	decode(t, dispatch(c, Command{ID: "7", Command: "clone"}), &opened)
	if opened.ID != "clone-id" {
		t.Fatalf("clone did not report what it opened: %+v", opened)
	}
	want := "fork:a,clone,switch:s,new,model:p/m,clone"
	if got := strings.Join(host.ops, ","); got != want {
		t.Fatalf("the operations did not reach the host as themselves:\ngot  %s\nwant %s", got, want)
	}
}

// TestCompactSaysWhenThereWasNothingToDo — a conversation too short to
// summarise is not a failure, the same shape as aborting when nothing runs.
func TestCompactSaysWhenThereWasNothingToDo(t *testing.T) {
	c, host := newChannel(t)
	host.tooShort = true
	var data map[string]any
	decode(t, dispatch(c, Command{ID: "1", Command: "compact", Instructions: "focus"}), &data)
	if data["compacted"] != false {
		t.Fatalf("a too-short conversation was reported compacted: %v", data)
	}
	if host.ops[0] != "compact:focus" {
		t.Fatalf("the instructions did not reach the host: %v", host.ops)
	}
}

// TestGetCommandsNamesWhatIsHereAndWhatIsNot, so a client can see the shape of
// what is coming rather than discovering each gap by sending into it.
func TestGetCommandsNamesWhatIsHereAndWhatIsNot(t *testing.T) {
	c, _ := newChannel(t)
	var data struct {
		Commands []CommandInfo `json:"commands"`
		NotHere  []string      `json:"not_here"`
	}
	decode(t, dispatch(c, Command{ID: "1", Command: "get_commands"}), &data)
	if len(data.Commands) != 1 || data.Commands[0].Name != "help" || len(data.NotHere) != 1 {
		t.Fatalf("get_commands did not report both halves: %+v", data)
	}
}
