package dash

import (
	"fmt"
	"strings"

	"github.com/dethmasque/ackn4th/internal/task"
)

// The voice says little on its own: moving around, it names what you
// land on, and between keys it says what changed (a task finished, an
// error). The rest is on request:
//
//	w  where you are
//	i  more about what's selected
//	o  what you can do here
//	?  every key (read aloud)
//	V  the last phrase again
//
// Plain w, i and o work where no text is typed (the main screen, the
// details and help panels); alt+w, alt+i and alt+o, or F2, F3 and F4,
// work everywhere, forms and dialogs included.

// voiceIntro is said when the voice comes on.
const voiceIntro = "Voice on. As you move, I name what you land on. " +
	"Press w to hear where you are, i to hear more about what's selected, and o to hear what you can do there. " +
	"Question mark opens help, which lists every key. " +
	"In forms and dialogs, hold alt with w, i or o, or use F2, F3 and F4. " +
	"Shift v repeats what I said; v turns me off."

// voiceKey answers w, i and o (and their alt and F-key forms) when the
// voice is on. It reports whether k was one.
func (m *model) voiceKey(k string) bool {
	if m.speaker == nil {
		return false
	}
	var ask string
	switch k {
	case "alt+w", "f2":
		ask = "w"
	case "alt+i", "f3":
		ask = "i"
	case "alt+o", "f4":
		ask = "o"
	case "w", "i", "o":
		if m.roles != nil || m.setup != nil || m.settings != nil || m.form != nil || m.confirm != "" {
			return false // letters mean something there
		}
		ask = k
	default:
		return false
	}
	m.answered = true // speak mustn't talk over the answer
	switch ask {
	case "w":
		m.speaker.Say(m.whereAmI())
	case "i":
		m.speaker.Say(m.about())
	case "o":
		m.speaker.Say(m.options() + " Question mark opens help, which lists every key.")
	}
	return true
}

// brief is what the voice says after a key on its own: what you landed
// on, in a few words.
func (m *model) brief() string {
	switch {
	case m.settings != nil:
		return m.briefSettings()
	case m.roles != nil:
		return m.briefRoles()
	case m.setup != nil:
		return m.briefSetup()
	case m.confirm != "":
		return m.confirmQuestion()
	case m.help:
		return m.where() // asked for with ?: every key
	case m.form != nil:
		return m.form.where()
	case m.open != nil:
		return m.briefDetail()
	}
	s := m.briefMain()
	if _, _, ok := m.tip(); ok && m.entered {
		s += " A tip is showing: i reads it."
	}
	return s
}

func (m *model) briefMain() string {
	l := m.current()
	if l == nil {
		if m.opt.Launch != nil {
			return "No tasks yet. Press n to start one."
		}
		return "No tasks yet."
	}
	t := l.Task
	what := fmt.Sprintf("task %s, %s", t.Title(), t.Status)
	if !m.focusMain {
		return fmt.Sprintf("Task %d of %d: %s, %s.", m.sel+1, len(m.tasks), t.Title(), t.Status)
	}
	if m.view == vAgent && m.agent >= 0 && m.agent < len(l.Agents) && m.agent < len(t.Agents) {
		return m.agentLabel(l, m.agent) + " Zoomed in."
	}
	s := ""
	if m.entered {
		s = fmt.Sprintf("%s tab of %s. ", strings.ToLower(tabNames[m.view]), what)
	}
	switch m.view {
	case vOverview:
		if m.agent < 0 || len(t.Agents) == 0 {
			s += "The summary."
		} else if m.agent < len(t.Agents) && m.agent < len(l.Agents) {
			s += m.agentLabel(l, m.agent)
		}
	case vActivity:
		if evs := filterEvents(l, m.agent); len(evs) > 0 {
			i := m.selected(len(evs))
			s += fmt.Sprintf("Event %d of %d, %s", i+1, len(evs), m.spokenEvent(l, evs[i], false))
		} else {
			s += "No events yet."
		}
	}
	return strings.TrimSpace(s)
}

// agentLabel is agent i in a few words.
func (m *model) agentLabel(l *Live, i int) string {
	a, spec := l.Agents[i], l.Task.Agents[i]
	s := fmt.Sprintf("Agent %d of %d, %s, %s.", i+1, len(l.Task.Agents), spec.Role, a.Phase)
	if a.Voted {
		s += " Voted the task done."
	}
	return s
}

