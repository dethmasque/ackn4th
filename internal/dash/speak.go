package dash

import (
	"fmt"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/dethmasque/ackn4th/internal/task"
)

// The dashboard can read itself aloud (an accessibility option): after
// each key it says where you are and what is selected, and between keys
// what changed (a task finished, a message flashed). v turns it on or
// off; V repeats the last phrase.

// Speaker speaks phrases; a newer phrase cuts off an older one.
type Speaker interface {
	Say(string)
	Repeat()
	Close()
}

// Voice configures the spoken dashboard.
type Voice struct {
	// On starts the voice when the dashboard opens.
	On bool
	// Installed reports whether starting needs a download first.
	Installed func() bool
	// Start loads the voice, downloading it first if needed. It is slow
	// (seconds; minutes for the download) and runs off the UI.
	Start func() (Speaker, error)
	// Save records the choice in config.yaml.
	Save func(on bool) error
}

type voiceMsg struct {
	sp  Speaker
	err error
}

// startVoice loads the voice in the background.
func (m *model) startVoice() tea.Cmd {
	v := m.opt.Voice
	if v == nil || v.Start == nil || m.voiceBusy {
		return nil
	}
	m.voiceBusy = true
	if v.Installed != nil && !v.Installed() {
		m.say("downloading the voice (about 350 MB, one time)… keep using the dashboard")
	} else {
		m.say("starting the voice…")
	}
	return func() tea.Msg {
		sp, err := v.Start()
		return voiceMsg{sp, err}
	}
}

// toggleVoice is the v key.
func (m *model) toggleVoice() tea.Cmd {
	v := m.opt.Voice
	if v == nil {
		return nil
	}
	if m.speaker != nil {
		m.speaker.Close()
		m.speaker = nil
		m.say("voice off · v turns it back on")
		if v.Save != nil {
			v.Save(false)
		}
		return nil
	}
	return m.startVoice()
}

func (m *model) voiceStarted(msg voiceMsg) {
	m.voiceBusy = false
	if msg.err != nil {
		m.failed = "the voice couldn't start: " + msg.err.Error()
		return
	}
	if m.closed {
		msg.sp.Close()
		return
	}
	m.speaker = msg.sp
	if v := m.opt.Voice; v.Save != nil {
		v.Save(true)
	}
	m.say("voice on · v turns it off, shift v repeats")
}

// spoken is what speak compares against: the state before a message.
type spoken struct {
	flashAt int64
	failed  string
	field   string // the focused text field, if any
}

// focusedField names the focused text field, if one is.
func (m *model) focusedField() string {
	if f, ok := m.roles.typingRole(); ok {
		return f
	}
	if m.form == nil || m.form.drop != nil {
		return ""
	}
	cur := m.form.current()
	if in, _ := m.form.text(cur); in == nil {
		return ""
	}
	return fmt.Sprint(cur)
}

// speak says what msg changed, if the voice is on.
func (m *model) speak(msg tea.Msg, before spoken) {
	if m.speaker == nil {
		return
	}
	newFlash := m.flashAt.UnixNano() != before.flashAt && m.flash != ""
	newFail := m.failed != "" && m.failed != before.failed
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if msg.String() == "V" {
			return
		}
		// Typing isn't echoed: a field is said once, when it is reached.
		if f := m.focusedField(); f != "" && f == before.field && isTyping(msg.String()) && !newFail {
			return
		}
	case tickMsg:
		news := m.news
		m.news = nil
		switch {
		case newFail:
			news = append(news, m.failed)
		case newFlash:
			news = append(news, m.flash)
		}
		if len(news) > 0 {
			m.speaker.Say(strings.Join(news, ". "))
		}
		return
	case launchedMsg, loginDoneMsg, setupStatusMsg, voiceMsg, roleImportedMsg:
	default:
		return
	}
	switch {
	case isVoice(msg):
		m.speaker.Say(m.flash + ". " + m.where())
	case newFail:
		m.speaker.Say("Error: " + m.failed)
	case newFlash:
		m.speaker.Say(m.flash)
	default:
		m.speaker.Say(m.where())
	}
}

