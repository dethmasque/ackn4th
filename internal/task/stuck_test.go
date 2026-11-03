package task

import (
	"context"
	"fmt"
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

// Every turn after an agent's first restates the task.
func TestTurnsRemindTheTask(t *testing.T) {
	var in []string
	scripts := map[string]*script{"claude": {name: "claude", inputs: &in, replies: []*provider.Reply{
		textReply("one"), textReply("two"),
	}}}
	r, tk := setup(t, scripts, AgentSpec{Role: "backend_dev", Provider: "claude"})
	tk.MaxRounds = 2
	r.Store.Save(tk)
	r.Run(context.Background(), tk.ID, false)
	if len(in) != 2 || strings.Contains(in[0], "a reminder") || !strings.Contains(in[1], "# The task (a reminder: keep your work on it)\n\nbuild it") {
		t.Errorf("inputs %q", in)
	}
}

// Tool calls failing in a row get the agent told to stop and read the
// errors, then to hand off. Failed commands don't count.
func TestFailureStreaks(t *testing.T) {
	var in []string
	bad := func(i int) *provider.Reply {
		return toolReply(call("edit_file", map[string]string{"path": fmt.Sprintf("missing%d.go", i), "old_text": "a", "new_text": "b"}))
	}
	var replies []*provider.Reply
	for i := range 10 {
		replies = append(replies, bad(i))
	}
	replies = append(replies, textReply("blocked: those files don't exist"))
	scripts := map[string]*script{"claude": {name: "claude", inputs: &in, replies: replies}}
	r, tk := setup(t, scripts, AgentSpec{Role: "backend_dev", Provider: "claude"})
	r.MaxSteps = 40
	tk.MaxRounds = 1
	r.Store.Save(tk)
	r.Run(context.Background(), tk.ID, false)
	if !strings.Contains(in[5], "Your last 5 tool calls failed. Stop and read the errors") || strings.Contains(in[4], "Stop and read") {
		t.Errorf("after 5 failures: %q", in[5])
	}
	if strings.Count(strings.Join(in, ""), "Stop and read the errors") != 1 {
		t.Error("the nudge should come once")
	}
	if !strings.Contains(in[10], "Your last 10 tool calls failed. Do not call any more tools") || !strings.Contains(in[10], "what is blocking you") {
		t.Errorf("after 10 failures: %q", in[10])
	}
}

// Declaring done right after a failed command is flagged to the agent.
func TestDoneAfterAFailedCommand(t *testing.T) {
	var in []string
	scripts := map[string]*script{"claude": {name: "claude", inputs: &in, replies: []*provider.Reply{
		toolReply(call("run_command", map[string]string{"command": "npm test"})),
		toolReply(call("declare_done", map[string]string{"summary": "all good"})),
		textReply("done"),
	}}}
	r, tk := setup(t, scripts, AgentSpec{Role: "backend_dev", Provider: "claude"})
	tk.MaxRounds = 1
	r.Store.Save(tk)
	r.Run(context.Background(), tk.ID, false)
	if !strings.Contains(in[2], "Note: your latest command ($ npm test) failed") {
		t.Errorf("done after a failure: %q", in[2])
	}
}

// A team split for good, one agent declaring done every round and the
// other polishing on, gets the holdout nudged, then the task stopped.
func TestSplitTeamStops(t *testing.T) {
	for _, parallel := range []bool{true, false} {
		var voter, holdout []string
		var vr, hr []*provider.Reply
		for i := range 8 {
			vr = append(vr, toolReply(call("declare_done", map[string]string{"summary": "ok"})), textReply("done"))
			hr = append(hr, textReply(fmt.Sprintf("tweaked the wording again, pass %d of many", i)))
		}
		scripts := map[string]*script{
			"claude": {name: "claude", inputs: &voter, replies: vr},
			"gemini": {name: "gemini", inputs: &holdout, replies: hr},
		}
		r, tk := setup(t, scripts, AgentSpec{Role: "frontend_dev", Provider: "claude"}, AgentSpec{Role: "backend_dev", Provider: "gemini", Model: "g"})
		if parallel {
			tk.Mode = ModeParallel
		}
		tk.MaxRounds = 20
		r.Store.Save(tk)
		got, err := r.Run(context.Background(), tk.ID, false)
		if err != nil || got.Status != Stopped || !strings.Contains(got.Reason, "hasn't agreed for 5 rounds: frontend_dev kept declaring the task done while backend_dev kept working") {
			t.Fatalf("parallel %v: %s %q %v", parallel, got.Status, got.Reason, err)
		}
		if got.Round != 5 {
			t.Errorf("parallel %v: stopped in round %d, want 5", parallel, got.Round)
		}
		nudged := 0
		for _, in := range holdout {
			if strings.Contains(in, "# Your teammates say the task is done") {
				nudged++
			}
		}
		if nudged != 3 || strings.Contains(holdout[1], "say the task is done") {
			t.Errorf("parallel %v: nudged %d times, want 3 (rounds 3-5)", parallel, nudged)
		}
		if parallel && !strings.Contains(holdout[1], "declared the task done last round: frontend_dev") {
			t.Errorf("the holdout isn't told who voted last round: %q", holdout[1])
		}
	}
}
