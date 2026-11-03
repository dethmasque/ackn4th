package dash

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/dethmasque/ackn4th/internal/task"
)

// Follow-ups: a, on a task that isn't running, asks its team for more
// work. The same agents continue with their conversations and the
// workspace; every agent gets the prompt, and all must declare done again.

// followUpBox is the follow-up dialog: a prompt being written.
type followUpBox struct {
	task *task.Task
	in   input
}

// openFollowUp is the a key.
func (m *model) openFollowUp() {
	l := m.current()
	switch {
	case l == nil || m.opt.FollowUp == nil:
	case l.Task.Status == task.Running:
		m.say("task " + l.Task.Title() + " is still running: follow-ups are for when it stops (s stops it)")
	default:
		m.more = &followUpBox{task: l.Task}
	}
}

// moreKey handles keys in the follow-up dialog.
func (m *model) moreKey(k string) (tea.Model, tea.Cmd) {
	b := m.more
	switch k {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.more = nil
		m.say("cancelled")
	case "enter", "ctrl+s":
		prompt := strings.TrimSpace(b.in.String())
		if prompt == "" {
			m.say("write what the agents should do next, or esc to cancel")
			return m, nil
		}
		m.more = nil
		return m, m.resume(&resumeReq{task: b.task, followUp: prompt})
	case "shift+enter", "alt+enter", "ctrl+j":
		b.in.insert("\n")
	default:
		b.in.edit(k, false)
	}
	return m, nil
}

// drawMore floats the follow-up dialog over the screen.
func (m *model) drawMore(lines []string) string {
	b := m.more
	t := b.task
	w := min(72, m.w-6)
	inner := w - 4
	body := []string{
		pText("The same agents continue, with their conversations,"),
		pText("the workspace and the task's history. Every agent gets"),
		pText("this, and all of them have to declare done again."),
		pDim("in ") + pLime(ansi.Truncate(tildePath(t.Workspace), inner-3, "…")),
		"",
		pDim("What should they do next?"),
	}
	edit := editorLines(b.in, inner-2, true)
	const rows = 6
	if at := cursorLine(b.in, inner-2); len(edit) > rows {
		from := min(max(0, at-rows+1), len(edit)-rows)
		edit = edit[from : from+rows]
	}
	for len(edit) < 3 {
		edit = append(edit, "")
	}
	surface := mix(bg3, green, 0.22)
	for _, e := range edit {
		body = append(body, onp(surface)(fill(" "+e, inner, surface)))
	}
	rounds := "until every agent says it's done"
	if t.MaxRounds > 0 {
		start := 1
		if n := len(t.FollowUps); n > 0 {
			start = t.FollowUps[n-1].Round
		}
		rounds = fmt.Sprintf("up to %s, as its last run", plural(max(1, t.MaxRounds-start+1), "round"))
	}
	body = append(body, pDim("Runs in the background, "+rounds+"."), "",
		pill("⏎", bg3, lime)+pDim(" run it  ")+pill("shift ⏎", bg3, lime)+pDim(" new line  ")+pill("esc", bg3, lime)+pDim(" cancel"))
	out := []string{""}
	for _, l := range body {
		out = append(out, " "+l)
	}
	out = append(out, "")
	box := panel("FOLLOW-UP “"+ansi.Truncate(t.Title(), w-20, "…")+"”", "", out, w, len(out)+2, true, bg2)
	for i := range lines[:len(lines)-1] {
		lines[i] = onp(bg0)(fgp(backdropFg)(ansi.Strip(lines[i])))
	}
	h := len(out) + 2
	x, y := (m.w-w-2)/2, max(0, (m.h-1-h-1)/2)
	shadow(lines, x, y, w, h, m.w)
	overlayAt(lines, box, x, y, m.w)
	lines[len(lines)-1] = m.footer()
	return strings.Join(lines, "\n")
}

// whereMore is the follow-up dialog, spoken.
func (m *model) whereMore() string {
	v := strings.TrimSpace(m.more.in.String())
	if v == "" {
		v = "empty"
	}
	return "Follow-up for task " + m.more.task.Title() + ": the same agents continue in the same workspace, and all must declare done again. Prompt, text field: " + v + ". Enter runs it in the background; escape cancels."
}
