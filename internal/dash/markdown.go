package dash

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Agents write markdown. The dashboard draws its inline styles: **bold**,
// *italic*, `code`, # headings and [links](url), with the markers
// removed. It styles text after wrap has laid it out (wrap keeps
// markdown's shape: lists, quotes, code blocks), carrying a style across
// the lines it wraps over, so a bold phrase that wraps stays bold. Only
// what reads unambiguously is styled: snake_case and a lone * stay as
// they are.

var (
	pCode     = fgp(mix(text, cyan, 0.55))
	pHeading  = func(s string) string { return bold(fgp(lime)(s)) }
	italic    = func(s string) string { return "\x1b[3m" + s + "\x1b[23m" }
	headingRE = regexp.MustCompile(`^(\s*)#{1,6}\s+`)
	linkRE    = regexp.MustCompile(`^\[([^\]\n]+)\]\(([^)\s]+)\)`)
)

// mdState is the inline style open at the end of a line.
type mdState struct {
	bold, italic, code, fence bool
}

// markdown styles wrapped lines, base being the style of plain text.
func markdown(lines []string, base paint) []string {
	var st mdState
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = st.line(l, base)
	}
	return out
}

// mdLine styles one line of markdown on its own: an activity line.
// Headings inside it (text joined onto one line) lose their markers.
func mdLine(s string, base paint) string {
	var st mdState
	s = midHeadingRE.ReplaceAllString(s, "$1")
	return st.line(s, base)
}

var midHeadingRE = regexp.MustCompile(`(^|\s)#{1,6}\s+`)

func (st *mdState) line(l string, base paint) string {
	trim := strings.TrimSpace(l)
	if strings.HasPrefix(trim, "```") {
		st.fence = !st.fence
		*st = mdState{fence: st.fence}
		return pFaint(l)
	}
	if st.fence {
		return pCode(l)
	}
	if trim == "" {
		// Styles don't run across paragraphs: an unclosed ** ends here.
		*st = mdState{}
		return l
	}
	if m := headingRE.FindStringSubmatch(l); m != nil {
		var plain mdState
		return m[1] + pHeading(ansi.Strip(plain.spans(l[len(m[0]):], pText)))
	}
	return st.spans(l, base)
}

// spans styles inline markup in l, continuing from st.
func (st *mdState) spans(l string, base paint) string {
	var b, seg strings.Builder
	flush := func() {
		if seg.Len() == 0 {
			return
		}
		s := seg.String()
		seg.Reset()
		switch {
		case st.code:
			s = pCode(s)
		default:
			s = base(s)
		}
		if st.italic && !st.code {
			s = italic(s)
		}
		if st.bold && !st.code {
			s = bold(s)
		}
		b.WriteString(s)
	}
	r := []rune(l)
	at := func(i int) rune {
		if i < 0 || i >= len(r) {
			return ' '
		}
		return r[i]
	}
	space := func(c rune) bool { return c == ' ' || c == '\t' }
	for i := 0; i < len(r); i++ {
		c := r[i]
		if st.code {
			if c == '`' {
				flush()
				st.code = false
				continue
			}
			seg.WriteRune(c)
			continue
		}
		switch {
		case c == '`':
			flush()
			st.code = true
			continue
		case c == '*' && at(i+1) == '*':
			// ** opens before a word and closes after one.
			if st.bold && !space(at(i-1)) || !st.bold && !space(at(i+2)) && at(i+2) != '*' {
				flush()
				st.bold = !st.bold
				i++
				continue
			}
		case c == '*':
			if st.italic && !space(at(i-1)) || !st.italic && !space(at(i+1)) && (i == 0 || space(at(i-1)) || strings.ContainsRune("([\"'", at(i-1))) {
				flush()
				st.italic = !st.italic
				continue
			}
		case c == '[':
			if m := linkRE.FindStringSubmatch(string(r[i:])); m != nil {
				flush()
				b.WriteString(fgp(cyan)(m[1]) + pFaint(" ("+m[2]+")"))
				i += len([]rune(m[0])) - 1
				continue
			}
		}
		seg.WriteRune(c)
	}
	flush()
	return b.String()
}
