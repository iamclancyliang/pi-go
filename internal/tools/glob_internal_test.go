package tools

import "testing"

// TestGlobMatching pins the distinction a model relies on when it writes a
// pattern: `*` stays inside one path segment and `**` crosses them. Confusing
// the two returns either nothing or the whole tree.
func TestGlobMatching(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		// A single star never crosses a separator.
		{"*.go", "main.go", true},
		{"*.go", "src/main.go", false},
		{"src/*.go", "src/main.go", true},
		{"src/*.go", "src/deep/main.go", false},

		// A double star crosses any number of them, including none.
		{"**/*.go", "main.go", true},
		{"**/*.go", "src/deep/main.go", true},
		{"src/**/*_test.go", "src/a/b/x_test.go", true},
		{"src/**/*_test.go", "src/x_test.go", true},
		{"src/**/*_test.go", "other/x_test.go", false},
		{"**", "anything/at/all.txt", true},

		// A trailing double star includes the directory itself.
		{"vendor/**", "vendor/a/b.go", true},

		// Question marks and classes match exactly one character.
		{"?.go", "a.go", true},
		{"?.go", "ab.go", false},
		{"[abc].go", "b.go", true},
		{"[abc].go", "d.go", false},
		{"[a-c].go", "c.go", true},
		{"[!a-c].go", "d.go", true},
		{"[!a-c].go", "a.go", false},

		// Several stars in one segment must not go quadratic or lose track.
		{"*a*b*c", "xxaxxbxxc", true},
		{"*a*b*c", "xxaxxbxx", false},

		// A literal bracket that never closes is a filename, not a class.
		{"[abc.go", "[abc.go", true},

		{"exact.txt", "exact.txt", true},
		{"exact.txt", "other.txt", false},
	}
	for _, c := range cases {
		if got := matchGlob(c.pattern, c.path); got != c.want {
			t.Errorf("matchGlob(%q, %q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}

// TestGlobMatchingTerminates guards the backtracking: a pattern of many stars
// against a long non-matching name is the shape that hangs a naive matcher, and
// a model writes those routinely.
func TestGlobMatchingTerminates(t *testing.T) {
	done := make(chan bool, 1)
	go func() {
		done <- matchGlob("*a*a*a*a*a*a*a*a*b", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaac")
	}()
	select {
	case got := <-done:
		if got {
			t.Fatal("a pattern requiring a trailing b matched a name ending in c")
		}
	case <-timeoutAfterSeconds(5):
		t.Fatal("matching did not finish; the matcher backtracks without bound")
	}
}
