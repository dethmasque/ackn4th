package dash

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/dethmasque/ackn4th/internal/task"
)

// Where the work is. A task's changes stay in its workspace, uncommitted,
// and with --repo that's a clone in the default workspace root, not the
// directory ackn4th was started in. When a task isn't running, the
// overview says how many files are uncommitted, where, and how to get
// them out, so nobody has to know to look.

// changeState is what git last said about a task's workspace.
type changeState struct {
	n       int // files with uncommitted changes; -1 if unknown (not a repo)
	checked time.Time
	busy    bool
}

type changesMsg struct {
	id string
	n  int
}

// recheckChanges is how often a stopped task's workspace is looked at
// again, in case it was committed meanwhile.
const recheckChanges = 20 * time.Second

// checkChanges looks at the selected task's workspace in the background,
// if it isn't running and wasn't looked at recently.
func (m *model) checkChanges() tea.Cmd {
	l := m.current()
	if l == nil || m.opt.Uncommitted == nil || l.Task.Status == task.Running {
		return nil
	}
	if m.changes == nil {
		m.changes = map[string]*changeState{}
	}
	id, ws := l.Task.ID, l.Task.Workspace
	st := m.changes[id]
	if st != nil && (st.busy || time.Since(st.checked) < recheckChanges) {
		return nil
	}
	if st == nil {
		st = &changeState{n: -1}
		m.changes[id] = st
	}
	st.busy = true
	check := m.opt.Uncommitted
	return func() tea.Msg {
		n, err := check(ws)
		if err != nil {
			n = -1
		}
		return changesMsg{id, n}
	}
}

func (m *model) changesChecked(msg changesMsg) {
	if st := m.changes[msg.id]; st != nil {
		st.n, st.checked, st.busy = msg.n, time.Now(), false
	}
}

// changesNote is the overview's lines about l's uncommitted work, if any.
func (m *model) changesNote(l *Live, w int) []string {
	t := l.Task
	st := m.changes[t.ID]
	if t.Status == task.Running || st == nil || st.n <= 0 {
		return nil
	}
	where := tildePath(t.Workspace)
	elsewhere := ""
	if cwd, err := os.Getwd(); err == nil && !samePath(cwd, t.Workspace) {
		elsewhere = ", not in this directory"
		if t.Branch != "" {
			elsewhere = ", a clone on branch " + t.Branch
		}
	}
	head := fmt.Sprintf("▸ %s changed and not committed, in ", plural(st.n, "file"))
	out := []string{pCyan(head) + pLime(where) + pCyan(elsewhere)}
	if width(out[0]) > w {
		out = []string{pCyan(fmt.Sprintf("▸ %s changed and not committed, in", plural(st.n, "file"))), "  " + pLime(where) + pCyan(elsewhere)}
	}
	how := pDim("  commit them there")
	if t.PR == "" {
		how += pDim(", or ") + pLime("ackn4th open-pr "+t.ID) + pDim(" opens a draft PR")
	}
	return append(out, ansi.Truncate(how, w, "…"))
}

// changesSpoken is changesNote for the voice.
func (m *model) changesSpoken(l *Live) string {
	t := l.Task
	st := m.changes[t.ID]
	if t.Status == task.Running || st == nil || st.n <= 0 {
		return ""
	}
	return fmt.Sprintf(" %s changed and not committed, in %s.", plural(st.n, "file"), tildePath(t.Workspace))
}

var pCyan = fgp(cyan)

// samePath reports whether a and b are the same directory.
func samePath(a, b string) bool {
	fa, err1 := os.Stat(a)
	fb, err2 := os.Stat(b)
	if err1 != nil || err2 != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return os.SameFile(fa, fb)
}
