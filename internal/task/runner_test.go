package task

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dethmasque/ackn4th/internal/provider"
	"github.com/dethmasque/ackn4th/internal/role"
)

// script is a fake model: each SendText/SendToolResults pops the next reply.
type script struct {
	name    string
	replies []*provider.Reply
	inputs  *[]string
	// hooks[i], if set, runs while the model "generates" reply i.
	hooks map[int]func()
	// systems and tools record each session's prompt; prewarms counts
	// Prewarm calls.
	systems  []provider.System
	tools    [][]provider.ToolSpec
	prewarms int

	mu sync.Mutex // sessions of one script may run in parallel
}

func (s *script) Name() string                     { return s.name }
func (s *script) Model() string                    { return "fake" }
func (s *script) Verify(ctx context.Context) error { return nil }
func (s *script) Prewarm(context.Context, provider.System, []provider.ToolSpec) (provider.Usage, error) {
	s.prewarms++
	return provider.Usage{InputTokens: 1000}, nil
}
func (s *script) NewSession(sys provider.System, tools []provider.ToolSpec) provider.Session {
	s.systems = append(s.systems, sys)
	s.tools = append(s.tools, tools)
	return &fakeSession{s: s}
}
func (s *script) RestoreSession(_ provider.System, _ []provider.ToolSpec, raw json.RawMessage) (provider.Session, error) {
	fs := &fakeSession{s: s}
	return fs, json.Unmarshal(raw, &fs.st)
}

type fakeState struct {
	Pos     int                 `json:"pos"`
	Pending []provider.ToolCall `json:"pending"`
}

type fakeSession struct {
	s  *script
	st fakeState
}

func (f *fakeSession) next(in string) (*provider.Reply, error) {
	f.s.mu.Lock()
	*f.s.inputs = append(*f.s.inputs, in)
	f.s.mu.Unlock()
	if f.st.Pos >= len(f.s.replies) {
		return nil, errors.New("script exhausted")
	}
	r := f.s.replies[f.st.Pos]
	if h := f.s.hooks[f.st.Pos]; h != nil {
		h()
	}
	f.st.Pos++
	f.st.Pending = r.ToolCalls
	return r, nil
}
func (f *fakeSession) SendText(_ context.Context, text string, _ provider.Stream) (*provider.Reply, error) {
	return f.next(text)
}
func (f *fakeSession) SendToolResults(_ context.Context, rs []provider.ToolResult, extra string, _ provider.Stream) (*provider.Reply, error) {
	var parts []string
	for _, r := range rs {
		parts = append(parts, r.Content)
	}
	in := "results: " + strings.Join(parts, " | ")
	if extra != "" {
		in += "\n" + extra
	}
	return f.next(in)
}
func (f *fakeSession) PendingToolCalls() []provider.ToolCall { return f.st.Pending }
func (f *fakeSession) State() (json.RawMessage, error)       { return json.Marshal(f.st) }

func call(name string, input any) provider.ToolCall {
	raw, _ := json.Marshal(input)
	return provider.ToolCall{ID: name, Name: name, Input: raw}
}

func toolReply(calls ...provider.ToolCall) *provider.Reply {
	return &provider.Reply{ToolCalls: calls, StopReason: provider.StopToolUse}
}

func textReply(s string) *provider.Reply {
	return &provider.Reply{Text: s, StopReason: provider.StopEnd}
}

func setup(t *testing.T, scripts map[string]*script, agents ...AgentSpec) (*Runner, *Task) {
	t.Helper()
	data := t.TempDir()
	root := filepath.Join(data, "ws")
	os.MkdirAll(root, 0o755)
	roles, err := role.Load()
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore(data)
	tk := &Task{ID: NewID(), Prompt: "build it", Workspace: root, Agents: agents, MaxRounds: 5}
	if err := store.Create(tk); err != nil {
		t.Fatal(err)
	}
	r := &Runner{
		Store: store, Roles: roles, AllowedRoots: []string{root}, MaxSteps: 10, MaxFileBytes: 1 << 20,
		NewProvider: func(spec AgentSpec) (provider.Provider, error) { return scripts[spec.Provider], nil },
	}
	return r, tk
}

