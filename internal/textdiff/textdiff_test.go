package textdiff

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// TestMatchesJSDiff checks line and word diffs against jsdiff 8.0.4's own
// output (testdata/generate.ts).
func TestMatchesJSDiff(t *testing.T) {
	data, err := os.ReadFile("testdata/golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Old   string   `json:"old"`
		New   string   `json:"new"`
		Lines []Change `json:"lines"`
		Words []Change `json:"words"`
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		if got := Lines(c.Old, c.New); !sameChanges(got, c.Lines) {
			t.Errorf("Lines(%q, %q)\n got %+v\nwant %+v", c.Old, c.New, got, c.Lines)
		}
		if got := Words(c.Old, c.New); !sameChanges(got, c.Words) {
			t.Errorf("Words(%q, %q)\n got %+v\nwant %+v", c.Old, c.New, got, c.Words)
		}
	}
}

func sameChanges(a, b []Change) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}
