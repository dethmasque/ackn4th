package role

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestBuiltinsLoad(t *testing.T) {
	reg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	all := reg.All()
	if len(all) < 15 {
		t.Fatalf("only %d built-in roles", len(all))
	}
	for _, r := range all {
		if r.Summary == "" || len(r.Skills) == 0 || r.Prompt == "" {
			t.Errorf("%s is incomplete", r.Name)
		}
	}
	if r, err := reg.Get("code-reviewer"); err != nil || r.Name != "code_reviewer" {
		t.Errorf("hyphenated lookup: %v", err)
	}
}

func TestOverride(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "backend_dev.md"), []byte("---\nname: backend_dev\ntitle: Custom\n---\nBe custom.\n"), 0o644)
	reg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := reg.Get("backend_dev")
	if r.Title != "Custom" || !r.ReadOnly() {
		t.Errorf("override not applied: %+v", r)
	}
}

func TestParseErrors(t *testing.T) {
	for _, in := range []string{"no front matter", "---\nname: x\n", "---\nname: Bad-Name\n---\nbody", "---\nname: ok\n---\n"} {
		if _, err := Parse([]byte(in), "t"); err == nil {
			t.Errorf("Parse(%q) succeeded", in)
		}
	}
}

func TestCombinedRoles(t *testing.T) {
	reg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	r, err := reg.Get("backend-dev+qa_engineer+backend_dev")
	if err != nil {
		t.Fatal(err)
	}
	be, _ := reg.Get("backend_dev")
	qa, _ := reg.Get("qa_engineer")
	if r.Name != "backend_dev+qa_engineer" || !strings.Contains(r.Title, "&") {
		t.Fatalf("combined %q / %q", r.Name, r.Title)
	}
	for _, w := range append(be.Writable, qa.Writable...) {
		if !slices.Contains(r.Writable, w) {
			t.Errorf("combined role cannot write %s", w)
		}
	}
	if !strings.Contains(r.Prompt, strings.TrimSpace(be.Prompt)) || !strings.Contains(r.Prompt, strings.TrimSpace(qa.Prompt)) {
		t.Error("combined prompt is missing a role's prompt")
	}
	if one, _ := reg.Get("qa_engineer+qa_engineer"); one != qa {
		t.Error("a role combined with itself should be that role")
	}
	if _, err := reg.Get("backend_dev+nope"); err == nil {
		t.Error("unknown part accepted")
	}
}
