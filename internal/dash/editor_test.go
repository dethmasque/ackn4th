package dash

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestEditorWrapsBetweenWords(t *testing.T) {
	text := "improve on existing game that teaches kubernetes. this is great for people that understand cloud infrastructure a little bit already, but"
	in := input{r: []rune(text), cur: len([]rune(text))}
	lines := editorLines(in, 40, true)
	var joined []string
	for _, l := range lines {
		plain := ansi.Strip(l)
		if width(plain) > 40 {
			t.Errorf("%q is wider than 40", plain)
		}
		joined = append(joined, strings.TrimRight(plain, " "))
	}
	spans := softWrap(in.r, 39)
	for _, sp := range spans[:len(spans)-1] {
		if in.r[sp[1]-1] != ' ' {
			t.Errorf("a word was split:\n%s", strings.Join(joined, "\n"))
		}
	}
	// The text survives the wrapping, every character in place.
	var back strings.Builder
	for _, sp := range spans {
		back.WriteString(string(in.r[sp[0]:sp[1]]))
	}
	if back.String() != text {
		t.Errorf("rejoined: %q", back.String())
	}
	if got := cursorLine(in, 40); got != len(lines)-1 {
		t.Errorf("cursor on line %d of %d", got, len(lines))
	}
	// A word wider than the box is split; newlines still end lines.
	if got := softWrap([]rune("abcdefghij\nxy"), 4); len(got) != 4 || got[3] != [2]int{11, 13} {
		t.Errorf("spans = %v", got)
	}
}

func TestPromptArrowsMoveBetweenLines(t *testing.T) {
	f := &form{promptW: 20}
	f.prompt = input{r: []rune("one two three four five six seven eight")}
	f.prompt.cur = len(f.prompt.r) // last line
	last := cursorLine(f.prompt, 20)
	if !f.promptLine(true) || cursorLine(f.prompt, 20) != last-1 {
		t.Fatalf("up didn't move a line: on %d", cursorLine(f.prompt, 20))
	}
	f.prompt.cur = 0
	if f.promptLine(true) {
		t.Error("up on the first line should leave the prompt")
	}
	if !f.promptLine(false) || cursorLine(f.prompt, 20) != 1 {
		t.Errorf("down didn't move to line 2: on %d", cursorLine(f.prompt, 20))
	}
}
