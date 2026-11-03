package github

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRefs(t *testing.T) {
	got := Refs("Fix https://github.com/acme/web/issues/12 (see https://github.com/acme/web/pull/40, and again https://github.com/acme/web/issues/12).")
	if len(got) != 2 || got[0].Kind != "issue" || got[0].Number != 12 || got[1].Kind != "pull" || got[1].Number != 40 || got[1].Repo != "web" {
		t.Errorf("refs = %+v", got)
	}
	if len(Refs("https://github.com/acme/web and https://example.com/acme/web/issues/1")) != 0 {
		t.Error("only issue and pull URLs on github.com count")
	}
}

func TestParseRepo(t *testing.T) {
	for in, want := range map[string]string{"acme/web": "acme/web", "https://github.com/acme/web.git": "acme/web", "https://github.com/acme/my.repo/": "acme/my.repo"} {
		o, n, err := ParseRepo(in)
		if err != nil || o+"/"+n != want {
			t.Errorf("ParseRepo(%q) = %s/%s, %v", in, o, n, err)
		}
	}
	for _, bad := range []string{"web", "acme/web/issues/1", "https://gitlab.com/acme/web"} {
		if _, _, err := ParseRepo(bad); err == nil {
			t.Errorf("ParseRepo(%q) accepted", bad)
		}
	}
}

// fakeGH stands in for gh: it logs its arguments and stdin, and prints
// canned output.
func fakeGH(t *testing.T, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake gh is a shell script")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	p := filepath.Join(dir, "gh")
	os.WriteFile(p, []byte("#!/bin/sh\necho \"$@\" >> "+log+"\n"+script), 0o755)
	old := bin
	bin = func() (string, error) { return p, nil }
	t.Cleanup(func() { bin = old })
	return log
}

func TestFetch(t *testing.T) {
	fakeGH(t, `cat <<'EOF'
{"title":"Login breaks","body":"Steps: …","state":"OPEN","author":{"login":"ana"},"labels":[{"name":"bug"}],
 "comments":[{"author":{"login":"bo"},"body":"Also on Safari.","createdAt":"2026-10-01T10:00:00Z"}]}
EOF
`)
	s, err := Context(context.Background(), Refs("https://github.com/acme/web/issues/12"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"## From GitHub", "### Issue acme/web#12: Login breaks", "open · opened by ana · labels: bug", "Steps: …", "#### bo, 2026-10-01", "Also on Safari."} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
}

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out)
	}
}

// repo makes a workspace whose GitHub origin is a local bare repository,
// with one commit on main.
func repo(t *testing.T) (bare, ws string) {
	t.Helper()
	bare, ws = filepath.Join(t.TempDir(), "web.git"), t.TempDir()
	run(t, "", "git", "init", "-q", "--bare", bare)
	run(t, ws, "git", "init", "-q", "-b", "main")
	run(t, ws, "git", "config", "user.email", "t@example.com")
	run(t, ws, "git", "config", "user.name", "T")
	// "GitHub" is the local bare repository.
	run(t, ws, "git", "config", "url."+bare+".insteadOf", "https://github.com/acme/web.git")
	run(t, ws, "git", "remote", "add", "origin", "https://github.com/acme/web.git")
	os.WriteFile(filepath.Join(ws, "a.go"), []byte("package a\n"), 0o644)
	os.WriteFile(filepath.Join(ws, "gone.txt"), []byte("x"), 0o644)
	run(t, ws, "git", "add", ".")
	run(t, ws, "git", "commit", "-qm", "base")
	run(t, ws, "git", "push", "-q", "origin", "main")
	return bare, ws
}