func (m *model) briefDetail() string {
	if m.open.task {
		return "The whole task. i reads it."
	}
	l := m.current()
	if l == nil || m.open.at >= len(l.Events) {
		return ""
	}
	title, _ := m.detailBody(l, m.open.at, 200)
	return "Details: " + strings.ToLower(title) + ". i reads them."
}

func (m *model) briefSetup() string {
	v := m.setup
	if v.ask || v.loading || len(v.signOut) > 0 || v.err != "" || v.idx >= len(v.states) {
		return m.whereSetup()
	}
	st := v.states[v.idx]
	state := "not ready"
	switch {
	case st.Ready:
		state = "ready"
	case st.Missing:
		state = "not installed"
	}
	if v.picked[st.Name] {
		state += ", picked"
	}
	return fmt.Sprintf("%s, %s. %d of %d.", st.Name, state, v.idx+1, len(v.states))
}

func (m *model) briefRoles() string {
	v := m.roles
	if v.edit != nil || v.imp != nil || v.remove != "" || v.err != "" {
		return m.whereRoles()
	}
	e, ok := v.selected()
	if !ok {
		return "Roles. None."
	}
	return fmt.Sprintf("Roles, %d of %d: %s, %s.", v.idx+1, len(v.entries), e.Role.Name, e.Kind)
}

// confirmQuestion is a confirm dialog's question and selected answer.
func (m *model) confirmQuestion() string {
	l := m.current()
	if l == nil {
		return ""
	}
	q := fmt.Sprintf("%s task %s?", strings.ToUpper(m.confirm[:1])+m.confirm[1:], l.Task.Title())
	if m.confirm == "resume" {
		on := "each agent's own provider"
		if m.runOn != "" {
			on = m.runOn
		}
		q += " It runs on " + on + "."
	}
	return q + " " + yesNo(m.no, "Yes", "No") + "."
}

// whereAmI is the w key: the screen, and where on it you are.
func (m *model) whereAmI() string {
	switch {
	case m.settings != nil:
		return m.whereSettings()
	case m.roles != nil:
		return "The roles dialog. " + m.briefRoles()
	case m.setup != nil:
		return "The providers dialog. " + m.briefSetup()
	case m.confirm != "":
		return "A question: " + m.confirmQuestion()
	case m.help:
		return "The keys panel. Any key but w, i or o closes it."
	case m.form != nil && m.form.team != nil:
		return "The follow-up form, for task " + m.form.team.Title() + ". " + m.form.where()
	case m.form != nil:
		return "The new task form. " + m.form.where()
	}
	l := m.current()
	if l == nil {
		return "The dashboard. No tasks yet."
	}
	t := l.Task
	task := fmt.Sprintf("task %s, %s", t.Title(), t.Status)
	switch {
	case m.open != nil && m.open.task:
		return "The whole task, in a panel over the dashboard: " + task + "."
	case m.open != nil:
		return "An event's details, in a panel over the " + strings.ToLower(tabNames[vActivity]) + " tab of " + task + ". " + m.briefDetail()
	case !m.focusMain:
		return fmt.Sprintf("The dashboard's task list, %d tasks. Selected: %s.", len(m.tasks), task)
	case m.view == vAgent:
		return "Zoomed in on an agent of " + task + ". " + m.briefMain()
	}
	return fmt.Sprintf("The %s tab of %s. %s", strings.ToLower(tabNames[m.view]), task, m.briefMain())
}

// about is the i key: everything about what's selected.
func (m *model) about() string {
	s := m.aboutSelected()
	if tip, _, ok := m.tip(); ok {
		s += " Tip: " + tip.text
	}
	return s
}

func (m *model) aboutSelected() string {
	if m.settings != nil {
		return m.aboutSettings()
	}
	if m.roles != nil || m.setup != nil || m.confirm != "" || m.help || m.form != nil || m.open != nil {
		return m.where()
	}
	l := m.current()
	if l == nil {
		return m.where()
	}
	t := l.Task
	if !m.focusMain {
		return fmt.Sprintf("Task %s, %s. %s%s", t.Title(), t.Status, summary(l, false), m.changesSpoken(l))
	}
	switch m.view {
	case vAgent:
		if m.agent >= 0 && m.agent < len(l.Agents) && m.agent < len(t.Agents) {
			return m.whereAgent(l, m.agent, true)
		}
	case vOverview:
		if m.agent < 0 || len(t.Agents) == 0 {
			return summary(l, false) + m.changesSpoken(l) + " The whole prompt: " + t.Prompt
		}
		if m.agent < len(t.Agents) && m.agent < len(l.Agents) {
			return m.whereAgent(l, m.agent, true)
		}
	case vActivity:
		idx := eventIdx(l, m.agent)
		if len(idx) > 0 {
			at := idx[m.selected(len(idx))]
			return excerpt(m.spokenEvent(l, l.Events[at], true)+" "+m.detailSpoken(l, at, -1), 1500)
		}
	case vMessages:
		return handoffs(l)
	case vFiles:
		return changedFiles(l)
	}
	return m.where()
}

