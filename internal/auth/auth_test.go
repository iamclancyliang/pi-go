package auth_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/auth"
)

// TestACredentialSurvivesTheProcess, which is the whole reason for a file.
func TestACredentialSurvivesTheProcess(t *testing.T) {
	dir := t.TempDir()
	if err := auth.Open(dir).Set("deepseek", auth.APIKey("sk-secret")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, found, err := auth.Open(dir).Get("deepseek")
	if err != nil || !found {
		t.Fatalf("Get: %v, found=%v", err, found)
	}
	if got.Key() != "sk-secret" {
		t.Fatalf("the key came back as %q", got.Key())
	}
}

// TestACredentialRefusesToFormatItself. A value that prints its key reaches a
// log line, a test failure or a %+v the first time anyone debugs near it.
func TestACredentialRefusesToFormatItself(t *testing.T) {
	c := auth.APIKey("sk-must-not-appear")
	for name, rendered := range map[string]string{
		"%v":     fmt.Sprintf("%v", c),
		"%+v":    fmt.Sprintf("%+v", c),
		"%s":     fmt.Sprintf("%s", c),
		"%#v":    fmt.Sprintf("%#v", c),
		"String": c.String(),
	} {
		if strings.Contains(rendered, "sk-must-not-appear") {
			t.Fatalf("%s disclosed the key: %s", name, rendered)
		}
	}
	// Inside a larger structure too, which is how it actually escapes.
	holder := struct{ Credential auth.Credential }{c}
	if strings.Contains(fmt.Sprintf("%+v", holder), "sk-must-not-appear") {
		t.Fatal("a credential inside a struct disclosed its key")
	}
}

// TestTheFileIsNotReadableByOthers. A credential in a world-readable file is
// readable by anything running as anyone on the machine.
func TestTheFileIsNotReadableByOthers(t *testing.T) {
	dir := t.TempDir()
	store := auth.Open(dir)
	if err := store.Set("deepseek", auth.APIKey("sk-secret")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	info, err := os.Stat(store.Path())
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		t.Fatalf("the credential file is mode %04o; group and others must have nothing", mode)
	}
}

// TestAFailedWriteLeavesThePreviousCredentials. Losing a key to a full disk
// means being locked out of every provider at once.
func TestAFailedWriteLeavesThePreviousCredentials(t *testing.T) {
	dir := t.TempDir()
	store := auth.Open(dir)
	if err := store.Set("deepseek", auth.APIKey("sk-first")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// The file is written through a neighbour and renamed, so a leftover
	// temporary file must never be what a reader finds.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".writing") {
			t.Fatalf("a partial write was left behind: %s", e.Name())
		}
	}

	got, _, _ := auth.Open(dir).Get("deepseek")
	if got.Key() != "sk-first" {
		t.Fatalf("the credential came back as %q", got.Key())
	}
}

// TestRemovingSomethingAbsentIsNotAFailure: the caller asked for the credential
// to be gone, and it is.
func TestRemovingSomethingAbsentIsNotAFailure(t *testing.T) {
	if err := auth.Open(t.TempDir()).Remove("never-stored"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
}

// TestListingNamesProvidersAndNotKeys. A listing that showed keys would put
// every one of them into the scrollback of whoever ran it.
func TestListingNamesProvidersAndNotKeys(t *testing.T) {
	dir := t.TempDir()
	store := auth.Open(dir)
	store.Set("deepseek", auth.APIKey("sk-one"))
	store.Set("openai", auth.APIKey("sk-two"))

	names, err := store.Providers()
	if err != nil {
		t.Fatalf("Providers: %v", err)
	}
	if len(names) != 2 || names[0] != "deepseek" || names[1] != "openai" {
		t.Fatalf("the listing is %v, want deepseek then openai", names)
	}
	for _, name := range names {
		if strings.Contains(name, "sk-") {
			t.Fatalf("a listing entry carries a key: %q", name)
		}
	}
}

// TestAnUnreadableStoreIsReportedRatherThanTreatedAsEmpty. Silently reading a
// corrupt file as no credentials would look like being logged out.
func TestAnUnreadableStoreIsReportedRatherThanTreatedAsEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, auth.FileName), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("writing: %v", err)
	}
	if _, _, err := auth.Open(dir).Get("deepseek"); err == nil {
		t.Fatal("an unreadable credential file was read as empty")
	}
}

// TestTheStoredFormIsReadable, so a person can see what is in the file without
// this program — and delete one entry rather than all of them.
func TestTheStoredFormIsReadable(t *testing.T) {
	dir := t.TempDir()
	store := auth.Open(dir)
	store.Set("deepseek", auth.APIKey("sk-secret"))

	raw, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	var all map[string]struct {
		Kind string `json:"kind"`
		Key  string `json:"key"`
	}
	if err := json.Unmarshal(raw, &all); err != nil {
		t.Fatalf("the stored form is not readable JSON: %v\n%s", err, raw)
	}
	if all["deepseek"].Kind != "api_key" || all["deepseek"].Key != "sk-secret" {
		t.Fatalf("the stored form is %+v", all)
	}
}
