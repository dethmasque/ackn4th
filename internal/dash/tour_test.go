package dash

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/dethmasque/ackn4th/internal/task"
	"github.com/dethmasque/ackn4th/internal/tips"
)

func TestTourWalksThrough(t *testing.T) {
	t.Setenv(tips.Env, "")
	var launched []NewTask
	disabled := false
	for _, size := range [][2]int{{80, 22}, {120, 40}, {200, 60}} {
		opt := formOptions(task.NewStore(t.TempDir()), &launched)
		opt.Tips = tips.Load(t.TempDir(), true)
		opt.DisableTips = func() error { disabled = true; return nil }
		m := newModel(opt)
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		check := func(want string) {
			t.Helper()
			screen := m.render()
			if want != "" && !strings.Contains(screen, want) {
				t.Errorf("%v: expected a tip containing %q", size, want)
			}
			lines := strings.Split(screen, "\n")
			if len(lines) != size[1] {
				t.Errorf("%v: %d lines", size, len(lines))
			}
			for i, l := range lines {
				if w := width(l); w != size[0] {
					t.Errorf("%v: line %d is %d wide: %q", size, i, w, l)
				}
			}
		}
		check("TIP 1/3")
		m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // steps to the next tip
		if m.form != nil {
			t.Fatal("enter on a tip should show the next tip, not open the form")
		}
		check("TIP 2/3")                                // the task list, even while it's empty
		m.Update(tea.KeyPressMsg{Code: 'n', Text: "n"}) // dismisses and opens the form
		if m.form == nil {
			t.Fatal("the key that dismisses a tip should still act")
		}
		check("TIP 3/3")
		m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
		if s, _, ok := m.tip(); ok {
			t.Errorf("tip %s should be gone", s.id)
		}
		if got := m.form.prompt.String(); got != "x" {
			t.Errorf("the key should reach the form, prompt = %q", got)
		}
		check("")

		// Enter alone walks the whole tour: on tip 2 it opens the form,
		// where tip 3 is.
		opt.Tips = tips.Load(t.TempDir(), true)
		m = newModel(opt)
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		check("next, in the new-task form")
		m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if m.form == nil {
			t.Fatalf("%v: enter on tip 2 should open the form for tip 3", size)
		}
		check("TIP 3/3")
		if !strings.Contains(m.render(), "got it") {
			t.Errorf("%v: the last tip should say it's the last", size)
		}
		m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if _, _, ok := m.tip(); ok || m.form == nil {
			t.Errorf("%v: enter on the last tip should only close it", size)
		}

		opt.Tips = tips.Load(t.TempDir(), true)
		m = newModel(opt)
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m.Update(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
		if !disabled || m.opt.Tips.Enabled() {
			t.Error("ctrl+t should turn tips off")
		}
	}
}
