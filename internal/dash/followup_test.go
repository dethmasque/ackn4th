package dash

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/dethmasque/ackn4th/internal/task"
)

type followUpCall struct {
	id, prompt string
	team       []NewAgent
}

func followUpModel(t *testing.T) (*model, *[]followUpCall) {
	var launched []NewTask
	var got []followUpCall
	opt := formOptions(fixture(t), &launched)
	opt.FollowUp = func(tk *task.Task, prompt string, team []NewAgent) error {
		got = append(got, followUpCall{tk.ID, prompt, team})
		return nil
	}
	return newModel(opt), &got
}

func TestFollowUpDialog(t *testing.T) {
	m, got := followUpModel(t)
	for _, size := range [][2]int{{80, 22}, {160, 50}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m.tasks[0].Status = task.Running
		m.Update(keyMsg("a"))
		if m.form != nil || !strings.Contains(m.flash, "still running") {
			t.Fatalf("a running task can't take a follow-up: %q", m.flash)
		}
		m.tasks[0].Status = task.Completed
		m.Update(keyMsg("a"))
		if m.form == nil || m.form.team == nil {
			t.Fatal("a should open the follow-up form")
		}
		m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
		if m.form == nil || len(*got) != 0 || !strings.Contains(m.form.err, "next") {
			t.Fatalf("an empty follow-up shouldn't run: %q", m.form.err)
		}
		m.Update(tea.PasteMsg{Content: "Also document DELETE"})
		m.Update(keyMsg("!"))
		checkScreen(t, m, size, "FOLLOW-UP")
		checkScreen(t, m, size, "RUN IT")
		_, cmd := m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
		if cmd == nil {
			t.Fatal("ctrl+s should run the follow-up")
		}
		m.Update(cmd())
		if m.form != nil || len(*got) != 1 || (*got)[0].id != "k3f9qz" || (*got)[0].prompt != "Also document DELETE!" || (*got)[0].team != nil {
			t.Fatalf("got %+v", *got)
		}
		if !strings.Contains(m.flash, "follow-up started") {
			t.Errorf("flash %q", m.flash)
		}
		m.Update(keyMsg("a"))
		m.Update(keyMsg("esc"))
		if m.form != nil || len(*got) != 1 {
			t.Error("esc should cancel")
		}
		*got = nil
	}
}

// A follow-up can give its agents other roles, models and efforts, but
// not other providers, and agents can't be added or removed.
func TestFollowUpChangesTheTeam(t *testing.T) {
	m, got := followUpModel(t)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.tasks[0].Status = task.Completed
	m.Update(keyMsg("a"))
	f := m.form
	n := len(f.agents)
	if n != len(m.tasks[0].Agents) || n < 2 {
		t.Fatalf("the form should list the task's %d agents: %d", len(m.tasks[0].Agents), n)
	}
	for _, x := range f.fields() {
		if x.kind == "provider" || x.kind == "remove" || x.kind == "add" || x.kind == "name" || x.kind == "workspace" {
			t.Errorf("a follow-up shouldn't offer %v", x)
		}
	}
	f.focusOn(field{"role", 0})
	press(m, "x", "ctrl+n")
	if len(f.agents) != n {
		t.Error("agents can't be removed or added in a follow-up")
	}
	press(m, "enter", "down", "enter") // the role list: the next role
	newRole := f.agents[0].role
	f.focusOn(field{"effort", 1})
	press(m, "enter", "down", "enter")
	if newRole == m.tasks[0].Agents[0].Role || f.agents[1].effort == m.tasks[0].Agents[1].Effort {
		t.Fatalf("role %q effort %q unchanged", newRole, f.agents[1].effort)
	}
	f.prompt = newInput("Now review it")
	_, cmd := m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	m.Update(cmd())
	if len(*got) != 1 || len((*got)[0].team) != n {
		t.Fatalf("got %+v", *got)
	}
	team := (*got)[0].team
	if team[0].Role != newRole || team[1].Effort != f.agents[1].effort || team[0].Provider != m.tasks[0].Agents[0].Provider {
		t.Errorf("team %+v", team)
	}
}
