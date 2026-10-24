package session_test

import (
	"context"
	"testing"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/session"
)

// TestTheModelAndThinkingLevelInUseAreRecordedAndRestored, as Pi records them
// in model_change and thinking_level_change entries: the latest on the path
// is what a resumed conversation was last using.
func TestTheModelAndThinkingLevelInUseAreRecordedAndRestored(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	live := session.WithStore("sys", store)

	if err := live.RecordModel("deepseek", "deepseek-chat"); err != nil {
		t.Fatal(err)
	}
	if err := live.RecordThinking(ai.ThinkingHigh); err != nil {
		t.Fatal(err)
	}
	if err := live.Append(ai.Message{Role: ai.RoleUser, Content: "q"}); err != nil {
		t.Fatal(err)
	}
	if err := live.RecordModel("openai", "gpt-test"); err != nil {
		t.Fatal(err)
	}
	if err := live.RecordThinking(ai.ThinkingLow); err != nil {
		t.Fatal(err)
	}

	restored, err := session.Restore(context.Background(), "sys", store)
	if err != nil {
		t.Fatal(err)
	}
	for name, s := range map[string]*session.Session{"live": live, "restored": restored} {
		provider, model, ok := s.RecordedModel()
		if !ok || provider != "openai" || model != "gpt-test" {
			t.Errorf("%s: recorded model %s/%s (%v)", name, provider, model, ok)
		}
		if level, ok := s.RecordedThinking(); !ok || level != ai.ThinkingLow {
			t.Errorf("%s: recorded thinking %q (%v)", name, level, ok)
		}
	}
}

// TestSettingsRecordsAreNotPartOfTheConversation: the model never sees them,
// and a session holding nothing else is still an empty one.
func TestSettingsRecordsAreNotPartOfTheConversation(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	s := session.WithStore("sys", store)
	if err := s.RecordModel("deepseek", "deepseek-chat"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordThinking(ai.ThinkingMedium); err != nil {
		t.Fatal(err)
	}
	if got := s.Project().Messages; len(got) != 1 {
		t.Fatalf("the projection carries %d messages, want only the system prompt", len(got))
	}
	info, err := session.Describe(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if info.Entries != 0 {
		t.Fatalf("a session with only settings records lists %d entries, so --continue would resume it", info.Entries)
	}
}

// TestNothingRecordedSaysSo rather than answering with empty values a caller
// would apply.
func TestNothingRecordedSaysSo(t *testing.T) {
	s := session.New("sys")
	if _, _, ok := s.RecordedModel(); ok {
		t.Fatal("a fresh session reports a recorded model")
	}
	if _, ok := s.RecordedThinking(); ok {
		t.Fatal("a fresh session reports a recorded thinking level")
	}
}
