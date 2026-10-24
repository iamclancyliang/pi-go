package tools_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/tools"
)

// editFile runs one edit call against name in dir, each pair being
// {oldText, newText}. The arguments are marshalled rather than written as a
// literal so the Unicode these tests are about reaches the tool unescaped.
func editFile(t *testing.T, dir, name string, pairs ...[2]string) (string, error) {
	t.Helper()
	type pair struct {
		OldText string `json:"oldText"`
		NewText string `json:"newText"`
	}
	in := struct {
		Path  string `json:"path"`
		Edits []pair `json:"edits"`
	}{Path: name}
	for _, p := range pairs {
		in.Edits = append(in.Edits, pair{OldText: p[0], NewText: p[1]})
	}
	args, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	res, err := (&tools.Edit{Root: dir}).Call(context.Background(), string(args))
	return res.Content, err
}

func mustEdit(t *testing.T, dir, name string, pairs ...[2]string) {
	t.Helper()
	if _, err := editFile(t, dir, name, pairs...); err != nil {
		t.Fatalf("edit failed: %v", err)
	}
}

// TestAFuzzyEditLeavesEveryUnchangedLineByteIdentical is the property fuzzy
// matching rests on. Matching happens in a normalised copy of the file, with
// trailing whitespace gone; writing that copy back would silently strip the
// whitespace from every line in the file, including the ones nobody edited.
// Only the lines a replacement touches may come from the normalised copy.
func TestAFuzzyEditLeavesEveryUnchangedLineByteIdentical(t *testing.T) {
	dir := t.TempDir()
	before := strings.Join([]string{
		"keep before  ",
		"first target  ",
		"first after",
		"keep middle   ",
		"second target  ",
		"second after",
		"keep after  ",
		"",
	}, "\n")
	writeFile(t, dir, "a.txt", before)

	mustEdit(t, dir, "a.txt",
		[2]string{"first target\nfirst after", "FIRST\nFIRST2"},
		[2]string{"second target\nsecond after", "SECOND\nSECOND2"},
	)

	want := strings.Join([]string{
		"keep before  ",
		"FIRST",
		"FIRST2",
		"keep middle   ",
		"SECOND",
		"SECOND2",
		"keep after  ",
		"",
	}, "\n")
	if got := readBack(t, dir, "a.txt"); got != want {
		t.Fatalf("the file holds %q, want %q", got, want)
	}
}

// TestAFuzzyEditPreservesTheOccurrenceItDidNotTouch. The replacement here is
// identical, once normalised, to the line after it. Preservation decided by
// comparing normalised lines would then align the untouched line with the
// wrong one and lose its whitespace; deciding it from the replacement's own
// range does not.
func TestAFuzzyEditPreservesTheOccurrenceItDidNotTouch(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "replace me   \nafter   \n")

	mustEdit(t, dir, "a.txt", [2]string{"replace me\n", "after\n"})

	if got := readBack(t, dir, "a.txt"); got != "after\nafter   \n" {
		t.Fatalf("the file holds %q", got)
	}
}

