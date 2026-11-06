package dash

import (
	"fmt"
	"strings"
	"time"

	"github.com/dethmasque/ackn4th/internal/event"
)

// The activity log is written to be glanced at: a clock time, r4, a role
// cut to fit, symbols, 2.1k in. Someone listening can't see the columns,
// so the voice says an event in words: when, which round, who in full,
// and numbers with their units.

// spokenEvent is e as a sentence. full adds what the screen leaves out:
// how long ago, the provider and model, and the whole text.
func (m *model) spokenEvent(l *Live, e event.Event, full bool) string {
	who, spec := "ackn4th", false
	if e.Agent > 0 && e.Agent <= len(l.Task.Agents) {
		a := l.Task.Agents[e.Agent-1]
		who, spec = a.Role, true
		if full {
			who += fmt.Sprintf(" (agent %d, on %s, model %s)", e.Agent, a.Provider, a.Model)
		}
	}
	text := func(s string) string {
		s = oneLine(s)
		if !full {
			s = excerpt(s, 140)
		}
		return strings.TrimSuffix(strings.TrimSpace(s), ".")
	}
	var what string
	switch e.Kind {
	case event.TaskStarted:
		what = "the task started: " + text(e.Text)
	case event.FollowUp:
		what = "you gave a follow-up: " + text(e.Text)
	case event.UserMessage:
		what = "the team picked up your message: " + text(e.Text)
	case event.RoundStarted:
		what = fmt.Sprintf("round %d started", e.Round)
	case event.TurnStarted:
		what = who + "'s turn started"
	case event.Context:
		what = who + " caught up on what the others did: " + text(e.Text)
	case event.ToolCall:
		what = fmt.Sprintf("%s used the tool %s", who, e.Tool)
		if e.Text != "" {
			what += ", on " + text(e.Text)
		}
	case event.ToolResult:
		what = fmt.Sprintf("%s's tool %s failed: %s", who, e.Tool, text(e.Text))
	case event.Message:
		what = who + " handed off: " + text(e.Text)
	case event.Reasoning:
		what = who + " thought: " + text(e.Text)
	case event.DoneVote:
		what = who + " declared the task done: " + text(e.Text)
	case event.TurnEnded:
		what = who + "'s turn ended. " + spokenUsage(e, full)
	case event.Compacted:
		what = who + "'s context was compacted: its older history was summarized to make room"
	case event.Notice:
		what = "notice: " + text(e.Text)
	case event.TaskStopped:
		what = "the task stopped: " + text(e.Text)
	case event.TaskCompleted:
		what = "the task completed"
	case event.TaskFailed:
		what = "the task failed: " + text(e.Text)
	default:
		what = strings.ReplaceAll(string(e.Kind), "_", " ")
		if spec {
			what = who + ": " + what
		}
		if e.Text != "" {
			what += ": " + text(e.Text)
		}
	}
	when := "at " + e.Time.Local().Format("3:04 PM")
	if full {
		when += ", " + agoSpoken(time.Since(e.Time))
	}
	round := ""
	if e.Round > 0 && e.Kind != event.RoundStarted {
		round = fmt.Sprintf(", in round %d", e.Round)
	}
	return strings.TrimSuffix(fmt.Sprintf("%s%s, %s", when, round, what), ".") + "."
}

// spokenUsage is a turn's tokens in words.
func spokenUsage(e event.Event, full bool) string {
	s := fmt.Sprintf("It read %s of input", spokenTokens(e.InTok))
	if e.CacheTok > 0 && e.InTok > 0 {
		s += fmt.Sprintf(", %d percent of it from the cache", e.CacheTok*100/e.InTok)
	}
	s += fmt.Sprintf(", and wrote %s.", spokenTokens(e.OutTok))
	if full && e.CtxTok > 0 {
		s += fmt.Sprintf(" Its context held %s.", spokenTokens(e.CtxTok))
	}
	return s
}

// spokenTokens is n tokens, rounded as someone would say it.
func spokenTokens(n int64) string {
	switch {
	case n == 1:
		return "1 token"
	case n < 1000:
		return fmt.Sprintf("%d tokens", n)
	case n < 1_000_000:
		return fmt.Sprintf("about %d tokens", (n+50)/100*100)
	}
	return fmt.Sprintf("about %.1f million tokens", float64(n)/1e6)
}

// agoSpoken is how long ago d was, in words.
func agoSpoken(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < 2*time.Minute:
		return "a minute ago"
	case d < time.Hour:
		return fmt.Sprintf("%d minutes ago", int(d.Minutes()))
	case d < 2*time.Hour:
		return "an hour ago"
	case d < 48*time.Hour:
		return fmt.Sprintf("%d hours ago", int(d.Hours()))
	}
	return fmt.Sprintf("%d days ago", int(d.Hours()/24))
}
