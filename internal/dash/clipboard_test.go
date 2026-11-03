package dash

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/dethmasque/ackn4th/internal/event"
)

// run carries out a command the way Bubble Tea would, feeding what it
// returns back to the model.
func run(m *model, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			run(m, c)
		}
	case copiedMsg:
		m.Update(msg)
	}
}

func TestCopying(t *testing.T) {
	store := fixture(t)
	store.AppendEvent("k3f9qz", event.Event{Kind: event.ToolCall, Agent: 1, Role: "backend_dev", Round: 2, Time: time.Now(),
		Tool: "run_command", Text: "$ go test ./...", Detail: "command: go test ./... -run TestDelete"})
	var clip []string
	m := newModel(Options{Store: store, Copy: func(s string) error { clip = append(clip, s); return nil }})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	send := func(msg tea.Msg) { _, cmd := m.Update(msg); run(m, cmd) }
	last := func() string {
		t.Helper()
		if len(clip) == 0 {
			t.Fatal("nothing was copied")
		}
		return clip[len(clip)-1]
	}

	// c on an activity line copies it with its details, in full.
	m.key("2")
	send(tea.KeyPressMsg{Code: 'c', Text: "c"})
	if got := last(); !strings.Contains(got, "run_command") || !strings.Contains(got, "command: go test ./... -run TestDelete") {
		t.Errorf("c copied %q", got)
	}
	if !strings.Contains(m.flash, "copied the event") {
		t.Errorf("flash = %q", m.flash)
	}

	// The open panel has a ⧉ copy button.
	m.key("enter")
	m.render()
	b := m.copyButton
	if b.w == 0 {
		t.Fatal("no copy button on the panel")
	}
	n := len(clip)
	send(tea.MouseClickMsg{X: b.x + 1, Y: b.y, Button: tea.MouseLeft})
	if len(clip) != n+1 || !strings.Contains(last(), "TestDelete") {
		t.Errorf("the button didn't copy the event: %q", last())
	}
	m.key("esc")

	// Dragging selects, highlights, and copies on release: text from the
	// panel it started in, without borders or the panel beside it.
	screen := strings.Split(ansi.Strip(m.render()), "\n")
	y := -1
	for i, l := range screen {
		if strings.Contains(l, "ROUND 2") {
			y = i
		}
	}
	if y < 0 {
		t.Fatal("no ROUND 2 line on screen")
	}
	leftW := min(36, max(28, m.w/4))
	send(tea.MouseClickMsg{X: leftW + 1, Y: y, Button: tea.MouseLeft})
	send(tea.MouseMotionMsg{X: m.w - 1, Y: y + 1, Button: tea.MouseLeft})
	if !strings.Contains(m.render(), selection.bg()) {
		t.Error("the selection isn't highlighted")
	}
	send(tea.MouseReleaseMsg{X: m.w - 1, Y: y + 1, Button: tea.MouseLeft})
	got := last()
	if !strings.Contains(got, "ROUND 2") || strings.Count(got, "\n") != 1 || strings.ContainsAny(got, "│╮") {
		t.Errorf("drag copied %q", got)
	}
	// ctrl+c copies the selection again instead of quitting.
	n = len(clip)
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("ctrl+c did nothing")
	}
	if msg := cmd(); msg == tea.Quit() {
		t.Fatal("ctrl+c quit with a selection on screen")
	}
	run(m, cmd)
	if len(clip) != n+1 || last() != got {
		t.Errorf("ctrl+c copied %q", last())
	}
	// Any other key clears it.
	m.key("f")
	m.Update(tea.KeyPressMsg{Code: 'f', Text: "f"})
	if m.drag != nil {
		t.Error("a key should clear the selection")
	}

	// A right-click copies the line under the pointer.
	send(tea.MouseClickMsg{X: leftW + 5, Y: y, Button: tea.MouseRight})
	if got := last(); !strings.Contains(got, "ROUND 2") || strings.Contains(got, "\n") || strings.ContainsAny(got, "│") {
		t.Errorf("right-click copied %q", got)
	}

	// A plain click still clicks, and selects nothing.
	send(tea.MouseClickMsg{X: 5, Y: y, Button: tea.MouseLeft})
	send(tea.MouseReleaseMsg{X: 5, Y: y, Button: tea.MouseLeft})
	if m.drag != nil {
		t.Error("a click left a selection")
	}

	// Without a clipboard tool it says where the text went.
	m.Update(copiedMsg{what: "1 line", err: errNoClipboard})
	if !strings.Contains(m.flash, "terminal's clipboard") {
		t.Errorf("flash = %q", m.flash)
	}
}
