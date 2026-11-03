package task

import (
	"strings"
	"testing"
)

func TestCleanName(t *testing.T) {
	if got, err := CleanName("  Rate   limiting \t"); err != nil || got != "Rate limiting" {
		t.Fatalf("CleanName = %q, %v", got, err)
	}
	for _, bad := range []string{"", "   ", strings.Repeat("x", NameMax+1), "a\x1b[31mb"} {
		if _, err := CleanName(bad); err == nil {
			t.Errorf("CleanName(%q) accepted", bad)
		}
	}
}

func TestDefaultName(t *testing.T) {
	for prompt, want := range map[string]string{
		"Add rate limiting to the API, with tests":                                  "Add rate limiting to the API, with tests",
		"Fix https://github.com/acme/web/issues/12":                                 "Fix",
		"Complete every requirement in https://github.com/example/example/issues/1": "Complete every requirement in",
		"Build a CLI todo app in Go with tests and a README and CI\nmore detail":    "Build a CLI todo app in Go with tests…",
		"https://example.com/only-a-link":                                           "untitled",
	} {
		if got := DefaultName(prompt); got != want {
			t.Errorf("DefaultName(%q) = %q, want %q", prompt, got, want)
		}
	}
}

func TestNamesFindAndSurviveSaves(t *testing.T) {
	s := NewStore(t.TempDir())
	a := &Task{ID: "aaaaaa", Name: "Rate limiting", Prompt: "p"}
	b := &Task{ID: "bbbbbb", Prompt: "Write the README"}
	for _, tk := range []*Task{a, b} {
		if err := s.Create(tk); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Find("rate LIMITING")
	if err != nil || got.ID != a.ID || got.Name != "Rate limiting" {
		t.Fatalf("Find by name = %v, %v", got, err)
	}
	if got, err := s.Find("bbb"); err != nil || got.Title() != "Write the README" {
		t.Fatalf("Find by prefix = %v, %v", got, err)
	}

	// A runner saving its stale copy doesn't undo a rename.
	stale, _ := s.Load(a.ID)
	if err := s.Rename(a, "API limits"); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(stale); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Load(a.ID); got.Name != "API limits" {
		t.Fatalf("name after a stale save = %q", got.Name)
	}

	if err := s.Rename(b, "API limits"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Find("api limits"); err == nil || !strings.Contains(err.Error(), "several") {
		t.Fatalf("an ambiguous name = %v", err)
	}
	if _, err := s.Find("../x"); err == nil {
		t.Fatal("a path was accepted as a task")
	}
}
