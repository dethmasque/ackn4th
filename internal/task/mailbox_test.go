package task

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dethmasque/ackn4th/internal/event"
	"github.com/dethmasque/ackn4th/internal/provider"
)

func done(summary string) []*provider.Reply {
	return []*provider.Reply{toolReply(call("declare_done", map[string]string{"summary": summary})), textReply("ok")}
}

// A message from the person reaches an agent mid-turn, with its next
// request, and its teammate at its next turn; the task keeps running.
func TestUserMessageMidTurn(t *testing.T) {
	for _, mode := range []string{"", ModeParallel} {
		var claudeIn, geminiIn []string
		var r *Runner
		var tk *Task
		scripts := map[string]*script{
			"claude": {name: "claude", inputs: &claudeIn,
				replies: append([]*provider.Reply{toolReply(call("list_dir", map[string]string{"path": "."}))}, done("built")...),
				hooks: map[int]func(){0: func() {
					if err := r.Store.MessageTeam(tk.ID, "", "Use tabs, not spaces.", time.Now()); err != nil {
						t.Error(err)
					}
				}}},
			"gemini": {name: "gemini", inputs: &geminiIn, replies: done("reviewed")},
		}
		r, tk = setup(t, scripts,
			AgentSpec{Role: "backend_dev", Provider: "claude"},
			AgentSpec{Role: "code_reviewer", Provider: "gemini"})
		tk.Mode = mode
		r.Store.Save(tk)
		got, err := r.Run(context.Background(), tk.ID, false)
		if err != nil || got.Status != Completed {
			t.Fatalf("%q: %s %v", mode, got.Status, err)
		}
		// claude's request after the list_dir results carries it.
		if !strings.Contains(claudeIn[1], "# Message from the person who gave the task") || !strings.Contains(claudeIn[1], "Use tabs") {
			t.Errorf("%q: claude's next request lacks the message: %q", mode, claudeIn[1])
		}
		seen := false
		for _, in := range geminiIn {
			seen = seen || strings.Contains(in, "Use tabs")
		}
		if !seen {
			t.Errorf("%q: gemini never got the message: %q", mode, geminiIn)
		}
		if got.Agents[0].Turns != 1 || got.Agents[1].Turns != 1 {
			t.Errorf("%q: a message read in time shouldn't cost a round: turns %d/%d", mode, got.Agents[0].Turns, got.Agents[1].Turns)
		}
		if len(got.Inbox) != 0 {
			t.Errorf("%q: delivered messages should leave the inbox: %+v", mode, got.Inbox)
		}
		if left, _ := os.ReadDir(r.Store.MailboxDir(tk.ID)); len(left) != 0 {
			t.Errorf("%q: the mailbox should be empty: %v", mode, left)
		}
		evs, _, _ := r.Store.ReadEvents(tk.ID, 0)
		logged := false
		for _, e := range evs {
			logged = logged || (e.Kind == event.UserMessage && e.Text == "Use tabs, not spaces." && e.Detail == "all")
		}
		if !logged {
			t.Errorf("%q: the message isn't in the log", mode)
		}
	}
}

// A message that arrives after its recipient declared done calls off the
// vote: the task can't finish with it unread.
func TestUserMessageCallsOffTheVote(t *testing.T) {
	var claudeIn, geminiIn []string
	var r *Runner
	var tk *Task
	scripts := map[string]*script{
		"claude": {name: "claude", inputs: &claudeIn, replies: append(done("built"), done("built, with the change")...)},
		"gemini": {name: "gemini", inputs: &geminiIn, replies: append(done("reviewed"), done("reviewed again")...),
			hooks: map[int]func(){0: func() {
				if err := r.Store.MessageTeam(tk.ID, "backend_dev", "Rename the flag to --dry-run.", time.Now()); err != nil {
					t.Error(err)
				}
			}}},
	}
	r, tk = setup(t, scripts,
		AgentSpec{Role: "backend_dev", Provider: "claude"},
		AgentSpec{Role: "code_reviewer", Provider: "gemini"})
	got, err := r.Run(context.Background(), tk.ID, false)
	if err != nil || got.Status != Completed {
		t.Fatalf("%s %v", got.Status, err)
	}
	if got.Agents[0].Turns != 2 || got.Agents[1].Turns != 2 {
		t.Fatalf("the team should have needed a second round: turns %d/%d", got.Agents[0].Turns, got.Agents[1].Turns)
	}
	if !strings.Contains(claudeIn[2], "--dry-run") {
		t.Errorf("claude's second turn lacks the message: %q", claudeIn[2])
	}
	for _, in := range geminiIn {
		if strings.Contains(in, "--dry-run") {
			t.Errorf("a message --to backend_dev reached the reviewer: %q", in)
		}
	}
}

