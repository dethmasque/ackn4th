// Package ui renders ackn4th's ASCII-art terminal output: banners, boxes,
// tables, gauges and the animations that play while agents work.
package ui

import (
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"golang.org/x/term"
)

// Term describes an output stream and what it can render.
type Term struct {
	W       io.Writer
	Color   bool // ANSI colors allowed
	Effects bool // animations allowed (interactive terminal, effects enabled)
	Width   int
}

// NewTerm inspects w. Colors and effects are disabled for non-terminals,
// when NO_COLOR is set, or when ACKN4TH_NO_FX is set. FORCE_COLOR enables
// colors (never animations) even when w is not a terminal, e.g. for less -R.
func NewTerm(w io.Writer, effects bool) *Term {
	t := &Term{W: w, Width: 80}
	f, ok := w.(*os.File)
	if ok && term.IsTerminal(int(f.Fd())) {
		t.Color = os.Getenv("NO_COLOR") == ""
		t.Effects = effects && os.Getenv("ACKN4TH_NO_FX") == ""
		if width, _, err := term.GetSize(int(f.Fd())); err == nil && width > 20 {
			t.Width = width
		}
	}
	if os.Getenv("FORCE_COLOR") != "" && os.Getenv("NO_COLOR") == "" {
		t.Color = true
	}
	if t.Width > 110 {
		t.Width = 110
	}
	return t
}

// ANSI styles. The palette leans on terminal green, as is traditional when
// jacking in.
const (
	reset   = "\x1b[0m"
	bold    = "\x1b[1m"
	dim     = "\x1b[2m"
	green   = "\x1b[38;5;46m"
	lime    = "\x1b[38;5;120m"
	moss    = "\x1b[38;5;28m"
	amber   = "\x1b[38;5;214m"
	red     = "\x1b[38;5;196m"
	cyan    = "\x1b[38;5;51m"
	magenta = "\x1b[38;5;177m"
	coral   = "\x1b[38;5;209m"
	gray    = "\x1b[38;5;244m"
)

func (t *Term) style(code, s string) string {
	if !t.Color || s == "" {
		return s
	}
	return code + s + reset
}

func (t *Term) Bold(s string) string    { return t.style(bold, s) }
func (t *Term) Dim(s string) string     { return t.style(dim, s) }
func (t *Term) Green(s string) string   { return t.style(green, s) }
func (t *Term) Lime(s string) string    { return t.style(lime, s) }
func (t *Term) Moss(s string) string    { return t.style(moss, s) }
func (t *Term) Amber(s string) string   { return t.style(amber, s) }
func (t *Term) Red(s string) string     { return t.style(red, s) }
func (t *Term) Cyan(s string) string    { return t.style(cyan, s) }
func (t *Term) Magenta(s string) string { return t.style(magenta, s) }
func (t *Term) Gray(s string) string    { return t.style(gray, s) }

// ProviderColor gives each provider a stable accent: Claude amber, Gemini
// cyan, Claude Code coral.
func (t *Term) ProviderColor(provider, s string) string {
	switch provider {
	case "claude":
		return t.Amber(s)
	case "gemini":
		return t.Cyan(s)
	case "claude-code":
		return t.style(coral, s)
	case "openai":
		return t.Green(s)
	case "deepseek":
		return t.style("\x1b[38;2;77;107;254m", s)
	case "codex":
		return t.style("\x1b[38;2;140;200;255m", s)
	case "cursor":
		return t.style("\x1b[38;2;230;230;230m", s)
	case "copilot":
		return t.style("\x1b[38;2;180;140;255m", s)
	}
	return t.Magenta(s)
}

// Println writes a line.
func (t *Term) Println(parts ...string) {
	io.WriteString(t.W, strings.Join(parts, "")+"\n")
}

// visibleLen counts runes, ignoring ANSI escape sequences.
func visibleLen(s string) int {
	n := 0
	for i := 0; i < len(s); {
		if s[i] == '\x1b' {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			i++
			continue
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
		n++
	}
	return n
}

// pad right-pads s with spaces to visible width w.
func pad(s string, w int) string {
	if n := visibleLen(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}

// Truncate shortens s to at most w runes, adding an ellipsis. s must not
// contain ANSI codes.
func Truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= w {
		return s
	}
	if w == 1 {
		return "…"
	}
	return string(r[:w-1]) + "…"
}

// Wrap breaks plain text into lines no wider than w runes, preserving
// existing line breaks.
func Wrap(s string, w int) []string {
	if w < 10 {
		w = 10
	}
	var out []string
	for _, para := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(para) == "" {
			out = append(out, "")
			continue
		}
		indent := para[:len(para)-len(strings.TrimLeft(para, " \t"))]
		line := indent
		for _, word := range strings.Fields(para) {
			for utf8.RuneCountInString(word) > w {
				if strings.TrimSpace(line) != "" {
					out = append(out, line)
					line = indent
				}
				r := []rune(word)
				out = append(out, string(r[:w]))
				word = string(r[w:])
			}
			switch {
			case strings.TrimSpace(line) == "":
				line = indent + word
			case utf8.RuneCountInString(line)+1+utf8.RuneCountInString(word) > w:
				out = append(out, line)
				line = indent + word
			default:
				line += " " + word
			}
		}
		out = append(out, line)
	}
	return out
}
