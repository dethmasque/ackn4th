package dash

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/dethmasque/ackn4th/internal/task"
)

// formOptions is a dashboard with the new-task form enabled; launched
// collects what it starts.
func formOptions(store *task.Store, launched *[]NewTask) Options {
	return Options{
		Store: store,
		Launch: func(req NewTask) (string, error) {
			*launched = append(*launched, req)
			return "n3wtsk", nil
		},
		Trusted: func(dir string) bool { return dir == "~/code/todo-api" },
		Roles: []Choice{
			{Value: "backend_dev", Note: "Backend Developer"}, {Value: "code_reviewer", Note: "Code Reviewer"},
			{Value: "qa_engineer", Note: "QA Engineer"}, {Value: "tech_writer", Note: "Technical Writer"},
		},
		Providers: []ProviderChoice{
			{Name: "claude", Note: "Anthropic", Models: []string{"claude-opus-5-5", "claude-sonnet-5-5"}, Efforts: []string{"low", "high", "max"}},
			{Name: "gemini", Note: "Google", Models: []string{"gemini-3.1-pro-preview"}, Efforts: []string{"low", "high"}},
		},
		Defaults: FormDefaults{Workspace: "~/code/todo-api", Provider: "claude", Roles: []string{"backend_dev", "code_reviewer"}},
	}
}

func press(m *model, keys ...string) {
	for _, k := range keys {
		if m.form != nil {
			if _, done := m.form.key(k); done {
				m.form = nil
			}
			continue
		}
		m.key(k)
	}
}

func TestFormRendersEveryState(t *testing.T) {
	var launched []NewTask
	// prompt, name, workspace, repo, branch, mode, rounds, cost cap,
	// draft PR: then the first agent's role.
	toRole := []string{"n", "tab", "tab", "tab", "tab", "tab", "tab", "tab", "tab", "tab"}
	for _, size := range [][2]int{{80, 22}, {120, 40}, {200, 60}} {
		for _, keys := range [][]string{
			{"n"},
			{"n", "h", "i"},
			append(toRole, "enter"),          // role dropdown
			append(toRole, "right", "enter"), // provider dropdown
			append(toRole, "right", "right", "enter"), // model dropdown
			{"n", "f1"}, // help
			{"n", "ctrl+n", "ctrl+n", "ctrl+n", "ctrl+n", "ctrl+n", "ctrl+n", "ctrl+n", "ctrl+n"},
			{"n", "ctrl+s"}, // validation error
		} {
			m := newModel(formOptions(task.NewStore(t.TempDir()), &launched))
			m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			press(m, keys...)
			if m.form == nil {
				t.Fatalf("%v: the form did not open", keys)
			}
			if keys[len(keys)-1] == "enter" && m.form.drop == nil {
				t.Fatalf("%v: no dropdown opened on %q", keys, m.form.current().kind)
			}
			lines := strings.Split(m.render(), "\n")
			if len(lines) != size[1] {
				t.Errorf("%dx%d %v: %d lines", size[0], size[1], keys, len(lines))
			}
			for i, l := range lines {
				if w := width(l); w != size[0] {
					t.Errorf("%dx%d %v: line %d is %d wide: %q", size[0], size[1], keys, i, w, l)
				}
			}
		}
	}
}

