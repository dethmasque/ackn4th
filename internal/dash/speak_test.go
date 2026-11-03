package dash

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/dethmasque/ackn4th/internal/event"
	"github.com/dethmasque/ackn4th/internal/task"
	"github.com/dethmasque/ackn4th/internal/tips"
	"github.com/dethmasque/ackn4th/internal/voice"
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
	case "alt+w":
		return tea.KeyPressMsg{Code: 'w', Mod: tea.ModAlt}
	case "f4":
		return tea.KeyPressMsg{Code: tea.KeyF4}
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
	// Turning it on says how to use it, then what is selected.
	for _, want := range []string{"Voice on.", "Press w", "i to hear more", "o to hear what you can do", "Question mark opens help, which lists every key", "F2, F3 and F4", "Task 1 of 1: Add DELETE"} {
		if !strings.Contains(sp.last(), want) {
			t.Fatalf("on: want %q in %q", want, sp.last())
		}
	}
	steps := []struct{ key, want, not string }{
		{"down", "Task 1 of 1", "Round"}, // moving says only what is selected
		{"tab", "overview tab of task Add DELETE", "Round"},
		{"w", "The overview tab of task Add DELETE", ""},
		{"i", "Its latest handoff", ""},
		{"o", "Enter zooms into the agent", ""},
		{"o", "s stops it", "r resumes"},
		{"?", "Keys. up, down, j, k: select a task", ""},
		{"w", "The keys panel", ""}, // answered without closing it
		{"esc", "Agent 1 of 3", "Keys."},
		{"n", "New task. Prompt, text field: empty", "Tab moves"},
		{"h", "", ""}, // typing isn't echoed
		{"w", "", ""}, // nor are letters that are voice keys elsewhere
		{"alt+w", "The new task form. New task. Prompt, text field: hw", ""},
		{"f4", "Type to edit", ""},
		{"tab", "Task name, text field, empty: it will be called hw", ""},
		{"tab", "Workspace directory: ~/code/todo-api", ""},
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
		if len(sp.said) != n+1 {
			t.Errorf("%s: said %d phrases, want 1: %q", s.key, len(sp.said)-n, sp.said[n:])
		}
		if !strings.Contains(sp.last(), s.want) || s.not != "" && strings.Contains(sp.last(), s.not) {
			t.Errorf("%s: said %q, want %q (and not %q)", s.key, sp.last(), s.want, s.not)
		}
	}
	if !m.help && m.form == nil {
		t.Error("the form should be open")
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

// With the voice alone you can tell what a task is: the list and the task
// say its state, open to-dos and prompt, and a tip on screen comes after
// that instead of in place of it.
func TestVoiceSummarizesTasks(t *testing.T) {
	t.Setenv(tips.Env, "")
	var launched []NewTask
	opt := formOptions(fixture(t), &launched)
	opt.Tips = tips.Load(t.TempDir(), true)
	opt.Tips.Done("dash.welcome")
	m := newModel(opt)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	sp := &fakeSpeaker{}
	m.speaker = sp
	if _, _, ok := m.tip(); !ok {
		t.Fatal("expected the task-list tip on screen")
	}

	m.Update(keyMsg("i"))
	said := sp.last()
	for _, want := range []string{"Task Add DELETE", "Round 1 of 10", "1 open to-do", "Decide whether DELETE", "Prompt: Add DELETE"} {
		if !strings.Contains(said, want) {
			t.Errorf("task list: want %q in %q", want, said)
		}
	}
	if i, j := strings.Index(said, "Prompt:"), strings.Index(said, "Tip:"); j < 0 || i > j {
		t.Errorf("the tip should follow the description: %q", said)
	}

	opt.Tips.Done("dash.tasks")
	m.Update(keyMsg("tab"))
	if !strings.Contains(sp.last(), "overview tab of task Add DELETE") || strings.Contains(sp.last(), "Round 1 of 10") {
		t.Errorf("entering the task should name it, briefly: %q", sp.last())
	}
	m.Update(keyMsg("j"))
	if strings.Contains(sp.last(), "overview tab") || !strings.Contains(sp.last(), "Agent 2 of") {
		t.Errorf("moving within the tab should say only the selection: %q", sp.last())
	}
	m.Update(keyMsg("i"))
	if !strings.Contains(sp.last(), "Agent 2 of 3, qa_engineer on claude") {
		t.Errorf("i should say all about the agent: %q", sp.last())
	}
	m.Update(keyMsg("4"))
	if sp.last() != "messages tab of task Add DELETE /todos/{id} with tests, and…, running." {
		t.Errorf("messages tab: %q", sp.last())
	}
	m.Update(keyMsg("i"))
	if !strings.Contains(sp.last(), "handed off") && !strings.Contains(sp.last(), "No handoffs") {
		t.Errorf("i on the messages tab: %q", sp.last())
	}
}

func TestExcerpt(t *testing.T) {
	got := excerpt("## Done\n\nAdded `DELETE` and **tests**. "+strings.Repeat("word ", 100), 60)
	if strings.ContainsAny(got, "#`*\n") || !strings.HasSuffix(got, "and more.") || len(got) > 80 {
		t.Errorf("%q", got)
	}
	if excerpt("short", 60) != "short." {
		t.Error(excerpt("short", 60))
	}
}

// Keys move only within the focused pane: on the task list, ←→ don't pick
// agents in the task's flow.
func TestArrowsStayInFocusedPane(t *testing.T) {
	m := newModel(Options{Store: fixture(t)})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if m.focusMain {
		t.Fatal("the task list should have focus on load")
	}
	start := m.agent
	for _, k := range []tea.KeyPressMsg{{Code: tea.KeyRight}, {Code: tea.KeyLeft}, {Code: 'l', Text: "l"}} {
		m.Update(k)
		if m.agent != start || m.focusMain {
			t.Fatalf("%s moved to agent %d (focus on task: %v)", k, m.agent, m.focusMain)
		}
	}
	if footer := ansi.Strip(m.footer()); strings.Contains(footer, "agent") {
		t.Errorf("the list's footer offers agent keys: %q", footer)
	}
	m.Update(keyMsg("tab"))
	m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if m.agent == start {
		t.Error("in the task's pane, → should pick the next agent")
	}
}

// The voice says an event in words: a clock time as people say it, the
// round, the whole role, and numbers with their units.
func TestSpokenEvent(t *testing.T) {
	m := newModel(Options{Store: fixture(t)})
	l := m.current()
	l.Task.Agents[0].Role = "software_architect"
	at := time.Date(2026, 11, 3, 12, 7, 5, 0, time.Local)
	ended := event.Event{Time: at, Kind: event.TurnEnded, Round: 4, Agent: 1, InTok: 2140, CacheTok: 856, OutTok: 812, CtxTok: 30500}
	got := m.spokenEvent(l, ended, false)
	want := "at 12:07 PM, in round 4, software_architect's turn ended. It read about 2100 tokens of input, 40 percent of it from the cache, and wrote 812 tokens."
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	full := m.spokenEvent(l, ended, true)
	for _, w := range []string{"ago", "(agent 1, on ", "Its context held about 30500 tokens."} {
		if !strings.Contains(full, w) {
			t.Errorf("full: want %q in %q", w, full)
		}
	}
	call := event.Event{Time: at, Kind: event.ToolCall, Round: 4, Agent: 1, Tool: "read_file", Text: "main.go"}
	if got := m.spokenEvent(l, call, false); got != "at 12:07 PM, in round 4, software_architect used the tool read_file, on main.go." {
		t.Errorf("%q", got)
	}
	if got := voice.Normalize(m.spokenEvent(l, call, false)); !strings.Contains(got, "twelve oh seven P M") || !strings.Contains(got, "software architect used the tool read file") {
		t.Errorf("spoken: %q", got)
	}
}
