package notes

import (
	"strings"
	"testing"
)

func TestUpdateReplaceDelete(t *testing.T) {
	b := Open(t.TempDir(), "/work/project")
	if v := b.Version(); v != "" {
		t.Fatalf("empty notes have version %q", v)
	}
	if err := b.Update("Build", "run `just test`", "qa_engineer"); err != nil {
		t.Fatal(err)
	}
	if err := b.Update("Architecture", "cmd/ + internal/\n## sneaky heading", "tech_lead"); err != nil {
		t.Fatal(err)
	}
	v1 := b.Version()
	if err := b.Update("build", "run `just check`", "sre"); err != nil {
		t.Fatal(err)
	}
	text, _ := b.Read()
	if strings.Contains(text, "just test") || !strings.Contains(text, "just check") || !strings.Contains(text, "_— sre,") {
		t.Fatalf("section not replaced:\n%s", text)
	}
	if strings.Count(text, "\n## ") != 2 || !strings.Contains(text, "### sneaky heading") {
		t.Fatalf("headings in content must not create sections:\n%s", text)
	}
	if b.Version() == v1 {
		t.Fatal("version did not change")
	}
	if err := b.Update("Build", "", "sre"); err != nil {
		t.Fatal(err)
	}
	if err := b.Update("Architecture", "", "sre"); err != nil {
		t.Fatal(err)
	}
	if text, _ := b.Read(); text != "" {
		t.Fatalf("notes should be gone, got %q", text)
	}
	if err := b.Update("Nope", "", "sre"); err == nil {
		t.Fatal("deleting a missing section should fail")
	}
}

func TestLimits(t *testing.T) {
	b := Open(t.TempDir(), "/w")
	if err := b.Update("x", strings.Repeat("a", MaxBytes), "me"); err == nil {
		t.Fatal("oversized notes accepted")
	}
	if err := b.Update("a\nb", "c", "me"); err == nil {
		t.Fatal("multi-line section name accepted")
	}
	if Open("/d", "/w").Path() == Open("/d", "/w2").Path() {
		t.Fatal("different workspaces share notes")
	}
}