func TestFormBuildsTheTask(t *testing.T) {
	var launched []NewTask
	m := newModel(formOptions(task.NewStore(t.TempDir()), &launched))
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	press(m, "enter") // no tasks: enter opens the form
	f := m.form
	if f == nil {
		t.Fatal("enter on the welcome screen should open the form")
	}
	if f.request(); f.err != "" {
		t.Fatal("request should not set err")
	}
	if _, err := f.request(); err == nil || !strings.Contains(err.Error(), "prompt") {
		t.Errorf("an empty prompt should fail: %v", err)
	}
	m.Update(tea.PasteMsg{Content: "Add DELETE /todos/{id}\nwith tests"})
	press(m, "tab", "tab", "tab", "tab", "tab", "space") // name → workspace → repo → branch → mode: parallel
	f.focusOn(field{"role", 1})
	press(m, "enter", "down", "down", "space", "enter") // code_reviewer + tech_writer
	press(m, "right", "enter", "down", "enter")         // provider: gemini
	press(m, "ctrl+n")
	if len(f.agents) != 3 || f.agents[2].provider != "gemini" {
		t.Fatalf("a new row should copy the last provider: %+v", f.agents)
	}
	press(m, "ctrl+x")
	f.focusOn(field{"effort", 0})
	press(m, "enter", "down", "down", "enter") // low → high

	req, err := f.request()
	if err != nil {
		t.Fatal(err)
	}
	if req.Prompt != "Add DELETE /todos/{id}\nwith tests" || !req.Parallel || req.Workspace != "~/code/todo-api" || len(req.Agents) != 2 {
		t.Fatalf("request = %+v", req)
	}
	want := []NewAgent{
		{Role: "backend_dev", Provider: "claude", Model: "claude-opus-5-5", Effort: "high"},
		{Role: "code_reviewer+tech_writer", Provider: "gemini", Model: "gemini-3.1-pro-preview"},
	}
	for i, a := range req.Agents {
		if a != want[i] {
			t.Errorf("agent %d = %+v, want %+v", i, a, want[i])
		}
	}
	if f.groups() != 2 {
		t.Errorf("groups = %d", f.groups())
	}

	// The name is optional, tidied, and checked.
	f.name = newInput("  Delete   todos ")
	if r, err := f.request(); err != nil || r.Name != "Delete todos" {
		t.Errorf("named request = %+v, %v", r, err)
	}
	f.name = newInput(strings.Repeat("x", task.NameMax+1))
	if _, err := f.request(); err == nil || f.current().kind != "name" {
		t.Errorf("long name: err %v, focus %v", err, f.current())
	}
	f.name = newInput("")

	// A GitHub repo replaces the workspace (no trust needed); the branch
	// goes along.
	f.repo, f.branch = newInput("acme/web"), newInput("feat/x")
	f.workspace = newInput("/elsewhere")
	if r, err := f.request(); err != nil || r.Repo != "acme/web" || r.Branch != "feat/x" {
		t.Errorf("repo request = %+v, %v", r, err)
	}
	f.repo, f.branch = newInput(""), newInput("")

	// An untrusted workspace needs the box ticked.
	f.workspace = newInput("/elsewhere")
	if _, err := f.request(); err == nil || f.current().kind != "trust" {
		t.Errorf("untrusted workspace: err %v, focus %v", err, f.current())
	}
	press(m, "space")
	cmd := f.launch()
	if cmd == nil {
		t.Fatal("launch should return a command")
	}
	m.Update(cmd())
	if m.form != nil || len(launched) != 1 || !launched[0].Trust {
		t.Errorf("after launch: form %v, launched %+v", m.form != nil, launched)
	}
}

func TestFormOffersLogin(t *testing.T) {
	m := newModel(Options{
		Store: task.NewStore(t.TempDir()),
		Launch: func(NewTask) (string, error) {
			return "", &LoginRequired{Providers: []string{"gemini"}, Err: errors.New("not signed in to gemini")}
		},
		Login:     func([]string) *exec.Cmd { return exec.Command("true") },
		Providers: []ProviderChoice{{Name: "gemini", Models: []string{"g"}}},
		Roles:     []Choice{{Value: "backend_dev"}},
		Defaults:  FormDefaults{Workspace: "/w"},
	})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	press(m, "n")
	m.form.prompt = newInput("x")
	m.Update(m.form.launch()())
	if m.form == nil || len(m.form.login) != 1 || !strings.Contains(m.render(), "ctrl+l") {
		t.Fatalf("expected a sign-in offer, got err %q", m.form.err)
	}
	if cmd, _ := m.form.key("ctrl+l"); cmd == nil {
		t.Error("ctrl+l should hand the terminal to login")
	}
}