func TestMessageTeamChecks(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.MessageTeam("k1", "all", "   ", time.Now()); err == nil {
		t.Error("an empty message should be refused")
	}
	if err := s.MessageTeam("k1", "all", strings.Repeat("x", MaxUserMessage+1), time.Now()); err == nil {
		t.Error("an over-long message should be refused")
	}
	if err := s.MessageTeam("k1", "", "hi", time.Now()); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(s.MailboxDir("k1"))
	if len(entries) != 1 || strings.HasPrefix(entries[0].Name(), ".") {
		t.Fatalf("mailbox = %v", entries)
	}
	if info, _ := entries[0].Info(); info.Mode().Perm() != 0o600 {
		t.Errorf("a message file should be private: %v", info.Mode())
	}
	p, err := readPost(filepath.Join(s.MailboxDir("k1"), entries[0].Name()))
	if err != nil || p.To != "all" || p.Text != "hi" {
		t.Errorf("post = %+v %v", p, err)
	}
}

// collect skips files still being written, drops what it can't use with a
// note in the log, and never reads a message twice.
func TestCollect(t *testing.T) {
	r, tk := setup(t, nil, AgentSpec{Role: "backend_dev", Provider: "claude"}, AgentSpec{Role: "code_reviewer", Provider: "gemini"})
	rn := &run{r: r, t: tk, members: []string{"backend_dev", "code_reviewer"}}
	dir := r.Store.MailboxDir(tk.ID)
	r.Store.MessageTeam(tk.ID, "code_reviewer", "look at auth.go", time.Now())
	r.Store.MessageTeam(tk.ID, "nobody", "lost", time.Now())
	os.WriteFile(filepath.Join(dir, "garbage.json"), []byte("{not json"), 0o600)
	os.WriteFile(filepath.Join(dir, ".tmp-123"), []byte(`{"to":"all","text":"half"}`), 0o600)
	rn.mu.Lock()
	rn.collect()
	rn.collect()
	rn.mu.Unlock()
	if len(tk.Inbox) != 1 || !tk.Inbox[0].User || tk.Inbox[0].To != 2 || tk.Inbox[0].Text != "look at auth.go" {
		t.Fatalf("inbox = %+v", tk.Inbox)
	}
	left, _ := os.ReadDir(dir)
	if len(left) != 1 || left[0].Name() != ".tmp-123" {
		t.Errorf("only the file being written should be left: %v", left)
	}
	evs, _, _ := r.Store.ReadEvents(tk.ID, 0)
	var notes []string
	for _, e := range evs {
		if e.Kind == event.Notice {
			notes = append(notes, e.Text)
		}
	}
	if len(notes) != 2 || !strings.Contains(strings.Join(notes, "|"), `"nobody"`) {
		t.Errorf("notices = %q", notes)
	}
}

// Mail without a valid sender is dropped, never shown as the person's or
// an agent's: a zero From means nothing on its own.
func TestMailNeedsAValidSender(t *testing.T) {
	r, tk := setup(t, nil, AgentSpec{Role: "backend_dev", Provider: "claude"}, AgentSpec{Role: "code_reviewer", Provider: "gemini"})
	rn := &run{r: r, t: tk, members: []string{"backend_dev", "code_reviewer"}}
	if err := rn.send(0, 2, "forged"); err == nil {
		t.Error("send from agent 0 should be refused")
	}
	tk.Inbox = []Mail{{ID: 1, From: 0, To: 2, Text: "zero value"}, {ID: 2, From: 9, To: 2, Text: "gone"}, {ID: 3, From: 1, To: 2, Text: "real"}}
	text, ack := rn.inbox(2)
	if strings.Contains(text, "zero value") || strings.Contains(text, "gone") || strings.Contains(text, "the person") || !strings.Contains(text, "**backend_dev** (claude): real") {
		t.Errorf("inbox text = %q", text)
	}
	ack()
	if len(tk.Inbox) != 0 {
		t.Errorf("invalid mail should be cleared too: %+v", tk.Inbox)
	}
}
