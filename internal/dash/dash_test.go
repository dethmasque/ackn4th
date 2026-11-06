package dash

import (
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	tea "charm.land/bubbletea/v2"

	"github.com/dethmasque/ackn4th/internal/event"
	"github.com/dethmasque/ackn4th/internal/provider"
	"github.com/dethmasque/ackn4th/internal/task"
)

// fixture creates a parallel task mid-round: one agent using a tool, one
// done with its round, one that declared done.
func fixture(t *testing.T) *task.Store {
	t.Helper()
	store := task.NewStore(t.TempDir())
	tk := &task.Task{ID: "k3f9qz", Prompt: "Add DELETE /todos/{id} with tests, and document it in the README so the API reference stays complete",
		Workspace: "/home/me/todo-api", Mode: task.ModeParallel, MaxRounds: 10, Round: 2, Status: task.Running, PID: os.Getpid(),
		Agents: []task.AgentSpec{
			{Role: "backend_dev", Provider: "claude", Model: "claude-opus-5-5", Effort: "high", CompactAt: 200000, Turns: 2, Context: 43000,
				Usage: provider.Usage{InputTokens: 60000, OutputTokens: 4000, CachedTokens: 45000}},
			{Role: "qa_engineer", Provider: "claude", Model: "claude-opus-5-5", Effort: "high", CompactAt: 200000, Turns: 2, RoundDone: 2},
			{Role: "tech_writer", Provider: "gemini", Model: "gemini-3.1-pro-preview", CompactAt: 200000, Turns: 1, Done: true},
		}}
	if err := store.Create(tk); err != nil {
		t.Fatal(err)
	}
	tk.Status = task.Running // Create records it as pending
	tk.Todos = []task.Todo{{ID: 1, Text: "Decide whether DELETE of a missing todo returns 404 or 204; the API reference doesn't say, and both are common.", By: "backend_dev", Round: 1}}
	store.Save(tk)
	// A live runner holds the task's lock; without it the task reads as stopped.
	release, err := store.Lock(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	now := time.Now()
	at := func(s int) time.Time { return now.Add(time.Duration(s-60) * time.Second) }
	for _, e := range []event.Event{
		{Kind: event.TaskStarted, Text: "start", Round: 1},
		{Kind: event.RoundStarted, Round: 1},
		{Kind: event.TurnStarted, Agent: 1, Role: "backend_dev", Round: 1},
		{Kind: event.ToolCall, Agent: 1, Role: "backend_dev", Tool: "write_file", Text: "store.go (2.1 KB)", Round: 1},
		{Kind: event.Message, Agent: 1, Role: "backend_dev", Text: "Added Delete to the store with a not-found error. QA: please cover the 404.", Round: 1},
		{Kind: event.TurnEnded, Agent: 1, Role: "backend_dev", InTok: 30000, OutTok: 2000, CacheTok: 20000, Round: 1},
		{Kind: event.TurnStarted, Agent: 3, Role: "tech_writer", Round: 1},
		{Kind: event.ToolCall, Agent: 3, Role: "tech_writer", Tool: "edit_file", Text: "README.md", Round: 1},
		{Kind: event.DoneVote, Agent: 3, Role: "tech_writer", Text: "README documents DELETE", Round: 1},
		{Kind: event.TurnEnded, Agent: 3, Role: "tech_writer", InTok: 9000, OutTok: 800, Round: 1},
		{Kind: event.RoundStarted, Round: 2},
		{Kind: event.TurnStarted, Agent: 1, Role: "backend_dev", Round: 2},
		{Kind: event.Context, Agent: 1, Role: "backend_dev", Text: "notes 397 B · 1 changed file(s)", Round: 2},
		{Kind: event.ToolCall, Agent: 1, Role: "backend_dev", Tool: "edit_file", Text: "main.go", Round: 2},
	} {
		if e.Time.IsZero() {
			e.Time = at(len(e.Text))
		}
		store.AppendEvent(tk.ID, e)
	}
	return store
}

func TestRenderEveryView(t *testing.T) {
	store := fixture(t)
	for _, size := range [][2]int{{120, 40}, {80, 22}, {200, 60}} {
		m := newModel(Options{Store: store})
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		for _, keys := range [][]string{{}, {"tab"}, {"enter", "enter"}, {"2"}, {"3"}, {"4"}, {"?"}, {"s"}} {
			m.view, m.help, m.confirm, m.focusMain = vOverview, false, "", false
			for _, k := range keys {
				m.key(k)
			}
			out := m.render()
			lines := strings.Split(out, "\n")
			if len(lines) > size[1] {
				t.Errorf("%dx%d %v: %d lines", size[0], size[1], keys, len(lines))
			}
			for i, l := range lines {
				if w := width(l); w != size[0] {
					t.Errorf("%dx%d %v: line %d is %d wide: %q", size[0], size[1], keys, i, w, l)
				}
			}
			if os.Getenv("DASH_SHOW") != "" && size[0] == 120 {
				t.Logf("keys %v:\n%s", keys, out)
			}
		}
	}
}

func TestAgentPhases(t *testing.T) {
	store := fixture(t)
	m := newModel(Options{Store: store})
	l := m.current()
	if l == nil {
		t.Fatal("no task")
	}
	if a := l.Agents[0]; a.Phase != Tool || a.Activity != "edit_file main.go" || a.Steps != 1 {
		t.Errorf("backend_dev = %v %q steps %d", a.Phase, a.Activity, a.Steps)
	}
	if !l.Agents[2].Voted || l.Agents[2].Phase != Idle {
		t.Errorf("tech_writer should be idle with a done vote: %+v", l.Agents[2])
	}
	if f := l.Files["store.go"]; f == nil || f.Ops != "W" || f.By[0] != "backend_dev" {
		t.Errorf("files = %+v", l.Files)
	}
	if l.Working() != 1 || l.Votes() != 1 {
		t.Errorf("working %d votes %d", l.Working(), l.Votes())
	}
}

func TestLiveView(t *testing.T) {
	store := fixture(t)
	lv, _ := json.Marshal(task.Live{Updated: time.Now(), Phase: "tool", Tool: "write_file", Path: "main.go",
		Preview: "package main\n\nfunc main() {\n\tserve()\n}", Bytes: 5000})
	os.WriteFile(store.LivePath("k3f9qz", 0), lv, 0o600)
	m := newModel(Options{Store: store})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if !strings.Contains(m.render(), "✎ write_file main.go · 4.9 KB so far") {
		t.Error("the agent's card should say what it is writing")
	}
	m.key("tab")
	m.key("enter")
	screen := m.render()
	for _, want := range []string{"LIVE · what the model is writing right now", "func main() {", "    serve()"} {
		if !strings.Contains(screen, want) {
			t.Errorf("agent view lacks %q:\n%s", want, screen)
		}
	}
	for i, l := range strings.Split(screen, "\n") {
		if width(l) != 120 {
			t.Errorf("line %d is %d wide", i, width(l))
		}
	}
	// Between streams there is nothing live to show.
	os.Remove(store.LivePath("k3f9qz", 0))
	m.poll()
	if !strings.Contains(m.render(), "waiting on the model") {
		t.Error("an agent with no live output should say it is waiting")
	}
}

const minColorDistance = 85.0

func colorDistance(c1, c2 rgb) float64 {
	rMean := (float64(c1.r) + float64(c2.r)) / 2.0
	dR := float64(c1.r) - float64(c2.r)
	dG := float64(c1.g) - float64(c2.g)
	dB := float64(c1.b) - float64(c2.b)

	weightR := 2.0 + rMean/256.0
	weightG := 4.0
	weightB := 2.0 + (255.0-rMean)/256.0

	return math.Sqrt(weightR*dR*dR + weightG*dG*dG + weightB*dB*dB)
}

// TestProviderColorsDistinct checks that provider colors are distinct
// enough to the human eye. We are more sensitive to changes to green,
// so it is weighted higher than red and blue.
func TestProviderColorsDistinct(t *testing.T) {
	providers := []string{
		"claude",
		"gemini",
		"claude-code",
		"openai",
		"deepseek",
		"codex",
		"cursor",
		"copilot",
		"unknown-provider",
	}

	for i := 0; i < len(providers); i++ {
		for j := i + 1; j < len(providers); j++ {
			p1, p2 := providers[i], providers[j]
			c1, c2 := providerColor(p1), providerColor(p2)
			if c1 == c2 {
				t.Errorf("%q and %q share the exact same color (%+v)", p1, p2, c1)
				continue
			}

			if dist := colorDistance(c1, c2); dist < minColorDistance {
				t.Errorf(
					"%q (%+v) and %q (%+v) are too visually similar: distance %.1f (minimum %.1f)",
					p1, c1, p2, c2, dist, minColorDistance,
				)
			}

		}
	}
}

func TestForYou(t *testing.T) {
	m := newModel(Options{Store: fixture(t)})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 60})
	screen := ansi.Strip(m.render())
	if !strings.Contains(screen, "FOR YOU · 1 open") || !strings.Contains(screen, "☐ #1  Decide whether DELETE") {
		t.Errorf("the overview should list open items:\n%s", screen)
	}
}
