package dash

import (
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// The configuration screen (C): config.yaml's everyday settings, saved
// one at a time as they change. On a fresh install it opens first, before
// the walkthrough.

// Settings configures the configuration screen.
type Settings struct {
	// Load returns every setting with its current value, and the file
	// they are saved in.
	Load func() ([]Setting, string, error)
	// Save writes one setting and returns its value as saved (tidied, e.g.
	// "20" for "$20"), or why it can't be.
	Save func(key, value string) (string, error)
	// Offer opens the screen on its own: a first run.
	Offer bool
	// Done records that the offer was made, so it isn't again.
	Done func()
}

// SettingKind is how a setting is changed.
type SettingKind int

const (
	Toggle SettingKind = iota // on or off: "true" or "false"
	Pick                      // one of Choices
	Text                      // typed: a number, a duration, a path
)

// Setting is one configurable value.
type Setting struct {
	Key     string // its config key, e.g. spend.daily
	Section string // the heading it is listed under
	Label   string
	Help    string // a sentence on what it does
	Kind    SettingKind
	Value   string
	Choices []string // Pick's choices; "" reads as Empty
	// Empty is what no value means ("no cap", "the provider's default").
	Empty string
	// Unit follows a typed value: "dollars", "rounds".
	Unit string
}

// on reports whether a toggle is on.
func (s Setting) on() bool { return s.Value == "true" }

// shown is the value as the screen and the voice say it.
func (s Setting) shown() string {
	switch {
	case s.Kind == Toggle && s.on():
		return "on"
	case s.Kind == Toggle:
		return "off"
	case s.Value == "" || s.Value == "0" && s.Empty != "":
		return s.Empty
	case s.Unit != "":
		return s.Value + " " + s.Unit
	}
	return s.Value
}

type settingsView struct {
	items  []Setting
	path   string // where they are saved
	idx    int
	top    int    // first row shown
	edit   *input // the value being typed, for a Text setting
	status string // what the last change did: "saved", or an error
	failed bool
	first  bool // opened on a first run
}

// openSettings shows the configuration screen (the C key).
func (m *model) openSettings(first bool) {
	s := m.opt.Settings
	if s == nil || s.Load == nil {
		return
	}
	items, path, err := s.Load()
	if err != nil {
		m.failed = "configuration: " + err.Error()
		return
	}
	m.settings = &settingsView{items: items, path: path, first: first}
	m.liveSettings()
}

// maybeOfferSettings opens the screen on a first run, before the
// walkthrough's tooltips.
func (m *model) maybeOfferSettings() {
	s := m.opt.Settings
	if s == nil || !s.Offer || m.settings != nil || m.setup != nil || m.roles != nil || m.form != nil || m.confirm != "" || m.help {
		return
	}
	s.Offer = false
	m.openSettings(true)
}

func (m *model) closeSettings() {
	if m.settings.first {
		if d := m.opt.Settings.Done; d != nil {
			d()
		}
	}
	m.settings = nil
}

// liveSettings shows what the dashboard is doing now for the settings it
// controls itself (the voice may have been toggled with v).
func (m *model) liveSettings() {
	for i := range m.settings.items {
		if it := &m.settings.items[i]; it.Key == "voice.enabled" {
			it.Value = fmt.Sprint(m.speaker != nil || m.voiceBusy)
		}
	}
}

// settingsTyping reports whether a value is being typed.
func (m *model) settingsTyping() bool { return m.settings != nil && m.settings.edit != nil }

func (m *model) settingsKey(k string) tea.Cmd {
	v := m.settings
	if k == "ctrl+c" {
		return tea.Quit
	}
	if v.edit != nil {
		switch k {
		case "enter":
			val := strings.TrimSpace(v.edit.String())
			v.edit = nil
			return m.saveSetting(val)
		case "esc":
			v.edit, v.status, v.failed = nil, "not changed", false
		default:
			v.edit.edit(k, false)
		}
		return nil
	}
	if len(v.items) == 0 {
		if k == "esc" || k == "q" {
			m.closeSettings()
		}
		return nil
	}
	moved := true
	switch k {
	case "esc", "q":
		m.closeSettings()
		return nil
	case "up", "k":
		v.idx = (v.idx - 1 + len(v.items)) % len(v.items)
	case "down", "j", "tab":
		v.idx = (v.idx + 1) % len(v.items)
	case "shift+tab":
		v.idx = (v.idx - 1 + len(v.items)) % len(v.items)
	case "pgup":
		v.idx = max(0, v.idx-8)
	case "pgdown":
		v.idx = min(len(v.items)-1, v.idx+8)
	case "home", "g":
		v.idx = 0
	case "end", "G":
		v.idx = len(v.items) - 1
	default:
		moved = false
	}
	if moved {
		v.status, v.failed = "", false
		return nil
	}
	it := v.items[v.idx]
	switch {
	case k != "enter" && k != "space" && k != "left" && k != "right" && k != "h" && k != "l":
		return nil
	case it.Kind == Toggle:
		return m.saveSetting(fmt.Sprint(!it.on()))
	case it.Kind == Pick && len(it.Choices) > 0:
		i := slices.Index(it.Choices, it.Value)
		step := 1
		if k == "left" || k == "h" {
			step = -1
		}
		return m.saveSetting(it.Choices[(i+step+len(it.Choices))%len(it.Choices)])
	case it.Kind == Text && (k == "enter" || k == "space"):
		in := newInput(it.Value)
		v.edit, v.status, v.failed = &in, "", false
	}
	return nil
}

// saveSetting saves the selected setting as val, and applies the ones
// the dashboard itself uses.
func (m *model) saveSetting(val string) tea.Cmd {
	v := m.settings
	it := &v.items[v.idx]
	if it.Key == "voice.enabled" && m.opt.Voice != nil {
		// The voice saves its own setting once it has started (or
		// stopped), so a voice that can't start isn't recorded as on.
		if (val == "true") != (m.speaker != nil || m.voiceBusy) {
			it.Value = val
			v.status, v.failed = "saved", false
			return m.toggleVoice()
		}
		return nil
	}
	saved, err := m.opt.Settings.Save(it.Key, val)
	if err != nil {
		v.status, v.failed = err.Error(), true
		return nil
	}
	it.Value, v.status, v.failed = saved, "saved", false
	switch it.Key {
	case "voice.name", "voice.speed":
		// Heard at once: the voice starts again with it.
		if m.speaker != nil {
			m.speaker.Close()
			m.speaker, m.voiceRestart = nil, true
			return m.startVoice()
		}
	case "effects":
		m.opt.Effects = it.on()
	case "tips":
		if !it.on() {
			m.opt.Tips.Disable()
		}
	}
	return nil
}

func (m *model) drawSettings(lines []string) string {
	v := m.settings
	m.liveSettings()
	w := min(100, m.w-6)
	inner := w - 4
	var out []string
	add := func(s ...string) {
		for _, l := range s {
			out = append(out, " "+l)
		}
	}
	add("")
	if v.first {
		add(pText("Welcome to ackn4th. This is how it is set up; change anything now, or later with ")+pLime("C")+pText("."),
			pDim("Esc continues to the walkthrough. Every setting is in "+tildePath(v.path)+"."), "")
	}
	// The list scrolls to keep the selection in view; headings take rows.
	type row struct {
		head string
		item int
	}
	var rows []row
	for i, it := range v.items {
		if i == 0 || it.Section != v.items[i-1].Section {
			if i > 0 {
				rows = append(rows, row{item: -1})
			}
			rows = append(rows, row{head: it.Section, item: -1})
		}
		rows = append(rows, row{item: i})
	}
	helpH := 1
	for _, it := range v.items {
		helpH = max(helpH, len(wrap(it.Help, inner)))
	}
	room := max(5, m.h-len(out)-11-helpH)
	at := slices.IndexFunc(rows, func(r row) bool { return r.item == v.idx })
	if at < v.top+1 {
		v.top = max(0, at-2) // its heading too, near the top
	}
	if at >= v.top+room {
		v.top = at - room + 1
	}
	v.top = min(v.top, max(0, len(rows)-room))
	labelW := min(36, inner/2)
	for _, r := range rows[v.top:min(len(rows), v.top+room)] {
		switch {
		case r.head != "":
			name, note, _ := strings.Cut(r.head, " · ")
			h := pLime(bold(strings.ToUpper(name)))
			if note != "" {
				h += pDim(" · " + note)
			}
			add(h)
		case r.item < 0:
			add("")
		default:
			it, focused := v.items[r.item], r.item == v.idx
			add(m.settingRow(it, focused, labelW, inner))
		}
	}
	if len(rows) > room {
		add(pFaint(fmt.Sprintf("  %d of %d · ↑↓ for more", v.idx+1, len(v.items))))
	}
	add("")
	// The help takes as many rows as the longest one needs, so the box
	// doesn't grow and shrink as the selection moves.
	helpRows := 1
	for _, it := range v.items {
		helpRows = max(helpRows, len(wrap(it.Help, inner)))
	}
	var help []string
	if v.idx < len(v.items) {
		help = wrap(v.items[v.idx].Help, inner)
	}
	for i := range helpRows {
		if i < len(help) {
			add(pDim(help[i]))
		} else {
			add("")
		}
	}
	switch {
	case v.failed:
		add(pRed("✗ " + fit(v.status, inner-2)))
	case v.status != "":
		add(pLime("✓ " + fit(v.status, inner-2)))
	default:
		add("")
	}
	keys := "↑↓ choose · ⏎ space change · ←→ pick · esc closes · saved as you go"
	if v.edit != nil {
		keys = "type the value · ⏎ saves · esc cancels · empty means " + v.items[v.idx].Empty
		if v.items[v.idx].Empty == "" {
			keys = "type the value · ⏎ saves · esc cancels"
		}
	}
	add(pDim(fit(keys, inner)))
	box := panel("CONFIGURATION", "", out, w, len(out)+2, true, bg2)
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

// settingRow is one setting: its label and its value's control.
func (m *model) settingRow(it Setting, focused bool, labelW, inner int) string {
	valW := inner - labelW - 3
	label := pText(fit(it.Label, labelW))
	var val string
	switch {
	case focused && m.settings.edit != nil:
		val = textField(*m.settings.edit, valW, true)
	case it.Kind == Toggle:
		val = checkbox(it.shown(), it.on(), focused)
	case it.Kind == Pick:
		val = dropField("‹ "+it.shown()+" ›", min(valW, 30), green, focused, false)
	default:
		tw := min(valW, 30)
		shown := pWhite(fit(it.shown(), tw-2))
		if it.Value == "" || it.Value == "0" && it.Empty != "" {
			shown = pDim(fit(it.shown(), tw-2))
		}
		surface := bg3
		if focused {
			surface = mix(bg3, green, 0.22)
		}
		val = onp(surface)(fill(" "+shown, tw, surface))
	}
	mark := "  "
	if focused {
		mark = pLime("▶ ")
	}
	return mark + label + " " + val
}

// --- what the voice says ---

// settingWords is a setting and its value: "Daily cap, all providers: 20
// dollars."
func settingWords(it Setting) string {
	return it.Label + ": " + it.shown() + "."
}

func (m *model) briefSettings() string {
	v := m.settings
	if len(v.items) == 0 {
		return "Configuration. Nothing to set."
	}
	it := v.items[v.idx]
	if v.edit != nil {
		val := strings.TrimSpace(v.edit.String())
		if val == "" {
			val = "empty"
		}
		return "Editing " + it.Label + ", text field: " + val + ". Type the value, enter saves, escape cancels."
	}
	s := settingWords(it)
	switch {
	case v.failed:
		return "Not saved: " + v.status + ". " + s
	case v.status == "saved":
		return s + " Saved."
	case v.status != "":
		return s + " " + sentence(v.status)
	}
	if v.idx == 0 || v.items[v.idx-1].Section != it.Section {
		s = spokenSection(it.Section) + ". " + s
	}
	return s + fmt.Sprintf(" %d of %d.", v.idx+1, len(v.items))
}

func (m *model) whereSettings() string {
	v := m.settings
	s := "The configuration dialog. Changes are saved as you make them. "
	if v.first {
		s = "Welcome to ackn4th. This is its configuration; change anything now, or later with capital C. Escape continues to the walkthrough. "
	}
	if len(v.items) == 0 {
		return s
	}
	it := v.items[v.idx]
	return s + fmt.Sprintf("%s, setting %d of %d. ", spokenSection(it.Section), v.idx+1, len(v.items)) + m.briefSettings()
}

func (m *model) aboutSettings() string {
	v := m.settings
	if len(v.items) == 0 {
		return m.whereSettings()
	}
	it := v.items[v.idx]
	s := spokenSection(it.Section) + ". " + settingWords(it) + " " + it.Help
	if it.Kind == Pick {
		var cs []string
		for _, c := range it.Choices {
			if c == "" {
				c = it.Empty
			}
			cs = append(cs, c)
		}
		s += " The choices: " + strings.Join(cs, ", ") + "."
	}
	return s + " Saved as " + it.Key + " in " + tildePath(v.path) + "."
}

func (m *model) settingsOptions() string {
	v := m.settings
	if v.edit != nil {
		return "Type the value. Enter saves it, escape cancels. Leave it empty for " + strings.TrimSuffix(v.items[v.idx].Empty, ".") + "."
	}
	s := "Up and down choose a setting. "
	if len(v.items) > 0 {
		switch v.items[v.idx].Kind {
		case Toggle:
			s += "Enter or space turns this one on or off. "
		case Pick:
			s += "Left and right, or enter, change this one. "
		case Text:
			s += "Enter edits this one. "
		}
	}
	return s + "Each change is saved at once. Escape closes."
}

// settingsDue holds the walkthrough back until the first-run settings
// have been shown.
func (m *model) settingsDue() bool {
	s := m.opt.Settings
	return s != nil && s.Offer
}

// spokenSection reads a heading like "claude · Anthropic" as a pause.
func spokenSection(s string) string { return strings.ReplaceAll(s, " · ", ", ") }
