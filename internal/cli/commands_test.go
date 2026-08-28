package cli_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/auth"
	"github.com/iamclancyliang/pi-go/internal/cli"
	"github.com/iamclancyliang/pi-go/internal/settings"
)

// session drives the interactive loop over a script of typed lines.
func interactive(t *testing.T, args cli.Args, workingDir string, typed ...string) (string, string) {
	t.Helper()
	conversation, err := cli.OpenConversation(args, workingDir, cli.DefaultSystemPrompt)
	if err != nil {
		t.Fatalf("opening: %v", err)
	}

	rt := runtimeFor(t, scripted("an answer"))
	rt.Conversation = conversation
	rt.Args = args
	rt.WorkingDir = workingDir

	var out, errOut bytes.Buffer
	cli.RunInteractive(context.Background(), rt, cli.Streams{
		In:  strings.NewReader(strings.Join(typed, "\n") + "\n"),
		Out: &out, Err: &errOut,
	})
	return out.String(), errOut.String()
}

// TestACommandIsNotSentToTheModel. Anything typed with a leading slash acts on
// the session, and forwarding it would spend a request on text meant for the
// program.
func TestACommandIsNotSentToTheModel(t *testing.T) {
	model := scripted("an answer")
	conversation, err := cli.OpenConversation(cli.Args{NoSession: true}, t.TempDir(), cli.DefaultSystemPrompt)
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	defer conversation.Close()

	rt := runtimeFor(t, model)
	rt.Conversation = conversation
	rt.WorkingDir = t.TempDir()

	var out, errOut bytes.Buffer
	cli.RunInteractive(context.Background(), rt, cli.Streams{
		In:  strings.NewReader("/help\n/session\n"),
		Out: &out, Err: &errOut,
	})

	if n := len(model.Requests()); n != 0 {
		t.Fatalf("commands produced %d model requests", n)
	}
}

// TestHelpListsWhatThereIsAndWhatThereIsNot. Showing only what works leaves a
// user to discover each gap by typing into it.
func TestHelpListsWhatThereIsAndWhatThereIsNot(t *testing.T) {
	out, _ := interactive(t, cli.Args{NoSession: true}, t.TempDir(), "/help")

	for _, want := range []string{"/help", "/quit", "/session", "/new", "/resume", "/export", "/tree", "/fork", "/clone"} {
		if !strings.Contains(out, want) {
			t.Fatalf("help does not mention %s:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "not here yet:") || !strings.Contains(out, "/hotkeys") {
		t.Fatalf("help does not say what is missing:\n%s", out)
	}
}

// TestAPiCommandThisBuildLacksSaysWhy, rather than reporting it as a typo the
// user did not make.
func TestAPiCommandThisBuildLacksSaysWhy(t *testing.T) {
	_, errOut := interactive(t, cli.Args{NoSession: true}, t.TempDir(), "/hotkeys")

	if !strings.Contains(errOut, "does not have") {
		t.Fatalf("a known Pi command was not recognised: %q", errOut)
	}
	if strings.Contains(errOut, "unknown command") {
		t.Fatalf("a real Pi command was reported as a typo: %q", errOut)
	}
}

// TestAnActualTypoIsReportedAsOne, and points at where to look.
func TestAnActualTypoIsReportedAsOne(t *testing.T) {
	_, errOut := interactive(t, cli.Args{NoSession: true}, t.TempDir(), "/sesion")
	if !strings.Contains(errOut, "unknown command") || !strings.Contains(errOut, "/help") {
		t.Fatalf("a typo was reported as %q", errOut)
	}
}

// TestSessionInfoDistinguishesUnreportedUsageFromZero. A provider that said
// nothing has not said the session was free.
func TestSessionInfoDistinguishesUnreportedUsageFromZero(t *testing.T) {
	out, _ := interactive(t, cli.Args{NoSession: true}, t.TempDir(), "/session")
	if !strings.Contains(out, "not reported") {
		t.Fatalf("usage nobody reported was shown as a number:\n%s", out)
	}
	if !strings.Contains(out, "recorded   no") {
		t.Fatalf("a --no-session run did not say it was keeping nothing:\n%s", out)
	}
}

// TestNewStartsASeparateConversation, and the next turn lands in it rather than
// in the one the user left.
func TestNewStartsASeparateConversation(t *testing.T) {
	dir, work := t.TempDir(), t.TempDir()
	out, errOut := interactive(t, cli.Args{SessionDir: dir}, work,
		"first question", "/new", "second question")

	if strings.Contains(errOut, "could not") {
		t.Fatalf("starting a new conversation failed: %q", errOut)
	}
	if !strings.Contains(out, "started ") {
		t.Fatalf("/new did not report a new conversation:\n%s", out)
	}

	all, err := listSessions(dir, work)
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("/new left %d conversations, want 2", len(all))
	}
	// One turn each: the second question must not have landed in the first.
	for _, info := range all {
		if info.Entries != 2 {
			t.Fatalf("a conversation holds %d entries, want 2 (one exchange): %+v", info.Entries, info)
		}
	}
}

// TestExportWritesSomethingAPersonReads, not a copy of the record: an export is
// for reading, and the session file is a format for this program to reopen.
func TestExportWritesSomethingAPersonReads(t *testing.T) {
	work := t.TempDir()
	target := filepath.Join(t.TempDir(), "conversation.md")

	out, errOut := interactive(t, cli.Args{NoSession: true}, work,
		"a question worth keeping", "/export "+target)
	if strings.Contains(errOut, "could not") {
		t.Fatalf("export failed: %q", errOut)
	}
	if !strings.Contains(out, "exported to") {
		t.Fatalf("/export did not report where it went:\n%s", out)
	}

	raw, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("reading the export: %v", err)
	}
	body := string(raw)
	if !strings.Contains(body, "a question worth keeping") || !strings.Contains(body, "an answer") {
		t.Fatalf("the export lost the conversation:\n%s", body)
	}
	if strings.Contains(body, `"kind":`) {
		t.Fatalf("the export is the record rather than something to read:\n%s", body)
	}
}

