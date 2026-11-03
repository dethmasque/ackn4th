package dash

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/dethmasque/ackn4th/internal/task"
)

// The dashboard can read itself aloud (an accessibility option): after
// each key it names what you landed on, briefly, and between keys what
// changed (a task finished, a message flashed). More is on request: see
// voicekeys.go. v turns it on or off; V repeats the last phrase.

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
		m.voiceRestart = false
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
	m.say("voice on · w where you are, i about the selection, o what you can do, ? every key")
}

// spoken is what speak compares against: the state before a message.
type spoken struct {
	flashAt int64
	failed  string
	field   string // the focused text field, if any
	place   string // which task and tab
}

// focusedField names the focused text field, if one is.
func (m *model) focusedField() string {
	if f, ok := m.roles.typingRole(); ok {
		return f
	}
	if m.settingsTyping() {
		return "setting " + m.settings.items[m.settings.idx].Key
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

// place names the task and tab on screen, so speak knows when it changes.
func (m *model) place() string {
	// Panels over it (help, the form, details) and zooming into an agent
	// don't leave it: closing them shouldn't repeat the summary.
	if !m.focusMain {
		return ""
	}
	id, v := "", m.view
	if l := m.current(); l != nil {
		id = l.Task.ID
	}
	if v == vAgent {
		v = vOverview
	}
	return fmt.Sprint(id, v)
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
		if msg.String() == "V" || m.answered {
			m.answered = false
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
	// Arriving at a task, or another of its tabs, also says what the
	// task is; moving within a tab says only what is selected.
	m.entered = m.place() != before.place
	defer func() { m.entered = false }()
	switch {
	case isVoice(msg) && m.voiceRestart:
		m.voiceRestart = false
		m.speaker.Say(m.brief())
	case isVoice(msg):
		m.speaker.Say(voiceIntro + " " + m.brief())
	case newFail:
		m.speaker.Say("Error: " + m.failed)
	case newFlash:
		m.speaker.Say(m.flash)
	default:
		m.speaker.Say(m.brief())
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
	case m.settings != nil:
		return m.whereSettings()
	case m.roles != nil:
		return m.whereRoles()
	case m.setup != nil:
		return m.whereSetup()
	case m.confirm != "":
		return m.confirmQuestion() + " " + m.options()
	case m.help:
		var b strings.Builder
		b.WriteString("Keys. ")
		for _, r := range helpRows {
			b.WriteString(keyWords(r[0]) + ": " + r[1] + ". ")
		}
		b.WriteString("v: voice on or off. Shift v: repeat. w: where you are. i: more about what's selected. o: what you can do. In forms and dialogs, alt w, alt i and alt o, or F2, F3 and F4. Any other key closes this.")
		return b.String()
	case m.form != nil:
		return m.form.where()
	case m.open != nil:
		return m.whereDetail()
	}
	s := m.whereMain()
	if tip, _, ok := m.tip(); ok {
		// The tip comes after the screen, so moving around still says
		// what is selected first.
		s += " Tip: " + tip.text + " Enter shows the next tip; control t turns tips off."
	}
	return s
}

// whereMain describes the task list and the open task.
func (m *model) whereMain() string {
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
		return fmt.Sprintf("Task list, %d of %d: %s. %s%s Tab opens it.", m.sel+1, len(m.tasks), what, summary(l, true), m.changesSpoken(l))
	}
	if m.view == vAgent && m.agent >= 0 && m.agent < len(l.Agents) && m.agent < len(t.Agents) {
		return m.whereAgent(l, m.agent, true) + " Escape goes back."
	}
	s := fmt.Sprintf("%s tab of %s.", strings.ToLower(tabNames[m.view]), what)
	if m.entered {
		// Just arrived: say what the task is before what is selected.
		s += " " + summary(l, false) + m.changesSpoken(l)
	}
	switch m.view {
	case vOverview:
		if m.agent < 0 || len(t.Agents) == 0 {
			s += " The summary is selected: " + t.Prompt + " Enter shows the whole task."
		} else if i := m.agent; i < len(t.Agents) && i < len(l.Agents) {
			s += " " + m.whereAgent(l, i, false) + " Enter zooms in."
		}
	case vActivity:
		if evs := filterEvents(l, m.agent); len(evs) > 0 {
			i := m.selected(len(evs))
			s += fmt.Sprintf(" Event %d of %d, %s Enter shows its details.", i+1, len(evs), m.spokenEvent(l, evs[i], false))
		} else {
			s += " No events yet."
		}
	case vMessages:
		s += " " + handoffs(l)
	case vFiles:
		s += " " + changedFiles(l)
	}
	return s
}

// whereAgent describes agent i: what it is doing and, zoomed in, what it
// last handed off.
func (m *model) whereAgent(l *Live, i int, zoomed bool) string {
	a, spec := l.Agents[i], l.Task.Agents[i]
	s := fmt.Sprintf("Agent %d of %d, %s on %s, %s.", i+1, len(l.Task.Agents), spec.Role, spec.Provider, a.Phase)
	if a.Activity != "" && a.Phase != Idle {
		s += " " + sentence(a.Activity)
	}
	if a.Voted {
		s += " It says the task is done."
	}
	if a.Errors > 0 {
		s += fmt.Sprintf(" %s failed.", plural(a.Errors, "tool call"))
	}
	if zoomed && a.Message != "" {
		s += " Its latest handoff: " + excerpt(a.Message, 400)
	}
	return s
}

// summary is a task in a few spoken sentences: where it stands, and what
// it is about (the prompt, cut short in the task list).
func summary(l *Live, short bool) string {
	t := l.Task
	var b strings.Builder
	if t.MaxRounds > 0 {
		fmt.Fprintf(&b, "Round %d of %d", min(t.Round, t.MaxRounds), t.MaxRounds)
	} else {
		fmt.Fprintf(&b, "Round %d", t.Round)
	}
	n := len(t.Agents)
	fmt.Fprintf(&b, ", %s", plural(n, "agent"))
	if w := l.Working(); w > 0 {
		fmt.Fprintf(&b, ", %d working", w)
	}
	if v := l.Votes(); v > 0 {
		fmt.Fprintf(&b, ", %d of %d say done", v, n)
	}
	b.WriteString(".")
	open := 0
	first := ""
	for _, td := range t.Todos {
		if !td.Done {
			if open == 0 {
				first = td.Text
			}
			open++
		}
	}
	if open > 0 {
		fmt.Fprintf(&b, " %s for you, the first: %s", plural(open, "open to-do"), excerpt(first, 160))
	}
	if t.Reason != "" && t.Status != task.Running {
		label := "Reason"
		if t.Status == task.Failed {
			label = "Failed"
		}
		fmt.Fprintf(&b, " %s: %s", label, excerpt(t.Reason, 300))
	}
	limit := 400
	if short {
		limit = 160
	}
	fmt.Fprintf(&b, " Prompt: %s", excerpt(t.Prompt, limit))
	return b.String()
}

// handoffs says how many handoffs there are and reads the latest.
func handoffs(l *Live) string {
	latest, n := -1, 0
	for i, a := range l.Agents {
		if a.Message == "" {
			continue
		}
		n++
		if latest < 0 || a.MessageRound >= l.Agents[latest].MessageRound {
			latest = i
		}
	}
	if latest < 0 {
		return "No handoffs yet."
	}
	return fmt.Sprintf("%s handed off. The latest, from agent %d, %s: %s", plural(n, "agent"), latest+1,
		l.Task.Agents[latest].Role, excerpt(l.Agents[latest].Message, 400))
}

// changedFiles names the files the agents changed, most recent first.
func changedFiles(l *Live) string {
	if len(l.Files) == 0 {
		return "No files changed yet."
	}
	fs := make([]*File, 0, len(l.Files))
	for _, f := range l.Files {
		fs = append(fs, f)
	}
	sort.Slice(fs, func(i, j int) bool { return fs[i].Last.After(fs[j].Last) })
	var names []string
	for _, f := range fs[:min(len(fs), 5)] {
		names = append(names, f.Path)
	}
	s := plural(len(fs), "file") + " changed: " + strings.Join(names, ", ")
	if len(fs) > 5 {
		s += fmt.Sprintf(", and %d more", len(fs)-5)
	}
	return s + "."
}

// excerpt is text for speaking: markup dropped, whitespace collapsed, and
// cut at a word near n characters.
func excerpt(s string, n int) string {
	s = strings.NewReplacer("`", "", "**", "", "__", "", "#", "").Replace(ansi.Strip(s))
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		cut := strings.LastIndex(s[:n], " ")
		if cut < n/2 {
			cut = n
		}
		s = strings.TrimRight(s[:cut], ",;:") + ", and more"
	}
	return sentence(s)
}

// sentence ends s with a full stop, so speech pauses after it.
func sentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsRune(".!?", rune(s[len(s)-1])) {
		return s
	}
	return s + "."
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
		if f.team != nil {
			return "Follow-up for task " + f.team.Title() + ". What should the agents do next? Text field: " + value(&f.prompt, "empty") +
				". Below it, each agent's role, model and effort, which you can change. The same agents continue; a new model starts that agent's conversation fresh."
		}
		return "New task. Prompt, text field: " + value(&f.prompt, "empty") + "."
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
			return "Mode: parallel."
		}
		return "Mode: turns."
	case "rounds":
		return "Rounds: " + value(&f.rounds, "until done")
	case "cost":
		if v := strings.TrimSpace(f.cost.String()); v != "" {
			return "Spending cap: " + v + " dollars, estimated. Text field."
		}
		return "Spending cap, text field, empty: the configured cap applies, if there is one."
	case "pr":
		return "Open a draft pull request at the end, checkbox, " + check(f.draftPR)
	case "role":
		a, n := agent()
		return n + ", role: " + a.role + "."
	case "provider":
		a, n := agent()
		return n + ", provider: " + a.provider + "."
	case "model":
		a, n := agent()
		if a.editModel != nil {
			return n + ", model, text field: " + value(a.editModel, "empty")
		}
		return n + ", model: " + f.modelLabel(a) + "."
	case "effort":
		a, n := agent()
		e := a.effort
		if e == "" {
			e = "default"
		}
		return n + ", effort: " + e + "."
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
