package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/dethmasque/ackn4th/internal/notes"
	"github.com/dethmasque/ackn4th/internal/provider"
	"github.com/dethmasque/ackn4th/internal/workspace"
)

func run(t *testing.T, e *Executor, name string, input any) Result {
	t.Helper()
	raw, _ := json.Marshal(input)
	return e.Run(context.Background(), provider.ToolCall{ID: "1", Name: name, Input: raw})
}

func TestExecutor(t *testing.T) {
	ws, err := workspace.Open(t.TempDir(), []string{"**"}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	e := &Executor{WS: ws}

	if r := run(t, e, WriteFile, map[string]any{"path": "a/b.txt", "content": "hello\nworld\n"}); r.IsError {
		t.Fatal(r.Content)
	}
	if r := run(t, e, ReadFile, map[string]any{"path": "a/b.txt", "offset": 1}); r.Content != "hello\nworld\n" {
		t.Errorf("read = %q", r.Content)
	}
	if r := run(t, e, EditFile, map[string]any{"path": "a/b.txt", "old_string": "world", "new_string": "there"}); r.IsError {
		t.Error(r.Content)
	}
	if r := run(t, e, SearchFiles, map[string]any{"pattern": "th.re"}); !strings.Contains(r.Content, "a/b.txt:2:") {
		t.Errorf("search = %q", r.Content)
	}
	if r := run(t, e, ListDir, map[string]any{}); !strings.Contains(r.Content, "a/b.txt") {
		t.Errorf("list = %q", r.Content)
	}
	if r := run(t, e, DeclareDone, map[string]any{"summary": "shipped"}); !r.Done || r.DoneSummary != "shipped" {
		t.Errorf("declare_done = %+v", r)
	}
}

func TestInvalidInputIsRejected(t *testing.T) {
	ws, _ := workspace.Open(t.TempDir(), []string{"**"}, 1<<20)
	defer ws.Close()
	e := &Executor{WS: ws}

	// A truncated streamed input must not run.
	r := e.Run(context.Background(), provider.ToolCall{Name: WriteFile, Input: json.RawMessage(`{"path":"x.txt","content":"par`)})
	if !r.IsError {
		t.Error("truncated input executed")
	}
	if r := run(t, e, WriteFile, map[string]any{"path": "x.txt"}); !r.IsError {
		t.Error("missing content accepted")
	}
	if r := run(t, e, ReadFile, map[string]any{"path": "x", "bogus": 1}); !r.IsError {
		t.Error("unknown field accepted")
	}
	if r := run(t, e, "rm_rf", map[string]any{}); !r.IsError {
		t.Error("unknown tool accepted")
	}
}

func TestReadOnlyRoleCannotWrite(t *testing.T) {
	ws, _ := workspace.Open(t.TempDir(), nil, 1<<20)
	defer ws.Close()
	e := &Executor{WS: ws}
	if r := run(t, e, WriteFile, map[string]any{"path": "x.go", "content": "package x\n"}); !r.IsError {
		t.Fatalf("read-only role wrote a file: %q", r.Content)
	}
}

func TestRereadsAreNotResent(t *testing.T) {
	ws, _ := workspace.Open(t.TempDir(), []string{"**"}, 1<<20)
	defer ws.Close()
	e := &Executor{WS: ws}
	run(t, e, WriteFile, map[string]any{"path": "x.go", "content": "package x\n"})
	if r := run(t, e, ReadFile, map[string]any{"path": "./x.go"}); !strings.Contains(r.Content, "unchanged") {
		t.Fatalf("re-read of a just-written file = %q", r.Content)
	}
	run(t, e, EditFile, map[string]any{"path": "x.go", "old_string": "x", "new_string": "y"})
	if r := run(t, e, ReadFile, map[string]any{"path": "x.go"}); r.Content != "package y\n" {
		t.Fatalf("read after edit = %q", r.Content)
	}
	if r := run(t, e, ReadFile, map[string]any{"path": "x.go"}); !strings.Contains(r.Content, "unchanged") {
		t.Fatalf("second read = %q", r.Content)
	}
	e.Forget()
	if r := run(t, e, ReadFile, map[string]any{"path": "x.go"}); r.Content != "package y\n" {
		t.Fatalf("read after Forget = %q", r.Content)
	}
}

func TestUpdateNotes(t *testing.T) {
	ws, _ := workspace.Open(t.TempDir(), nil, 1<<20)
	defer ws.Close()
	book := notes.Open(t.TempDir(), "/proj")
	e := &Executor{WS: ws, Notes: book, Author: "code_reviewer (gemini)"}
	if r := run(t, e, UpdateNotes, map[string]any{"section": "Gotchas", "content": "tests need TZ=UTC"}); r.IsError {
		t.Fatal(r.Content)
	}
	text, _ := book.Read()
	if !strings.Contains(text, "## Gotchas") || !strings.Contains(text, "code_reviewer (gemini)") {
		t.Fatalf("notes = %q", text)
	}
	if r := run(t, &Executor{WS: ws}, UpdateNotes, map[string]any{"section": "a", "content": "b"}); !r.IsError {
		t.Fatal("update without a notes book should fail")
	}
}

func TestNoBlindOrStaleWrites(t *testing.T) {
	dir := t.TempDir()
	open := func() *workspace.Workspace {
		ws, _ := workspace.Open(dir, []string{"**"}, 1<<20)
		t.Cleanup(func() { ws.Close() })
		return ws
	}
	team := &Team{Members: []string{"backend_dev", "frontend_dev"}}
	var changes []string
	team.Changed = func(agent int, p string) { changes = append(changes, fmt.Sprint(agent, p)) }
	a := &Executor{WS: open(), Team: team, Self: 1}
	b := &Executor{WS: open(), Team: team, Self: 2}

	run(t, a, WriteFile, map[string]any{"path": "api.go", "content": "v1\n"})
	if r := run(t, b, WriteFile, map[string]any{"path": "api.go", "content": "mine\n"}); !r.IsError || !strings.Contains(r.Content, "read it first") {
		t.Fatalf("blind overwrite allowed: %q", r.Content)
	}
	run(t, b, ReadFile, map[string]any{"path": "api.go"})
	run(t, a, EditFile, map[string]any{"path": "api.go", "old_string": "v1", "new_string": "v2"})
	r := run(t, b, EditFile, map[string]any{"path": "api.go", "old_string": "v", "new_string": "w"})
	if !r.IsError || !strings.Contains(r.Content, "changed since you last read it (by backend_dev)") {
		t.Fatalf("stale edit allowed: %q", r.Content)
	}
	// A diff of the change brings b up to date without a re-read.
	b.Acknowledge("api.go", "v1\n", "v2\n")
	if r := run(t, b, EditFile, map[string]any{"path": "api.go", "old_string": "v2", "new_string": "v3"}); r.IsError {
		t.Fatal(r.Content)
	}
	// Windowed reads count as knowing the file.
	run(t, a, ReadFile, map[string]any{"path": "api.go", "offset": 1, "limit": 1})
	if r := run(t, a, DeletePath, map[string]any{"path": "api.go"}); r.IsError {
		t.Fatal(r.Content)
	}
	if strings.Join(changes, ",") != "1api.go,1api.go,2api.go,1api.go" {
		t.Fatalf("changes = %v", changes)
	}
}

func TestMessageTeammate(t *testing.T) {
	ws, _ := workspace.Open(t.TempDir(), nil, 1<<20)
	defer ws.Close()
	var got []string
	team := &Team{Members: []string{"backend_dev", "qa_engineer", "tech_writer"},
		Send: func(from, to int, text string) error {
			got = append(got, fmt.Sprint(from, "→", to, ":", text))
			return nil
		}}
	e := &Executor{WS: ws, Team: team, Self: 1}
	run(t, e, MessageTeam, map[string]any{"to": "QA_Engineer", "message": "tests in api_test.go"})
	run(t, e, MessageTeam, map[string]any{"to": "all", "message": "renaming User"})
	if strings.Join(got, "|") != "1→2:tests in api_test.go|1→2:renaming User|1→3:renaming User" {
		t.Fatalf("sent %v", got)
	}
	if r := run(t, e, MessageTeam, map[string]any{"to": "backend_dev", "message": "me"}); !r.IsError {
		t.Fatal("messaging yourself should fail")
	}
}