func TestDuoReachesConsensus(t *testing.T) {
	var claudeIn, geminiIn []string
	scripts := map[string]*script{
		"claude": {name: "claude", inputs: &claudeIn, replies: []*provider.Reply{
			toolReply(call("write_file", map[string]string{"path": "main.go", "content": "package main\n"})),
			toolReply(call("add_user_todo", map[string]string{"text": "Pick a license for the project."})),
			textReply("wrote main.go, please review"),
			// round 2: declares done
			toolReply(call("declare_done", map[string]string{"summary": "main.go exists"})),
			textReply("done from my side"),
			// round 3: done again
			toolReply(call("declare_done", map[string]string{"summary": "still done"})),
			textReply("agreed"),
		}},
		"gemini": {name: "gemini", inputs: &geminiIn, replies: []*provider.Reply{
			textReply("looks thin, add a comment"), // round 1: concerns
			textReply("hmm, one more look"),        // round 2: no vote, resets claude's
			toolReply(call("declare_done", map[string]string{"summary": "reviewed"})),
			textReply("ship it"),
		}},
	}
	r, tk := setup(t, scripts,
		AgentSpec{Role: "backend_dev", Provider: "claude"},
		AgentSpec{Role: "code_reviewer", Provider: "gemini"})

	got, err := r.Run(context.Background(), tk.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != Completed {
		t.Fatalf("status = %s (%s)", got.Status, got.Reason)
	}
	if got.Round != 4 || got.Agents[0].Turns != 3 || got.Agents[1].Turns != 3 {
		t.Errorf("round=%d turns=%d/%d", got.Round, got.Agents[0].Turns, got.Agents[1].Turns)
	}
	if _, err := os.Stat(filepath.Join(tk.Workspace, "main.go")); err != nil {
		t.Error("agent's file was not written to the workspace")
	}
	if len(got.OpenTodos()) != 1 || got.Todos[0].By != "backend_dev" {
		t.Errorf("todos = %+v", got.Todos)
	}
	if !strings.Contains(geminiIn[0], "#1 (backend_dev): Pick a license") {
		t.Errorf("teammates should see the open items: %q", geminiIn[0])
	}
	sum, _ := os.ReadFile(r.Store.SummaryPath(tk.ID))
	for _, want := range []string{"· completed", "## For you (1 open)", "**#1** Pick a license", "`main.go` (backend_dev, claude)", "Declared done: still done"} {
		if !strings.Contains(string(sum), want) {
			t.Errorf("summary.md lacks %q:\n%s", want, sum)
		}
	}
	if !strings.Contains(geminiIn[0], "# Task") || !strings.Contains(geminiIn[0], "wrote main.go") {
		t.Errorf("reviewer kickoff missing task or handoff: %q", geminiIn[0])
	}
	if strings.Contains(claudeIn[2], "# Task") {
		t.Error("task prompt repeated after kickoff")
	}
	if !strings.Contains(got.Reason, "[code_reviewer] reviewed") {
		t.Errorf("summary = %q", got.Reason)
	}
}

func TestRoundLimitAndResume(t *testing.T) {
	var in []string
	scripts := map[string]*script{"claude": {name: "claude", inputs: &in, replies: []*provider.Reply{
		textReply("step one"),
		textReply("step two"),
		toolReply(call("declare_done", map[string]string{"summary": "all done"})),
		textReply("bye"),
	}}}
	r, tk := setup(t, scripts, AgentSpec{Role: "tech_lead", Provider: "claude"})
	tk.MaxRounds = 2
	r.Store.Save(tk)

	got, err := r.Run(context.Background(), tk.ID, false)
	if err != nil || got.Status != Stopped || !strings.Contains(got.Reason, "round limit") {
		t.Fatalf("status=%s reason=%q err=%v", got.Status, got.Reason, err)
	}
	got.MaxRounds = 3
	r.Store.Save(got)
	got, err = r.Run(context.Background(), tk.ID, false)
	if err != nil || got.Status != Completed || got.Reason != "all done" {
		t.Fatalf("after resume: status=%s reason=%q err=%v", got.Status, got.Reason, err)
	}
}

func TestResumeAnswersPendingToolCalls(t *testing.T) {
	var in []string
	s := &script{name: "claude", inputs: &in, replies: []*provider.Reply{
		toolReply(call("write_file", map[string]string{"path": "a.txt", "content": "x"})),
		// The script runs out here, simulating a failure mid-turn.
	}}
	r, tk := setup(t, map[string]*script{"claude": s}, AgentSpec{Role: "tech_lead", Provider: "claude"})
	got, err := r.Run(context.Background(), tk.ID, false)
	if err == nil || got.Status != Failed {
		t.Fatalf("want failure, got %s %v", got.Status, err)
	}
	if !got.Resumable() {
		t.Fatal("failed task should be resumable")
	}

	// Provide the rest of the script and resume: the model gets the pending
	// tool results first, not the task prompt again.
	s.replies = append(s.replies,
		toolReply(call("read_file", map[string]string{"path": "a.txt"})),
		toolReply(call("declare_done", map[string]string{"summary": "ok"})),
		textReply("done"))
	got, err = r.Run(context.Background(), tk.ID, false)
	if err != nil || got.Status != Completed {
		t.Fatalf("after resume: %s %v", got.Status, err)
	}
	// The pending write is re-run. a.txt was written before the failure and
	// this process has not read it, so the write is refused rather than
	// overwriting blindly; the model reads the file and carries on.
	if len(in) != 5 || !strings.Contains(in[2], "results: Error: a.txt already exists") {
		t.Errorf("resume did not answer the pending call first: %q", in)
	}
}

func TestStoreFind(t *testing.T) {
	s := NewStore(t.TempDir())
	a := &Task{ID: "abc123", Agents: []AgentSpec{{}}}
	b := &Task{ID: "abd456", Agents: []AgentSpec{{}}}
	s.Create(a)
	s.Create(b)
	if got, err := s.Find("abc"); err != nil || got.ID != "abc123" {
		t.Errorf("Find(abc) = %v, %v", got, err)
	}
	if _, err := s.Find("ab"); err == nil {
		t.Error("ambiguous prefix accepted")
	}
	if _, err := s.Find("zzz"); err == nil {
		t.Error("missing task found")
	}
}

func TestExpiredSessionStopsMidTurn(t *testing.T) {
	var in []string
	s := &script{name: "claude", inputs: &in, replies: []*provider.Reply{
		toolReply(call("write_file", map[string]string{"path": "a.txt", "content": "x"})),
		toolReply(call("declare_done", map[string]string{"summary": "ok"})),
		textReply("done"),
	}}
	r, tk := setup(t, map[string]*script{"claude": s}, AgentSpec{Role: "tech_lead", Provider: "claude"})
	requests := 0
	r.Authorize = func(string) error {
		requests++
		if requests > 1 {
			return errors.New("session expired")
		}
		return nil
	}
	got, err := r.Run(context.Background(), tk.ID, false)
	if err != nil || got.Status != Stopped || !strings.Contains(got.Reason, "session expired") {
		t.Fatalf("status=%s reason=%q err=%v", got.Status, got.Reason, err)
	}
	if len(in) != 1 {
		t.Fatalf("a request was sent after expiry: %q", in)
	}

	r.Authorize = nil // logged in again
	if got, err = r.Run(context.Background(), tk.ID, false); err != nil || got.Status != Completed {
		t.Fatalf("after re-login: %s %v", got.Status, err)
	}
}

func TestAgentsCatchUp(t *testing.T) {
	var claudeIn, geminiIn []string
	scripts := map[string]*script{
		"claude": {name: "claude", inputs: &claudeIn, replies: []*provider.Reply{
			toolReply(
				call("write_file", map[string]string{"path": "main.go", "content": "package main\n"}),
				call("update_notes", map[string]string{"section": "Build and test", "content": "go test ./..."}),
			),
			textReply("wrote main.go"),
			textReply("saw the review"),
		}},
		"gemini": {name: "gemini", inputs: &geminiIn, replies: []*provider.Reply{
			toolReply(call("write_file", map[string]string{"path": "REVIEW.md", "content": "LGTM\n"})),
			textReply("reviewed"),
		}},
	}
	r, tk := setup(t, scripts,
		AgentSpec{Role: "backend_dev", Provider: "claude"},
		AgentSpec{Role: "code_reviewer", Provider: "gemini"})
	os.WriteFile(filepath.Join(tk.Workspace, "README.md"), []byte("# demo\nhello\n"), 0o644)
	tk.MaxRounds = 1
	r.Store.Save(tk)

	if got, err := r.Run(context.Background(), tk.ID, false); err != nil || got.Status != Stopped {
		t.Fatalf("first run: %v %v", got.Status, err)
	}
	brief := scripts["gemini"].systems[0].Shared
	for _, want := range []string{"# Project brief", "README.md", "hello"} {
		if !strings.Contains(brief, want) {
			t.Fatalf("shared system prompt lacks %q:\n%s", want, brief)
		}
	}
	for _, want := range []string{"# Project notes", "go test ./...",
		"# Changes since your last turn", "A main.go +1  (backend_dev, claude)", "+package main"} {
		if !strings.Contains(geminiIn[0], want) {
			t.Fatalf("reviewer's first input lacks %q:\n%s", want, geminiIn[0])
		}
	}

	// The operator edits a file between runs.
	os.WriteFile(filepath.Join(tk.Workspace, "README.md"), []byte("# demo\nhello, world\n"), 0o644)
	tk, _ = r.Store.Load(tk.ID)
	tk.MaxRounds = 2
	r.Store.Save(tk)
	r.Run(context.Background(), tk.ID, false)
	if s := scripts["claude"].systems; s[len(s)-1] != s[0] {
		t.Fatal("system prompt changed on resume; the brief must stay frozen so the cache stays valid")
	}

	in := claudeIn[len(claudeIn)-1]
	for _, want := range []string{"A REVIEW.md +1  (code_reviewer, gemini)", "M README.md +1 -1  (" + operator + ")", "+hello, world"} {
		if !strings.Contains(in, want) {
			t.Fatalf("backend_dev's round-2 input lacks %q:\n%s", want, in)
		}
	}
	for _, unwanted := range []string{"# Project brief", "# Project notes", "main.go"} {
		if strings.Contains(in, unwanted) {
			t.Fatalf("backend_dev's round-2 input repeats %q:\n%s", unwanted, in)
		}
	}
}

func TestParallelRound(t *testing.T) {
	geminiStarted, claudeSent := make(chan struct{}), make(chan struct{})
	wait := func(ch chan struct{}, what string) {
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Errorf("timed out waiting for %s: agents are not running in parallel", what)
		}
	}
	var claudeIn, geminiIn []string
	scripts := map[string]*script{
		"claude": {name: "claude", inputs: &claudeIn, replies: []*provider.Reply{
			toolReply(call("message_teammate", map[string]string{"to": "qa_engineer", "message": "api.go is coming"})),
			toolReply(call("write_file", map[string]string{"path": "api.go", "content": "package api\n"})),
			textReply("wrote api.go"),
			toolReply(call("declare_done", map[string]string{"summary": "api done"})),
			textReply("done"),
		}, hooks: map[int]func(){
			0: func() { wait(geminiStarted, "the reviewer to start") },
			1: func() { close(claudeSent) },
		}},
		"gemini": {name: "gemini", inputs: &geminiIn, replies: []*provider.Reply{
			toolReply(call("list_dir", map[string]string{})),
			textReply("waiting for api.go"),
			toolReply(call("declare_done", map[string]string{"summary": "tested"})),
			textReply("done"),
		}, hooks: map[int]func(){
			0: func() { close(geminiStarted); wait(claudeSent, "the backend dev's message") },
		}},
	}
	r, tk := setup(t, scripts,
		AgentSpec{Role: "backend_dev", Provider: "claude"},
		AgentSpec{Role: "qa_engineer", Provider: "gemini"})
	tk.Mode = ModeParallel
	r.Store.Save(tk)

	got, err := r.Run(context.Background(), tk.ID, false)
	if err != nil || got.Status != Completed || got.Round != 2 {
		t.Fatalf("status=%s round=%d reason=%q err=%v", got.Status, got.Round, got.Reason, err)
	}
	// The message arrived mid-turn, alongside the reviewer's tool results.
	if !strings.Contains(geminiIn[1], "# Messages from teammates") || !strings.Contains(geminiIn[1], "**backend_dev** (claude): api.go is coming") {
		t.Fatalf("message not delivered mid-turn: %q", geminiIn[1])
	}
	// Round 2 starts with everyone's closing messages and the changes.
	for _, want := range []string{"Teammates' closing messages", "wrote api.go", "A api.go +1  (backend_dev, claude)"} {
		if !strings.Contains(geminiIn[2], want) {
			t.Fatalf("reviewer's round-2 input lacks %q:\n%s", want, geminiIn[2])
		}
	}
	if !strings.Contains(claudeIn[3], "waiting for api.go") {
		t.Fatalf("backend dev did not get the reviewer's message: %s", claudeIn[3])
	}
	if len(got.Inbox) != 0 {
		t.Fatalf("undelivered mail left: %+v", got.Inbox)
	}
}