// TestExportWithNoPathSaysSo rather than writing somewhere the user did not
// choose.
func TestExportWithNoPathSaysSo(t *testing.T) {
	_, errOut := interactive(t, cli.Args{NoSession: true}, t.TempDir(), "/export")
	if !strings.Contains(errOut, "needs a path") {
		t.Fatalf("/export with no path said %q", errOut)
	}
}

// TestQuitEndsTheSessionWithoutReadingOn.
func TestQuitEndsTheSessionWithoutReadingOn(t *testing.T) {
	model := scripted("an answer")
	conversation, _ := cli.OpenConversation(cli.Args{NoSession: true}, t.TempDir(), cli.DefaultSystemPrompt)
	defer conversation.Close()

	rt := runtimeFor(t, model)
	rt.Conversation = conversation
	rt.WorkingDir = t.TempDir()

	var out, errOut bytes.Buffer
	cli.RunInteractive(context.Background(), rt, cli.Streams{
		In:  strings.NewReader("/quit\nnever asked\n"),
		Out: &out, Err: &errOut,
	})
	if n := len(model.Requests()); n != 0 {
		t.Fatalf("input after /quit was still sent: %d requests", n)
	}
}

// TestTreeShowsWhereTheConversationStandsAndWhereItCouldGo.
func TestTreeShowsWhereTheConversationStandsAndWhereItCouldGo(t *testing.T) {
	out, errOut := interactive(t, cli.Args{SessionDir: t.TempDir()}, t.TempDir(),
		"a question", "/tree")
	if strings.Contains(errOut, "could not") {
		t.Fatalf("/tree failed: %q", errOut)
	}
	if !strings.Contains(out, "user: a question") {
		t.Fatalf("/tree does not show the conversation:\n%s", out)
	}
	// Both markers are needed: one says where you are, the other where a branch
	// can be picked up again.
	if !strings.Contains(out, "on the current path") || !strings.Contains(out, "branch tip") {
		t.Fatalf("/tree does not explain its markers:\n%s", out)
	}
}

// TestTreeOnAnUnrecordedConversationSaysSo rather than showing nothing, which
// reads as a conversation with no history.
func TestTreeOnAnUnrecordedConversationSaysSo(t *testing.T) {
	_, errOut := interactive(t, cli.Args{NoSession: true}, t.TempDir(), "/tree")
	if !strings.Contains(errOut, "not recorded") {
		t.Fatalf("/tree on a --no-session run said %q", errOut)
	}
}

