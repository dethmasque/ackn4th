package dash

import (
	"errors"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/dethmasque/ackn4th/internal/task"
)

func settingKey(k string) tea.KeyPressMsg {
	switch k {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	}
	return keyMsg(k)
}

// testSettings is a configuration with one of each kind, and records what
// was saved.
func testSettings(saved *[]string) *Settings {
	items := []Setting{
		{Section: "Spending caps", Key: "spend.daily", Label: "All of ackn4th, a day", Kind: Text, Unit: "dollars", Empty: "no cap", Help: "Caps a day."},
		{Section: "Commands", Key: "commands.network", Label: "Commands reach the internet", Kind: Toggle, Value: "false", Help: "Network."},
		{Section: "Models", Key: "providers.claude.effort", Label: "claude effort", Kind: Pick, Value: "high", Choices: []string{"low", "medium", "high"}, Help: "Effort."},
		{Section: "Dashboard", Key: "effects", Label: "Animations", Kind: Toggle, Value: "true", Help: "Effects."},
	}
	return &Settings{
		Load: func() ([]Setting, string, error) {
			return slices.Clone(items), "/home/me/.config/ackn4th/config.yaml", nil
		},
		Save: func(key, value string) (string, error) {
			if value == "-1" {
				return "", errors.New("give an amount in dollars")
			}
			*saved = append(*saved, key+"="+value)
			return strings.TrimPrefix(value, "$"), nil
		},
	}
}

// C opens the configuration over any screen; every kind of setting
// changes and is saved at once, and it fits every size.
func TestSettingsScreen(t *testing.T) {
	var saved []string
	opt := Options{Store: fixture(t), Settings: testSettings(&saved), Effects: true}
	for _, size := range [][2]int{{80, 22}, {120, 40}} {
		saved = nil
		m := newModel(opt)
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m.Update(keyMsg("C"))
		if m.settings == nil {
			t.Fatal("C didn't open the configuration")
		}
		checkScreen(t, m, size, "CONFIGURATION")

		// Typed: enter edits, enter saves.
		for _, k := range []string{"enter", "$", "2", "0", "enter"} {
			m.Update(settingKey(k))
		}
		checkScreen(t, m, size, "20 dollars")
		// v types, it doesn't toggle the voice.
		m.Update(settingKey("enter"))
		m.Update(settingKey("v"))
		if m.settings.edit == nil || !strings.HasSuffix(m.settings.edit.String(), "v") {
			t.Error("v while typing should be typed")
		}
		m.Update(keyMsg("esc"))
		// A bad value says why and keeps the old one.
		for _, k := range []string{"enter", "backspace", "backspace", "-", "1", "enter"} {
			m.Update(settingKey(k))
		}
		if !m.settings.failed || m.settings.items[0].Value != "20" {
			t.Errorf("a bad value: failed %v, value %q", m.settings.failed, m.settings.items[0].Value)
		}
		checkScreen(t, m, size, "give an amount in dollars")

		m.Update(keyMsg("down"))
		m.Update(settingKey("space")) // toggle
		m.Update(keyMsg("down"))
		m.Update(settingKey("right")) // pick: high → low
		m.Update(keyMsg("down"))
		m.Update(settingKey("enter")) // animations off, at once
		want := "spend.daily=$20 commands.network=true providers.claude.effort=low effects=false"
		if got := strings.Join(saved, " "); got != want {
			t.Errorf("saved %q, want %q", got, want)
		}
		if m.opt.Effects {
			t.Error("animations should stop at once")
		}
		checkScreen(t, m, size, "")
		m.Update(keyMsg("esc"))
		if m.settings != nil {
			t.Error("esc should close it")
		}
	}
}

// The voice reads each setting with its value, says when a change is
// saved or why not, and answers w, i and o for the screen.
func TestVoiceReadsSettings(t *testing.T) {
	var saved []string
	sp := &fakeSpeaker{}
	m := newModel(Options{Store: fixture(t), Settings: testSettings(&saved), Voice: &Voice{}})
	m.w, m.h = 120, 40
	m.speaker = sp
	m.Update(keyMsg("C"))
	if got := sp.last(); !strings.Contains(got, "Spending caps. All of ackn4th, a day: no cap. 1 of 4.") {
		t.Errorf("on open: %q", got)
	}
	m.Update(settingKey("enter"))
	if got := sp.last(); !strings.Contains(got, "Editing All of ackn4th, a day, text field: empty") {
		t.Errorf("editing: %q", got)
	}
	n := len(sp.said)
	m.Update(keyMsg("5"))
	if len(sp.said) != n {
		t.Errorf("typing was echoed: %q", sp.last())
	}
	m.Update(settingKey("enter"))
	if got := sp.last(); got != "All of ackn4th, a day: 5 dollars. Saved." {
		t.Errorf("saved: %q", got)
	}
	m.Update(keyMsg("down"))
	if got := sp.last(); !strings.Contains(got, "Commands. Commands reach the internet: off. 2 of 4.") {
		t.Errorf("moving to a toggle: %q", got)
	}
	m.Update(settingKey("space"))
	if got := sp.last(); got != "Commands reach the internet: on. Saved." {
		t.Errorf("toggling: %q", got)
	}
	m.Update(keyMsg("down"))
	m.Update(settingKey("right"))
	if got := sp.last(); got != "claude effort: low. Saved." {
		t.Errorf("picking: %q", got)
	}
	m.Update(keyMsg("alt+w"))
	if got := sp.last(); !strings.Contains(got, "The configuration dialog.") || !strings.Contains(got, "Models, setting 3 of 4") {
		t.Errorf("w: %q", got)
	}
	m.Update(keyMsg("f4"))
	if got := sp.last(); !strings.Contains(got, "Left and right, or enter, change this one.") {
		t.Errorf("o: %q", got)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyF3})
	if got := sp.last(); !strings.Contains(got, "The choices: low, medium, high.") || !strings.Contains(got, "providers.claude.effort in ~/.config/ackn4th/config.yaml") &&
		!strings.Contains(got, "providers.claude.effort in /home/me/.config/ackn4th/config.yaml") {
		t.Errorf("i: %q", got)
	}
}

