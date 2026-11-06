package dash

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestInsertCleansPastedText(t *testing.T) {
	in := newInput("")
	in.insert("a\tb\r\nc\rd\x1b[31me\x07")
	if got, want := in.String(), "a    b\nc\nd[31me"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestEditorLinesStayInsideWidth(t *testing.T) {
	in := newInput("")
	in.insert("\tcol1\tcol2\tcol3\t" + strings.Repeat("x", 200) + "\n\t\tindented")
	for _, l := range editorLines(in, 30, true) {
		if w := ansi.StringWidth(l); w > 30 {
			t.Fatalf("line is %d cells wide, over 30: %q", w, l)
		}
	}
	// Text saved with raw tabs also draws inside the width.
	raw := input{r: []rune("a\tb\tc\tdddddddddddddddddddddddddddddddd")}
	for _, l := range editorLines(raw, 20, false) {
		if strings.ContainsRune(l, '\t') || ansi.StringWidth(l) > 20 {
			t.Fatalf("bad line %q", l)
		}
	}
}
