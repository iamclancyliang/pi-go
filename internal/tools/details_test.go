package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The details a tool reports beside its text, which the interface shows as
// warnings under the output.

func detailsDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("match one\nmatch two\n"+strings.Repeat("x", 600)+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestToolsReportWhatLimitedThem(t *testing.T) {
	dir := detailsDir(t)
	ctx := t.Context()

	res, err := (&Grep{Root: dir}).Call(ctx, `{"pattern":"match|x","limit":2}`)
	if err != nil {
		t.Fatal(err)
	}
	if d, ok := res.Details.(GrepDetails); !ok || d.MatchLimitReached != 2 || d.Truncation != nil {
		t.Fatalf("grep details %#v", res.Details)
	}
	res, err = (&Grep{Root: dir}).Call(ctx, `{"pattern":"x{600}"}`)
	if err != nil {
		t.Fatal(err)
	}
	if d, ok := res.Details.(GrepDetails); !ok || !d.LinesTruncated {
		t.Fatalf("grep long-line details %#v", res.Details)
	}

	res, err = (&Find{Root: dir}).Call(ctx, `{"pattern":"*.txt","limit":1}`)
	if err != nil {
		t.Fatal(err)
	}
	if d, ok := res.Details.(FindDetails); !ok || d.ResultLimitReached != 1 {
		t.Fatalf("find details %#v", res.Details)
	}

	res, err = (&Ls{Root: dir}).Call(ctx, `{"limit":1}`)
	if err != nil {
		t.Fatal(err)
	}
	if d, ok := res.Details.(LsDetails); !ok || d.EntryLimitReached != 1 {
		t.Fatalf("ls details %#v", res.Details)
	}

	res, err = (&Read{Root: dir, Limits: Limits{MaxLines: 1}}).Call(ctx, `{"path":"a.txt"}`)
	if err != nil {
		t.Fatal(err)
	}
	if d, ok := res.Details.(ReadDetails); !ok || d.Truncation == nil || d.Truncation.By != TruncatedByLines {
		t.Fatalf("read details %#v", res.Details)
	}

	res, err = (&Bash{Dir: dir, Limits: Limits{MaxLines: 2}}).Call(ctx, `{"command":"seq 1 10"}`)
	if err != nil {
		t.Fatal(err)
	}
	if d, ok := res.Details.(BashDetails); !ok || d.Truncation == nil || d.Truncation.OutputLines != 2 || d.FullOutputPath == "" {
		t.Fatalf("bash details %#v", res.Details)
	}

	// Nothing limited, nothing reported.
	for _, r := range []Result{
		must(t)((&Grep{Root: dir}).Call(ctx, `{"pattern":"one"}`)),
		must(t)((&Read{Root: dir}).Call(ctx, `{"path":"a.txt"}`)),
		must(t)((&Bash{Dir: dir}).Call(ctx, `{"command":"echo hi"}`)),
	} {
		if r.Details != nil {
			t.Fatalf("details without a limit: %#v", r.Details)
		}
	}
}

func must(t *testing.T) func(Result, error) Result {
	return func(r Result, err error) Result {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
}