// TestFuzzyMatching holds the normalisations, each from Pi's own test of the
// same name, so a model that writes what it would naturally type still lands
// on the text a file actually holds.
func TestFuzzyMatching(t *testing.T) {
	cases := []struct {
		name    string
		file    string
		oldText string
		newText string
		want    string
	}{
		{
			name:    "trailing whitespace is ignored",
			file:    "line one   \nline two  \nline three\n",
			oldText: "line one\nline two\n",
			newText: "replaced\n",
			want:    "replaced\nline three\n",
		},
		{
			name:    "fullwidth punctuation matches its ASCII form",
			file:    "你好，世界\n你好（世界）\n",
			oldText: "你好,世界\n你好(世界)\n",
			newText: "你好，pi\n你好(pi)\n",
			want:    "你好，pi\n你好(pi)\n",
		},
		{
			name:    "compatibility-equivalent forms match",
			file:    "ＡＢＣ１２３\ncafe\u0301\n",
			oldText: "ABC123\ncafé\n",
			newText: "XYZ789\ncoffee\n",
			want:    "XYZ789\ncoffee\n",
		},
		{
			name:    "smart single quotes match ASCII ones",
			file:    "console.log(\u2018hello\u2019);\n",
			oldText: "console.log('hello');",
			newText: "console.log('world');",
			want:    "console.log('world');\n",
		},
		{
			name:    "smart double quotes match ASCII ones",
			file:    "const msg = \u201cHello World\u201d;\n",
			oldText: `const msg = "Hello World";`,
			newText: `const msg = "Goodbye";`,
			want:    "const msg = \"Goodbye\";\n",
		},
		{
			name:    "Unicode dashes match an ASCII hyphen",
			file:    "range: 1\u20135\nbreak\u2014here\n",
			oldText: "range: 1-5\nbreak-here",
			newText: "range: 10-50\nbreak--here",
			want:    "range: 10-50\nbreak--here\n",
		},
		{
			name:    "a non-breaking space matches a space",
			file:    "hello\u00a0world\n",
			oldText: "hello world",
			newText: "hello universe",
			want:    "hello universe\n",
		},
		{
			name:    "an exact match is used as it stands",
			file:    "const x = 'exact';\nconst y = 'other';\n",
			oldText: "const x = 'exact';",
			newText: "const x = 'changed';",
			want:    "const x = 'changed';\nconst y = 'other';\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, dir, "a.txt", c.file)

			out, err := editFile(t, dir, "a.txt", [2]string{c.oldText, c.newText})
			if err != nil {
				t.Fatalf("edit failed: %v", err)
			}
			if !strings.Contains(out, "Successfully replaced") {
				t.Fatalf("the result does not report success: %q", out)
			}
			if got := readBack(t, dir, "a.txt"); got != c.want {
				t.Fatalf("the file holds %q, want %q", got, c.want)
			}
		})
	}
}

// TestFuzzyMatchingStillFailsOnAbsentText: normalising is not a licence to
// match something merely similar.
func TestFuzzyMatchingStillFailsOnAbsentText(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "completely different content\n")

	_, err := editFile(t, dir, "a.txt", [2]string{"this does not exist", "replacement"})
	if err == nil || !strings.Contains(err.Error(), "could not find the exact text") {
		t.Fatalf("absent text was not reported as missing: %v", err)
	}
}

// TestUniquenessIsJudgedAfterNormalising. The exact text appears once here,
// but a second line differs from it only by its quotes; an edit that claims to
// be unique while a near-identical line sits beside it is the ambiguity the
// uniqueness rule exists to refuse.
func TestUniquenessIsJudgedAfterNormalising(t *testing.T) {
	dir := t.TempDir()
	const before = "say \u201chi\u201d\nsay \"hi\"\n"
	writeFile(t, dir, "a.txt", before)

	_, err := editFile(t, dir, "a.txt", [2]string{`say "hi"`, "replaced"})
	if err == nil || !strings.Contains(err.Error(), "found 2 occurrences") {
		t.Fatalf("near-duplicates were not refused as ambiguous: %v", err)
	}
	if got := readBack(t, dir, "a.txt"); got != before {
		t.Fatalf("a refused edit still changed the file: %q", got)
	}
}

// TestFuzzyMatchingWorksAcrossSeveralEdits. One edit needing normalisation
// moves every edit in the call into normalised space, so they are still
// matched against one coherent view of the original.
func TestFuzzyMatchingWorksAcrossSeveralEdits(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "console.log(\u2018hello\u2019);\nhello\u00a0world\n")

	mustEdit(t, dir, "a.txt",
		[2]string{"console.log('hello');\n", "console.log('world');\n"},
		[2]string{"hello world\n", "hello universe\n"},
	)

	if got := readBack(t, dir, "a.txt"); got != "console.log('world');\nhello universe\n" {
		t.Fatalf("the file holds %q", got)
	}
}

// TestAFuzzyEditKeepsWindowsLineEndingsAndTheByteOrderMark. Line endings and
// the mark are taken off before matching and put back after; fuzzy matching
// sits between the two and must not disturb either.
func TestAFuzzyEditKeepsWindowsLineEndingsAndTheByteOrderMark(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "\ufeffkeep  \r\nsay \u201chi\u201d\r\nkeep too  \r\n")

	mustEdit(t, dir, "a.txt", [2]string{`say "hi"`, `say "bye"`})

	if got := readBack(t, dir, "a.txt"); got != "\ufeffkeep  \r\nsay \"bye\"\r\nkeep too  \r\n" {
		t.Fatalf("the file holds %q", got)
	}
}
