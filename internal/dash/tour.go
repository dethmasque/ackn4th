package dash

import (
	"fmt"
	"strings"
)

// The first-run walkthrough: a tooltip at a time, each pointing at the
// part of the screen it explains, shown until dismissed once.

type tourStep struct {
	id   string
	text string
}

var tour = []tourStep{
	{"dash.welcome", "Start here: press n (or click the button) and describe a task. A team of agents takes turns in your directory until they all agree it's done."},
	{"dash.tasks", "Your tasks. ↑↓ picks one, tab moves into it, 1-4 switch tabs, ⏎ zooms into an agent, s stops, r resumes. Quitting never stops a task."},
	{"dash.form", "Write the prompt, then one row per agent. ⏎ opens a role, provider, model or effort list; space there picks several roles; del removes a row; ctrl+s launches."},
}

// tip is the walkthrough step for what's on screen, if any.
func (m *model) tip() (tourStep, int, bool) {
	t := m.opt.Tips
	if !t.Enabled() || m.setup != nil || m.roles != nil || m.confirm != "" || m.help || m.failed != "" || m.w < 80 || m.h < 22 {
		return tourStep{}, 0, false
	}
	id := "dash.tasks"
	switch {
	case m.form != nil:
		if m.form.drop != nil || m.form.busy {
			return tourStep{}, 0, false
		}
		id = "dash.form"
	case (len(m.tasks) == 0 || m.replay) && t.Pending("dash.welcome") && m.opt.Launch != nil:
		id = "dash.welcome"
	}
	if !t.Pending(id) {
		return tourStep{}, 0, false
	}
	for i, s := range tour {
		if s.id == id {
			return s, i, true
		}
	}
	return tourStep{}, 0, false
}

// replayTour starts the walkthrough over (the t key), tips off or not.
func (m *model) replayTour() {
	if m.opt.Tips == nil {
		return
	}
	m.opt.Tips.Replay("dash.")
	m.replay = true
	m.view, m.focusMain = vOverview, false
}

// tipKey dismisses the tooltip on screen. ⏎, space and esc only step to
// the next tip; any other key also does its usual job (n still opens the
// form). ctrl+t turns tips off for good. It reports whether the key was
// used up.
func (m *model) tipKey(k string) bool {
	s, _, ok := m.tip()
	if !ok {
		return false
	}
	if k == "ctrl+t" {
		m.opt.Tips.Disable()
		msg := "tips are off · `ackn4th enable-tips` brings them back"
		if m.opt.DisableTips != nil {
			if err := m.opt.DisableTips(); err != nil {
				msg = "tips are off for now (couldn't save that: " + err.Error() + ")"
			}
		}
		m.say(msg)
		return true
	}
	m.opt.Tips.Done(s.id)
	switch k {
	case "enter", "space", "esc":
		return true
	}
	return false
}

// tipBox draws a tooltip w cells wide with a pointer on the side facing
// its target: "left" or "up".
func tipBox(s tourStep, n int, w int, point string) []string {
	surf := hex("#1d2a12")
	border := fgp(amber)
	inner := w - 2
	head := fmt.Sprintf(" ✦ TIP %d/%d ", n+1, len(tour))
	var lines []string
	at := inner * 3 / 4 // where an "up" pointer sits
	if point == "up" {
		lines = append(lines, fill(strings.Repeat(" ", at+1)+border("▲"), w, bg0))
	}
	rest := border(strings.Repeat("─", max(0, inner-width(head))) + "╮")
	if point == "up" {
		n := max(0, inner-width(head))
		k := min(max(0, at-width(head)), n-1)
		rest = border(strings.Repeat("─", k) + "┴" + strings.Repeat("─", n-k-1) + "╮")
	}
	lines = append(lines, border("╭")+bold(pAmber(head))+rest)
	for _, l := range wrap(s.text, inner-2) {
		lines = append(lines, border("│")+fill(" "+pWhite(l), inner, surf)+border("│"))
	}
	foot := pAmber(" ⏎") + pDim(" next · ") + pAmber("ctrl+t") + pDim(" hides tips")
	lines = append(lines, border("│")+fill(foot, inner, surf)+border("│"))
	lines = append(lines, border("╰"+strings.Repeat("─", inner)+"╯"))
	for i := range lines {
		if point == "up" && i == 0 {
			continue
		}
		lines[i] = fill(lines[i], w, surf)
	}
	if point == "left" {
		for i := range lines {
			p := " "
			if i == 1 {
				p = pAmber("◀")
			}
			lines[i] = fill(p, 1, bg0) + lines[i]
		}
	}
	return lines
}

// drawTip floats the current tooltip over the screen.
func (m *model) drawTip(screen string) string {
	s, n, ok := m.tip()
	if !ok {
		return screen
	}
	lines := strings.Split(screen, "\n")
	w := min(54, m.w-6)
	var box []string
	var x, y int
	top := len(m.hero()) + 1 // the NEW TASK button's row
	leftW := min(36, max(28, m.w/4))
	switch s.id {
	case "dash.welcome":
		box = tipBox(s, n, w, "left")
		x, y = 2+width("+ NEW TASK")+6, top-1
	case "dash.tasks":
		box = tipBox(s, n, w, "left")
		x, y = leftW-1, top+3
	case "dash.form":
		box = tipBox(s, n, w, "up")
		fw := min(112, m.w-4)
		x, y = m.form.x+fw-w-3, m.form.y+m.form.belowAgents
	}
	bw := width(box[0])
	x = min(max(0, x), m.w-bw-2)
	y = min(max(0, y), len(lines)-1-len(box)-1)
	shadow(lines, x, y, bw, len(box), m.w)
	overlayAt(lines, strings.Join(box, "\n"), x, y, m.w)
	return strings.Join(lines, "\n")
}
