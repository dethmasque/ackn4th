package dash

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/dethmasque/ackn4th/internal/event"
	"github.com/dethmasque/ackn4th/internal/task"
)

// A failed command, opened from the activity tab, shows why the agent ran
// it, the command in full, and what came back.
func TestActivityDetail(t *testing.T) {
	store := fixture(t)
	now := time.Now()
	for i, e := range []event.Event{
		{Kind: event.Reasoning, Text: "I need three.js; maybe it is installed somewhere on this machine."},
		{Kind: event.ToolCall, Tool: "run_command", Text: `$ find / -name "three*.js"`, Detail: "command: find / -name \"three*.js\" 2>/dev/null | head\ntimeout_seconds: 120"},
		{Kind: event.ToolResult, Tool: "run_command", Text: "killed after 2m00s (timed out)", Error: true, Detail: "killed after 2m00s (timed out)\n\x1b[31m(no output)\x1b[0m"},
		{Kind: event.ToolCall, Tool: "write_file", Text: "index.html (2.9 KB)", Detail: "path: index.html"},
	} {
		e.Agent, e.Role, e.Round, e.Time = 1, "backend_dev", 2, now.Add(time.Duration(i)*time.Second)
		store.AppendEvent("k3f9qz", e)
	}
	m := newModel(Options{Store: store})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.key("2")
	m.key("up") // from the newest (write_file) to the failed command
	if m.follow {
		t.Fatal("moving up should stop following")
	}
	screen := ansi.Strip(m.render())
	if !strings.Contains(screen, "⏎ details") {
		t.Errorf("no hint:\n%s", screen)
	}
	m.key("enter")
	if m.open == nil {
		t.Fatal("enter didn't open the event")
	}
	screen = ansi.Strip(m.render())
	for _, want := range []string{
		"RUN_COMMAND · FAILED",
		"maybe it is installed somewhere", // the reasoning before the call
		`command: find / -name "three*.js" 2>/dev/null | head`,
		"timeout_seconds: 120",
		"RESULT · ✗ failed", "killed after 2m00s (timed out)",
		"(no output)",
	} {
		if !strings.Contains(screen, want) {
			t.Errorf("the detail lacks %q:\n%s", want, screen)
		}
	}
	if strings.Contains(m.render(), "\x1b[31m(no output)") {
		t.Error("the tool's own escape codes reached the screen")
	}
	if s := m.where(); !strings.Contains(s, "Details: run_command · failed") {
		t.Errorf("voice says %q", s)
	}
	m.key("right") // the next event: the write_file call, with no result yet
	if screen = ansi.Strip(m.render()); !strings.Contains(screen, "WRITE_FILE · RUNNING") {
		t.Errorf("right didn't move on:\n%s", screen)
	}
	m.key("esc")
	if m.open != nil || m.view != vActivity {
		t.Fatal("esc should close the detail and stay on the activity tab")
	}
	m.key("G")
	if !m.follow {
		t.Error("G should follow again")
	}
	// Every size still fits.
	m.key("up")
	m.key("enter")
	for _, size := range [][2]int{{80, 22}, {200, 60}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		lines := strings.Split(m.render(), "\n")
		if len(lines) != size[1] {
			t.Errorf("%v: %d lines", size, len(lines))
		}
		for i, l := range lines {
			if w := width(l); w != size[0] {
				t.Errorf("%v: line %d is %d wide", size, i, w)
			}
		}
	}
}

func TestClip(t *testing.T) {
	s := strings.Repeat("a", 5000) + "é" + strings.Repeat("z", 5000)
	got := event.Clip(s, 1000)
	if len(got) > 1100 || !strings.HasPrefix(got, "aaa") || !strings.HasSuffix(got, "zzz") || !strings.Contains(got, "bytes left out") {
		t.Errorf("Clip = %d bytes: %.60q…", len(got), got)
	}
	if event.Clip("short", 1000) != "short" {
		t.Error("Clip changed a short string")
	}
}

