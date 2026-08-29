package tools

import (
	"sort"
	"strings"
)

// Contribution is what one tool adds to the system prompt.
type Contribution struct {
	// Snippet is the one line describing the tool in the available-tools list.
	//
	// A tool with no snippet is NOT listed, which is Pi's rule and a deliberate
	// one: the registry decides what can be called, and this decides what the
	// model is told about. A tool registered for a caller's own use — an
	// extension's private helper — should not appear in a list the model reads
	// as its options.
	Snippet string

	// Guidelines are how to use it well: the rules a model cannot infer from
	// an argument schema. They are the substantive half — an argument's type
	// says nothing about whether edits are matched against the original file.
	Guidelines []string
}

// SystemPrompt assembles what the model is told, from the tools it is given.
type SystemPrompt struct {
	// Preamble opens the prompt. Empty uses the default.
	Preamble string

	// Append is added after the assembled prompt, for a caller that wants to
	// add to the default rather than replace it.
	Append string

	// Custom replaces the preamble, the tool list and the guidelines entirely.
	// The working directory and any project context still follow it: they are
	// facts about where the agent is, not instructions it can be relieved of.
	Custom string

	// WorkingDir is where the agent is running.
	WorkingDir string

	// Context is the project's own instructions, in the order they should be
	// read — the nearest last, since the most specific should be read closest
	// to the task.
	Context []ContextFile
}

// ContextFile is one project instruction file and where it came from.
//
// The path travels with the content because the model is told it: an
// instruction the user can trace to a file is one they can change, and one
// they cannot trace reads as the tool inventing rules.
type ContextFile struct {
	Path    string
	Content string
}

// DefaultPreamble is what the agent is told it is.
//
// Pi's preamble names its own README, docs and examples so a model can answer
// questions about Pi itself. That block is deliberately not carried here:
// pointing a model at another program's manual would have it answer questions
// about Pi when asked about pi-go.
const DefaultPreamble = "You are an expert coding assistant operating inside pi-go, a coding " +
	"agent harness. You help users by reading files, executing commands, editing code, and " +
	"writing new files."

// alwaysGuidelines close every prompt, as they do in Pi.
var alwaysGuidelines = []string{
	"Be concise in your responses",
	"Show file paths clearly when working with files",
}

// Build renders the system prompt for a set of tools.
//
// Order matters and is Pi's: what the agent is, what it can do, how to do it
// well, then the project's own instructions, then where it is standing. The
// project's instructions come after the general ones so that a repository can
// contradict a default without arguing with something the model reads later.
func (p SystemPrompt) Build(registered []Tool) string {
	var b strings.Builder

	if p.Custom != "" {
		b.WriteString(p.Custom)
	} else {
		preamble := p.Preamble
		if preamble == "" {
			preamble = DefaultPreamble
		}
		b.WriteString(preamble)
		b.WriteString("\n\nAvailable tools:\n")
		b.WriteString(toolList(registered))
		b.WriteString("\n\nIn addition to the tools above, you may have access to other custom " +
			"tools depending on the project.\n\nGuidelines:\n")
		b.WriteString(guidelines(registered))
	}

	if p.Append != "" {
		b.WriteString("\n\n")
		b.WriteString(p.Append)
	}
	if len(p.Context) > 0 {
		b.WriteString("\n\n<project_context>\n\nProject-specific instructions and guidelines:\n\n")
		for _, file := range p.Context {
			b.WriteString("<project_instructions path=\"" + file.Path + "\">\n")
			b.WriteString(strings.TrimRight(file.Content, "\n"))
			b.WriteString("\n</project_instructions>\n\n")
		}
		b.WriteString("</project_context>\n")
	}
	if p.WorkingDir != "" {
		b.WriteString("\nCurrent working directory: " + p.WorkingDir)
	}
	return b.String()
}

// toolList is the "Available tools" section: only the tools that offered a
// snippet, in a stable order.
func toolList(registered []Tool) string {
	var lines []string
	for _, tool := range registered {
		if snippet := tool.Prompt().Snippet; snippet != "" {
			lines = append(lines, "- "+tool.Name()+": "+snippet)
		}
	}
	if len(lines) == 0 {
		// Said rather than left blank: a model shown an empty heading may read
		// it as a list it failed to receive.
		return "(none)"
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// guidelines collects what the tools ask for, deduplicated in the order they
// were offered, with the two that always close.
//
// Deduplicated because two tools may give the same advice, and a prompt that
// repeats itself spends context saying one thing twice.
func guidelines(registered []Tool) string {
	var ordered []string
	seen := map[string]bool{}
	add := func(line string) {
		line = strings.TrimSpace(line)
		if line == "" || seen[line] {
			return
		}
		seen[line] = true
		ordered = append(ordered, line)
	}

	// One conditional, as Pi has it: told to use bash for exploration only
	// when nothing better is offered. With grep, find or ls available, that
	// advice would push the model toward the shell for work a tool does better
	// and reports more cheaply.
	if has(registered, "bash") && !has(registered, "grep") &&
		!has(registered, "find") && !has(registered, "ls") {
		add("Use bash for file operations like ls, rg, find")
	}
	for _, tool := range registered {
		for _, line := range tool.Prompt().Guidelines {
			add(line)
		}
	}
	for _, line := range alwaysGuidelines {
		add(line)
	}

	for i, line := range ordered {
		ordered[i] = "- " + line
	}
	return strings.Join(ordered, "\n")
}

func has(registered []Tool, name string) bool {
	for _, tool := range registered {
		if tool.Name() == name {
			return true
		}
	}
	return false
}
