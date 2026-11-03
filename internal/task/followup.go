package task

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// FollowUp is more work asked of a task's team after a run: the same
// agents, conversations, workspace and history, with a new request.
type FollowUp struct {
	Prompt string    `json:"prompt"`
	Round  int       `json:"round"` // the round it starts in
	Time   time.Time `json:"time"`
	// Changes says how the team changed with it (a role, a model or an
	// effort), so every agent knows who it is working with now.
	Changes []string `json:"changes,omitempty"`
}

// AddFollowUp queues a follow-up for t, which must not be running: every
// agent gets it at the start of its next turn, and every agent has to
// declare done again. rounds bounds the follow-up's run: 0 means until the
// agents agree, and -1 keeps the task's own limit (as many rounds as its
// current run allowed). Resume the task to run it.
func (t *Task) AddFollowUp(prompt string, rounds int, now time.Time) error {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return errors.New("a follow-up needs a prompt")
	}
	if t.Status == Running {
		return fmt.Errorf("task %s is running: stop it first, or add a note with resume-task --note when it stops", t.Label())
	}
	// A finished parallel round leaves every agent done with it: the
	// follow-up starts the next one.
	if t.Parallel() {
		finished := true
		for _, a := range t.Agents {
			if a.RoundDone < t.Round {
				finished = false
			}
		}
		if finished {
			t.Round++
		}
	}
	if rounds < 0 && t.MaxRounds > 0 {
		start := 1
		if n := len(t.FollowUps); n > 0 {
			start = t.FollowUps[n-1].Round
		}
		rounds = max(1, t.MaxRounds-start+1)
	}
	if rounds >= 0 {
		t.MaxRounds = 0
		if rounds > 0 {
			t.MaxRounds = t.Round - 1 + rounds
		}
	}
	t.FollowUps = append(t.FollowUps, FollowUp{Prompt: prompt, Round: t.Round, Time: now})
	for i := range t.Agents {
		t.Agents[i].Done, t.Agents[i].DoneSummary = false, ""
	}
	t.Status, t.Reason = Stopped, "a follow-up is waiting: resume the task to run it"
	return nil
}

// pendingFollowUps is what agent n hasn't been given yet, in order.
func (t *Task) pendingFollowUps(n int) []FollowUp {
	seen := min(t.Agents[n].FollowUpsSeen, len(t.FollowUps))
	return t.FollowUps[seen:]
}

// FollowUpPending reports whether some agent still has a follow-up to get.
func (t *Task) FollowUpPending() bool {
	for i := range t.Agents {
		if len(t.pendingFollowUps(i)) > 0 {
			return true
		}
	}
	return false
}