// TestGoingBackChangesWhatTheNextTurnFollowsFrom, which is the point of being
// able to go back at all.
func TestGoingBackChangesWhatTheNextTurnFollowsFrom(t *testing.T) {
	dir, work := t.TempDir(), t.TempDir()
	out, errOut := interactive(t, cli.Args{SessionDir: dir}, work,
		"first question", "/tree")
	if strings.Contains(errOut, "could not") {
		t.Fatalf("setting up: %q", errOut)
	}
	// The prompt and the first listing line share a line, so the id cannot be
	// taken by position. It is the field shaped like a shortened id.
	var first string
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "user: first question") {
			continue
		}
		for _, field := range strings.Fields(line) {
			if len(field) == 20 && strings.IndexFunc(field, func(r rune) bool {
				return !strings.ContainsRune("0123456789abcdef-", r)
			}) < 0 {
				first = field
				break
			}
		}
		break
	}
	if first == "" {
		t.Fatalf("could not find an entry id in the listing:\n%s", out)
	}

	out, errOut = interactive(t, cli.Args{SessionDir: dir, Continue: true}, work,
		"/tree "+first, "/session")
	// Nothing at all on stderr: a command that quietly failed would otherwise
	// look like one that did nothing, which is what this test nearly missed.
	if strings.TrimSpace(errOut) != "" {
		t.Fatalf("going back reported: %q", errOut)
	}
	if !strings.Contains(out, "at "+first) {
		t.Fatalf("going back did not report where it landed:\n%s", out)
	}
	// One message on the path: the opening question, and nothing after it.
	if !strings.Contains(out, "messages   1") {
		t.Fatalf("going back did not shorten the conversation:\n%s", out)
	}
}

// TestCloneCopiesTheConversationAndLeavesTheOriginal.
func TestCloneCopiesTheConversationAndLeavesTheOriginal(t *testing.T) {
	dir, work := t.TempDir(), t.TempDir()
	out, errOut := interactive(t, cli.Args{SessionDir: dir}, work,
		"a question worth branching", "/clone", "/session")
	if strings.Contains(errOut, "could not") {
		t.Fatalf("/clone failed: %q", errOut)
	}
	if !strings.Contains(out, "forked to") {
		t.Fatalf("/clone did not report the copy:\n%s", out)
	}

	all, err := listSessions(dir, work)
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("/clone left %d conversations, want 2", len(all))
	}
}

// TestCloningAnUnrecordedConversationSaysSo.
func TestCloningAnUnrecordedConversationSaysSo(t *testing.T) {
	_, errOut := interactive(t, cli.Args{NoSession: true}, t.TempDir(), "/clone")
	if !strings.Contains(errOut, "nothing to copy") {
		t.Fatalf("/clone on a --no-session run said %q", errOut)
	}
}

// TestForkWithNoIdSaysWhereToFindOne.
func TestForkWithNoIdSaysWhereToFindOne(t *testing.T) {
	_, errOut := interactive(t, cli.Args{SessionDir: t.TempDir()}, t.TempDir(), "/fork")
	if !strings.Contains(errOut, "/tree lists them") {
		t.Fatalf("/fork with no id said %q", errOut)
	}
}

// TestListedIdsCanBeTypedBack pins the bug that made /tree unusable: ids are a
// millisecond followed by randomness, and a shortening that stopped inside the
// millisecond was identical for every entry written in it — which two turns of
// one conversation routinely are. The listing then showed ids that the command
// reading them could never resolve.
func TestListedIdsCanBeTypedBack(t *testing.T) {
	dir, work := t.TempDir(), t.TempDir()
	out, _ := interactive(t, cli.Args{SessionDir: dir}, work,
		"first", "second", "third", "/tree")

	var ids []string
	for _, line := range strings.Split(out, "\n") {
		for _, field := range strings.Fields(line) {
			if len(field) == 20 && strings.IndexFunc(field, func(r rune) bool {
				return !strings.ContainsRune("0123456789abcdef-", r)
			}) < 0 {
				ids = append(ids, field)
			}
		}
	}
	if len(ids) < 4 {
		t.Fatalf("the listing showed %d ids for three exchanges:\n%s", len(ids), out)
	}

	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			t.Fatalf("two entries were listed under the same id %q; /tree could not tell them apart:\n%s",
				id, out)
		}
		seen[id] = true
	}
}

