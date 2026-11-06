package dash

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// Copying. The dashboard takes the mouse (to click and scroll), which
// switches off the terminal's own text selection, so it brings its own:
// drag to select and the text is copied when you let go; ctrl+c or a
// right-click copy it again, and a right-click with nothing selected
// copies the line under the pointer. c copies whatever is selected in
// full (an activity line with its details, the open panel, the task),
// not just what fits on screen; panels have a ⧉ copy button too.

// copyLabel marks a clickable copy button on a panel's top edge.
const copyLabel = "⧉ copy"

// systemCopy puts text on the system clipboard with the platform's tool.
// Over SSH that would be the remote machine's clipboard, so it isn't used
// there: the terminal's clipboard (OSC 52) reaches the local one instead.
func systemCopy(text string) error {
	if os.Getenv("SSH_CONNECTION") != "" || os.Getenv("SSH_TTY") != "" {
		return errNoClipboard
	}
	var tools [][]string
	switch runtime.GOOS {
	case "darwin":
		tools = [][]string{{"pbcopy"}}
	case "windows":
		tools = [][]string{{"clip.exe"}}
	default:
		tools = [][]string{{"wl-copy"}, {"xclip", "-selection", "clipboard"}, {"xsel", "--clipboard", "--input"}}
	}
	for _, t := range tools {
		path, err := exec.LookPath(t[0])
		if err != nil {
			continue
		}
		cmd := exec.Command(path, t[1:]...)
		cmd.Stdin = strings.NewReader(text)
		return cmd.Run()
	}
	return errNoClipboard
}

var errNoClipboard = errors.New("no clipboard tool")

type copiedMsg struct {
	what string
	err  error
}

// copyText copies text, saying what it was. It goes to the system
// clipboard and to the terminal's, whichever this terminal can use.
func (m *model) copyText(what, text string) tea.Cmd {
	text = strings.TrimRight(text, "\n ")
	if text == "" {
		return nil
	}
	sys := m.opt.Copy
	if sys == nil {
		sys = systemCopy
	}
	return tea.Batch(tea.SetClipboard(text), func() tea.Msg {
		return copiedMsg{what: what, err: sys(text)}
	})
}

func (m *model) copied(msg copiedMsg) {
	switch {
	case msg.err == nil:
		m.say("copied " + msg.what)
	case errors.Is(msg.err, errNoClipboard):
		m.say("sent " + msg.what + " to the terminal's clipboard (if your terminal allows it)")
	default:
		m.failed = "couldn't copy: " + msg.err.Error()
	}
}

// lineCount says how much text is, for "copied 3 lines".
func lineCount(text string) string {
	n := strings.Count(strings.TrimRight(text, "\n"), "\n") + 1
	if n == 1 {
		return "1 line"
	}
	return fmt.Sprintf("%d lines", n)
}

// plain is styled lines as text: no escape codes, no trailing spaces.
func plain(lines []string) string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = strings.TrimRight(ansi.Strip(l), " ")
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n")
}

// copySelected is the c key: it copies what is selected, in full.
func (m *model) copySelected() tea.Cmd {
	l := m.current()
	if m.drag != nil && m.drag.done {
		return m.copyDrag()
	}
	if l == nil {
		return nil
	}
	t := l.Task
	switch {
	case m.open != nil && m.open.task:
		return m.copyText("the task", t.Title()+" ("+t.ID+")\n\n"+plain(m.taskBody(l, 100)))
	case m.open != nil:
		return m.copyText("the event", m.eventText(l, m.open.at))
	case !m.focusMain:
		return m.copyText("the task's ID, "+t.ID, t.ID)
	case m.view == vActivity:
		idx := m.logIdx(l)
		if len(idx) == 0 {
			return nil
		}
		return m.copyText("the event", m.eventText(l, idx[m.selected(len(idx))]))
	case m.view == vOverview && m.agent < 0:
		return m.copyText("the task", t.Title()+" ("+t.ID+")\n\n"+plain(m.taskBody(l, 100)))
	case (m.view == vOverview || m.view == vAgent) && m.agent < len(l.Agents):
		a := l.Agents[m.agent]
		if a.Message == "" {
			m.say("agent " + fmt.Sprint(m.agent+1) + " hasn't handed off yet: nothing to copy")
			return nil
		}
		return m.copyText(fmt.Sprintf("agent %d's latest handoff", m.agent+1), a.Message)
	case m.view == vMessages:
		return m.copyText("the handoffs", plain(m.messages(l, 100, 1<<20)[2:]))
	case m.view == vFiles:
		return m.copyText("the file list", plain(m.files(l, 120)))
	}
	return nil
}

// eventText is an event as text: its log line, then everything the
// details panel shows.
func (m *model) eventText(l *Live, at int) string {
	e := l.Events[at]
	_, body := m.detailBody(l, at, 100)
	return strings.TrimSpace(ansi.Strip(m.eventLine(l, e))) + "\n\n" + plain(body)
}

// dragSel is a mouse selection, from where the button went down to where
// it is (or was let go), within one box on screen.
type dragSel struct {
	x0, y0, x1, y1 int
	left, right    int // the box's inside, in columns: [left, right)
	moved, done    bool
}

