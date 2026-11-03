package workspace

import (
	"regexp"
	"strings"
	"sync"
)

var (
	globMu    sync.Mutex
	globCache = map[string]*regexp.Regexp{}
)

// MatchGlob reports whether the slash-separated relative path matches pattern.
//
// Patterns follow gitignore conventions:
//   - `*` matches within one path segment, `?` matches one character
//   - `**` matches any number of segments (including none)
//   - a pattern with no slash matches the file's base name at any depth,
//     so `*.md` matches both README.md and docs/guide.md
//   - a trailing `/` matches a directory and everything inside it
func MatchGlob(pattern, path string) bool {
	if pattern == "" {
		return false
	}
	if strings.HasSuffix(pattern, "/") {
		pattern += "**"
	}
	if !strings.Contains(pattern, "/") {
		pattern = "**/" + pattern
	}
	return compileGlob(pattern).MatchString(path)
}

func compileGlob(pattern string) *regexp.Regexp {
	globMu.Lock()
	defer globMu.Unlock()
	if re, ok := globCache[pattern]; ok {
		return re
	}
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		switch {
		case c == '*' && i+1 < len(pattern) && pattern[i+1] == '*':
			i++
			if i+1 < len(pattern) && pattern[i+1] == '/' {
				// "**/" — zero or more leading directories.
				i++
				b.WriteString("(?:.*/)?")
			} else {
				b.WriteString(".*")
			}
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	re := regexp.MustCompile(b.String())
	globCache[pattern] = re
	return re
}