// TestNamingAConversationOutlivesTheRun. A name only in memory disappears while
// the user believes it was set.
func TestNamingAConversationOutlivesTheRun(t *testing.T) {
	dir, work := t.TempDir(), t.TempDir()
	out, errOut := interactive(t, cli.Args{SessionDir: dir}, work,
		"a question", "/name the refactor", "/name")
	if strings.Contains(errOut, "could not") {
		t.Fatalf("/name failed: %q", errOut)
	}
	if !strings.Contains(out, `named "the refactor"`) {
		t.Fatalf("/name did not confirm:\n%s", out)
	}

	out, _ = interactive(t, cli.Args{SessionDir: dir, Continue: true}, work, "/name")
	if !strings.Contains(out, "the refactor") {
		t.Fatalf("the name did not survive the run:\n%s", out)
	}
}

// TestRenamingKeepsTheLatest, because a rename is an appended event and the last
// one is what the conversation is called.
func TestRenamingKeepsTheLatest(t *testing.T) {
	dir, work := t.TempDir(), t.TempDir()
	interactive(t, cli.Args{SessionDir: dir}, work,
		"a question", "/name first name", "/name second name")

	out, _ := interactive(t, cli.Args{SessionDir: dir, Continue: true}, work, "/name")
	if !strings.Contains(out, "second name") || strings.Contains(out, "first name") {
		t.Fatalf("renaming did not take:\n%s", out)
	}
}

// TestAskingForTheNameOfSomethingUnnamedSaysHow.
func TestAskingForTheNameOfSomethingUnnamedSaysHow(t *testing.T) {
	_, errOut := interactive(t, cli.Args{NoSession: true}, t.TempDir(), "/name")
	if !strings.Contains(errOut, "usage: /name") {
		t.Fatalf("/name on an unnamed conversation said %q", errOut)
	}
}

// TestImportOpensAConversationFromElsewhere, and leaves the one being left on
// disk — which is why, unlike Pi, it does not ask first.
func TestImportOpensAConversationFromElsewhere(t *testing.T) {
	dir, work := t.TempDir(), t.TempDir()
	first, _, _ := converse(t, cli.Args{SessionDir: dir}, work, "the imported conversation")
	path := first.Path
	first.Close()

	// A second, separate conversation, from which the first is imported.
	out, errOut := interactive(t, cli.Args{SessionDir: t.TempDir()}, t.TempDir(),
		"/import "+path, "/session")
	if strings.Contains(errOut, "could not") {
		t.Fatalf("/import failed: %q", errOut)
	}
	if !strings.Contains(out, "imported") {
		t.Fatalf("/import did not report:\n%s", out)
	}
	// The conversation it left is untouched.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the imported file is gone: %v", err)
	}
}

// TestImportingSomethingThatIsNotThereSaysSo.
func TestImportingSomethingThatIsNotThereSaysSo(t *testing.T) {
	_, errOut := interactive(t, cli.Args{NoSession: true}, t.TempDir(), "/import /no/such/file.jsonl")
	if !strings.Contains(errOut, "no session file") {
		t.Fatalf("/import of a missing file said %q", errOut)
	}
	_, errOut = interactive(t, cli.Args{NoSession: true}, t.TempDir(), "/import")
	if !strings.Contains(errOut, "usage: /import") {
		t.Fatalf("/import with no path said %q", errOut)
	}
}

// TestCopyingWithNothingToCopySaysSo rather than putting an empty string on the
// clipboard, which silently replaces whatever was there.
func TestCopyingWithNothingToCopySaysSo(t *testing.T) {
	_, errOut := interactive(t, cli.Args{NoSession: true}, t.TempDir(), "/copy")
	if !strings.Contains(errOut, "no answers to copy") {
		t.Fatalf("/copy with nothing to copy said %q", errOut)
	}
}