// box is the inside of the panel under (x, y), so a selection over
// several lines takes text from that panel only, without its borders or
// its neighbours.
func (m *model) box(x, y int) (left, right int) {
	switch {
	case m.open != nil:
		bw := min(112, m.w-4)
		left = (m.w - bw) / 2
		return left + 1, left + bw - 1
	case m.help:
		bw := min(80, m.w-4)
		left = (m.w - bw) / 2
		return left + 1, left + bw - 1
	case m.form != nil:
		fw := min(112, m.w-4)
		return m.form.x + 1, m.form.x + fw + 1
	case m.setup != nil || m.roles != nil || m.settings != nil || m.confirm != "":
		return 0, m.w
	}
	leftW := min(36, max(28, m.w/4))
	if y < len(m.hero()) || y >= m.h-1 {
		return 0, m.w
	}
	if x < leftW {
		return 1, leftW - 1
	}
	return leftW + 1, m.w - 1
}

// span is the columns a selection covers on row y: from its start on the
// first row, to its end on the last, the whole box between.
func (d *dragSel) span(y int) (a, b int, ok bool) {
	x0, y0, x1, y1 := d.x0, d.y0, d.x1, d.y1
	if y1 < y0 || y1 == y0 && x1 < x0 {
		x0, y0, x1, y1 = x1, y1, x0, y0
	}
	if y < y0 || y > y1 {
		return 0, 0, false
	}
	a, b = d.left, d.right
	if y == y0 {
		a = max(a, x0)
	}
	if y == y1 {
		b = min(b, x1+1)
	}
	return a, b, a < b
}

// highlight paints the selection over the screen.
func (m *model) highlight(screen string) string {
	d := m.drag
	if d == nil || !d.moved {
		return screen
	}
	lines := strings.Split(screen, "\n")
	for y := range lines {
		a, b, ok := d.span(y)
		if !ok {
			continue
		}
		mid := ansi.Strip(ansi.Cut(lines[y], a, b))
		lines[y] = ansi.Cut(lines[y], 0, a) + "\x1b[0m" + onp(selection)(fgp(white)(mid)) + "\x1b[0m" + ansi.Cut(lines[y], b, m.w)
	}
	return strings.Join(lines, "\n")
}

// dragText is the selected text, from the screen as last drawn.
func (m *model) dragText() string {
	d := m.drag
	lines := strings.Split(ansi.Strip(m.screen), "\n")
	var out []string
	for y := range lines {
		if a, b, ok := d.span(y); ok {
			out = append(out, strings.TrimRight(ansi.Cut(lines[y], a, b), " "))
		}
	}
	return strings.Trim(strings.Join(out, "\n"), "\n")
}

func (m *model) copyDrag() tea.Cmd {
	text := m.dragText()
	return m.copyText(lineCount(text), text)
}

// rowText is the line under the pointer, inside its panel.
func (m *model) rowText(x, y int) string {
	lines := strings.Split(ansi.Strip(m.screen), "\n")
	if y < 0 || y >= len(lines) {
		return ""
	}
	left, right := m.box(x, y)
	return strings.TrimSpace(strings.Trim(ansi.Cut(lines[y], left, right), "│┃▌ "))
}

// mouse handles selecting and copying with the mouse; it reports whether
// it used the event up.
func (m *model) mouse(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case tea.MouseClickMsg:
		switch msg.Button {
		case tea.MouseLeft:
			if hit := m.copyButton; hit.w > 0 && msg.Y == hit.y && msg.X >= hit.x && msg.X < hit.x+hit.w {
				return m.copySelected(), true
			}
			left, right := m.box(msg.X, msg.Y)
			m.drag = &dragSel{x0: msg.X, y0: msg.Y, x1: msg.X, y1: msg.Y, left: left, right: right}
			return nil, false // a click still clicks
		case tea.MouseRight:
			if m.drag != nil && m.drag.done {
				return m.copyDrag(), true
			}
			if text := m.rowText(msg.X, msg.Y); text != "" {
				return m.copyText("the line", text), true
			}
			return nil, true
		}
	case tea.MouseMotionMsg:
		if d := m.drag; d != nil && !d.done && msg.Button == tea.MouseLeft {
			d.x1, d.y1 = min(max(msg.X, 0), m.w-1), min(max(msg.Y, 0), m.h-1)
			d.moved = d.moved || d.x1 != d.x0 || d.y1 != d.y0
			return nil, true
		}
	case tea.MouseReleaseMsg:
		if d := m.drag; d != nil && !d.done {
			if !d.moved {
				m.drag = nil
				return nil, false
			}
			d.done = true
			return m.copyDrag(), true
		}
	}
	return nil, false
}

// rect is a clickable spot on screen: w cells from (x, y).
type rect struct{ x, y, w int }

// noteCopyButton finds the ⧉ copy label on a block's top edge, drawn with
// its corner at (x, y), so a click there copies.
func (m *model) noteCopyButton(block string, x, y int) {
	top, _, _ := strings.Cut(ansi.Strip(block), "\n")
	if i := strings.Index(top, copyLabel); i >= 0 {
		m.copyButton = rect{x + ansi.StringWidth(top[:i]), y, ansi.StringWidth(copyLabel)}
	}
}
