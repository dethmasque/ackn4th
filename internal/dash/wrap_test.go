package dash

import (
	"strings"
	"testing"
)

func TestWrap(t *testing.T) {
	for _, c := range []struct {
		name, in string
		w        int
		want     []string
	}{
		{"words", "make a browser based game where you fit things", 20,
			[]string{"make a browser based", "game where you fit", "things"}},
		{"paragraphs", "first\n\nsecond\n\n", 20, []string{"first", "", "second"}},
		{"list items hang", "Done:\n- added rate limiting to every API route\n12. wrote the tests", 24,
			[]string{"Done:", "- added rate limiting to", "  every API route", "12. wrote the tests"}},
		{"indent kept", "    indented text that wraps around", 20,
			[]string{"    indented text", "    that wraps", "    around"}},
		{"code keeps spacing", "```\nif x {\n\treturn  y\n}\n```", 20,
			[]string{"```", "if x {", "    return  y", "}", "```"}},
		{"long word split", "see https://example.com/a/very/long/path now", 16,
			[]string{"see", "https://example.", "com/a/very/long/", "path now"}},
		{"tiny width", "a b", 2, []string{"a b"}},
	} {
		got := wrap(c.in, c.w)
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
		for _, l := range got {
			if width(l) > max(c.w, 8) {
				t.Errorf("%s: %q is wider than %d", c.name, l, c.w)
			}
		}
	}
}