func TestBareDashboardOpensForm(t *testing.T) {
	m := newModel(formOptions(task.NewStore(t.TempDir()), new([]NewTask)))
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if !strings.Contains(m.render(), "NEW TASK") {
		t.Error("the welcome screen should offer a NEW TASK button")
	}
	m.Update(tea.MouseClickMsg{X: 60, Y: 20, Button: tea.MouseLeft})
	if m.form == nil {
		t.Error("a click on the welcome screen should open the form")
	}
	if os.Getenv("DASH_SHOW") != "" {
		t.Log("\n" + m.render())
	}
}

func TestResumeDialog(t *testing.T) {
	store := task.NewStore(t.TempDir())
	tk := &task.Task{ID: "ay9886", Prompt: "p", Workspace: "/w", MaxRounds: 10, Round: 1,
		Agents: []task.AgentSpec{{Role: "backend_dev", Provider: "claude", Model: "m"}}}
	store.Create(tk)
	tk.Status, tk.Reason = task.Failed, "claude: out of API credits: Your credit balance is too low to access the Anthropic API."
	store.Save(tk)
	var resumed int
	fail := errors.New("not signed in")
	m := newModel(Options{Store: store, Resume: func(*task.Task, string) error { resumed++; return fail }})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if !strings.Contains(m.render(), "to resume where it left off") {
		t.Error("a failed task should say how to resume it")
	}
	m.key("r")
	m.key("x") // a stray key must not cancel the dialog
	screen := m.render()
	if m.confirm != "resume" || !strings.Contains(screen, "RESUME “p”?") || !strings.Contains(screen, "credit balance") {
		t.Fatalf("expected the resume dialog with the failure reason:\n%s", screen)
	}
	for i, l := range strings.Split(screen, "\n") {
		if width(l) != 100 {
			t.Errorf("line %d is %d wide", i, width(l))
		}
	}
	m.key("enter")
	if resumed != 1 || !strings.Contains(m.render(), "resume failed: not signed in") {
		t.Fatalf("resumed %d times; footer should keep the error", resumed)
	}
	m.flashAt = m.flashAt.Add(-time.Minute)
	if !strings.Contains(m.render(), "resume failed") {
		t.Error("the error should stay until a key is pressed")
	}
	m.key("down")
	if strings.Contains(m.render(), "resume failed") {
		t.Error("a key should dismiss the error")
	}
	m.key("r")
	m.key("esc")
	if resumed != 1 || m.confirm != "" {
		t.Error("esc should cancel")
	}
}

func TestResumeOnClaudeCode(t *testing.T) {
	store := task.NewStore(t.TempDir())
	tk := &task.Task{ID: "ay9886", Prompt: "p", Workspace: "/w", MaxRounds: 10, Round: 1,
		Agents: []task.AgentSpec{{Role: "backend_dev", Provider: "claude", Model: "m"}}}
	store.Create(tk)
	tk.Status, tk.Reason = task.Failed, "backend_dev (claude): claude: out of API credits: Your credit balance is too low."
	store.Save(tk)
	var calls []string
	signedIn := false
	opt := formOptions(store, new([]NewTask))
	opt.Providers = append(opt.Providers, ProviderChoice{Name: "claude-code", Note: "your Claude plan, via local Claude Code"})
	opt.Login = func([]string) *exec.Cmd { return exec.Command("true") }
	opt.Resume = func(_ *task.Task, prov string) error {
		calls = append(calls, prov)
		if !signedIn {
			return &LoginRequired{Providers: []string{prov}, Err: errors.New("claude-code was not verified")}
		}
		return nil
	}
	m := newModel(opt)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.key("r")
	screen := m.render()
	if m.runOn != "claude-code" || !strings.Contains(screen, "◂ claude-code ▸") || !strings.Contains(screen, "start fresh conversations") {
		t.Fatalf("out of credits, the dialog should suggest claude-code:\n%s", screen)
	}
	for i, l := range strings.Split(screen, "\n") {
		if width(l) != 100 {
			t.Errorf("line %d is %d wide", i, width(l))
		}
	}
	m.key("down") // wraps to "as before"
	if m.runOn != "" || !strings.Contains(m.render(), "◂ as before ▸") {
		t.Errorf("runOn = %q", m.runOn)
	}
	m.key("up")
	_, cmd := m.key("enter")
	if cmd == nil || m.resuming == nil || len(calls) != 1 || calls[0] != "claude-code" {
		t.Fatalf("a resume needing sign-in should open it: calls %v", calls)
	}
	signedIn = true
	m.Update(loginDoneMsg{})
	if len(calls) != 2 || calls[1] != "claude-code" || !strings.Contains(m.render(), "resumed p on claude-code") {
		t.Errorf("after sign-in the resume should be retried: calls %v", calls)
	}
}

