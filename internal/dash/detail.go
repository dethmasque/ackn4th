package dash

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"

	"github.com/dethmasque/ackn4th/internal/event"
	"github.com/dethmasque/ackn4th/internal/task"
)

// The activity tab has a cursor: ↑↓ select an event and enter opens it,
// with everything the log has on it. A tool call shows its arguments,
// what came back, and the agent's reasoning just before it; a failure
// shows the same from the other side.

// detail is an open event (its index in the task's whole log), or with
// task set the selected task itself, in full; and how far it is scrolled.
type detail struct {
	at, scroll int
	task       bool
}

// eventIdx lists the indexes, in l.Events, of the events the activity tab
// shows for one agent (0-based) or all (-1).
func eventIdx(l *Live, agent int) []int {
	var out []int
	for i, e := range l.Events {
		if e.Kind == event.ToolResult && !e.Error {
			continue // shown with its call
		}
		if agent >= 0 && e.Agent != agent+1 && !(e.Agent == 0 && e.Kind != event.Notice) {
			continue
		}
		out = append(out, i)
	}
	return out
}

// source is whose events the activity log shows besides an agent's.
type source int

const (
	srcAgent  source = iota // m.agent's, or everyone's when it is -1
	srcYou                  // the person's: follow-ups and messages
	srcSystem               // ackn4th's own: task and round markers, notices
)

// filterPos is the activity filter's place in its cycle: all, you,
// ackn4th, then each agent.
func (m *model) filterPos() int {
	switch {
	case m.src == srcYou:
		return 1
	case m.src == srcSystem:
		return 2
	case m.agent < 0:
		return 0
	}
	return m.agent + 3
}

func (m *model) setFilterPos(p int) {
	m.src, m.agent = srcAgent, -1
	switch {
	case p == 1:
		m.src = srcYou
	case p == 2:
		m.src = srcSystem
	case p >= 3:
		m.agent = p - 3
	}
}

// logIdx is eventIdx for the activity log's filter, which may be the
// person or ackn4th rather than an agent.
func (m *model) logIdx(l *Live) []int {
	if m.src == srcAgent {
		return eventIdx(l, m.agent)
	}
	var out []int
	for i, e := range l.Events {
		if e.Agent != 0 {
			continue
		}
		if e.ByUser() == (m.src == srcYou) {
			out = append(out, i)
		}
	}
	return out
}

// logEvents is the activity log's events under its filter.
func (m *model) logEvents(l *Live) []event.Event {
	var out []event.Event
	for _, i := range m.logIdx(l) {
		out = append(out, l.Events[i])
	}
	return out
}

// selected is the activity cursor's place in a list of n events: the
// newest while following.
func (m *model) selected(n int) int {
	if m.follow || m.evSel >= n {
		m.evSel = n - 1
	}
	return max(0, m.evSel)
}

// moveEvent moves the activity cursor; reaching the newest event follows
// new ones again.
func (m *model) moveEvent(d int) {
	l := m.current()
	if l == nil {
		return
	}
	n := len(m.logIdx(l))
	if n == 0 {
		return
	}
	m.evSel = min(max(m.selected(n)+d, 0), n-1)
	m.follow = d > 0 && m.evSel == n-1
}

// openDetail opens the event under the activity cursor.
func (m *model) openDetail() {
	l := m.current()
	if l == nil {
		return
	}
	idx := m.logIdx(l)
	if len(idx) == 0 {
		return
	}
	m.open = &detail{at: idx[m.selected(len(idx))]}
}

// detailKey handles a key while an event is open.
func (m *model) detailKey(k string) {
	l := m.current()
	if l == nil {
		m.open = nil
		return
	}
	switch k {
	case "esc", "enter", "q", "backspace":
		m.open = nil
	case "up", "k":
		m.open.scroll = max(0, m.open.scroll-1)
	case "down", "j":
		m.open.scroll++
	case "pgup", "ctrl+u":
		m.open.scroll = max(0, m.open.scroll-m.pageSize())
	case "pgdown", "ctrl+d", "space":
		m.open.scroll += m.pageSize()
	case "home", "g":
		m.open.scroll = 0
	case "left", "h", "right", "l":
		if m.open.task {
			break
		}
		// The previous or next event in the list the cursor walks.
		idx := m.logIdx(l)
		pos := 0
		for i, at := range idx {
			if at <= m.open.at {
				pos = i
			}
		}
		if k == "left" || k == "h" {
			pos--
		} else {
			pos++
		}
		if pos >= 0 && pos < len(idx) {
			m.open = &detail{at: idx[pos]}
			m.evSel, m.follow = pos, false
		}
	}
}