// A long prompt is cut on the overview, with a hint; e shows the whole
// task, and long activity lines wrap instead of running off the edge.
func TestExpandAndWrap(t *testing.T) {
	store := fixture(t)
	tk, _ := store.Load("k3f9qz")
	tk.Prompt = strings.Repeat("Make the game look like a Valve title, with real lighting. ", 14) + "THE END."
	tk.Todos = append(tk.Todos, task.Todo{ID: 2, Text: strings.Repeat("Pick a licence for the sound effects before release. ", 4) + "LAST TODO.", By: "qa_engineer", Round: 2})
	store.Save(tk)
	store.AppendEvent(tk.ID, event.Event{Kind: event.Message, Agent: 1, Role: "backend_dev", Round: 2, Time: time.Now(),
		Text: strings.Repeat("Wired the flush timer to the game loop and the camera toggle to C. ", 3) + "HANDOFF END."})

	m := newModel(Options{Store: store})
	for _, size := range [][2]int{{80, 22}, {120, 40}, {200, 60}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m.key("1")
		m.focusMain = true
		screen := ansi.Strip(m.render())
		if strings.Contains(screen, "THE END.") || strings.Contains(screen, "shows it all") {
			t.Errorf("%v: the prompt should be cut, with no hint until it's selected:\n%s", size, screen)
		}
		m.key("up") // from the first agent to the summary
		if screen = ansi.Strip(m.render()); m.agent != -1 || !strings.Contains(screen, "⏎ shows it all") {
			t.Errorf("%v: up didn't select the summary (agent %d):\n%s", size, m.agent, screen)
		}
		if s := m.where(); !strings.Contains(s, "summary is selected") {
			t.Errorf("voice: %q", s)
		}
		m.key("enter")
		if m.open == nil || !m.open.task {
			t.Fatal("enter on the summary didn't open the task")
		}
		m.key("G") // nothing: G isn't a reader key
		for i := 0; i < 20; i++ {
			m.key("down")
		}
		screen = ansi.Strip(m.render())
		if !strings.Contains(screen, "LAST TODO.") {
			t.Errorf("%v: scrolled to the end, the reader lacks the last to-do:\n%s", size, screen)
		}
		m.key("g")
		if screen = ansi.Strip(m.render()); !strings.Contains(screen, "PROMPT") {
			t.Errorf("%v: g should go back to the top:\n%s", size, screen)
		}
		if s := m.where(); !strings.Contains(s, "THE END.") {
			t.Errorf("the voice doesn't read the whole prompt: %q", s)
		}
		m.key("esc")
		if m.open != nil {
			t.Fatal("esc didn't close it")
		}
		m.key("down") // back to the first agent; enter zooms in, as before
		m.key("enter")
		if m.view != vAgent || m.agent != 0 {
			t.Errorf("%v: down then enter should zoom into agent 1, got view %d agent %d", size, m.view, m.agent)
		}
		m.key("esc")

		// The activity tab wraps the long handoff instead of cutting it.
		m.key("2")
		screen = ansi.Strip(m.render())
		if !strings.Contains(screen, "timer to the") {
			t.Errorf("%v: the handoff didn't wrap:\n%s", size, screen)
		}
		lines := strings.Split(m.render(), "\n")
		if len(lines) != size[1] {
			t.Errorf("%v: %d lines", size, len(lines))
		}
		for i, l := range lines {
			if w := width(l); w != size[0] {
				t.Errorf("%v: line %d is %d wide", size, i, w)
			}
		}
	}
}

// An agent CLI's failed tool (no call event of ours) shows its whole
// result, wrapped, not one cut line.
func TestCLIFailureInFull(t *testing.T) {
	store := fixture(t)
	long := "equipment cost HUD uses a placeholder label: expected 'target' but the test found 'par' in src/ui/render.js line 212, " +
		"and the contrast check reports 4.1:1 for the muted text, under the 4.5:1 it needs. THE-END"
	store.AppendEvent("k3f9qz", event.Event{Kind: event.ToolResult, Tool: "bash", Agent: 1, Role: "backend_dev", Round: 2, Time: time.Now(),
		Text: long[:159] + "…", Detail: long, Error: true})
	m := newModel(Options{Store: store})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m.key("2")
	m.key("enter")
	screen := ansi.Strip(m.render())
	if !strings.Contains(screen, "THE-END") || !strings.Contains(screen, "RESULT · ✗ failed") {
		t.Errorf("the result isn't shown in full:\n%s", screen)
	}
	var got []string
	resultText(event.Event{Text: long[:159] + "…", Detail: long}, func(s string) { got = append(got, s) })
	if len(got) != 1 || got[0] != long {
		t.Errorf("the summary is repeated above the full result: %q", got)
	}
	got = nil
	resultText(event.Event{Text: "exit 1 · 0s", Detail: "npm ERR! test failed"}, func(s string) { got = append(got, s) })
	if len(got) != 2 {
		t.Errorf("a summary that says more is dropped: %q", got)
	}
}

