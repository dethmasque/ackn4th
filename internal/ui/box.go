package ui

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Box draws lines inside a double-ruled frame with an optional title.
//
//	╔══[ TITLE ]═════════╗
//	║ line               ║
//	╚════════════════════╝
func (t *Term) Box(title string, lines []string) {
	inner := 0
	for _, l := range lines {
		if n := visibleLen(l); n > inner {
			inner = n
		}
	}
	if n := visibleLen(title) + 6; n > inner {
		inner = n
	}
	if inner < 52 {
		inner = 52
	}
	if max := t.Width - 4; inner > max {
		inner = max
	}
	top := t.Green("╔" + strings.Repeat("═", inner+2) + "╗")
	if visibleLen(title)+6 > inner+2 {
		title = truncateVisible(title, inner-4)
	}
	if title != "" {
		fill := strings.Repeat("═", inner+2-visibleLen("══[ "+title+" ]"))
		top = t.Green("╔══[ ") + t.Bold(t.Lime(title)) + t.Green(" ]"+fill+"╗")
	}
	t.Println(top)
	for _, l := range lines {
		t.Println(t.Green("║ "), pad(truncateVisible(l, inner), inner), t.Green(" ║"))
	}
	t.Println(t.Green("╚" + strings.Repeat("═", inner+2) + "╝"))
}

// Table renders rows under a header, each column sized to fit.
func (t *Term) Table(header []string, rows [][]string) {
	widths := make([]int, len(header))
	for i, h := range header {
		widths[i] = visibleLen(h)
	}
	for _, r := range rows {
		for i, c := range r {
			if i < len(widths) && visibleLen(c) > widths[i] {
				widths[i] = visibleLen(c)
			}
		}
	}
	line := func(l, m, r string) string {
		parts := make([]string, len(widths))
		for i, w := range widths {
			parts[i] = strings.Repeat("─", w+2)
		}
		return t.Moss(l + strings.Join(parts, m) + r)
	}
	row := func(cells []string, style func(string) string) string {
		var b strings.Builder
		b.WriteString(t.Moss("│"))
		for i, w := range widths {
			c := ""
			if i < len(cells) {
				c = cells[i]
			}
			if style != nil {
				c = style(c)
			}
			b.WriteString(" " + pad(c, w) + " " + t.Moss("│"))
		}
		return b.String()
	}
	t.Println(line("┌", "┬", "┐"))
	t.Println(row(header, func(s string) string { return t.Bold(t.Lime(s)) }))
	t.Println(line("├", "┼", "┤"))
	for _, r := range rows {
		t.Println(row(r, nil))
	}
	t.Println(line("└", "┴", "┘"))
}

// Gauge renders a fill bar: [██████░░░░░░] for frac in [0,1].
func (t *Term) Gauge(frac float64, width int) string {
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	full := int(frac*float64(width) + 0.5)
	bar := strings.Repeat("█", full)
	empty := strings.Repeat("░", width-full)
	color := t.Green
	switch {
	case frac < 0.15:
		color = t.Red
	case frac < 0.35:
		color = t.Amber
	}
	return t.Moss("[") + color(bar) + t.Moss(empty+"]")
}

// KV renders aligned key/value lines for use inside a Box.
func (t *Term) KV(pairs ...[2]string) []string {
	w := 0
	for _, p := range pairs {
		if len(p[0]) > w {
			w = len(p[0])
		}
	}
	out := make([]string, len(pairs))
	for i, p := range pairs {
		out[i] = t.Gray(fmt.Sprintf("%-*s", w, p[0])) + "  " + p[1]
	}
	return out
}

// Duration formats d compactly: 3d4h, 17h42m, 5m12s.
func Duration(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
	case d >= time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}

// Ago formats the time since ts.
func Ago(ts time.Time) string {
	if ts.IsZero() {
		return "—"
	}
	return Duration(time.Since(ts).Round(time.Second)) + " ago"
}

// Bytes formats a byte count.
func Bytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// Tokens formats a token count: 950, 12.4k, 1.2M.
func Tokens(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return fmt.Sprintf("%d", n)
}

// truncateVisible shortens s to w visible runes, keeping ANSI codes intact.
func truncateVisible(s string, w int) string {
	if visibleLen(s) <= w {
		return s
	}
	var b strings.Builder
	n := 0
	for i := 0; i < len(s); {
		if s[i] == '\x1b' {
			j := i
			for j < len(s) && s[j] != 'm' {
				j++
			}
			b.WriteString(s[i:min(j+1, len(s))])
			i = j + 1
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if n == w-1 {
			b.WriteString("…")
			if strings.Contains(s, "\x1b") {
				b.WriteString(reset)
			}
			break
		}
		b.WriteRune(r)
		n++
		i += size
	}
	return b.String()
}