// options is the o key: what you can do here.
func (m *model) options() string {
	switch {
	case m.settings != nil:
		return m.settingsOptions()
	case m.roles != nil:
		if m.roles.edit != nil || m.roles.imp != nil {
			return "Type to edit. Tab and shift tab move between fields. Control s saves. Escape cancels."
		}
		return "Up and down choose a role. a adds one, e edits, i imports from a file or URL, d removes one of yours. Escape closes."
	case m.setup != nil:
		if m.setup.ask {
			return "Left and right choose. Enter confirms."
		}
		return "Up and down choose a provider. Space picks it, enter signs in, x signs out. Escape closes."
	case m.confirm != "":
		s := "Left and right choose, enter confirms. y says yes; n or escape cancels."
		if m.confirm == "resume" {
			s += " Up and down change the provider it runs on."
		}
		return s
	case m.help:
		return "w, i and o answer as always. Any other key closes the panel."
	case m.form != nil:
		return m.form.options()
	case m.open != nil:
		s := "Up and down scroll. c copies all of it. Escape closes."
		if !m.open.task {
			s = "Up and down scroll. Left and right go to the previous or next event. c copies all of it. Escape closes."
		}
		return s
	}
	l := m.current()
	if l == nil {
		return "n starts a new task. p sets up providers. q quits."
	}
	t := l.Task
	var acts []string
	switch {
	case t.Status == task.Running:
		acts = append(acts, "s stops it")
	default:
		if t.Resumable() {
			acts = append(acts, "r resumes it")
		}
		acts = append(acts, "a gives it a follow-up", "x deletes it")
	}
	taskKeys := strings.Join(acts, ", ") + ". e reads the whole task. n starts a new one."
	if !m.focusMain {
		return "Up and down choose a task. Enter or tab opens it. " + taskKeys + " c copies its ID."
	}
	tabs := " 1 to 4 switch tabs: overview, activity, files, messages. Escape goes back to the task list. " + taskKeys
	switch m.view {
	case vAgent:
		return "Escape goes back to the overview. c copies the agent's latest handoff." + tabs
	case vOverview:
		return "Left and right, or up and down, choose an agent or the summary above them. Enter zooms into the agent, or reads the whole task from the summary. c copies." + tabs
	case vActivity:
		return "Up and down choose an event. Enter opens its details. Left and right show one agent's events, or all. f follows new events. c copies the event." + tabs
	case vMessages:
		return "Up and down scroll. Left and right show one agent's handoffs, or all. c copies them." + tabs
	}
	return "Up and down scroll. c copies the list." + tabs
}

// options says what the form's focused control does.
func (f *form) options() string {
	if f.drop != nil {
		s := "Up and down choose. Enter picks."
		if f.drop.multi != nil {
			s = "Up and down choose. Space marks several; enter picks."
		}
		return s + " Escape closes the list."
	}
	common := " Tab and shift tab move between fields. Control s launches the task. F1 explains every field. Escape cancels."
	remove := " x removes this agent."
	if f.team != nil {
		common = " Tab and shift tab move between fields. Control s runs the follow-up in the background. Escape cancels."
		remove = ""
	}
	cur := f.current()
	switch cur.kind {
	case "role", "provider", "model", "effort":
		if cur.kind == "model" && f.agents[cur.agent].editModel != nil {
			return "Type the model's name." + common
		}
		return "Enter opens the list." + remove + common
	case "trust", "pr":
		return "Space checks or unchecks it." + common
	case "mode":
		return "Space switches between taking turns and working in parallel." + common
	case "remove":
		return "Enter or x removes this agent." + common
	case "add", "cancel", "launch":
		return "Enter presses it." + common
	}
	if in, _ := f.text(cur); in != nil {
		return "Type to edit; up and down move between lines of the prompt." + common
	}
	return strings.TrimSpace(common)
}
