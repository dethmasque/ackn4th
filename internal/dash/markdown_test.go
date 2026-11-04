package dash

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestMarkdown(t *testing.T) {
	in := []string{
		"## Before Windows ships",
		"- **G3 (needs your decision):** the localhost exemption covers the",
		"  whole profile, see `docs/adr/0001.md` and *why* it matters.",
		"- **an unclosed bold",
		"",
		"snake_case_name and 2 * 3 * 4 stay, [the ADR](https://x.y/adr) links.",
		"```go",
		"x := **not bold**",
		"```",
	}
	out := markdown(in, pText)
	plainOut := make([]string, len(out))
	for i, l := range out {
		plainOut[i] = ansi.Strip(l)
	}
	want := []string{
		"Before Windows ships",
		"- G3 (needs your decision): the localhost exemption covers the",
		"  whole profile, see docs/adr/0001.md and why it matters.",
		"- an unclosed bold",
		"",
		"snake_case_name and 2 * 3 * 4 stay, the ADR (https://x.y/adr) links.",
		"```go",
		"x := **not bold**",
		"```",
	}
	for i := range want {
		if plainOut[i] != want[i] {
			t.Errorf("line %d: %q, want %q", i, plainOut[i], want[i])
		}
	}
	bolded := func(l, word string) bool {
		i := strings.Index(l, word)
		return i >= 0 && strings.LastIndex(l[:i], "\x1b[1m") > strings.LastIndex(l[:i], "\x1b[22m")
	}
	if !bolded(out[1], "G3") || bolded(out[1], "the localhost") {
		t.Errorf("bold: %q", out[1])
	}
	if !strings.Contains(out[2], "\x1b[3m") || bolded(out[2], "whole") {
		t.Errorf("italic, and bold not leaking onto the next line: %q", out[2])
	}
	if bolded(out[5], "snake") {
		t.Errorf("an unclosed ** ran past its paragraph: %q", out[5])
	}
	if got := ansi.Strip(mdLine("Done. ## Changes **README.md** updated", pText)); got != "Done. Changes README.md updated" {
		t.Errorf("one line: %q", got)
	}
}
