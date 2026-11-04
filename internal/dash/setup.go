package dash

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// Provider setup: on a fresh install, once the walkthrough's tooltips are
// done (or switched off), the dashboard offers to sign in to providers.
// `p` opens the same screen any time.

// Setup configures the provider-setup screen.
type Setup struct {
	// Offer asks on open: nobody is signed in and the user hasn't said
	// "later" before.
	Offer bool
	// Status reports every provider's state. It may take a few seconds
	// (it asks each CLI), so it runs off the UI.
	Status func() []ProviderState
	// Done records that the offer was answered, so it isn't made again.
	Done func()
	// Logout signs out of providers: ackn4th deletes what it holds for
	// them. Nil hides it.
	Logout func(providers []string) error
}

// ProviderState is one provider's readiness.
type ProviderState struct {
	Name string
	Note string // what it is, e.g. "your ChatGPT plan, via the Codex CLI"
	// Ready: signed in, so agents can use it.
	Ready bool
	// Missing: its CLI isn't installed; Hint says how to get it.
	Missing bool
	// Detail is the status line: the account, or what signing in needs.
	Detail string
	Hint   string
}

type setupView struct {
	ask     bool // the "set up now?" question, before the list
	no      bool // LATER is selected
	states  []ProviderState
	loading bool
	idx     int
	picked  map[string]bool
	err     string
	// signOut holds the providers x would sign out of, until x confirms.
	signOut []string
}

type setupStatusMsg []ProviderState

func (m *model) loadSetup() tea.Cmd {
	m.setup.loading = true
	status := m.opt.Setup.Status
	return func() tea.Msg { return setupStatusMsg(status()) }
}

// maybeOfferSetup opens the "set up providers?" question when it is due:
// on offer, with no tooltip, form or dialog in the way.
func (m *model) maybeOfferSetup() tea.Cmd {
	s := m.opt.Setup
	if s == nil || !s.Offer || m.setup != nil || m.roles != nil || m.settings != nil || m.form != nil || m.confirm != "" || m.help {
		return nil
	}
	if _, _, ok := m.tip(); ok {
		return nil
	}
	if t := m.opt.Tips; t.Enabled() && t.Pending("dash.welcome") && len(m.tasks) == 0 {
		return nil // the walkthrough comes first
	}
	s.Offer = false
	m.setup = &setupView{ask: true, picked: map[string]bool{}}
	return m.loadSetup()
}

// openSetup shows the provider list (the `p` key).
func (m *model) openSetup() tea.Cmd {
	if m.opt.Setup == nil || m.opt.Setup.Status == nil {
		return nil
	}
	m.setup = &setupView{picked: map[string]bool{}}
	return m.loadSetup()
}

func (m *model) closeSetup() {
	m.setup = nil
	if d := m.opt.Setup.Done; d != nil {
		d()
	}
}

func (m *model) setupKey(k string) tea.Cmd {
	v := m.setup
	if v.ask {
		switch k {
		case "left", "right", "tab", "shift+tab", "h", "l":
			v.no = !v.no
		case "y", "Y":
			v.ask = false
		case "enter", "space":
			if v.no {
				m.closeSetup()
				m.say("set up providers any time with p")
			} else {
				v.ask = false
			}
		case "n", "N", "esc", "q":
			m.closeSetup()
			m.say("set up providers any time with p")
		case "ctrl+c":
			return tea.Quit
		}
		return nil
	}
	if k != "x" {
		v.signOut = nil
	}
	switch k {
	case "esc", "q":
		m.closeSetup()
	case "ctrl+c":
		return tea.Quit
	case "x":
		return m.setupSignOut()
	case "up", "k":
		if len(v.states) > 0 {
			v.idx = (v.idx - 1 + len(v.states)) % len(v.states)
		}
	case "down", "j":
		if len(v.states) > 0 {
			v.idx = (v.idx + 1) % len(v.states)
		}
	case "space":
		if v.idx < len(v.states) {
			n := v.states[v.idx].Name
			v.picked[n] = !v.picked[n]
		}
	case "r":
		return m.loadSetup()
	case "enter":
		names := v.chosen()
		for _, n := range names {
			for _, st := range v.states {
				if st.Name == n && st.Missing {
					v.err = n + " isn't installed: " + st.Hint
					return nil
				}
			}
		}
		if len(names) == 0 || m.opt.Login == nil {
			return nil
		}
		v.err = ""
		return tea.ExecProcess(m.opt.Login(names), func(err error) tea.Msg { return loginDoneMsg{err} })
	}
	return nil
}

