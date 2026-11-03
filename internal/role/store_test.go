package role

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreSaveAndLoad(t *testing.T) {
	s := Store{Dir: filepath.Join(t.TempDir(), "roles")}
	r := Template("data_scientist")
	r.Skills, r.Writable = []string{"Python", "SQL"}, []string{"notebooks/**", "*.ipynb"}
	if r.Title != "Data Scientist" {
		t.Errorf("title %q", r.Title)
	}
	if _, err := s.Save(r, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(r, false); !errors.Is(err, ErrExists) {
		t.Errorf("second save: %v", err)
	}
	reg, err := Load(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reg.Get("data_scientist")
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Data Scientist" || len(got.Writable) != 2 || got.Prompt != strings.TrimSpace(r.Prompt) {
		t.Errorf("round trip: %+v", got)
	}
	// Read-only roles round-trip with no writable.
	ro := Template("auditor")
	ro.Writable = nil
	s.Save(ro, false)
	reg, _ = Load(s.Dir)
	if a, _ := reg.Get("auditor"); a == nil || !a.ReadOnly() {
		t.Error("auditor should be read-only")
	}
	if err := s.Remove("auditor"); err != nil || s.Custom("auditor") {
		t.Errorf("remove: %v", err)
	}
}

func TestValidate(t *testing.T) {
	for _, r := range []*Role{
		{Name: "Bad-Name", Prompt: "x"},
		{Name: "edit", Prompt: "x"},
		{Name: "ok", Prompt: "  "},
		{Name: "ok", Prompt: "x", Writable: []string{"../outside/**"}},
		{Name: "ok", Prompt: "x", Writable: []string{"/etc/**"}},
	} {
		if Validate(r) == nil {
			t.Errorf("%+v accepted", r)
		}
	}
}

func TestOverrideAndRemove(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	b, ok := Builtin("code_reviewer")
	if !ok {
		t.Fatal("no built-in code_reviewer")
	}
	b.Prompt = "Review harder."
	s.Save(b, false)
	reg, _ := Load(s.Dir)
	if r, _ := reg.Get("code_reviewer"); r.Prompt != "Review harder." {
		t.Error("the override should win")
	}
	s.Remove("code_reviewer")
	reg, _ = Load(s.Dir)
	if r, _ := reg.Get("code_reviewer"); r.Prompt == "Review harder." {
		t.Error("removing the override should restore the built-in")
	}
	if err := s.Remove("code_reviewer"); err == nil {
		t.Error("removing an unedited built-in should say there's nothing to remove")
	}
}

func TestInvalidFileDoesNotBreakLoad(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "broken.md"), []byte("no front matter"), 0o600)
	reg, err := Load(dir)
	if err != nil || len(reg.Invalid) != 1 {
		t.Fatalf("err=%v invalid=%v", err, reg.Invalid)
	}
	if _, err := reg.Get("backend_dev"); err != nil {
		t.Error(err)
	}
}

func TestImport(t *testing.T) {
	file := "---\nname: game_designer\ntitle: Game Designer\nwritable: [\"design/**\"]\n---\nDesign the fun.\n"
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(file)) }))
	defer srv.Close()
	old := http.DefaultClient
	http.DefaultClient = srv.Client()
	defer func() { http.DefaultClient = old }()

	data, err := Fetch(context.Background(), srv.URL+"/game_designer.md")
	if err != nil {
		t.Fatal(err)
	}
	s := Store{Dir: t.TempDir()}
	r, p, err := s.SaveFile(data, "url", false)
	if err != nil || r.Name != "game_designer" || filepath.Base(p) != "game_designer.md" {
		t.Fatalf("import: %v %v %s", r, err, p)
	}
	if _, _, err := s.SaveFile([]byte("---\nname: x\n---\n"), "bad", false); err == nil {
		t.Error("a role without a prompt should be refused")
	}
	if _, err := Fetch(context.Background(), "http://example.com/r.md"); err == nil {
		t.Error("plain http should be refused")
	}
	local := filepath.Join(t.TempDir(), "r.md")
	os.WriteFile(local, []byte(file), 0o600)
	if _, err := Fetch(context.Background(), local); err != nil {
		t.Error(err)
	}
}
