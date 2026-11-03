package dash

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// rgb is a truecolor value. Bubble Tea downsamples to what the terminal
// supports (256 colors, 16, or none with NO_COLOR).
type rgb struct{ r, g, b uint8 }

func hex(s string) rgb {
	v, _ := strconv.ParseUint(strings.TrimPrefix(s, "#"), 16, 32)
	return rgb{uint8(v >> 16), uint8(v >> 8), uint8(v)}
}

func (c rgb) fg() string { return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", c.r, c.g, c.b) }
func (c rgb) bg() string { return fmt.Sprintf("\x1b[48;2;%d;%d;%dm", c.r, c.g, c.b) }

// mix blends a toward b by t in [0,1].
func mix(a, b rgb, t float64) rgb {
	t = math.Min(math.Max(t, 0), 1)
	l := func(x, y uint8) uint8 { return uint8(float64(x) + (float64(y)-float64(x))*t + 0.5) }
	return rgb{l(a.r, b.r), l(a.g, b.g), l(a.b, b.b)}
}

// The phosphor theme: deep green-black surfaces, a terminal-green accent,
// and a stable accent per provider.
var (
	bg0    = hex("#050906") // screen
	bg1    = hex("#0a120c") // panels
	bg2    = hex("#101c13") // cards
	bg3    = hex("#173022") // selection
	edge   = hex("#1f3a28") // quiet borders
	text   = hex("#c9f5d0")
	dim    = hex("#6b8f74")
	faint  = hex("#3a5442")
	green  = hex("#39ff6a")
	lime   = hex("#a6ff9e")
	moss   = hex("#1f9e48")
	amber  = hex("#ffb347")
	cyan   = hex("#5ee7ff")
	violet = hex("#c79bff")
	// selection is a mouse selection's background.
	selection = hex("#2c5d8a")
	coral     = hex("#ff8a65")
	pink      = hex("#b48cff")
	skyblue   = hex("#8cc8ff")
	neutral   = hex("#e6e6e6")
	grass     = hex("#10a37f")
	red       = hex("#ff5f6d")
	white     = hex("#ecfff0")
)

// Paint functions reset only what they set (foreground, background, bold),
// so they nest inside a panel without wiping its background.
type paint func(string) string

func fgp(c rgb) paint {
	pre := c.fg()
	return func(s string) string {
		if s == "" {
			return ""
		}
		return pre + s + "\x1b[39m"
	}
}

func onp(c rgb) paint {
	pre := c.bg()
	return func(s string) string {
		if s == "" {
			return ""
		}
		return pre + s + "\x1b[49m"
	}
}

func bold(s string) string { return "\x1b[1m" + s + "\x1b[22m" }

var (
	pText  = fgp(text)
	pDim   = fgp(dim)
	pFaint = fgp(faint)
	pGreen = fgp(green)
	pLime  = fgp(lime)
	pMoss  = fgp(moss)
	pAmber = fgp(amber)
	pRed   = fgp(red)
	pWhite = fgp(white)
)

// providerColor gives each provider a stable accent, as the CLI does.
func providerColor(p string) rgb {
	switch p {
	case "claude":
		return amber
	case "gemini":
		return cyan
	case "claude-code":
		return coral
	case "openai":
		return grass
	case "codex":
		return skyblue
	case "cursor":
		return neutral
	case "copilot":
		return pink
	}
	return violet
}

// pill is a label on a colored background.
func pill(label string, bgc, fgc rgb) string {
	return onp(bgc)(fgp(fgc)(bold(" " + label + " ")))
}

// width is the cell width of s, ignoring escape codes.
func width(s string) int { return lipgloss.Width(s) }

// fit truncates or pads s to exactly w cells.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if width(s) > w {
		s = ansi.Truncate(s, w, "…")
	}
	return s + strings.Repeat(" ", max(0, w-width(s)))
}

// fill makes s exactly w cells on background c. Nested paints that end
// their own background hand it back to c.
func fill(s string, w int, c rgb) string {
	b := c.bg()
	return b + strings.ReplaceAll(fit(s, w), "\x1b[49m", b) + "\x1b[49m"
}

// panel draws a rounded, filled frame of exactly w×h cells around lines,
// on the screen background; lines bring their own left margin. The title sits in a pill on the top edge; a
// focused panel glows green.
func panel(title, right string, lines []string, w, h int, focused bool, surface rgb) string {
	if w < 6 || h < 2 {
		return ""
	}
	border := fgp(edge)
	titleBg, titleFg := bg2, dim
	if focused {
		border, titleBg, titleFg = fgp(moss), green, bg0
	}
	inner := w - 2
	top := border("╭─")
	if title != "" {
		top += pill(title, titleBg, titleFg)
	}
	tail := ""
	if right != "" {
		tail = " " + right + " " + border("─╮")
	} else {
		tail = border("─╮")
	}
	top += border(strings.Repeat("─", max(0, w-width(top)-width(tail)))) + tail
	var b strings.Builder
	b.WriteString(fill(top, w, bg0) + "\n")
	for i := 0; i < h-2; i++ {
		l := ""
		if i < len(lines) {
			l = lines[i]
		}
		b.WriteString(fill(border("│"), 1, bg0) + fill(l, inner, surface) + fill(border("│"), 1, bg0) + "\n")
	}
	b.WriteString(fill(border("╰"+strings.Repeat("─", inner)+"╯"), w, bg0))
	return b.String()
}

