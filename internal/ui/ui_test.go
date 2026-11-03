package ui

import (
	"bytes"
	"strings"
	"testing"
)

func TestBoxKeepsFrame(t *testing.T) {
	var buf bytes.Buffer
	term := &Term{W: &buf, Color: true, Width: 40}
	term.Box("T", []string{term.Green(strings.Repeat("x", 100)), "short"})
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	for _, l := range lines {
		if n := visibleLen(l); n != visibleLen(lines[0]) {
			t.Errorf("ragged frame: %d vs %d in %q", n, visibleLen(lines[0]), l)
		}
	}
}

func TestWrap(t *testing.T) {
	got := Wrap("the quick brown fox jumps over the lazy dog", 15)
	for _, l := range got {
		if len(l) > 15 {
			t.Errorf("line too long: %q", l)
		}
	}
	if strings.Join(got, " ") != "the quick brown fox jumps over the lazy dog" {
		t.Errorf("words lost: %q", got)
	}
	if got := Wrap(strings.Repeat("a", 25), 10); len(got) != 3 {
		t.Errorf("long word not split: %q", got)
	}
}

func TestTruncate(t *testing.T) {
	if got := Truncate("héllo world", 5); got != "héll…" {
		t.Errorf("Truncate = %q", got)
	}
	if got := truncateVisible("\x1b[1mabcdef\x1b[0m", 4); visibleLen(got) != 4 {
		t.Errorf("truncateVisible = %q", got)
	}
}

func TestBoxLongTitle(t *testing.T) {
	var b strings.Builder
	term := &Term{W: &b, Width: 60}
	term.Box("NOTES · "+strings.Repeat("/very/long/path", 10), []string{"x"})
	for _, l := range strings.Split(strings.TrimSpace(b.String()), "\n") {
		if n := visibleLen(l); n != 60 {
			t.Fatalf("line is %d wide, want 60: %q", n, l)
		}
	}
}