// related finds an event's partner: a tool call's result, or a failed
// result's call. A call still running has none yet.
func related(evs []event.Event, at int) (call, result int) {
	call, result = -1, -1
	e := evs[at]
	switch e.Kind {
	case event.ToolCall:
		call = at
		for i := at + 1; i < len(evs); i++ {
			if o := evs[i]; o.Agent == e.Agent {
				if o.Kind == event.ToolResult && o.Tool == e.Tool {
					return call, i
				}
				if o.Kind == event.TurnEnded || o.Kind == event.TurnStarted {
					break
				}
			}
		}
	case event.ToolResult:
		result = at
		for i := at - 1; i >= 0; i-- {
			if o := evs[i]; o.Agent == e.Agent {
				if o.Kind == event.ToolCall && o.Tool == e.Tool {
					return i, result
				}
				if o.Kind == event.TurnStarted {
					break
				}
			}
		}
	}
	return call, result
}

// reasoningBefore finds what the agent was thinking when it made a call:
// the reasoning saved for the reply the call came in.
func reasoningBefore(evs []event.Event, at int) string {
	agent := evs[at].Agent
	for i := at - 1; i >= 0; i-- {
		o := evs[i]
		if o.Agent != agent {
			continue
		}
		switch o.Kind {
		case event.Reasoning:
			return o.Text
		case event.ToolCall, event.ToolResult, event.Message, event.DoneVote, event.Compacted, event.Notice:
			continue // the same reply
		}
		return ""
	}
	return ""
}