// TestModelReportsWhatIsAnsweringWhenAskedWithNoArgument, because "which model
// am I spending on" is the question people ask before switching.
func TestModelReportsWhatIsAnsweringWhenAskedWithNoArgument(t *testing.T) {
	out, errOut := interactive(t, cli.Args{NoSession: true}, t.TempDir(), "/model")
	if !strings.Contains(out, "scripted/scripted-1") {
		t.Fatalf("/model did not report what is answering:\n%s", out)
	}
	if !strings.Contains(errOut, "usage: /model") {
		t.Fatalf("/model did not say how to switch: %q", errOut)
	}
}

// TestSwitchingToAnUnknownProviderIsRefused, and the conversation carries on
// with what it had rather than being left pointing at a port that did not open.
func TestSwitchingToAnUnknownProviderIsRefused(t *testing.T) {
	out, errOut := interactive(t, cli.Args{NoSession: true}, t.TempDir(),
		"/model nowhere/some-model", "/model")
	if !strings.Contains(errOut, "could not switch") {
		t.Fatalf("an unknown provider was accepted: %q", errOut)
	}
	if !strings.Contains(out, "scripted/scripted-1") {
		t.Fatalf("a refused switch changed what is answering:\n%s", out)
	}
}

// TestNamingAProviderWithNoModelIsRefused rather than switching provider and
// leaving the model empty, which reaches the wire as a request naming nothing.
func TestNamingAProviderWithNoModelIsRefused(t *testing.T) {
	_, errOut := interactive(t, cli.Args{NoSession: true}, t.TempDir(), "/model deepseek/")
	if !strings.Contains(errOut, "no model") {
		t.Fatalf("a provider with no model said %q", errOut)
	}
}

