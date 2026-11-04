package dash

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/dethmasque/ackn4th/internal/task"
)

// The form says where a task for a GitHub repo will work, since it isn't
// the directory ackn4th was started in.
func TestFormSaysWhereTheRepoGoes(t *testing.T) {
	var launched []NewTask
	opt := formOptions(task.NewStore(t.TempDir()), &launched)
	opt.RepoDir = func(repo string) (string, bool, error) {
		switch repo {
		case "acme/web":
			return "/home/me/ackn4th-workspaces/web", false, nil
		case "acme/api":
			return "/home/me/ackn4th-workspaces/api", true, nil
		}
		return "", false, errors.New(`github: "` + repo + `" isn't a repository: use owner/name`)
	}
	for _, size := range [][2]int{{80, 22}, {120, 40}} {
		m := newModel(opt)
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m.Update(keyMsg("n"))
		m.form.repo = newInput("acme/web")
		checkScreen(t, m, size, "not used; clones to")
		if !strings.Contains(ansi.Strip(m.render()), "ackn4th-workspaces/web") {
			t.Errorf("%v: the clone's path isn't shown", size)
		}
		m.form.repo = newInput("acme/api")
		checkScreen(t, m, size, "not used; reuses")
		m.form.repo = newInput("not a repo")
		checkScreen(t, m, size, "owner/name")
	}
}

// A stopped task with uncommitted work says where it is.
func TestUncommittedChangesNote(t *testing.T) {
	asked := 0
	m := newModel(Options{Store: fixture(t), Uncommitted: func(dir string) (int, error) { asked++; return 15, nil }})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if cmd := m.checkChanges(); cmd != nil {
		t.Fatal("a running task's workspace shouldn't be looked at")
	}
	m.tasks[0].Status = task.Completed
	m.current().Task.Status = task.Completed
	cmd := m.checkChanges()
	if cmd == nil {
		t.Fatal("a stopped task's workspace should be looked at")
	}
	if m.checkChanges() != nil {
		t.Error("looked at twice at once")
	}
	m.Update(cmd())
	m.focusMain = true
	screen := ansi.Strip(m.render())
	if !strings.Contains(screen, "15 files changed and not committed") || !strings.Contains(screen, "ackn4th open-pr k3f9qz") {
		t.Errorf("no note about the uncommitted work:\n%s", screen)
	}
	if m.checkChanges() != nil || asked != 1 {
		t.Error("looked at again too soon")
	}
	m.focusMain = false
	if !strings.Contains(m.where(), "15 files changed and not committed") {
		t.Errorf("the voice doesn't say it: %q", m.where())
	}
}

// Typing a repo hides the trust checkbox, which mustn't move focus off
// the repo field.
func TestTypingARepoKeepsFocus(t *testing.T) {
	var launched []NewTask
	opt := formOptions(task.NewStore(t.TempDir()), &launched)
	opt.Trusted = func(string) bool { return false }
	m := newModel(opt)
	m.Update(keyMsg("n"))
	m.form.focusOn(field{kind: "repo"})
	for _, k := range "acme/web" {
		m.Update(keyMsg(string(k)))
	}
	if got := m.form.repo.String(); got != "acme/web" || m.form.current().kind != "repo" {
		t.Errorf("repo %q, branch %q, focus %v", got, m.form.branch.String(), m.form.current())
	}
}