// clean makes logged text safe to draw: no escape codes or control
// characters, tabs as spaces.
func clean(s string) string {
	s = ansi.Strip(strings.ReplaceAll(s, "\r\n", "\n"))
	s = strings.ReplaceAll(s, "\t", "    ")
	return strings.Map(func(r rune) rune {
		if r != '\n' && unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

// block lays out logged output (a command, what a tool sent back) as
// written, hard-wrapped to w cells.
func block(s string, w int, style func(string) string) []string {
	var out []string
	for _, line := range strings.Split(strings.TrimRight(clean(s), "\n"), "\n") {
		for _, l := range hardwrap(line, w) {
			out = append(out, "  "+style(l))
		}
	}
	return out
}

// prose lays out writing (a prompt, a handoff, reasoning) word-wrapped to
// w cells.
func prose(s string, w int, style func(string) string) []string {
	var out []string
	for _, l := range markdown(wrap(clean(s), w), style) {
		out = append(out, "  "+l)
	}
	return out
}

// detailBox draws the open event, w×h at most.
func (m *model) detailBox(w, h int) string {
	l := m.current()
	if l == nil || m.open == nil {
		return ""
	}
	bw := min(112, w-4)
	inner := bw - 6
	if m.open.task {
		t := l.Task
		head := []string{"", " " + pDim(t.ID+" · "+string(t.Status)+" · started "+t.CreatedAt.Local().Format("Jan 2 15:04")+" · in ") + pLime(tildePath(t.Workspace)), ""}
		foot := []string{"", pDim(" ↑↓ scroll  ·  esc closes")}
		return m.scrollBox(strings.ToUpper(t.Title()), head, m.taskBody(l, inner), foot, bw, h)
	}
	if m.open.at >= len(l.Events) {
		return ""
	}
	evs := l.Events
	e := evs[m.open.at]
	title, body := m.detailBody(l, m.open.at, inner)

	who := "ackn4th"
	if e.Agent > 0 && e.Agent <= len(l.Task.Agents) {
		spec := l.Task.Agents[e.Agent-1]
		who = fgp(providerColor(spec.Provider))(spec.Role) + pDim(" · "+spec.Provider+" · "+spec.Model)
	}
	head := []string{"", " " + pDim(e.Time.Local().Format("Jan 2 15:04:05")+fmt.Sprintf(" · round %d · ", e.Round)) + who, ""}
	idx := m.logIdx(l)
	pos := 0
	for i, at := range idx {
		if at <= m.open.at {
			pos = i + 1
		}
	}
	foot := []string{"", pFaint(fmt.Sprintf(" event %d of %d", pos, len(idx))) + pDim("  ·  ↑↓ scroll  ·  ←→ previous / next event  ·  esc closes")}
	return m.scrollBox(title, head, body, foot, bw, h)
}

// scrollBox draws the open panel: head, as much of body as fits h (from
// the scroll position), then foot.
func (m *model) scrollBox(title string, head, body, foot []string, bw, h int) string {
	rows := max(3, min(len(body), h-2-len(head)-len(foot)))
	m.open.scroll = min(m.open.scroll, max(0, len(body)-rows))
	shown := body[m.open.scroll:min(len(body), m.open.scroll+rows)]
	right := pLime(copyLabel) + pDim(" c")
	if len(body) > rows {
		right = pDim(fmt.Sprintf("%d-%d of %d lines  ", m.open.scroll+1, m.open.scroll+len(shown), len(body))) + right
	}
	lines := append(append(head, shown...), foot...)
	return panel(title, right, lines, bw, len(lines)+2, true, bg2)
}

// detailBody is an event's title and text, inner cells wide.
func (m *model) detailBody(l *Live, at, inner int) (string, []string) {
	evs := l.Events
	e := evs[at]
	var out []string
	section := func(label string, c rgb) { out = append(out, " "+bold(fgp(c)(label))) }
	gap := func() { out = append(out, "") }
	text := func(s string) { out = append(out, block(s, inner, pText)...) }
	words := func(s string) { out = append(out, prose(s, inner, pText)...) }
	dim := func(s string) { out = append(out, prose(s, inner, pDim)...) }

	switch e.Kind {
	case event.ToolCall, event.ToolResult:
		call, result := related(evs, at)
		tool := strings.ToUpper(e.Tool)
		title := tool
		if call >= 0 {
			if why := reasoningBefore(evs, call); why != "" {
				section("WHY · the agent's reasoning just before", violet)
				dim(why)
				gap()
			}
			c := evs[call]
			section("CALL · "+c.Tool, lime)
			text(cmpOr(c.Detail, c.Text))
			gap()
		}
		switch {
		case result < 0:
			title += " · RUNNING"
			section("RESULT", amber)
			dim("not back yet: the tool is still running")
		case evs[result].Error:
			title += " · FAILED"
			section("RESULT · ✗ failed", red)
			resultText(evs[result], text)
		default:
			section("RESULT · ✓", green)
			resultText(evs[result], text)
		}
		if call >= 0 && evs[call].Detail == "" && (result < 0 || evs[result].Detail == "") {
			gap()
			dim("This task's log is older than the full details: only the summaries above were kept.")
		}
		return title, out
	case event.Reasoning:
		section("REASONING", violet)
		dim(e.Text)
		return "REASONING", out
	case event.Message:
		section("HANDOFF", lime)
		words(e.Text)
		return "MESSAGE", out
	case event.DoneVote:
		section("DECLARED DONE", lime)
		words(e.Text)
		return "DONE VOTE", out
	case event.TaskFailed:
		section("WHY IT FAILED", red)
		words(e.Text)
		gap()
		dim("The background runner's own output: " + tildePath(m.opt.Store.LogPath(l.Task.ID)))
		return "TASK FAILED", out
	case event.TurnEnded:
		text(fmt.Sprintf("%s in (%s cached) · %s out · context %s", tokens(e.InTok), tokens(e.CacheTok), tokens(e.OutTok), tokens(e.CtxTok)))
		return "TURN ENDED", out
	}
	words(cmpOr(e.Text, string(e.Kind)))
	return strings.ToUpper(strings.ReplaceAll(string(e.Kind), "_", " ")), out
}

// taskBody is the selected task in full: what the dashboard elsewhere
// cuts to fit.
func (m *model) taskBody(l *Live, inner int) []string {
	t := l.Task
	var out []string
	section := func(label string, c rgb) { out = append(out, " "+bold(fgp(c)(label))) }
	section("PROMPT", lime)
	out = append(out, prose(t.Prompt, inner, pText)...)
	for i, f := range t.FollowUps {
		out = append(out, "")
		section(fmt.Sprintf("FOLLOW-UP %d · round %d", i+1, f.Round), cyan)
		out = append(out, prose(f.Prompt, inner, pText)...)
	}
	out = append(out, "", " "+bold(pLime("WORKSPACE"))+"  "+pText(tildePath(t.Workspace)))
	if t.Reason != "" {
		label, c := "WHY IT STOPPED", amber
		switch t.Status {
		case task.Failed:
			label, c = "WHY IT FAILED", red
		case task.Completed:
			label, c = "HOW IT ENDED", green
		}
		out = append(out, "")
		section(label, c)
		out = append(out, prose(t.Reason, inner, pText)...)
	}
	if t.Branch != "" {
		out = append(out, "", " "+bold(pLime("BRANCH"))+"  "+pText(t.Branch))
	}
	if open := t.OpenTodos(); len(open) > 0 {
		out = append(out, "")
		section(fmt.Sprintf("FOR YOU · %d open", len(open)), amber)
		for _, td := range open {
			mark := fmt.Sprintf("☐ #%d ", td.ID)
			lines := wrap(clean(td.Text), inner-width(mark))
			for i, line := range lines {
				lead := strings.Repeat(" ", width(mark))
				if i == 0 {
					lead = mark
				}
				out = append(out, "  "+pAmber(lead)+pText(line))
			}
			out = append(out, "  "+strings.Repeat(" ", width(mark))+pDim(fmt.Sprintf("from %s, round %d", td.By, td.Round)))
		}
	}
	return out
}

// whereDetail says the open event or task, for the voice.
func (m *model) whereDetail() string {
	l := m.current()
	if l == nil || m.open == nil {
		return ""
	}
	if m.open.task {
		var b strings.Builder
		b.WriteString("The whole task. ")
		for _, line := range m.taskBody(l, 200) {
			if t := strings.TrimSpace(ansi.Strip(line)); t != "" {
				b.WriteString(t + ". ")
			}
		}
		return b.String() + "Escape closes."
	}
	if m.open.at >= len(l.Events) {
		return ""
	}
	title, _ := m.detailBody(l, m.open.at, 200)
	return "Details: " + strings.ToLower(title) + ", " + m.spokenEvent(l, l.Events[m.open.at], true) + " " +
		m.detailSpoken(l, m.open.at, 6) + " Up and down scroll, left and right move between events, escape closes."
}

// detailSpoken is the details panel's text for the voice, at most n
// lines of it (n < 0: all). A turn's token line, which spokenEvent
// already says in words, is left out.
func (m *model) detailSpoken(l *Live, at, n int) string {
	if l.Events[at].Kind == event.TurnEnded {
		return ""
	}
	_, body := m.detailBody(l, at, 200)
	var b strings.Builder
	for i, line := range body {
		if i == n {
			b.WriteString("… ")
			break
		}
		if t := strings.TrimSpace(ansi.Strip(line)); t != "" {
			b.WriteString(strings.TrimSuffix(t, ".") + ". ")
		}
	}
	return strings.TrimSpace(b.String())
}

// tildePath shortens a path in the home directory to ~/….
func tildePath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rel, err := filepath.Rel(home, p); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.Join("~", rel)
		}
	}
	return p
}

// resultText writes a tool's result in full, wrapped: its summary, then
// what the model got back when that says more.
func resultText(r event.Event, text func(string)) {
	sum := strings.TrimSpace(r.Text)
	if sum != "" && !strings.HasPrefix(strings.Join(strings.Fields(r.Detail), " "), strings.TrimSuffix(sum, "…")) {
		text(sum)
	}
	if r.Detail != "" {
		text(r.Detail)
	}
}