func TestDeleteTask(t *testing.T) {
	store := task.NewStore(t.TempDir())
	for _, id := range []string{"aaaaaa", "bbbbbb"} {
		tk := &task.Task{ID: id, Prompt: "p", Workspace: "/w", Agents: []task.AgentSpec{{Role: "r", Provider: "claude", Model: "m"}}}
		store.Create(tk)
		tk.Status = task.Failed
		store.Save(tk)
	}
	opt := formOptions(store, new([]NewTask))
	opt.Delete = func(tk *task.Task) error { return store.Delete(tk.ID) }
	m := newModel(opt)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if !strings.Contains(m.render(), "+ NEW TASK") {
		t.Error("the task list should always offer + NEW TASK")
	}
	first := m.current().Task.ID
	m.key("x")
	if m.confirm != "delete" || !m.no {
		t.Fatal("x should ask, with CANCEL selected")
	}
	m.key("enter")
	if len(m.tasks) != 2 {
		t.Fatal("enter on CANCEL must not delete")
	}
	m.key("x")
	m.key("left")
	m.key("enter")
	if len(m.tasks) != 1 || m.current().Task.ID == first {
		t.Fatalf("expected %s deleted, have %d tasks", first, len(m.tasks))
	}
	m.key("x")
	m.key("y")
	if len(m.tasks) != 0 || !strings.Contains(m.render(), "no tasks yet") {
		t.Fatal("deleting the last task should show the empty list")
	}
	m.key("n")
	if m.form == nil {
		t.Error("n should open the form after deleting everything")
	}
}

func TestFormRoleEnterReplaces(t *testing.T) {
	var launched []NewTask
	m := newModel(formOptions(task.NewStore(t.TempDir()), &launched))
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	press(m, "n")
	f := m.form
	f.focusOn(field{"role", 0}) // backend_dev
	press(m, "enter", "down", "enter")
	if got := f.agents[0].role; got != "code_reviewer" {
		t.Errorf("enter on a new role should replace the old one, got %q", got)
	}
	press(m, "enter", "space", "down", "space", "enter") // unmark code_reviewer, mark qa_engineer
	if got := f.agents[0].role; got != "qa_engineer" {
		t.Errorf("space should unmark the current role, got %q", got)
	}
}

