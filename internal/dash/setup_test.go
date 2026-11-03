package dash

import (
	"os/exec"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/dethmasque/ackn4th/internal/task"
	"github.com/dethmasque/ackn4th/internal/tips"
)

func setupStates() []ProviderState {
	return []ProviderState{
		{Name: "claude-code", Note: "your Claude plan", Ready: true, Detail: "toni@example.com (Max)"},
		{Name: "codex", Note: "your ChatGPT plan, via the Codex CLI (sandboxed)", Detail: "needs sign-in"},
		{Name: "cursor", Note: "your Cursor plan", Missing: true, Hint: "curl https://cursor.com/install -fsS | bash"},
	}
}

func checkScreen(t *testing.T, m *model, size [2]int, want string) {
	t.Helper()
	screen := m.render()
	if want != "" && !strings.Contains(screen, want) {
		t.Errorf("%v: expected %q on screen", size, want)
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

func TestSetupOfferedAfterTour(t *testing.T) {
	t.Setenv(tips.Env, "")
	for _, size := range [][2]int{{80, 22}, {120, 40}, {200, 60}} {
		var launched []NewTask
		done := 0
		opt := formOptions(task.NewStore(t.TempDir()), &launched)
		opt.Tips = tips.Load(t.TempDir(), true)
		opt.Setup = &Setup{Offer: true, Status: setupStates, Done: func() { done++ }}
		var logins [][]string
		opt.Login = func(p []string) *exec.Cmd { logins = append(logins, p); return exec.Command("true") }
		m := newModel(opt)
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m.Update(tickMsg{})
		if m.setup != nil {
			t.Fatal("setup must wait for the welcome tip")
		}
		m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // the welcome tip…
		m.Update(tickMsg{})
		if m.setup != nil {
			t.Fatal("setup must wait for the task-list tip")
		}
		m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // …and the task-list tip
		_, cmd := m.Update(tickMsg{})
		if m.setup == nil || !m.setup.ask {
			t.Fatal("setup should be offered once the tip is gone")
		}
		checkScreen(t, m, size, "Set up providers now?")
		_ = cmd
		m.Update(setupStatusMsg(setupStates()))
		m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if m.setup.ask {
			t.Fatal("enter on SET UP should show the list")
		}
		checkScreen(t, m, size, "codex")
		m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if !strings.Contains(m.setup.err, "isn't installed") {
			t.Errorf("signing in to a missing CLI should explain, got %q", m.setup.err)
		}
		checkScreen(t, m, size, "install:")
		m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
		m.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
		_, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if cmd == nil || len(logins) != 1 || logins[0][0] != "codex" {
			t.Fatalf("enter should sign in to codex, got %v", logins)
		}
		m.Update(loginDoneMsg{})
		if m.setup == nil || !m.setup.loading {
			t.Error("after signing in the list should recheck")
		}
		m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
		if m.setup != nil || done != 1 {
			t.Errorf("esc should close and record the answer (done=%d)", done)
		}
		m.Update(tickMsg{})
		if m.setup != nil {
			t.Error("setup is offered once")
		}
		m.Update(tea.KeyPressMsg{Code: 'p', Text: "p"})
		if m.setup == nil || m.setup.ask {
			t.Error("p should open the provider list")
		}
	}
}

func TestSetupLater(t *testing.T) {
	var launched []NewTask
	done := false
	opt := formOptions(task.NewStore(t.TempDir()), &launched)
	opt.Tips = tips.Off()
	opt.Setup = &Setup{Offer: true, Status: setupStates, Done: func() { done = true }}
	m := newModel(opt)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.Update(tickMsg{})
	if m.setup == nil {
		t.Fatal("with tips off, setup is offered straight away")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.setup != nil || !done {
		t.Error("LATER should close and record the answer")
	}
}

// x asks once, then signs out of the highlighted provider; any other key
// cancels.
func TestSetupSignOut(t *testing.T) {
	var launched []NewTask
	opt := formOptions(task.NewStore(t.TempDir()), &launched)
	opt.Tips = tips.Off()
	var out [][]string
	opt.Setup = &Setup{Status: setupStates, Logout: func(p []string) error { out = append(out, p); return nil }}
	m := newModel(opt)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.Update(tea.KeyPressMsg{Code: 'p', Text: "p"})
	m.Update(setupStatusMsg(setupStates()))
	x := tea.KeyPressMsg{Code: 'x', Text: "x"}
	m.Update(x)
	checkScreen(t, m, [2]int{120, 40}, "press x again to sign out of claude-code")
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m.Update(x)
	if len(out) != 0 {
		t.Fatal("moving should cancel the first x")
	}
	m.Update(x)
	if len(out) != 1 || out[0][0] != "codex" {
		t.Fatalf("signed out of %v, want codex", out)
	}
}

// t replays the walkthrough from the welcome tip, even with tasks on screen
// and tips off.
func TestTourOnRequest(t *testing.T) {
	var launched []NewTask
	store := task.NewStore(t.TempDir())
	store.Create(&task.Task{ID: "ay9886", Prompt: "p", Workspace: "/w", MaxRounds: 10,
		Agents: []task.AgentSpec{{Role: "backend_dev", Provider: "claude", Model: "m"}}})
	opt := formOptions(store, &launched)
	opt.Tips = tips.Off()
	m := newModel(opt)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if _, _, ok := m.tip(); ok {
		t.Fatal("no tips while they're off")
	}
	m.Update(tea.KeyPressMsg{Code: 't', Text: "t"})
	for _, want := range []string{"dash.welcome", "dash.tasks"} {
		s, _, ok := m.tip()
		if !ok || s.id != want {
			t.Fatalf("tip = %q, want %s", s.id, want)
		}
		checkScreen(t, m, [2]int{120, 40}, "TIP")
		m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	}
	m.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	if s, _, ok := m.tip(); !ok || s.id != "dash.form" {
		t.Fatalf("the form's tip should follow, got %q", s.id)
	}
}
