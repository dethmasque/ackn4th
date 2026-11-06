package dash

import (
	"strings"
	"testing"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/dethmasque/ackn4th/internal/task"
)

// pastedSamples are clipboard contents that used to wreck the layout:
// tabs, lone carriage returns, escapes, wide characters, very long lines.
var pastedSamples = map[string]string{
	"tabs":      "func main() {\n\tif x {\n\t\treturn\t// done\n\t}\n}",
	"crlf":      "line one\r\nline two\r\nline three",
	"lone cr":   "progress 10%\rprogress 50%\rprogress 100%",
	"escape":    "\x1b[31mred\x1b[0m text \x1b]0;title\x07 here",
	"wide":      strings.Repeat("日本語のテキスト、", 30),
	"mods":     strings.Repeat("👍🏻👍🏼👍🏽👍🏾👍🏿👋🏻👋🏼👋🏽👋🏾👋🏿",50),   
	"emoji":     strings.Repeat("ok 👍🏽 ", 40),
	"long line": strings.Repeat("x", 500),
	"long url":  "see https://example.com/" + strings.Repeat("a/b", 100),
	"controls":  "a\x00b\x08c\x7fd",
}

func TestPastedTextStaysInsideTheForm(t *testing.T) {
	for name, sample := range pastedSamples {
		for _, size := range [][2]int{{80, 24}, {120, 40}} {
			var launched []NewTask
			m := newModel(formOptions(task.NewStore(t.TempDir()), &launched))
			m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			press(m, "n")
			m.Update(tea.PasteMsg{Content: sample})
			out := m.render()
			if strings.ContainsAny(ansi.Strip(out), "\t\r\x00\x07\x08\x7f\x1b") {
				t.Errorf("%s %v: raw control characters reached the screen", name, size)
			}
			lines := strings.Split(out, "\n")
			if len(lines) != size[1] {
				t.Errorf("%s %v: %d lines", name, size, len(lines))
			}
			for i, l := range lines {
				if w := width(l); w != size[0] {
					t.Errorf("%s %v: line %d is %d cells wide: %q", name, size, i, w, ansi.Strip(l))
				}
			}
		}
	}
}

// Pasted text is stored the way it is displayed: no control characters
// except newlines in the multi-line prompt, none at all in other fields.
func TestPasteStoresNoControlCharacters(t *testing.T) {
	for name, sample := range pastedSamples {
		var launched []NewTask
		m := newModel(formOptions(task.NewStore(t.TempDir()), &launched))
		m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
		press(m, "n")
		m.Update(tea.PasteMsg{Content: sample})
		for _, r := range m.form.prompt.String() {
			if r != '\n' && unicode.IsControl(r) {
				t.Errorf("%s: prompt kept %U", name, r)
			}
		}
		press(m, "tab") // name
		m.Update(tea.PasteMsg{Content: sample})
		for _, r := range m.form.name.String() {
			if unicode.IsControl(r) {
				t.Errorf("%s: single-line field kept %U", name, r)
			}
		}
	}
}

// A lone CR is a line break, like CRLF; tabs become spaces.
func TestPasteNormalizesWhitespace(t *testing.T) {
	var launched []NewTask
	m := newModel(formOptions(task.NewStore(t.TempDir()), &launched))
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	press(m, "n")
	m.Update(tea.PasteMsg{Content: "a\tb\rc\r\nd"})
	got := m.form.prompt.String()
	if strings.ContainsAny(got, "\t\r") {
		t.Fatalf("prompt = %q", got)
	}
	if lines := strings.Split(got, "\n"); len(lines) != 3 || lines[2] != "d" {
		t.Errorf("prompt lines = %q", lines)
	}
}

// Every wrapped row of a long paste fits the editor.
func TestEditorRowsFitForAnyPaste(t *testing.T) {
	for name, sample := range pastedSamples {
		in := newInput("")
		in.insert(sample)
		for _, w := range []int{10, 20, 40} {
			for _, l := range editorLines(in, w, true) {
				if got := width(l); got > w {
					t.Errorf("%s: row is %d cells in a %d-wide editor: %q", name, got, w, ansi.Strip(l))
				}
			}
		}
	}
}

// The single-line fields scroll sideways instead of overflowing.
func TestTextFieldFitsItsWidth(t *testing.T) {
	for name, sample := range pastedSamples {
		in := newInput("")
		in.insert(sample)
		for _, focused := range []bool{true, false} {
			for _, w := range []int{12, 30} {
				got := textField(in, w, focused)
				if cells := width(got); cells != w {
					t.Errorf("%s focused=%v: field is %d cells, want %d", name, focused, cells, w)
				}
				if strings.Contains(ansi.Strip(got), "\n") {
					t.Errorf("%s: field spans several lines", name)
				}
			}
		}
	}
}
