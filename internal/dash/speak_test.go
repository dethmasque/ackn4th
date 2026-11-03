package dash

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/dethmasque/ackn4th/internal/task"
)

type fakeSpeaker struct{ said []string }

func (f *fakeSpeaker) Say(s string) { f.said = append(f.said, s) }
func (f *fakeSpeaker) Repeat()      {}
func (f *fakeSpeaker) Close()       {}

func (f *fakeSpeaker) last() string {
	if len(f.said) == 0 {
		return ""
	}
	return f.said[len(f.said)-1]
}

func keyMsg(k string) tea.KeyPressMsg {
	switch k {
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "V":
		return tea.KeyPressMsg{Code: 'v', Text: "V", Mod: tea.ModShift}
	}
	return tea.KeyPressMsg{Code: rune(k[0]), Text: k}
}

func TestVoiceReadsTheDashboard(t *testing.T) {
	var launched []NewTask
	opt := formOptions(fixture(t), &launched)
	sp := &fakeSpeaker{}
	saved := []bool{}
	opt.Voice = &Voice{Start: func() (Speaker, error) { return sp, nil }, Save: func(on bool) error { saved = append(saved, on); return nil }}
	m := newModel(opt)
	m.w, m.h = 120, 40

	cmd := m.toggleVoice() // v
	m.Update(cmd())
	if !strings.HasPrefix(sp.last(), "voice on") || !strings.Contains(sp.last(), "Task list, 1 of 1: task Add DELETE") {
		t.Fatalf("on: %q", sp.last())
	}
	steps := []struct{ key, want string }{
		{"tab", "overview tab of task Add DELETE"},
		{"?", "Keys. up, down, j, k: select task"},
		{"esc", "overview tab"},
		{"n", "New task. Prompt, text field: empty"},
		{"h", ""}, // typing isn't echoed
		{"tab", "Task name, text field, empty: it will be called h"},
		{"tab", "Workspace directory: ~/code/todo-api"},
	}
	for _, s := range steps {
		n := len(sp.said)
		m.Update(keyMsg(s.key))
		if s.want == "" {
			if len(sp.said) != n {
				t.Errorf("%s: said %q", s.key, sp.last())
			}
			continue
		}
		if !strings.Contains(sp.last(), s.want) {
			t.Errorf("%s: said %q, want %q", s.key, sp.last(), s.want)
		}
	}
	if len(saved) != 1 || !saved[0] {
		t.Errorf("saved %v", saved)
	}
}

func TestVoiceAnnouncesChanges(t *testing.T) {
	m := newModel(Options{Store: fixture(t)})
	sp := &fakeSpeaker{}
	m.speaker = sp
	tk := m.tasks[0]
	tk.Status = task.Completed
	m.noteChanges(map[string]task.Status{tk.ID: task.Running})
	m.Update(tickMsg{})
	if !strings.Contains(sp.last(), "completed") {
		t.Errorf("said %q", sp.last())
	}
}

// The first-run welcome offers the voice, and v works there.
func TestVoiceOfferedAtWelcome(t *testing.T) {
	for _, size := range [][2]int{{80, 22}, {120, 40}} {
		var launched []NewTask
		opt := formOptions(task.NewStore(t.TempDir()), &launched)
		opt.Setup = &Setup{Offer: true, Status: setupStates}
		sp := &fakeSpeaker{}
		opt.Voice = &Voice{Start: func() (Speaker, error) { return sp, nil }}
		m := newModel(opt)
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m.Update(tickMsg{})
		if m.setup == nil || !m.setup.ask {
			t.Fatal("setup should be offered")
		}
		checkScreen(t, m, size, "to have the dashboard read aloud")
		if testing.Verbose() && size[0] == 80 {
			t.Log("\n" + ansi.Strip(m.render()))
		}
		_, cmd := m.Update(keyMsg("v"))
		m.Update(cmd())
		if m.setup == nil || !strings.Contains(sp.last(), "Set up providers now? Set up selected") {
			t.Errorf("said %q", sp.last())
		}
	}
}
