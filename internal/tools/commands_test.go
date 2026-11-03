package tools

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dethmasque/ackn4th/internal/provider"
	"github.com/dethmasque/ackn4th/internal/sandbox"
	"github.com/dethmasque/ackn4th/internal/workspace"
)

func TestRunCommand(t *testing.T) {
	if err := sandbox.Available(); err != nil {
		t.Skip(err)
	}
	dir := t.TempDir()
	ws, err := workspace.Open(dir, []string{"**"}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	var credited []string
	team := &Team{Command: func(agent int, run func()) []string {
		run()
		credited = append(credited, "gen.txt")
		return []string{"gen.txt"}
	}}
	e := &Executor{WS: ws, Team: team, Self: 1, Commands: &Commands{
		Policy:  CommandPolicy{Timeout: time.Minute},
		Sandbox: sandbox.Config{Workspace: dir, Scratch: filepath.Join(t.TempDir(), "s")},
	}}
	run(t, e, WriteFile, map[string]any{"path": "gen.txt", "content": "old\n"})
	r := run(t, e, RunCommand, map[string]any{"command": "echo built > gen.txt; echo ok; exit 3"})
	if !r.IsError || !strings.HasPrefix(r.Content, "exit 3 · ") || !strings.Contains(r.Content, "ok") || !strings.Contains(r.Content, "files this command changed: gen.txt") {
		t.Errorf("result = %+v", r)
	}
	if len(credited) != 1 {
		t.Error("the command's changes should be credited")
	}
	// The agent must re-read a file a command changed before editing it.
	if r := run(t, e, EditFile, map[string]any{"path": "gen.txt", "old_string": "built", "new_string": "x"}); !r.IsError {
		t.Errorf("edit without re-reading: %+v", r)
	}
	if r := run(t, e, RunCommand, map[string]any{"command": "sleep 5", "timeout_seconds": 1}); !r.IsError || !strings.Contains(r.Content, "timed out") {
		t.Errorf("timeout: %+v", r)
	}
	if r := run(t, &Executor{WS: ws}, RunCommand, map[string]any{"command": "ls"}); !r.IsError {
		t.Error("without Commands, run_command must fail")
	}
}

func TestTodos(t *testing.T) {
	var items []string
	team := &Team{
		Todo:    func(agent int, text string) (int, error) { items = append(items, text); return len(items), nil },
		Resolve: func(agent, id int, note string) error { items[id-1] = "done: " + note; return nil },
	}
	e := &Executor{Team: team, Self: 2}
	if r := run(t, e, AddTodo, map[string]any{"text": "  Rotate the staging API key  "}); r.IsError || !strings.Contains(r.Content, "#1") {
		t.Errorf("add: %+v", r)
	}
	if r := run(t, e, AddTodo, map[string]any{"text": " "}); !r.IsError {
		t.Error("an empty item should be refused")
	}
	if r := run(t, e, ResolveTodo, map[string]any{"id": 1, "note": "the team did it"}); r.IsError || items[0] != "done: the team did it" {
		t.Errorf("resolve: %+v %v", r, items)
	}
	if specs := Specs(SpecOptions{Commands: &CommandPolicy{Timeout: time.Minute}}); !hasTool(specs, RunCommand) || !hasTool(specs, AddTodo) {
		t.Error("specs should list run_command and add_user_todo")
	}
	if hasTool(Specs(SpecOptions{}), RunCommand) {
		t.Error("run_command should be off without a policy")
	}
}

func hasTool(specs []provider.ToolSpec, name string) bool {
	for _, s := range specs {
		if s.Name == name {
			return true
		}
	}
	return false
}
