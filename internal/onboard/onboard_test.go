package onboard

import (
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dethmasque/ackn4th/internal/workspace"
)

// apply replays edits onto a and checks the result is b.
func apply(t *testing.T, a, b []string, edits []edit) {
	t.Helper()
	var got []string
	ai := 0
	for _, e := range edits {
		switch e.kind {
		case ' ':
			if a[ai] != e.text {
				t.Fatalf("kept line %d mismatch", ai)
			}
			got = append(got, e.text)
			ai++
		case '-':
			ai++
		case '+':
			got = append(got, e.text)
		}
	}
	if ai != len(a) || strings.Join(got, "\n") != strings.Join(b, "\n") {
		t.Fatalf("edit script does not turn a into b:\na=%q\nb=%q\ngot=%q", a, b, got)
	}
}

func TestDiffLinesRandom(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	words := []string{"a", "b", "c", "d", "e"}
	gen := func() []string {
		out := make([]string, rng.Intn(30))
		for i := range out {
			out[i] = words[rng.Intn(len(words))]
		}
		return out
	}
	for range 500 {
		a, b := gen(), gen()
		edits, ok := diffLines(a, b)
		if !ok {
			t.Fatal("small diff gave up")
		}
		apply(t, a, b, edits)
	}
}

func TestUnified(t *testing.T) {
	a := splitLines("one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\n")
	b := splitLines("one\ntwo\nTHREE\nfour\nfive\nsix\nseven\neight\nnine\nten\neleven\n")
	edits, _ := diffLines(a, b)
	got, adds, dels := unified(edits, 1)
	want := "@@ -2,3 +2,3 @@\n two\n-three\n+THREE\n four\n@@ -10,1 +10,2 @@\n ten\n+eleven\n"
	if got != want || adds != 2 || dels != 1 {
		t.Fatalf("got (+%d -%d)\n%s\nwant\n%s", adds, dels, got, want)
	}
}

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotDigest(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "main.go", "package main\n\nfunc main() {}\n")
	write(t, dir, "old.txt", "bye\n")
	write(t, dir, ".gitignore", "bin/\n*.log\n")
	write(t, dir, "bin/app", "binary")
	write(t, dir, "sub/debug.log", "noise")
	write(t, dir, ".env", "SECRET=1\n")
	ws, err := workspace.Open(dir, []string{"**"}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()

	s1 := Take(ws, nil)
	for _, p := range []string{"bin/app", "sub/debug.log"} {
		if _, ok := s1.Files[p]; ok {
			t.Fatalf("%s should be ignored", p)
		}
	}
	if f := s1.Files[".env"]; f.Hash != "" || f.Text != "" {
		t.Fatal("secret file contents must not be snapshotted")
	}

	write(t, dir, "main.go", "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(1) }\n")
	os.Remove(filepath.Join(dir, "old.txt"))
	write(t, dir, "new.md", "# hi\n")
	// Same size and mtime as before but different content is not
	// re-read; bump the mtime as any real write would.
	future := time.Now().Add(time.Second)
	os.Chtimes(filepath.Join(dir, "main.go"), future, future)

	s2 := Take(ws, s1)
	ch := Diff(s1, s2)
	var ops []string
	for _, c := range ch {
		ops = append(ops, string(c.Op)+" "+c.Path)
	}
	if strings.Join(ops, ",") != "M main.go,A new.md,D old.txt" {
		t.Fatalf("changes = %v", ops)
	}
	d, shown := Digest(ch, map[string]string{"main.go": "backend_dev (claude)"}, "", 16<<10)
	if len(shown) != 2 {
		t.Fatalf("shown = %+v", shown)
	}
	if mine, _ := Digest(ch, map[string]string{"main.go": "me", "new.md": "me", "old.txt": "me"}, "me", 16<<10); mine != "" {
		t.Fatalf("an agent's own changes should not be in its digest:\n%s", mine)
	}
	for _, want := range []string{"M main.go +3 -1  (backend_dev (claude))", "+import \"fmt\"", "-func main() {}", "+++ b/new.md", "D old.txt"} {
		if !strings.Contains(d, want) {
			t.Fatalf("digest missing %q:\n%s", want, d)
		}
	}
	if d, _ := Digest(Diff(s2, Take(ws, s2)), nil, "", 1000); d != "" {
		t.Fatal("no changes should give an empty digest")
	}
}

func TestBrief(t *testing.T) {
	dir := t.TempDir()
	ws, _ := workspace.Open(dir, nil, 1<<20)
	if b := Brief(ws, 8000); !strings.Contains(b, "empty") {
		t.Fatalf("empty workspace brief: %s", b)
	}
	ws.Close()

	write(t, dir, "go.mod", "module example.com/x\n\ngo 1.27\n")
	write(t, dir, "AGENTS.md", "Always run the tests.\n")
	write(t, dir, "README.md", "# X\n\nDoes things.\n")
	write(t, dir, "internal/a/b/c/deep.go", "package c\n")
	write(t, dir, ".github/workflows/ci.yml", "on: push\n")
	ws, _ = workspace.Open(dir, nil, 1<<20)
	defer ws.Close()
	b := Brief(ws, 16000)
	for _, want := range []string{"Go 1", "## AGENTS.md", "Always run the tests.", "## README.md", "## go.mod", "module example.com/x", "b/  (1 files)", ".github/workflows/ci.yml"} {
		if !strings.Contains(b, want) {
			t.Fatalf("brief missing %q:\n%s", want, b)
		}
	}
	if small := Brief(ws, 1200); len(small) > 1200 {
		t.Fatalf("brief is %d bytes, over its 1200 budget", len(small))
	}
}
