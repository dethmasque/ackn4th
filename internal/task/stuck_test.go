package task

import (
	"context"
	"strings"
	"testing"

	"github.com/dethmasque/ackn4th/internal/event"
	"github.com/dethmasque/ackn4th/internal/provider"
)

// A repeat of a lookup the agent already made this turn, with nothing
// changed since, isn't run; after a few, the agent is told to hand off.
// Changing a file makes lookups fresh again.
func TestRepeatedLookupsAreRefused(t *testing.T) {
	var in []string
	ls := call("list_dir", map[string]string{"path": "."})
	write := call("write_file", map[string]string{"path": "a.txt", "content": "a"})
	scripts := map[string]*script{"claude": {name: "claude", inputs: &in, replies: []*provider.Reply{
		toolReply(ls),
		toolReply(ls), // refused
		toolReply(write),
		toolReply(ls),                // runs: a file changed
		toolReply(ls), toolReply(ls), // refused: the 3rd refusal asks for the handoff
		toolReply(ls), // refused again
		textReply("listed it"),
		toolReply(call("declare_done", map[string]string{"summary": "ok"})), textReply("done"),
	}}}
	r, tk := setup(t, scripts, AgentSpec{Role: "backend_dev", Provider: "claude"})
	tk.MaxRounds = 2
	r.Store.Save(tk)
	r.Run(context.Background(), tk.ID, false)
	refused := 0
	for _, s := range in {
		refused += strings.Count(s, "Not run: you already made exactly this call")
	}
	if refused != 4 {
		t.Errorf("refused %d repeats, want 4", refused)
	}
	if !strings.Contains(in[6], "You keep repeating calls") || strings.Contains(in[5], "You keep repeating") {
		t.Errorf("after 3 refusals the agent should be told to hand off: %q", in[6])
	}
	if strings.Contains(in[4], "Not run") || !strings.Contains(in[4], "a.txt") {
		t.Errorf("a lookup after a change should run: %q", in[4])
	}
}

// An agent giving the same handoff three turns running stops the task,
// with a reason saying what to do.
func TestStuckAgentStopsTheTask(t *testing.T) {
	var in []string
	same := "Your message will be read by the next agent."
	scripts := map[string]*script{"gemini": {name: "gemini", inputs: &in, replies: []*provider.Reply{
		textReply(same), textReply(same), textReply(same), textReply(same), textReply(same),
	}}}
	r, tk := setup(t, scripts, AgentSpec{Role: "backend_dev", Provider: "gemini", Model: "g"})
	tk.MaxRounds = 10
	r.Store.Save(tk)
	got, err := r.Run(context.Background(), tk.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != Stopped || !strings.Contains(got.Reason, "same handoff three turns running") || !strings.Contains(got.Reason, "another model or provider") {
		t.Fatalf("status %s, reason %q", got.Status, got.Reason)
	}
	if got.Agents[0].Turns != 3 {
		t.Errorf("stopped after %d turns, want 3", got.Agents[0].Turns)
	}
	evs, _, _ := r.Store.ReadEvents(tk.ID, 0)
	if last := evs[len(evs)-1]; last.Kind != event.TaskStopped {
		t.Errorf("last event %s", last.Kind)
	}
	// Resuming gives it a fresh chance.
	got, _ = r.Run(context.Background(), tk.ID, false)
	if got.Agents[0].Turns < 4 {
		t.Errorf("resume should run again: %d turns", got.Agents[0].Turns)
	}
}

// Done votes may repeat: an agent waiting on teammates says so each turn.
func TestRepeatedDoneIsNotStuck(t *testing.T) {
	var a, b []string
	vote := func(s string) []*provider.Reply {
		return []*provider.Reply{toolReply(call("declare_done", map[string]string{"summary": s})), textReply("done")}
	}
	var ra []*provider.Reply
	for range 4 {
		ra = append(ra, vote("ok")...)
	}
	scripts := map[string]*script{
		"claude": {name: "claude", inputs: &a, replies: ra},
		"gemini": {name: "gemini", inputs: &b, replies: []*provider.Reply{
			toolReply(call("write_file", map[string]string{"path": "1", "content": "1"})), textReply("one"),
			toolReply(call("write_file", map[string]string{"path": "2", "content": "2"})), textReply("two"),
			toolReply(call("write_file", map[string]string{"path": "3", "content": "3"})), textReply("three"),
			toolReply(call("declare_done", map[string]string{"summary": "ok"})), textReply("done"),
		}},
	}
	r, tk := setup(t, scripts, AgentSpec{Role: "code_reviewer", Provider: "claude"}, AgentSpec{Role: "backend_dev", Provider: "gemini"})
	got, err := r.Run(context.Background(), tk.ID, false)
	if err != nil || got.Status != Completed {
		t.Fatalf("status %s, reason %q, err %v", got.Status, got.Reason, err)
	}
}

// Two turns running out of tool calls without changing a file stop the
// task, even with different handoffs.
func TestIdleAgentStopsTheTask(t *testing.T) {
	var in []string
	look := func(p string) *provider.Reply {
		return toolReply(call("search_files", map[string]string{"pattern": p}))
	}
	scripts := map[string]*script{"gemini": {name: "gemini", inputs: &in, replies: []*provider.Reply{
		look("a"), look("b"), textReply("looked at a and b"),
		look("c"), look("d"), textReply("looked at c and d"),
		look("e"), look("f"), textReply("never reached"),
	}}}
	r, tk := setup(t, scripts, AgentSpec{Role: "backend_dev", Provider: "gemini", Model: "g"})
	r.MaxSteps = 2
	got, err := r.Run(context.Background(), tk.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != Stopped || !strings.Contains(got.Reason, "without changing a file") || got.Agents[0].Turns != 2 {
		t.Fatalf("status %s after %d turns, reason %q", got.Status, got.Agents[0].Turns, got.Reason)
	}
	// Alone, it is asked for a report for the person, not a handoff.
	if !strings.Contains(in[2], "report for the person who gave the task") || strings.Contains(in[2], "handoff") {
		t.Errorf("solo wrap-up: %q", in[2])
	}
	// Agents are told to read with read_file, not cat through commands.
	sys := scripts["gemini"].systems[0]
	if all := sys.Shared + sys.Own; !strings.Contains(all, "Read files with read_file") || !strings.Contains(all, "not with cat, grep") {
		t.Error("the system prompt should steer reads to read_file")
	}
}
