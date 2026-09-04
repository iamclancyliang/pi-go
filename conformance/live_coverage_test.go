package conformance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEveryProviderPortHasALiveTest.
//
// Two ports reached the parity matrix as `compatible` while nothing in the tree
// could ever reach their provider: the offline suite ran against a recorded
// wire, and a recorded wire only proves the port reads what was recorded. The
// ports that say plainly they are unverified were in a better position — each
// carried a written test that runs the moment a credential appears.
//
// So this is the obligation rather than the habit: a port ships with a live
// test, gated by its own variable, skipping until someone consents to spend.
// Writing it costs nothing; it is what turns "we never checked" from something
// a person has to remember into something the repository states.
//
// This asserts the test EXISTS and is gated. It cannot assert the provider was
// reached — only a credential can do that, and the parity matrix records which
// ports have been.
func TestEveryProviderPortHasALiveTest(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("..", "internal", "provider"))
	if err != nil {
		t.Fatalf("reading the provider packages: %v", err)
	}

	ports := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		// The shared chat-completions dialect is not a provider: it reaches
		// nobody by itself, and the ports built on it carry the live tests.
		if name == "chatcompletions" {
			continue
		}
		ports++

		path := filepath.Join(name + "_live_test.go")
		source, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("the %s port has no live test (%s): a port nobody can point at its provider "+
				"is unverified, and saying so in a file costs nothing", name, path)
			continue
		}
		gate := "PI_GO_LIVE_" + strings.ToUpper(name)
		if !strings.Contains(string(source), gate) {
			t.Errorf("%s does not name the gate %s: a live test that runs without consent "+
				"spends a person's money on `go test ./...`", path, gate)
		}
		if !strings.Contains(string(source), "t.Skip") {
			t.Errorf("%s never skips: without a gate that skips, CI reaches the provider", path)
		}
	}

	if ports == 0 {
		t.Fatal("no provider packages were found, so this test asserted nothing")
	}
	if !t.Failed() {
		t.Logf("%d provider ports, each with a gated live test", ports)
	}
}