func isVoice(msg tea.Msg) bool { _, ok := msg.(voiceMsg); return ok }

func isTyping(k string) bool {
	return utf8.RuneCountInString(k) == 1 || k == "space" || k == "backspace" || k == "delete" ||
		strings.HasPrefix(k, "ctrl+") && k != "ctrl+s"
}

// noteChanges queues spoken news of tasks that changed status.
func (m *model) noteChanges(before map[string]task.Status) {
	if m.speaker == nil || before == nil {
		return
	}
	for _, t := range m.tasks {
		if old, ok := before[t.ID]; ok && old != t.Status {
			m.news = append(m.news, fmt.Sprintf("Task %s %s", t.Title(), t.Status))
		}
	}
}

func yesNo(no bool, yes, other string) string {
	if no {
		return other + " selected"
	}
	return yes + " selected"
}

// where describes the screen for someone who can't see it.
func (m *model) where() string {
	switch {
	case m.roles != nil:
		return m.whereRoles()
	case m.setup != nil:
		return m.whereSetup()
	case m.confirm != "":
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
			q += " It runs on " + on + "; up and down change that."
		}
		return q + " " + yesNo(m.no, "Yes", "No") + ". Left and right choose, enter confirms."
	case m.help:
		var b strings.Builder
		b.WriteString("Keys. ")
		for _, r := range helpRows {
			b.WriteString(keyWords(r[0]) + ": " + r[1] + ". ")
		}
		b.WriteString("v: voice on or off. Shift v: repeat. Press any key to close.")
		return b.String()
	case m.form != nil:
		return m.form.where()
	}
	if s, _, ok := m.tip(); ok {
		return "Tip: " + s.text + " Enter shows the next tip; control t turns tips off."
	}
	l := m.current()
	if l == nil {
		if m.opt.Launch != nil {
			return "No tasks yet. Press n to start one. Question mark lists the keys."
		}
		return "No tasks yet."
	}
	t := l.Task
	what := fmt.Sprintf("task %s, %s", t.Title(), t.Status)
	if !m.focusMain {
		return fmt.Sprintf("Task list, %d of %d: %s. Tab opens it.", m.sel+1, len(m.tasks), what)
	}
	if m.view == vAgent && m.agent >= 0 && m.agent < len(l.Agents) && m.agent < len(t.Agents) {
		a, spec := l.Agents[m.agent], t.Agents[m.agent]
		s := fmt.Sprintf("Agent %d of %d: %s on %s, %s.", m.agent+1, len(t.Agents), spec.Role, spec.Provider, a.Phase)
		if a.Activity != "" {
			s += " " + a.Activity + "."
		}
		return s + " Escape goes back."
	}
	s := fmt.Sprintf("%s tab of %s.", strings.ToLower(tabNames[m.view]), what)
	if m.view == vOverview && len(t.Agents) > 0 {
		i := max(m.agent, 0)
		if i < len(t.Agents) {
			s += fmt.Sprintf(" %d agents; agent %d, %s, is %s. Enter zooms in.", len(t.Agents), i+1, t.Agents[i].Role, l.Agents[i].Phase)
		}
	}
	return s
}

func (m *model) whereSetup() string {
	v := m.setup
	if v.ask {
		return "Welcome to ackn4th. Agents need a model provider, and none is signed in yet. Set up providers now? " +
			yesNo(v.no, "Set up", "Later") + ". Left and right choose, enter confirms."
	}
	if v.loading && len(v.states) == 0 {
		return "Set up providers. Checking each provider."
	}
	if len(v.signOut) > 0 {
		return "Press x again to sign out of " + strings.Join(v.signOut, ", ") + "."
	}
	if v.err != "" {
		return "Error: " + v.err
	}
	if v.idx >= len(v.states) {
		return "Set up providers."
	}
	st := v.states[v.idx]
	state := st.Detail
	switch {
	case st.Ready:
		state = "ready, " + st.Detail
	case st.Missing:
		state = "not installed. " + st.Hint
	}
	picked := ""
	if v.picked[st.Name] {
		picked = " Picked."
	}
	return fmt.Sprintf("Providers, %d of %d: %s, %s. %s.%s Space picks, enter signs in, x signs out, escape closes.",
		v.idx+1, len(v.states), st.Name, st.Note, state, picked)
}

