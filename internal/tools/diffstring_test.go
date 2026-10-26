package tools

import (
	"encoding/json"
	"os"
	"testing"
)

// TestDiffStringMatchesPi checks DiffString against Pi's generateDiffString
// (testdata/diff/generate.ts).
func TestDiffStringMatchesPi(t *testing.T) {
	data, err := os.ReadFile("testdata/diff/golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Old, New, Diff string
		First          int
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		diff, first := DiffString(c.Old, c.New, 4)
		if diff != c.Diff || first != c.First {
			t.Errorf("DiffString(%q, %q)\n got %q (first %d)\nwant %q (first %d)", c.Old, c.New, diff, first, c.Diff, c.First)
		}
	}
}

func TestEditReportsItsDiffAndPreviewMatches(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/f.txt"
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	args := `{"path":"f.txt","edits":[{"oldText":"two","newText":"2"}]}`
	preview, err := PreviewEdit(dir, args)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "one\ntwo\nthree\n" {
		t.Fatal("a preview wrote the file")
	}
	res, err := (&Edit{Root: dir}).Call(t.Context(), args)
	if err != nil {
		t.Fatal(err)
	}
	details, ok := res.Details.(EditDetails)
	if !ok || details != preview || details.FirstChangedLine != 2 || details.Diff != " 1 one\n-2 two\n+2 2\n 3 three" {
		t.Fatalf("details %#v, preview %#v", res.Details, preview)
	}
}
