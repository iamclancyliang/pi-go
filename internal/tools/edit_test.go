package tools_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/tools"
)

func readBack(t *testing.T, dir, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("reading back %s: %v", name, err)
	}
	return string(raw)
}

// TestEveryEditIsMatchedAgainstTheOriginalFile is the rule the whole design
// rests on.
//
// Applying edits one after another to a mutating buffer passes the obvious
// tests and diverges the moment two of them are near each other: here the
// second edit's oldText exists in the original file but not in the result of
// the first, so a sequential implementation reports it as missing. Both must
// land.
func TestEveryEditIsMatchedAgainstTheOriginalFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "alpha\nbeta\ngamma\n")

	call(t, &tools.Edit{Root: dir}, `{"path":"a.go","edits":[
		{"oldText":"alpha\nbeta","newText":"ALPHA\nBETA"},
		{"oldText":"gamma","newText":"GAMMA"}
	]}`)

	if got := readBack(t, dir, "a.go"); got != "ALPHA\nBETA\nGAMMA\n" {
		t.Fatalf("the file holds %q", got)
	}
}

// TestEditsApplyBackToFrontSoOffsetsHold. A replacement that changes length
// moves everything after it, so applying forwards would land later edits at the
// wrong place — subtly, and only when the lengths differ.
func TestEditsApplyBackToFrontSoOffsetsHold(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "one two three four\n")

	call(t, &tools.Edit{Root: dir}, `{"path":"a.txt","edits":[
		{"oldText":"one","newText":"a-much-longer-replacement"},
		{"oldText":"four","newText":"IV"}
	]}`)

	if got := readBack(t, dir, "a.txt"); got != "a-much-longer-replacement two three IV\n" {
		t.Fatalf("the file holds %q", got)
	}
}

// TestAmbiguousTextIsRefusedRatherThanGuessed. Text appearing twice identifies
// no single region, and editing the first would change somewhere the model
// never looked.
func TestAmbiguousTextIsRefusedRatherThanGuessed(t *testing.T) {
	dir := t.TempDir()
	const before = "x = 1\ny = 2\nx = 1\n"
	writeFile(t, dir, "a.txt", before)

	_, err := (&tools.Edit{Root: dir}).Call(context.Background(),
		`{"path":"a.txt","edits":[{"oldText":"x = 1","newText":"x = 9"}]}`)
	if err == nil {
		t.Fatal("an ambiguous edit was applied")
	}
	if !strings.Contains(err.Error(), "2 occurrences") {
		t.Fatalf("the failure does not say how ambiguous it was: %v", err)
	}
	if got := readBack(t, dir, "a.txt"); got != before {
		t.Fatalf("a refused edit still changed the file: %q", got)
	}
}

// TestOverlappingEditsAreRefused, naming both so the model knows which two to
// merge.
func TestOverlappingEditsAreRefused(t *testing.T) {
	dir := t.TempDir()
	const before = "alpha beta gamma\n"
	writeFile(t, dir, "a.txt", before)

	_, err := (&tools.Edit{Root: dir}).Call(context.Background(), `{"path":"a.txt","edits":[
		{"oldText":"alpha beta","newText":"X"},
		{"oldText":"beta gamma","newText":"Y"}
	]}`)
	if err == nil {
		t.Fatal("overlapping edits were applied")
	}
	if !strings.Contains(err.Error(), "edits[0] and edits[1] overlap") {
		t.Fatalf("the failure does not name both edits: %v", err)
	}
	if got := readBack(t, dir, "a.txt"); got != before {
		t.Fatalf("a refused edit still changed the file: %q", got)
	}
}

// TestNothingIsWrittenWhenAnyEditFails. A partially applied call leaves the
// file in a state the model never asked for and cannot predict.
func TestNothingIsWrittenWhenAnyEditFails(t *testing.T) {
	dir := t.TempDir()
	const before = "keep\nchange me\n"
	writeFile(t, dir, "a.txt", before)

	_, err := (&tools.Edit{Root: dir}).Call(context.Background(), `{"path":"a.txt","edits":[
		{"oldText":"change me","newText":"changed"},
		{"oldText":"absent","newText":"whatever"}
	]}`)
	if err == nil {
		t.Fatal("an edit naming missing text succeeded")
	}
	if got := readBack(t, dir, "a.txt"); got != before {
		t.Fatalf("the first edit was applied even though the second failed: %q", got)
	}
}

// TestAnEmptyOldTextIsRefused: it matches at every position at once, so
// replacing it has no single meaning.
func TestAnEmptyOldTextIsRefused(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "content\n")
	_, err := (&tools.Edit{Root: dir}).Call(context.Background(),
		`{"path":"a.txt","edits":[{"oldText":"","newText":"x"}]}`)
	if err == nil {
		t.Fatal("an empty oldText was accepted")
	}
	if !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("the failure does not say what was wrong: %v", err)
	}
}

// TestAnEditThatChangesNothingIsReported. Writing it would tell the model its
// change landed when the file is exactly as it was.
func TestAnEditThatChangesNothingIsReported(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "same\n")
	_, err := (&tools.Edit{Root: dir}).Call(context.Background(),
		`{"path":"a.txt","edits":[{"oldText":"same","newText":"same"}]}`)
	if err == nil {
		t.Fatal("an edit that changed nothing reported success")
	}
	if !strings.Contains(err.Error(), "no changes made") {
		t.Fatalf("the failure does not say what happened: %v", err)
	}
}

// TestWindowsLineEndingsSurviveAnEdit. Rewriting a CRLF file with LF endings
// would show as every line changed in the user's next diff.
func TestWindowsLineEndingsSurviveAnEdit(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "one\r\ntwo\r\nthree\r\n")

	// The model writes LF, because that is what it saw through read.
	call(t, &tools.Edit{Root: dir}, `{"path":"a.txt","edits":[{"oldText":"two","newText":"TWO"}]}`)

	got := readBack(t, dir, "a.txt")
	if got != "one\r\nTWO\r\nthree\r\n" {
		t.Fatalf("the file holds %q", got)
	}
}

// TestAByteOrderMarkSurvivesAnEdit: a model will not have included an invisible
// character in oldText, and dropping it rewrites the file's encoding as a side
// effect of an unrelated change.
func TestAByteOrderMarkSurvivesAnEdit(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "\ufeffone\ntwo\n")

	call(t, &tools.Edit{Root: dir}, `{"path":"a.txt","edits":[{"oldText":"one","newText":"ONE"}]}`)

	got := readBack(t, dir, "a.txt")
	if !strings.HasPrefix(got, "\ufeff") {
		t.Fatalf("the byte-order mark was dropped: %q", got)
	}
	if got != "\ufeffONE\ntwo\n" {
		t.Fatalf("the file holds %q", got)
	}
}

// TestEditingIsNotOfferedAsReadOnlyOrRepeatable, which the policy seam and the
// crash recovery both branch on.
func TestEditingIsNotOfferedAsReadOnlyOrRepeatable(t *testing.T) {
	got := (&tools.Edit{Root: t.TempDir()}).Execution()
	if got.ReadOnly {
		t.Fatal("edit declares itself read-only, and a policy denying mutation would let it through")
	}
	if got.Replay != tools.ReplayNever {
		t.Fatal("edit declares itself repeatable")
	}
}

// TestEditRegisters proves the nested array-of-objects schema — the most
// complex one the built-in set declares — survives the registry's check.
func TestEditRegisters(t *testing.T) {
	if err := tools.NewRegistry().Register(&tools.Edit{Root: t.TempDir()}); err != nil {
		t.Fatalf("edit did not register: %v", err)
	}
}