// keyWords reads a key legend: "↑ ↓  j k" is "up, down, j, k".
func keyWords(k string) string {
	r := strings.NewReplacer("↑", "up", "↓", "down", "←", "left", "→", "right", "⏎", "", "[", "left bracket", "]", "right bracket", "?", "question mark")
	return strings.Join(strings.Fields(r.Replace(k)), ", ")
}

// where describes the form's focused control.
func (f *form) where() string {
	if f.busy {
		return "Launching…"
	}
	if f.err != "" {
		return "Error: " + f.err
	}
	if d := f.drop; d != nil && d.idx < len(d.choices) {
		c := d.choices[d.idx]
		s := fmt.Sprintf("%s, %d of %d", c.label(), d.idx+1, len(d.choices))
		if c.Note != "" {
			s += ", " + c.Note
		}
		if d.multi != nil && d.multi[d.idx] {
			s += ", marked"
		}
		return s + "."
	}
	cur := f.current()
	value := func(in *input, empty string) string {
		if v := strings.TrimSpace(in.String()); v != "" {
			return v
		}
		return empty
	}
	check := func(on bool) string {
		if on {
			return "checked"
		}
		return "not checked"
	}
	agent := func() (agentRow, string) {
		return f.agents[cur.agent], fmt.Sprintf("Agent %d of %d", cur.agent+1, len(f.agents))
	}
	switch cur.kind {
	case "prompt":
		return "New task. Prompt, text field: " + value(&f.prompt, "empty") + ". Tab moves on; control s launches."
	case "name":
		if v := strings.TrimSpace(f.name.String()); v != "" {
			return "Task name, text field: " + v
		}
		if strings.TrimSpace(f.prompt.String()) == "" {
			return "Task name, text field, empty"
		}
		return "Task name, text field, empty: it will be called " + task.DefaultName(f.prompt.String())
	case "workspace":
		return "Workspace directory: " + value(&f.workspace, "empty")
	case "trust":
		return "Trust this directory, checkbox, " + check(f.trust)
	case "repo":
		return "GitHub repository, optional: " + value(&f.repo, "empty")
	case "branch":
		return "Branch, optional, created if it doesn't exist: " + value(&f.branch, "empty")
	case "mode":
		if f.parallel {
			return "Mode: parallel. Space switches to turns."
		}
		return "Mode: turns. Space switches to parallel."
	case "rounds":
		return "Rounds: " + value(&f.rounds, "until done")
	case "pr":
		return "Open a draft pull request at the end, checkbox, " + check(f.draftPR)
	case "role":
		a, n := agent()
		return n + ", role: " + a.role + ". Enter opens the list."
	case "provider":
		a, n := agent()
		return n + ", provider: " + a.provider + ". Enter opens the list."
	case "model":
		a, n := agent()
		if a.editModel != nil {
			return n + ", model, text field: " + value(a.editModel, "empty")
		}
		return n + ", model: " + a.model + ". Enter opens the list."
	case "effort":
		a, n := agent()
		e := a.effort
		if e == "" {
			e = "default"
		}
		return n + ", effort: " + e + ". Enter opens the list."
	case "remove":
		_, n := agent()
		return "Remove " + strings.ToLower(n[:1]) + n[1:] + ", button"
	case "add":
		return "Add an agent, button"
	case "cancel":
		return "Cancel, button"
	case "launch":
		return "Launch, button"
	}
	return ""
}