func TestFormRemovesEveryAgent(t *testing.T) {
	var launched []NewTask
	m := newModel(formOptions(task.NewStore(t.TempDir()), &launched))
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	press(m, "n")
	f := m.form
	f.focusOn(field{"role", 1})
	press(m, "delete")
	if len(f.agents) != 1 || f.agents[0].role != "backend_dev" {
		t.Fatalf("delete should remove the focused agent: %+v", f.agents)
	}
	press(m, "enter") // focus is on the remaining row's ✕
	if len(f.agents) != 0 || f.current().kind != "add" {
		t.Fatalf("the last agent should be removable: %+v, focus %v", f.agents, f.current())
	}
	f.prompt = newInput("do it")
	if _, err := f.request(); err == nil || !strings.Contains(err.Error(), "agent") {
		t.Errorf("no agents should fail validation: %v", err)
	}
	for _, size := range [][2]int{{80, 22}, {120, 40}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		for i, l := range strings.Split(m.render(), "\n") {
			if w := width(l); w != size[0] {
				t.Errorf("%v: line %d is %d wide", size, i, w)
			}
		}
	}
	press(m, "enter")
	if len(f.agents) != 1 {
		t.Errorf("add should bring a row back: %+v", f.agents)
	}
	// x removes the agent whose role, provider, model or effort is
	// focused, as the ✕ button does.
	press(m, "ctrl+n")
	f.focusOn(field{"effort", 0})
	press(m, "x")
	if len(f.agents) != 1 || f.current() != (field{"remove", 0}) {
		t.Errorf("x on an agent's effort should remove it: %+v, focus %v", f.agents, f.current())
	}
	// In a text field, x is a letter.
	f.focusOn(field{kind: "name"})
	press(m, "x")
	if len(f.agents) != 1 || f.name.String() != "x" {
		t.Errorf("x in a text field should type: agents %d, name %q", len(f.agents), f.name.String())
	}
}

func TestFormHelp(t *testing.T) {
	var launched []NewTask
	m := newModel(formOptions(task.NewStore(t.TempDir()), &launched))
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	press(m, "n", "tab", "tab", "tab")
	if k := m.form.current().kind; k != "repo" {
		t.Fatalf("focused %q, want repo", k)
	}
	if out := ansi.Strip(m.render()); !strings.Contains(out, "owner/name or a github.com URL") {
		t.Errorf("the repo field's tip is missing:\n%s", out)
	}
	// ? is typed into a text field; f1 opens the help from anywhere.
	press(m, "?")
	if m.form.help || m.form.repo.String() != "?" {
		t.Fatalf("? in a text field: help %v, repo %q", m.form.help, m.form.repo.String())
	}
	press(m, "f1")
	out := ansi.Strip(m.render())
	for _, want := range []string{"NEW TASK · HELP", "https://github.com/acme/web", "git@github.com:acme/web.git"} {
		if !strings.Contains(out, want) {
			t.Errorf("the help panel lacks %q:\n%s", want, out)
		}
	}
	press(m, "x") // any key closes it, and does nothing else
	if m.form == nil || m.form.help || m.form.repo.String() != "?" {
		t.Fatal("a key didn't just close the help")
	}
	press(m, "tab", "tab", "?") // ? off a text field (mode) opens it too
	if !m.form.help {
		t.Fatalf("? on %q didn't open the help", m.form.current().kind)
	}
	// At the smallest size it drops the keys, which the footer shows, but
	// nothing is cut off.
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 22})
	out = ansi.Strip(m.render())
	for _, want := range []string{"git@github.com:acme/web.git", "if it's already there.", "the current commit.", "share a cache.", "press any key"} {
		if !strings.Contains(out, want) {
			t.Errorf("at 80x22 the help lacks %q:\n%s", want, out)
		}
	}
}

func TestLegendListsEveryProvider(t *testing.T) {
	var launched []NewTask
	opt := formOptions(fixture(t), &launched)
	for _, p := range []string{"claude-code", "openai", "codex", "cursor", "copilot"} {
		opt.Providers = append(opt.Providers, ProviderChoice{Name: p})
	}
	m := newModel(opt)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 60})
	out := ansi.Strip(m.render())
	for _, p := range []string{"claude-code", "openai", "codex", "cursor", "copilot", "gemini"} {
		if !strings.Contains(out, "● "+p) {
			t.Errorf("the legend lacks %s:\n%s", p, out)
		}
	}
	if strings.Contains(out, "● other") {
		t.Error("the legend still says other")
	}
}
