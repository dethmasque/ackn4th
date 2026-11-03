package dash

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/dethmasque/ackn4th/internal/role"
	"github.com/dethmasque/ackn4th/internal/task"
)

// roleAdmin is the CLI's wiring, on a temporary store.
func roleAdmin(t *testing.T) (*Roles, role.Store) {
	store := role.Store{Dir: t.TempDir()}
	kind := func(r *role.Role) string {
		_, builtin := role.Builtin(r.Name)
		switch {
		case !store.Custom(r.Name):
			return "built-in"
		case builtin:
			return "yours, overrides built-in"
		}
		return "yours"
	}
	return &Roles{
		List: func() ([]RoleEntry, []string, error) {
			reg, err := role.Load(store.Dir)
			if err != nil {
				return nil, nil, err
			}
			var out []RoleEntry
			for _, r := range reg.All() {
				out = append(out, RoleEntry{Role: r, Kind: kind(r)})
			}
			return out, nil, nil
		},
		Save: func(r *role.Role, overwrite bool) error { _, err := store.Save(r, overwrite); return err },
		Import: func(src string, overwrite bool) (*role.Role, error) {
			data, err := role.Fetch(t.Context(), src)
			if err != nil {
				return nil, err
			}
			r, _, err := store.SaveFile(data, src, overwrite)
			return r, err
		},
		Remove: store.Remove,
	}, store
}

func typeText(m *model, s string) {
	for _, r := range s {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func key(m *model, k string) tea.Cmd {
	codes := map[string]tea.Key{
		"tab": {Code: tea.KeyTab}, "enter": {Code: tea.KeyEnter}, "esc": {Code: tea.KeyEscape},
		"down": {Code: tea.KeyDown}, "ctrl+s": {Code: 's', Mod: tea.ModCtrl}, "R": {Code: 'r', Text: "R", Mod: tea.ModShift},
	}
	k2, ok := codes[k]
	if !ok {
		k2 = tea.Key{Code: rune(k[0]), Text: k}
	}
	_, cmd := m.Update(tea.KeyPressMsg(k2))
	return cmd
}

func TestRolesScreen(t *testing.T) {
	var launched []NewTask
	opt := formOptions(task.NewStore(t.TempDir()), &launched)
	admin, store := roleAdmin(t)
	opt.RoleAdmin = admin
	sp := &fakeSpeaker{}
	m := newModel(opt)
	m.speaker = sp
	sizes := [][2]int{{80, 22}, {120, 40}, {200, 60}}
	check := func(want string) {
		t.Helper()
		for _, size := range sizes {
			m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			checkScreen(t, m, size, want)
		}
		m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	}

	key(m, "R")
	if m.roles == nil || len(m.roles.entries) != 18 {
		t.Fatalf("roles screen: %+v", m.roles)
	}
	check("a adds")
	if !strings.Contains(sp.last(), "Roles, 1 of 18: backend_dev") {
		t.Errorf("voice: %q", sp.last())
	}

	// Add one in the editor.
	key(m, "a")
	check("NEW ROLE")
	typeText(m, "game_designer")
	key(m, "tab")
	typeText(m, "Game Designer")
	key(m, "tab")
	key(m, "tab")
	typeText(m, "level design, balancing")
	if !strings.Contains(sp.last(), "") {
		t.Log(sp.last())
	}
	key(m, "tab")
	m.roles.edit.fields[reWritable] = newInput("")
	typeText(m, "design/**")
	key(m, "tab")
	m.roles.edit.fields[rePrompt] = newInput("")
	m.Update(tea.PasteMsg{Content: "You design the game.\nKeep it fun."})
	check("INSTRUCTIONS")
	key(m, "ctrl+s")
	if m.roles.edit != nil {
		t.Fatalf("not saved: %s", m.roles.edit.err)
	}
	reg, _ := role.Load(store.Dir)
	r, err := reg.Get("game_designer")
	if err != nil || r.Prompt != "You design the game.\nKeep it fun." || !slices.Equal(r.Skills, []string{"level design", "balancing"}) || !slices.Equal(r.Writable, []string{"design/**"}) {
		t.Fatalf("saved %+v, %v", r, err)
	}
	if !slices.ContainsFunc(m.opt.Roles, func(c Choice) bool { return c.Value == "game_designer" }) {
		t.Error("the new-task form should offer the new role")
	}

	// Adding it again asks before replacing.
	key(m, "a")
	typeText(m, "game_designer")
	m.roles.edit.fields[rePrompt] = newInput("Other.")
	key(m, "ctrl+s")
	if m.roles.edit == nil || !m.roles.edit.replace {
		t.Fatal("an existing role should need a second ctrl+s")
	}
	key(m, "esc")

	// Editing a built-in makes an override; removing it reverts.
	m.roles.idx = slices.IndexFunc(m.roles.entries, func(e RoleEntry) bool { return e.Role.Name == "code_reviewer" })
	key(m, "e")
	if m.roles.edit.focus != reTitle {
		t.Error("editing starts past the locked name")
	}
	m.roles.edit.fields[rePrompt] = newInput("Review harder.")
	key(m, "ctrl+s")
	e, _ := m.roles.selected()
	if e.Role.Name != "code_reviewer" || e.Kind != "yours, overrides built-in" {
		t.Fatalf("after edit: %+v", e)
	}
	key(m, "d")
	check("press d again")
	key(m, "d")
	if e, _ := m.roles.selected(); e.Kind != "built-in" || store.Custom("code_reviewer") {
		t.Error("removing the override should restore the built-in")
	}
	key(m, "d")
	if !strings.Contains(m.roles.err, "built in") {
		t.Errorf("removing a built-in: %q", m.roles.err)
	}

	// Import from a file.
	src := filepath.Join(t.TempDir(), "lore.md")
	os.WriteFile(src, []byte("---\nname: lore_writer\n---\nWrite the lore.\n"), 0o600)
	key(m, "i")
	check("IMPORT A ROLE")
	typeText(m, src)
	cmd := key(m, "enter")
	m.Update(cmd())
	if m.roles.imp != nil || !store.Custom("lore_writer") {
		t.Fatalf("import: %+v", m.roles.imp)
	}
	key(m, "i")
	typeText(m, src)
	m.Update(key(m, "enter")())
	if m.roles.imp == nil || !m.roles.imp.replace {
		t.Fatal("importing an existing role should ask to replace")
	}
	key(m, "esc")
	key(m, "esc")
	if m.roles != nil {
		t.Error("esc should close the screen")
	}
}