// The activity log tells the person's events from ackn4th's: follow-ups
// and messages are "you", and each has its own filter stop.
func TestYouAndAckn4thFilters(t *testing.T) {
	store := fixture(t)
	now := time.Now()
	for i, e := range []event.Event{
		{Kind: event.TaskStarted, Text: "resume"},
		{Kind: event.FollowUp, Text: "Also document DELETE"},
		{Kind: event.ToolCall, Agent: 1, Role: "backend_dev", Tool: "read_file", Text: "api.go"},
		{Kind: event.UserMessage, Text: "remove the old docs too", Detail: "all"},
		{Kind: event.Notice, Text: "spend at 80%"},
	} {
		e.Round, e.Time = 2, now.Add(time.Duration(i)*time.Second)
		store.AppendEvent("k3f9qz", e)
	}
	m := newModel(Options{Store: store})
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	m.key("2")
	l := m.current()
	texts := func() []string {
		var out []string
		for _, e := range m.logEvents(l) {
			out = append(out, e.Text)
		}
		return out
	}
	has := func(want ...string) {
		t.Helper()
		got := strings.Join(texts(), "|")
		for _, w := range want {
			if !strings.Contains(got, w) {
				t.Errorf("filter %d lacks %q: %q", m.filterPos(), w, got)
			}
		}
	}
	only := func(ok func(event.Event) bool) {
		t.Helper()
		for _, e := range m.logEvents(l) {
			if !ok(e) {
				t.Errorf("filter %d shows %s %q", m.filterPos(), e.Kind, e.Text)
			}
		}
	}
	has("resume", "Also document DELETE", "api.go", "remove the old docs too", "spend at 80%")
	for _, line := range []string{"follow-up", "remove the old docs"} {
		for _, e := range m.logEvents(l) {
			if strings.Contains(e.Text, line) || (line == "follow-up" && e.Kind == event.FollowUp) {
				lead, _ := m.eventParts(l, e)
				if !strings.Contains(ansi.Strip(lead), "you") {
					t.Errorf("%q isn't attributed to you: %q", line, ansi.Strip(lead))
				}
			}
		}
	}
	m.key("right")
	has("Also document DELETE", "remove the old docs too")
	only(func(e event.Event) bool { return e.ByUser() })
	if !strings.Contains(ansi.Strip(m.render()), "you") {
		t.Error("the bar should show the you filter")
	}
	m.key("right")
	has("resume", "spend at 80%")
	only(func(e event.Event) bool { return e.Agent == 0 && !e.ByUser() })
	m.key("right")
	if m.src != srcAgent || m.agent != 0 {
		t.Errorf("after ackn4th comes the first agent: src %d agent %d", m.src, m.agent)
	}
	m.key("left")
	m.key("left")
	m.key("left")
	if m.filterPos() != 0 {
		t.Errorf("left cycles back to all: %d", m.filterPos())
	}
	// The messages tab has no you or ackn4th stop.
	m.key("4")
	m.key("right")
	if m.src != srcAgent || m.agent != 0 {
		t.Errorf("messages: right from all is the first agent: src %d agent %d", m.src, m.agent)
	}
	// A narrow screen keeps the hint by showing only the selected stop.
	m.Update(tea.WindowSizeMsg{Width: 90, Height: 30})
	m.key("2")
	m.key("right")
	if bar := ansi.Strip(m.filterBar(l, 60)); !strings.Contains(bar, "you") || !strings.Contains(bar, "2/6") || width(m.filterBar(l, 60)) > 60 {
		t.Errorf("narrow bar = %q", bar)
	}
}