// chosen is the picked providers, or else the highlighted one.
func (v *setupView) chosen() []string {
	var names []string
	for _, st := range v.states {
		if v.picked[st.Name] {
			names = append(names, st.Name)
		}
	}
	if len(names) == 0 && v.idx < len(v.states) {
		names = []string{v.states[v.idx].Name}
	}
	return names
}

// setupSignOut asks for a second x, then signs out of the chosen
// providers.
func (m *model) setupSignOut() tea.Cmd {
	v, logout := m.setup, m.opt.Setup.Logout
	names := v.chosen()
	if logout == nil || len(names) == 0 {
		return nil
	}
	if !slices.Equal(v.signOut, names) {
		v.signOut = names
		return nil
	}
	v.signOut, v.picked = nil, map[string]bool{}
	if err := logout(names); err != nil {
		v.err = "sign-out: " + err.Error()
	} else {
		v.err = ""
		m.say("signed out of " + strings.Join(names, ", "))
	}
	return m.loadSetup()
}

// setupLoggedIn refreshes the list after a sign-in.
func (m *model) setupLoggedIn(err error) tea.Cmd {
	v := m.setup
	v.picked = map[string]bool{}
	if err != nil {
		v.err = "sign-in didn't finish (the reason was on the sign-in screen)"
	} else {
		m.say("signed in · press n to start a task")
	}
	return m.loadSetup()
}

func (m *model) drawSetup(lines []string) string {
	v := m.setup
	w := min(96, m.w-6)
	inner := w - 4
	var out []string
	add := func(s ...string) {
		for _, l := range s {
			out = append(out, " "+l)
		}
	}
	title := "SET UP PROVIDERS"
	if v.ask {
		title = "WELCOME TO ACKN4TH"
		add("",
			pText("Agents need a model provider. None is signed in yet."),
			pDim("Use your Claude, ChatGPT, Cursor or Copilot plan through its CLI,"),
			pDim("or an API key (Claude, Gemini, OpenAI)."),
			"")
		if m.opt.Voice != nil {
			add(pDim("Accessibility: press ")+pLime("v")+pDim(" to have the dashboard read aloud."), "")
		}
		add(pText("Set up providers now?"), "")
		yb, nb := button("SET UP", !v.no, bg2, true), button("LATER", v.no, bg2, false)
		pad := strings.Repeat(" ", max(0, inner-width(yb)-width(nb)-2))
		add(pad+nb+"  "+yb, pad+buttonShadow("LATER", bg2)+"  "+buttonShadow("SET UP", bg2))
	} else {
		add("", pText("Pick the providers you use, then sign in. They stay signed in until you sign out."), "")
		if v.loading && len(v.states) == 0 {
			add(pDim(m.spin(spinner) + " checking each provider…"))
		}
		nameW := 12
		for i, st := range v.states {
			box := pFaint("□ ")
			if v.picked[st.Name] {
				box = pLime("■ ")
			}
			state := pAmber("○ " + st.Detail)
			switch {
			case st.Ready:
				state = pLime("✓ " + st.Detail)
			case st.Missing:
				state = pRed("✗ not installed")
			}
			row := box + fgp(providerColor(st.Name))(bold(fit(st.Name, nameW))) + " " + pDim(fit(st.Note, inner-nameW-4-28)) + " " + fit(state, 26)
			if i == v.idx {
				row = onp(mix(bg2, green, 0.22))(fill(row, inner, mix(bg2, green, 0.22)))
			}
			add(row)
		}
		if v.idx < len(v.states) {
			if st := v.states[v.idx]; st.Missing && st.Hint != "" {
				add("", pAmber("install: ")+pDim(fit(st.Hint, inner-9)))
			} else {
				add("", "")
			}
		}
		switch {
		case len(v.signOut) > 0:
			add(pAmber(fit("press x again to sign out of "+strings.Join(v.signOut, ", ")+" (API keys are deleted)", inner)))
		case v.err != "":
			add(pRed("✗ " + fit(v.err, inner-2)))
		}
		help := "space picks · ⏎ signs in to the picked (or highlighted) ones · r rechecks · esc closes"
		if m.opt.Setup.Logout != nil {
			help = "space picks · ⏎ signs in · x signs out · r rechecks · esc closes"
		}
		add("", pDim(help))
	}
	box := panel(title, "", out, w, len(out)+2, true, bg2)
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
