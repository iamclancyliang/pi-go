package tui

import (
	"slices"
	"strings"
	"testing"
)

// These follow Pi's fuzzy.test.ts and select-list.test.ts at the pin.

func TestFuzzyMatch(t *testing.T) {
	if ok, score := FuzzyMatch("", "anything"); !ok || score != 0 {
		t.Fatalf("empty query: %v %v", ok, score)
	}
	if ok, _ := FuzzyMatch("longquery", "short"); ok {
		t.Fatal("a query longer than the text matched")
	}
	if ok, score := FuzzyMatch("test", "test"); !ok || score >= 0 {
		t.Fatalf("exact match: %v %v", ok, score)
	}
	if ok, _ := FuzzyMatch("abc", "aXbXc"); !ok {
		t.Fatal("in-order characters did not match")
	}
	if ok, _ := FuzzyMatch("abc", "cba"); ok {
		t.Fatal("out-of-order characters matched")
	}
	if ok, _ := FuzzyMatch("ABC", "abc"); !ok {
		t.Fatal("case mattered")
	}
	if ok, _ := FuzzyMatch("abc", "ABC"); !ok {
		t.Fatal("case mattered")
	}
	_, consecutive := FuzzyMatch("foo", "foobar")
	_, scattered := FuzzyMatch("foo", "f_o_o_bar")
	if consecutive >= scattered {
		t.Fatalf("consecutive %v not better than scattered %v", consecutive, scattered)
	}
	_, boundary := FuzzyMatch("fb", "foo-bar")
	_, inside := FuzzyMatch("fb", "afbx")
	if boundary >= inside {
		t.Fatalf("word boundary %v not better than inside %v", boundary, inside)
	}
	if ok, _ := FuzzyMatch("codex52", "gpt-5.2-codex"); !ok {
		t.Fatal("swapped letters and digits did not match")
	}
}

func TestFuzzyFilter(t *testing.T) {
	id := func(s string) string { return s }
	items := []string{"apple", "banana", "cherry"}
	if got := FuzzyFilter(items, "", id); !slices.Equal(got, items) {
		t.Fatalf("empty query: %q", got)
	}
	if got := FuzzyFilter(items, "an", id); !slices.Equal(got, []string{"banana"}) {
		t.Fatalf("filter: %q", got)
	}
	if got := FuzzyFilter([]string{"a_p_p", "app", "application"}, "app", id); got[0] != "app" {
		t.Fatalf("sort: %q", got)
	}
	if got := FuzzyFilter([]string{"clone", "cl"}, "cl", id); !slices.Equal(got, []string{"cl", "clone"}) {
		t.Fatalf("exact first: %q", got)
	}
	type model struct{ id, provider string }
	m := model{"gpt-5.5", "openai-codex"}
	if got := FuzzyFilter([]model{m}, "openai-codex/gpt-5.5", func(m model) string { return m.id + " " + m.provider }); len(got) != 1 {
		t.Fatal("slash-separated tokens did not match reordered text")
	}
}

func visibleIndexOf(t *testing.T, line, text string) int {
	t.Helper()
	i := strings.Index(line, text)
	if i < 0 {
		t.Fatalf("%q not in %q", text, line)
	}
	return VisibleWidth(line[:i])
}

func TestSelectListLayout(t *testing.T) {
	list := NewSelectList([]SelectItem{{Value: "test", Label: "test", Description: "Line one\nLine two\nLine three"}}, 5, SelectListTheme{}, SelectListLayout{})
	if got := list.Render(100)[0]; strings.Contains(got, "\n") || !strings.Contains(got, "Line one Line two Line three") {
		t.Fatalf("description %q", got)
	}

	long := "very-long-command-name-that-needs-truncation"
	list = NewSelectList([]SelectItem{
		{Value: "short", Label: "short", Description: "short description"},
		{Value: long, Label: long, Description: "long description"},
	}, 5, SelectListTheme{}, SelectListLayout{})
	out := list.Render(80)
	if visibleIndexOf(t, out[0], "short description") != visibleIndexOf(t, out[1], "long description") {
		t.Fatalf("descriptions not aligned:\n%s", strings.Join(out, "\n"))
	}

	list = NewSelectList([]SelectItem{{Value: "a", Label: "a", Description: "first"}, {Value: "bb", Label: "bb", Description: "second"}},
		5, SelectListTheme{}, SelectListLayout{MinPrimaryColumnWidth: 12, MaxPrimaryColumnWidth: 20})
	out = list.Render(80)
	if visibleIndexOf(t, out[0], "first") != 14 || visibleIndexOf(t, out[1], "second") != 14 {
		t.Fatalf("minimum column:\n%s", strings.Join(out, "\n"))
	}

	list = NewSelectList([]SelectItem{{Value: long, Label: long, Description: "first"}, {Value: "short", Label: "short", Description: "second"}},
		5, SelectListTheme{}, SelectListLayout{MinPrimaryColumnWidth: 12, MaxPrimaryColumnWidth: 20})
	out = list.Render(80)
	if visibleIndexOf(t, out[0], "first") != 22 || visibleIndexOf(t, out[1], "second") != 22 {
		t.Fatalf("maximum column:\n%s", strings.Join(out, "\n"))
	}

	list = NewSelectList([]SelectItem{{Value: long, Label: long, Description: "first"}, {Value: "short", Label: "short", Description: "second"}},
		5, SelectListTheme{}, SelectListLayout{MinPrimaryColumnWidth: 12, MaxPrimaryColumnWidth: 12,
			TruncatePrimary: func(c SelectListTruncateContext) string {
				if len(c.Text) <= c.MaxWidth {
					return c.Text
				}
				return c.Text[:max(0, c.MaxWidth-1)] + "…"
			}})
	out = list.Render(80)
	if !strings.Contains(out[0], "…") || visibleIndexOf(t, out[0], "first") != visibleIndexOf(t, out[1], "second") {
		t.Fatalf("custom truncation:\n%s", strings.Join(out, "\n"))
	}
}

func TestSelectListScrollsAndWraps(t *testing.T) {
	var items []SelectItem
	for _, v := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		items = append(items, SelectItem{Value: v, Label: v})
	}
	list := NewSelectList(items, 3, SelectListTheme{}, SelectListLayout{})
	var changed []string
	list.OnSelectionChange = func(i SelectItem) { changed = append(changed, i.Value) }
	list.HandleKey(Key{Name: "up"})
	if got, _ := list.SelectedItem(); got.Value != "g" {
		t.Fatalf("up from the top selected %q", got.Value)
	}
	out := list.Render(40)
	if len(out) != 4 || !strings.HasPrefix(out[2], "→ g") || out[3] != "  (7/7)" {
		t.Fatalf("render %q", out)
	}
	list.HandleKey(Key{Name: "down"})
	if got, _ := list.SelectedItem(); got.Value != "a" || !slices.Equal(changed, []string{"g", "a"}) {
		t.Fatalf("down from the bottom selected %q (changes %q)", got.Value, changed)
	}
	var selected string
	cancelled := false
	list.OnSelect = func(i SelectItem) { selected = i.Value }
	list.OnCancel = func() { cancelled = true }
	list.HandleKey(Key{Name: "enter"})
	list.HandleKey(Key{Name: "escape"})
	if selected != "a" || !cancelled {
		t.Fatalf("selected %q cancelled %v", selected, cancelled)
	}
	list.SetFilter("zz")
	if got := list.Render(40); len(got) != 1 || got[0] != "  No matching commands" {
		t.Fatalf("empty filter %q", got)
	}
}