func TestDraftPR(t *testing.T) {
	log := fakeGH(t, "cat > /dev/null; echo https://github.com/acme/web/pull/41\n")
	bare, ws := repo(t)
	os.WriteFile(filepath.Join(ws, "a.go"), []byte("package a\n\nfunc A() {}\n"), 0o644)
	os.Remove(filepath.Join(ws, "gone.txt"))
	os.WriteFile(filepath.Join(ws, "mine.txt"), []byte("not the agents'"), 0o644)

	url, err := DraftPR(context.Background(), ws, PR{Branch: "ackn4th/k3f9qz", Title: "Add A", Body: "Drafted by ackn4th.", Files: []string{"a.go", "gone.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	if url != "https://github.com/acme/web/pull/41" {
		t.Errorf("url = %q", url)
	}
	out, _ := exec.Command("git", "-C", bare, "show", "--stat", "--format=%s", "ackn4th/k3f9qz").Output()
	if s := string(out); !strings.Contains(s, "Add A") || !strings.Contains(s, "a.go") || !strings.Contains(s, "gone.txt") || strings.Contains(s, "mine.txt") {
		t.Errorf("pushed commit:\n%s", s)
	}
	if l, _ := os.ReadFile(log); !strings.Contains(string(l), "pr create --draft --base main --head ackn4th/k3f9qz --title Add A") {
		t.Errorf("gh calls:\n%s", l)
	}
	if _, err := DraftPR(context.Background(), ws, PR{Branch: "ackn4th/k3f9qz", Title: "x", Files: []string{"a.go"}}); err == nil {
		t.Error("an existing branch should be refused")
	}
}

func TestCheckout(t *testing.T) {
	fakeGH(t, "")
	bare, ws := repo(t)
	// A branch only on "GitHub".
	other := t.TempDir()
	run(t, "", "git", "clone", "-q", bare, other)
	run(t, other, "git", "switch", "-qc", "feature")
	os.WriteFile(filepath.Join(other, "f.txt"), []byte("f"), 0o644)
	run(t, other, "git", "add", ".")
	run(t, other, "git", "-c", "user.email=t@e", "-c", "user.name=T", "commit", "-qm", "feature")
	run(t, other, "git", "push", "-q", "origin", "feature")

	ctx := context.Background()
	if created, err := Checkout(ctx, ws, "feature"); err != nil || created {
		t.Fatalf("remote branch: created=%v %v", created, err)
	}
	if _, err := os.Stat(filepath.Join(ws, "f.txt")); err != nil {
		t.Error("the remote branch's commit should be checked out")
	}
	if created, err := Checkout(ctx, ws, "fresh/idea"); err != nil || !created {
		t.Fatalf("new branch: created=%v %v", created, err)
	}
	if created, err := Checkout(ctx, ws, "main"); err != nil || created {
		t.Fatalf("local branch: created=%v %v", created, err)
	}
	if b, _ := CurrentBranch(ctx, ws); b != "main" {
		t.Errorf("on %s", b)
	}
	if _, err := Checkout(ctx, ws, "bad..name"); err == nil {
		t.Error("an invalid name should be refused")
	}
}

// With an existing branch the commit lands on it, proposed for the
// default branch.
func TestDraftPRExistingBranch(t *testing.T) {
	log := fakeGH(t, `case "$1" in repo) echo main ;; pr) cat > /dev/null; echo https://github.com/acme/web/pull/42 ;; esac
`)
	bare, ws := repo(t)
	ctx := context.Background()
	Checkout(ctx, ws, "feature")
	os.WriteFile(filepath.Join(ws, "a.go"), []byte("package a // changed\n"), 0o644)
	if _, err := DraftPR(ctx, ws, PR{Branch: "feature", Existing: true, Title: "T", Body: "B", Files: []string{"a.go"}}); err != nil {
		t.Fatal(err)
	}
	if out, _ := exec.Command("git", "-C", bare, "log", "--format=%s", "feature").Output(); !strings.HasPrefix(string(out), "T\n") {
		t.Errorf("feature on origin:\n%s", out)
	}
	if l, _ := os.ReadFile(log); !strings.Contains(string(l), "pr create --draft --base main --head feature") {
		t.Errorf("gh calls:\n%s", l)
	}
	Checkout(ctx, ws, "main")
	if _, err := DraftPR(ctx, ws, PR{Branch: "main", Existing: true, Title: "T", Files: []string{"a.go"}}); err == nil {
		t.Error("a pull request from the default branch into itself should be refused")
	}
}
