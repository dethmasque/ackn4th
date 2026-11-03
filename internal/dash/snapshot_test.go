package dash

import (
	"os"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/dethmasque/ackn4th/internal/task"
)

// TestSnapshot writes rendered screens for visual review when
// DASH_SNAPSHOT names an output directory.
func TestSnapshot(t *testing.T) {
	dir := os.Getenv("DASH_SNAPSHOT")
	if dir == "" {
		t.Skip("set DASH_SNAPSHOT=dir to write screens")
	}
	store := fixture(t)
	for name, keys := range map[string][]string{"overview": {"tab"}, "agent": {"tab", "enter"}, "activity": {"2"}, "files": {"3"}, "messages": {"4"}, "help": {"?"}} {
		m := newModel(Options{Store: store, Effects: true})
		m.frame = 37
		m.Update(tea.WindowSizeMsg{Width: 140, Height: 42})
		for _, k := range keys {
			m.key(k)
		}
		os.WriteFile(dir+"/"+name+".ans", []byte(m.render()), 0o644)
	}
	for name, keys := range map[string][]string{
		"welcome":       {},
		"form":          {"n", "A", "d", "d", "space", "r", "a", "t", "e", "space", "l", "i", "m", "i", "t", "s", "tab", "tab", "space"},
		"form-role":     {"n", "tab", "tab", "tab", "tab", "enter", "down", "space", "down", "down", "space"},
		"form-provider": {"n", "tab", "tab", "tab", "tab", "down", "right", "enter", "down"},
		"form-model":    {"n", "ctrl+n", "ctrl+n", "up", "right", "right", "enter"},
	} {
		m := newModel(formOptions(task.NewStore(t.TempDir()), new([]NewTask)))
		m.opt.Effects, m.frame = true, 37
		m.Update(tea.WindowSizeMsg{Width: 140, Height: 42})
		press(m, keys...)
		os.WriteFile(dir+"/"+name+".ans", []byte(m.render()), 0o644)
	}
}