func TestParallelResumeRunsOnlyDueAgents(t *testing.T) {
	var claudeIn, geminiIn []string
	c := &script{name: "claude", inputs: &claudeIn} // fails at once
	g := &script{name: "gemini", inputs: &geminiIn, replies: []*provider.Reply{textReply("round 1 done")}}
	r, tk := setup(t, map[string]*script{"claude": c, "gemini": g},
		AgentSpec{Role: "backend_dev", Provider: "claude"},
		AgentSpec{Role: "qa_engineer", Provider: "gemini"})
	tk.Mode, tk.MaxRounds = ModeParallel, 1
	r.Store.Save(tk)
	// gemini's turn may or may not finish before claude's failure cancels
	// the round; either way the task fails and is resumable.
	got, _ := r.Run(context.Background(), tk.ID, false)
	if got.Status != Failed {
		t.Fatalf("status = %s", got.Status)
	}
	geminiDone := got.Agents[1].RoundDone == 1

	c.replies = []*provider.Reply{textReply("round 1, finally")}
	g.replies = append(g.replies, textReply("round 1 again"))
	got, err := r.Run(context.Background(), tk.ID, false)
	if err != nil || got.Status != Stopped || got.Round != 2 {
		t.Fatalf("after resume: %s round %d %v", got.Status, got.Round, err)
	}
	if want := map[bool]int{true: 1, false: 2}[geminiDone]; len(geminiIn) != want {
		t.Fatalf("qa_engineer ran %d times, want %d", len(geminiIn), want)
	}
}

