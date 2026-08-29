package tools_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/tools"
)

func promptFor(t *testing.T, p tools.SystemPrompt, names ...string) string {
	t.Helper()
	var chosen []tools.Tool
	for _, tool := range tools.BuiltIn(t.TempDir()) {
		for _, want := range names {
			if tool.Name() == want {
				chosen = append(chosen, tool)
			}
		}
	}
	if len(chosen) != len(names) {
		t.Fatalf("wanted %v, found %d tools", names, len(chosen))
	}
	return p.Build(chosen)
}

// TestTheToolSetChangesThePrompt is the coupling the inventory names: a
// hard-coded prompt loses it, and a tool the model was never told about is one
// it does not reach for.
func TestTheToolSetChangesThePrompt(t *testing.T) {
	few := promptFor(t, tools.SystemPrompt{}, "read", "ls")
	many := promptFor(t, tools.SystemPrompt{}, "read", "ls", "edit", "bash")

	if few == many {
		t.Fatal("offering four tools produced the same prompt as offering two")
	}
	if strings.Contains(few, "- edit:") {
		t.Fatalf("a tool that was not offered appears in the prompt:\n%s", few)
	}
	if !strings.Contains(many, "- edit:") || !strings.Contains(many, "- bash:") {
		t.Fatalf("an offered tool is missing from the list:\n%s", many)
	}
}

// TestTheRuleEditDependsOnReachesTheModel. Every oldText matching the ORIGINAL
// file is the one thing a model must know to use edit correctly, and no
// argument schema can say it.
func TestTheRuleEditDependsOnReachesTheModel(t *testing.T) {
	prompt := promptFor(t, tools.SystemPrompt{}, "edit")
	if !strings.Contains(prompt, "matched against the original file") {
		t.Fatalf("edit's central rule is not in the prompt:\n%s", prompt)
	}
}

// TestGuidelinesAreNotRepeated. Two tools may give the same advice, and a
// prompt that repeats itself spends context saying one thing twice.
func TestGuidelinesAreNotRepeated(t *testing.T) {
	prompt := promptFor(t, tools.SystemPrompt{}, "read", "write", "edit", "bash")
	if n := strings.Count(prompt, "Be concise in your responses"); n != 1 {
		t.Fatalf("a closing guideline appears %d times:\n%s", n, prompt)
	}
}

// TestBashIsOnlyToldToExploreWhenNothingBetterIsOffered. With grep, find or ls
// available, that advice pushes the model toward the shell for work a tool does
// better and reports more cheaply.
func TestBashIsOnlyToldToExploreWhenNothingBetterIsOffered(t *testing.T) {
	alone := promptFor(t, tools.SystemPrompt{}, "bash")
	if !strings.Contains(alone, "Use bash for file operations") {
		t.Fatalf("with only bash, the exploration guideline is missing:\n%s", alone)
	}
	withTools := promptFor(t, tools.SystemPrompt{}, "bash", "grep")
	if strings.Contains(withTools, "Use bash for file operations") {
		t.Fatalf("with grep available, bash is still recommended for exploration:\n%s", withTools)
	}
}

// TestACustomPromptReplacesTheAssemblyButNotTheFacts. Where the agent is and
// what the project said are facts about the run, not instructions a caller can
// relieve it of.
func TestACustomPromptReplacesTheAssemblyButNotTheFacts(t *testing.T) {
	prompt := promptFor(t, tools.SystemPrompt{
		Custom:     "You are something else entirely.",
		WorkingDir: "/somewhere",
		Context:    []tools.ContextFile{{Path: "/p/AGENTS.md", Content: "project rule"}},
	}, "read")

	if strings.Contains(prompt, "Available tools:") {
		t.Fatalf("a custom prompt did not replace the assembly:\n%s", prompt)
	}
	if !strings.Contains(prompt, "You are something else entirely.") {
		t.Fatalf("the custom prompt is missing:\n%s", prompt)
	}
	if !strings.Contains(prompt, "project rule") || !strings.Contains(prompt, "/somewhere") {
		t.Fatalf("a custom prompt dropped the project context or the directory:\n%s", prompt)
	}
}

// TestContextFilesReadNearestLast. A model reads later instructions as the more
// specific ones, so a repository must be able to narrow what its parent said.
func TestContextFilesReadNearestLast(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "project", "deep")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	write := func(dir, content string) {
		if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(content), 0o600); err != nil {
			t.Fatalf("writing: %v", err)
		}
	}
	write(filepath.Join(root, "project"), "the outer rule")
	write(nested, "the inner rule")

	files := tools.LoadContextFiles("", nested)
	if len(files) < 2 {
		t.Fatalf("found %d context files, want the outer and the inner", len(files))
	}
	outer, inner := -1, -1
	for i, f := range files {
		if strings.Contains(f.Content, "the outer rule") {
			outer = i
		}
		if strings.Contains(f.Content, "the inner rule") {
			inner = i
		}
	}
	if outer < 0 || inner < 0 {
		t.Fatalf("both files should be found: %+v", files)
	}
	if inner < outer {
		t.Fatalf("the nearer file reads before the farther one: %+v", files)
	}
}

// TestAnOverrideWinsWithinOneDirectory, which is what naming it an override
// means.
func TestAnOverrideWinsWithinOneDirectory(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("the ordinary one"), 0o600)
	os.WriteFile(filepath.Join(dir, "AGENTS.override.md"), []byte("the override"), 0o600)

	files := tools.LoadContextFiles("", dir)
	for _, f := range files {
		if strings.Contains(f.Content, "the ordinary one") {
			t.Fatalf("the overridden file was loaded as well: %+v", files)
		}
	}
	if len(files) == 0 || !strings.Contains(files[len(files)-1].Content, "the override") {
		t.Fatalf("the override was not loaded: %+v", files)
	}
}

// TestOneFileIsNeverAppliedTwice, which the agent directory sitting inside the
// walked ancestry would otherwise cause.
func TestOneFileIsNeverAppliedTwice(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("only once"), 0o600)

	files := tools.LoadContextFiles(dir, dir)
	seen := 0
	for _, f := range files {
		if strings.Contains(f.Content, "only once") {
			seen++
		}
	}
	if seen != 1 {
		t.Fatalf("one file was applied %d times: %+v", seen, files)
	}
}

// TestTheProjectsInstructionsCarryTheirPath. An instruction a user can trace to
// a file is one they can change; one they cannot reads as the tool inventing
// rules.
func TestTheProjectsInstructionsCarryTheirPath(t *testing.T) {
	prompt := tools.SystemPrompt{
		Context: []tools.ContextFile{{Path: "/p/AGENTS.md", Content: "a rule"}},
	}.Build(nil)
	if !strings.Contains(prompt, `path="/p/AGENTS.md"`) {
		t.Fatalf("the instruction does not say where it came from:\n%s", prompt)
	}
}