// The voice's own setting turns the voice on and off, as v does.
func TestSettingsToggleTheVoice(t *testing.T) {
	var saved []string
	sp := &fakeSpeaker{}
	opt := Options{Store: fixture(t), Settings: testSettings(&saved)}
	opt.Settings.Load = func() ([]Setting, string, error) {
		return []Setting{{Section: "Dashboard", Key: "voice.enabled", Label: "Read aloud", Kind: Toggle, Value: "false"}}, "config.yaml", nil
	}
	voiceSaved := []bool{}
	opt.Voice = &Voice{Start: func() (Speaker, error) { return sp, nil }, Save: func(on bool) error { voiceSaved = append(voiceSaved, on); return nil }}
	m := newModel(opt)
	m.w, m.h = 120, 40
	m.Update(keyMsg("C"))
	_, cmd := m.Update(settingKey("enter"))
	if cmd == nil {
		t.Fatal("turning it on should start the voice")
	}
	m.Update(cmd())
	if m.speaker == nil || len(voiceSaved) != 1 || !voiceSaved[0] || len(saved) != 0 {
		t.Errorf("voice on: speaker %v, voice saved %v, settings saved %v", m.speaker != nil, voiceSaved, saved)
	}
	if !strings.Contains(ansi.Strip(m.render()), "■ on") {
		t.Error("the row should show the voice on")
	}
	m.Update(settingKey("enter"))
	if m.speaker != nil || len(voiceSaved) != 2 || voiceSaved[1] {
		t.Errorf("voice off: speaker %v, saved %v", m.speaker != nil, voiceSaved)
	}
}

// On a first run the configuration opens by itself, before the
// walkthrough's tooltips, which follow once it is closed; and the keys
// panel lists C.
func TestSettingsOfferedFirst(t *testing.T) {
	var saved []string
	done := 0
	s := testSettings(&saved)
	s.Offer, s.Done = true, func() { done++ }
	var launched []NewTask
	opt := formOptions(task.NewStore(t.TempDir()), &launched)
	opt.Settings = s
	m := newModel(opt)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if _, _, ok := m.tip(); ok {
		t.Error("a tooltip came before the configuration")
	}
	m.Update(tickMsg{})
	if m.settings == nil || !m.settings.first {
		t.Fatal("the configuration wasn't offered")
	}
	checkScreen(t, m, [2]int{120, 40}, "Esc continues to the walkthrough")
	m.Update(keyMsg("esc"))
	if done != 1 || m.settings != nil {
		t.Errorf("closing: done %d", done)
	}
	m.Update(tickMsg{})
	if m.settings != nil {
		t.Error("offered twice")
	}
	if !strings.Contains(helpBox(120, 40, true), "configuration") {
		t.Error("the keys panel doesn't list C")
	}
	m.w = 240
	if !strings.Contains(ansi.Strip(m.footer()), " C  config ") {
		t.Errorf("the footer doesn't list C: %q", ansi.Strip(m.footer()))
	}
}

// The dialog keeps its size while the selection moves, whatever each
// setting's help takes, and provider sections read as name and note.
func TestSettingsKeepTheirSize(t *testing.T) {
	var saved []string
	s := testSettings(&saved)
	load := s.Load
	s.Load = func() ([]Setting, string, error) {
		items, path, err := load()
		items[1].Help = strings.Repeat("A long explanation of what this does. ", 8)
		items[2].Section = "claude · Anthropic"
		return items, path, err
	}
	sp := &fakeSpeaker{}
	m := newModel(Options{Store: fixture(t), Settings: s, Voice: &Voice{}})
	m.speaker = sp
	for _, size := range [][2]int{{80, 22}, {120, 40}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m.Update(keyMsg("C"))
		top := -1
		for i := range 4 {
			screen := strings.Split(ansi.Strip(m.render()), "\n")
			at := slices.IndexFunc(screen, func(l string) bool { return strings.Contains(l, "CONFIGURATION") })
			if top < 0 {
				top = at
			} else if at != top {
				t.Errorf("%v: the dialog moved from row %d to %d at setting %d", size, top, at, i+1)
			}
			checkScreen(t, m, size, "")
			m.Update(keyMsg("down"))
		}
		m.Update(keyMsg("esc"))
	}
	m.Update(keyMsg("C"))
	m.Update(keyMsg("down"))
	m.Update(keyMsg("down"))
	if !strings.Contains(ansi.Strip(m.render()), "CLAUDE · Anthropic") {
		t.Error("the provider heading isn't name · note")
	}
	if got := sp.last(); !strings.HasPrefix(got, "claude, Anthropic. claude effort") {
		t.Errorf("spoken heading: %q", got)
	}
}