func TestSameProviderTeamSharesPrefix(t *testing.T) {
	var in []string
	sc := &script{name: "claude", inputs: &in, replies: []*provider.Reply{
		toolReply(call("declare_done", map[string]string{"summary": "ok"})),
		textReply("done"),
	}}
	r, tk := setup(t, map[string]*script{"claude": sc},
		AgentSpec{Role: "backend_dev", Provider: "claude"},
		AgentSpec{Role: "code_reviewer", Provider: "claude"},
		AgentSpec{Role: "qa_engineer", Provider: "claude"})
	tk.Mode = ModeParallel
	r.Store.Save(tk)
	// Each agent gets its own session from the same script, so replies
	// are popped per session; the script has enough for one turn each.
	if _, err := r.Run(context.Background(), tk.ID, false); err != nil {
		t.Fatal(err)
	}
	if len(sc.systems) != 3 {
		t.Fatalf("%d sessions", len(sc.systems))
	}
	for i, sys := range sc.systems[1:] {
		if sys.Shared != sc.systems[0].Shared {
			t.Errorf("agent %d's shared prompt differs", i+2)
		}
		if sys.Own == sc.systems[0].Own {
			t.Errorf("agent %d has agent 1's role prompt", i+2)
		}
		if len(sc.tools[i+1]) != len(sc.tools[0]) {
			t.Errorf("agent %d has different tools", i+2)
		}
	}
	for _, role := range []string{"backend_dev", "code_reviewer", "qa_engineer"} {
		if strings.Contains(sc.systems[0].Shared, "("+role+")") {
			t.Errorf("shared prompt names a role as the reader's own: %s", role)
		}
	}
	if !strings.Contains(sc.systems[2].Own, "agent 3") {
		t.Errorf("own prompt does not say who the agent is:\n%s", sc.systems[2].Own)
	}
	if sc.prewarms != 1 {
		t.Errorf("prewarmed %d times, want once for the group", sc.prewarms)
	}
}