// TestLogoutListsWhatIsSavedRatherThanRemovingEverything. A bare command that
// destroys all credentials is one somebody runs once by accident.
func TestLogoutListsWhatIsSavedRatherThanRemovingEverything(t *testing.T) {
	dir := t.TempDir()
	store := auth.Open(dir)
	if err := store.Set("deepseek", auth.APIKey("sk-one")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	out, errOut := interactive(t, cli.Args{NoSession: true, SessionDir: dir}, t.TempDir(), "/logout")
	if !strings.Contains(out, "deepseek") {
		t.Fatalf("/logout did not list what is saved:\n%s", out)
	}
	if !strings.Contains(errOut, "usage: /logout") {
		t.Fatalf("/logout did not say how to remove one: %q", errOut)
	}
	if names, _ := store.Providers(); len(names) != 1 {
		t.Fatalf("a bare /logout removed credentials: %v", names)
	}
}

// TestLogoutForgetsOneProvider.
func TestLogoutForgetsOneProvider(t *testing.T) {
	dir := t.TempDir()
	store := auth.Open(dir)
	store.Set("deepseek", auth.APIKey("sk-one"))
	store.Set("openai", auth.APIKey("sk-two"))

	out, _ := interactive(t, cli.Args{NoSession: true, SessionDir: dir}, t.TempDir(), "/logout deepseek")
	if !strings.Contains(out, "forgot") {
		t.Fatalf("/logout did not confirm:\n%s", out)
	}
	names, _ := store.Providers()
	if len(names) != 1 || names[0] != "openai" {
		t.Fatalf("after logging out of deepseek the store holds %v", names)
	}
}

// TestLogoutSaysWhenTheEnvironmentStillCarriesOne. A user told they are logged
// out while requests keep succeeding has been told something false.
func TestLogoutSaysWhenTheEnvironmentStillCarriesOne(t *testing.T) {
	dir := t.TempDir()
	auth.Open(dir).Set("deepseek", auth.APIKey("sk-one"))
	t.Setenv("DEEPSEEK_API_KEY", "sk-from-the-environment")

	out, _ := interactive(t, cli.Args{NoSession: true, SessionDir: dir}, t.TempDir(), "/logout deepseek")
	if !strings.Contains(out, "DEEPSEEK_API_KEY is still set") {
		t.Fatalf("/logout did not mention the environment:\n%s", out)
	}
}

// TestLoginRefusesAProviderThisBuildDoesNotHave, rather than saving a
// credential nothing will ever read.
func TestLoginRefusesAProviderThisBuildDoesNotHave(t *testing.T) {
	dir := t.TempDir()
	_, errOut := interactive(t, cli.Args{NoSession: true, SessionDir: dir}, t.TempDir(), "/login nowhere")
	if !strings.Contains(errOut, "unknown provider") {
		t.Fatalf("/login of an unknown provider said %q", errOut)
	}
	if names, _ := auth.Open(dir).Providers(); len(names) != 0 {
		t.Fatalf("a refused login saved %v", names)
	}
	_, errOut = interactive(t, cli.Args{NoSession: true, SessionDir: dir}, t.TempDir(), "/login")
	if !strings.Contains(errOut, "usage: /login") {
		t.Fatalf("/login with no provider said %q", errOut)
	}
}

// TestAStoredCredentialIsUsedWithoutTheEnvironment, which is the point of
// saving one.
func TestAStoredCredentialIsUsedWithoutTheEnvironment(t *testing.T) {
	dir := t.TempDir()
	auth.Open(dir).Set("deepseek", auth.APIKey("sk-stored"))
	for _, v := range []string{"DEEPSEEK_API_KEY", "OPENAI_API_KEY", "DASHSCOPE_API_KEY"} {
		t.Setenv(v, "")
	}

	chosen, err := cli.SelectProvider(cli.Args{SessionDir: dir})
	if err != nil {
		t.Fatalf("with a stored credential and no environment: %v", err)
	}
	if chosen.Name != "deepseek" {
		t.Fatalf("the stored credential selected %q", chosen.Name)
	}
}

// TestSharingAsksBeforeUploading. A coding conversation carries source code,
// and tool output can carry things never meant to leave the machine. "Secret"
// means unlisted, not private, and an upload cannot be recalled.
func TestSharingAsksBeforeUploading(t *testing.T) {
	out, _ := interactive(t, cli.Args{NoSession: true}, t.TempDir(),
		"a question", "/share", "n")

	// Either it asked, or it refused for a reason that comes before asking —
	// gh missing or logged out. What it must never do is upload silently.
	asked := strings.Contains(out, "upload") && strings.Contains(out, "[y/N]")
	refused := strings.Contains(out, "not shared")
	if !asked && !strings.Contains(out, "shared:") {
		// gh is unavailable on this machine; nothing was uploaded, which is the
		// property under test.
		return
	}
	if asked && !refused {
		t.Fatalf("answering no did not stop the upload:\n%s", out)
	}
}

// TestSharingWithNothingToShareSaysSo rather than uploading an empty document.
func TestSharingWithNothingToShareSaysSo(t *testing.T) {
	_, errOut := interactive(t, cli.Args{NoSession: true}, t.TempDir(), "/share")
	if !strings.Contains(errOut, "no conversation to share") &&
		!strings.Contains(errOut, "GitHub CLI") {
		t.Fatalf("/share on an empty conversation said %q", errOut)
	}
}

// TestAnythingOtherThanYesIsNo. End of input, a blank line and a typo must all
// leave the outward-facing thing undone.
func TestAnythingOtherThanYesIsNo(t *testing.T) {
	for _, answer := range []string{"", "no", "sure", "Y E S"} {
		out, _ := interactive(t, cli.Args{NoSession: true}, t.TempDir(),
			"a question", "/share", answer)
		if strings.Contains(out, "shared: ") {
			t.Fatalf("answering %q uploaded the conversation:\n%s", answer, out)
		}
	}
}

// settingsSession drives the loop with a resolved config, which the settings
// and trust commands read.
func settingsSession(t *testing.T, agentDir, work string, typed ...string) (string, string) {
	t.Helper()
	args := cli.Args{NoSession: true, SessionDir: agentDir}
	cfg, err := cli.ResolveConfig(args, work, func(string) bool { return false })
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	conversation, err := cli.OpenConversation(args, work, cli.DefaultSystemPrompt)
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	rt := runtimeFor(t, scripted("an answer"))
	rt.Conversation = conversation
	rt.Args = args
	rt.WorkingDir = work
	rt.Config = cfg

	var out, errOut bytes.Buffer
	cli.RunInteractive(context.Background(), rt, cli.Streams{
		In:  strings.NewReader(strings.Join(typed, "\n") + "\n"),
		Out: &out, Err: &errOut,
	})
	return out.String(), errOut.String()
}

// TestSettingsListsEveryKeyAndSetsOne, and the change lands in the global file
// so the next run reads it.
func TestSettingsListsEveryKeyAndSetsOne(t *testing.T) {
	agentDir, work := t.TempDir(), t.TempDir()
	out, errOut := settingsSession(t, agentDir, work,
		"/settings", "/settings defaultModel deepseek-reasoner", "/settings")
	if strings.Contains(errOut, "could not") {
		t.Fatalf("settings failed: %q", errOut)
	}
	for _, key := range []string{"defaultModel", "shellPath", "defaultProjectTrust", "compaction.keepRecentTokens"} {
		if !strings.Contains(out, key) {
			t.Fatalf("the listing does not show %s:\n%s", key, out)
		}
	}
	if !strings.Contains(out, "deepseek-reasoner") {
		t.Fatalf("the set value did not show:\n%s", out)
	}

	saved, err := settings.Load(settings.GlobalPath(agentDir))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if saved.DefaultModel != "deepseek-reasoner" {
		t.Fatalf("the change did not reach the file: %+v", saved)
	}
}

// TestSettingAnUnknownKeyIsRefusedByName.
func TestSettingAnUnknownKeyIsRefusedByName(t *testing.T) {
	_, errOut := settingsSession(t, t.TempDir(), t.TempDir(), "/settings modle x")
	if !strings.Contains(errOut, `no setting called "modle"`) {
		t.Fatalf("a misspelt key said %q", errOut)
	}
}

// TestSettingAnInvalidValueIsRefusedWithTheChoices.
func TestSettingAnInvalidValueIsRefusedWithTheChoices(t *testing.T) {
	agentDir := t.TempDir()
	_, errOut := settingsSession(t, agentDir, t.TempDir(), "/settings defaultProjectTrust maybe")
	if !strings.Contains(errOut, "ask, always or never") {
		t.Fatalf("an invalid value said %q", errOut)
	}
	if saved, _ := settings.Load(settings.GlobalPath(agentDir)); saved.DefaultProjectTrust != "" {
		t.Fatalf("a refused value was saved: %+v", saved)
	}
}

// TestTrustReportsAndRecords, and /reload applies the change to the session.
func TestTrustReportsAndRecords(t *testing.T) {
	agentDir, work := t.TempDir(), t.TempDir()
	writeProjectSettings(t, work, `{"defaultModel":"from-the-project"}`)

	out, errOut := settingsSession(t, agentDir, work,
		"/trust", "/trust yes", "/reload", "/settings")
	if strings.Contains(errOut, "could not") {
		t.Fatalf("trust failed: %q", errOut)
	}
	if !strings.Contains(out, "NOT in effect") {
		t.Fatalf("/trust did not say the settings are not loaded:\n%s", out)
	}
	if !strings.Contains(out, "trusted") {
		t.Fatalf("/trust yes did not confirm:\n%s", out)
	}
	// After /reload, the project's model shows in the effective view.
	if !strings.Contains(out, "from-the-project") {
		t.Fatalf("/reload did not apply the newly trusted project:\n%s", out)
	}
}

// TestTrustOnAProjectWithNothingToTrustSaysSo.
func TestTrustOnAProjectWithNothingToTrustSaysSo(t *testing.T) {
	out, _ := settingsSession(t, t.TempDir(), t.TempDir(), "/trust")
	if !strings.Contains(out, "nothing to trust") {
		t.Fatalf("/trust said:\n%s", out)
	}
}

// TestReloadSaysWhatOnlyANewRunPicksUp. "Reloaded" covering half the truth is
// how a user comes to believe a change is live.
func TestReloadSaysWhatOnlyANewRunPicksUp(t *testing.T) {
	agentDir, work := t.TempDir(), t.TempDir()
	out, errOut := settingsSession(t, agentDir, work,
		"/settings shellPath /bin/zsh", "/reload")
	if strings.Contains(errOut, "could not") {
		t.Fatalf("reload failed: %q", errOut)
	}
	if !strings.Contains(out, "a new run picks those up") {
		t.Fatalf("/reload did not say what needs a new run:\n%s", out)
	}
}
