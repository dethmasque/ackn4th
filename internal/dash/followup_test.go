package dash

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/dethmasque/ackn4th/internal/task"
)

func TestFollowUpDialog(t *testing.T) {
	var got []string
	m := newModel(Options{Store: fixture(t), FollowUp: func(tk *task.Task, prompt string) error {
		got = append(got, tk.ID+": "+prompt)
		return nil
	}})
	for _, size := range [][2]int{{80, 22}, {160, 50}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m.tasks[0].Status = task.Running
		m.Update(keyMsg("a"))
		if m.more != nil || !strings.Contains(m.flash, "still running") {
			t.Fatalf("a running task can't take a follow-up: %q", m.flash)
		}
		m.tasks[0].Status = task.Completed
		m.Update(keyMsg("a"))
		if m.more == nil {
			t.Fatal("a should open the follow-up dialog")
		}
		m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if m.more == nil || len(got) != 0 {
			t.Fatal("an empty follow-up shouldn't run")
		}
		m.Update(tea.PasteMsg{Content: "Also document DELETE"})
		m.Update(keyMsg("!"))
		checkScreen(t, m, size, "FOLLOW-UP")
		m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if m.more != nil || len(got) != 1 || got[0] != "k3f9qz: Also document DELETE!" {
			t.Fatalf("got %q", got)
		}
		if !strings.Contains(m.flash, "follow-up started") {
			t.Errorf("flash %q", m.flash)
		}
		m.Update(keyMsg("a"))
		m.Update(keyMsg("esc"))
		if m.more != nil || len(got) != 1 {
			t.Error("esc should cancel")
		}
		got = nil
	}
}