// bar is a smooth horizontal gauge using eighth-blocks, its color shifting
// from green through amber to red as it fills.
func bar(frac float64, w int, track rgb) string {
	frac = math.Min(math.Max(frac, 0), 1)
	eighths := []string{"", "▏", "▎", "▍", "▌", "▋", "▊", "▉"}
	full := frac * float64(w)
	var b strings.Builder
	for i := 0; i < w; i++ {
		c := mix(green, amber, float64(i)/float64(max(1, w-1))*1.4)
		if float64(i)/float64(w) > 0.8 {
			c = mix(amber, red, (float64(i)/float64(w)-0.8)*5)
		}
		switch {
		case float64(i+1) <= full:
			b.WriteString(c.fg() + track.bg() + "█")
		case float64(i) < full:
			b.WriteString(c.fg() + track.bg() + eighths[int((full-float64(i))*8)])
			if eighths[int((full-float64(i))*8)] == "" {
				b.WriteString(" ")
			}
		default:
			b.WriteString(track.bg() + " ")
		}
	}
	return b.String() + "\x1b[39m\x1b[49m"
}

// spark draws values as a one-line bar chart.
func spark(vals []int, c rgb) string {
	ticks := []rune("▁▂▃▄▅▆▇█")
	top := 1
	for _, v := range vals {
		top = max(top, v)
	}
	var b strings.Builder
	for _, v := range vals {
		if v == 0 {
			b.WriteString(pFaint("·"))
			continue
		}
		i := v * (len(ticks) - 1) / top
		b.WriteString(fgp(mix(c, white, float64(i)/14))(string(ticks[i])))
	}
	return b.String()
}

// wrap breaks text into lines of at most w cells, between words where it
// can. It keeps the shape of what people and models write: blank lines
// stay, an indented or list line wraps under its own text, and fenced code
// keeps its spacing, cut at w instead of reflowed. A word wider than w is
// split.
func wrap(s string, w int) []string {
	if w < 8 {
		w = 8
	}
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\t", "    ")
	var out []string
	code := false
	for _, para := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(para), "```") {
			code = !code
			out = append(out, hardwrap(strings.TrimRight(para, " "), w)...)
			continue
		}
		if code {
			out = append(out, hardwrap(strings.TrimRight(para, " "), w)...)
			continue
		}
		out = append(out, wrapLine(para, w)...)
	}
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	return out
}

// listMarker is the start of a list item or quote: "- ", "* ", "1. ", "> ".
var listMarker = regexp.MustCompile(`^(?:[-*+•]|\d{1,3}[.)]|>)\s+`)

// wrapLine wraps one line of prose. Its indent is kept, and the lines it
// wraps onto line up under its text: after a list marker, if it has one.
func wrapLine(line string, w int) []string {
	body := strings.TrimLeft(line, " ")
	if body == "" {
		return []string{""}
	}
	indent := min(width(line)-width(body), w/2)
	hang := indent + width(listMarker.FindString(body))
	if hang > w/2 {
		hang = indent
	}
	var out []string
	cur, room := strings.Repeat(" ", indent), w-indent
	used := 0 // cells of words on cur
	flush := func() {
		out = append(out, cur)
		cur, room, used = strings.Repeat(" ", hang), w-hang, 0
	}
	for _, word := range strings.Fields(body) {
		for width(word) > room {
			if used > 0 {
				flush()
				continue
			}
			cur += ansi.Truncate(word, room, "")
			word = ansi.TruncateLeft(word, room, "")
			used = 1
			flush()
		}
		switch {
		case used == 0:
			cur += word
			used = width(word)
		case used+1+width(word) <= room:
			cur += " " + word
			used += 1 + width(word)
		default:
			flush()
			cur += word
			used = width(word)
		}
	}
	if used > 0 {
		out = append(out, cur)
	}
	return out
}

// hardwrap cuts a line every w cells, keeping its spacing.
func hardwrap(line string, w int) []string {
	return strings.Split(ansi.Hardwrap(line, w, true), "\n")
}

// tokens formats a token count like the CLI: 950, 12.4k, 1.2M.
func tokens(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}

// joinColumns places equal-height blocks side by side.
func joinColumns(blocks ...string) string {
	return lipgloss.JoinHorizontal(lipgloss.Top, blocks...)
}
