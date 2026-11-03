package voice

import (
	"regexp"
	"strconv"
	"strings"
)

// Voices are the model's English speakers, by speaker ID. af_ and am_ are
// American (female, male); bf_ and bm_ British.
var Voices = []string{
	"af_alloy", "af_aoede", "af_bella", "af_heart", "af_jessica", "af_kore", "af_nicole",
	"af_nova", "af_river", "af_sarah", "af_sky", "am_adam", "am_echo", "am_eric",
	"am_fenrir", "am_liam", "am_michael", "am_onyx", "am_puck", "am_santa",
	"bf_alice", "bf_emma", "bf_isabella", "bf_lily", "bm_daniel", "bm_fable", "bm_george", "bm_lewis",
}

// DefaultVoice is the voice used when none is configured: Kokoro's
// highest-rated.
const DefaultVoice = "af_heart"

// SpeakerID is name's speaker ID, or -1.
func SpeakerID(name string) int {
	for i, v := range Voices {
		if v == name {
			return i
		}
	}
	return -1
}

// The lexicon has words, not digits or symbols, and reads an unknown
// all-capitals word as nothing. Normalize rewrites what it can't say.

var (
	numberRE  = regexp.MustCompile(`\d+(?:\.\d+)?`)
	acronymRE = regexp.MustCompile(`\b[A-Z]{2,6}s?\b`)
	// Task IDs and other short letter-digit mixes, e.g. k3f9qz.
	mixedRE  = regexp.MustCompile(`\b(?:[a-z]+\d|\d+[a-z])[a-z0-9]*\b`)
	spacesRE = regexp.MustCompile(`\s+`)
	punctRE  = regexp.MustCompile(`\s+([,.:;!?])`)
	groupRE  = regexp.MustCompile(`(\d),(\d{3})\b`)
	ofRE     = regexp.MustCompile(`(\d)\s*/\s*(\d)`)
	rangeRE  = regexp.MustCompile(`\b(\d)-(\d)\b`)
	// snake_case and SCREAMING_SNAKE names, e.g. roles: TECH_LEAD.
	// Roles joined with "+" (backend_dev+qa_engineer) are one agent with
	// both: read "and"; between numbers "+" stays "plus".
	joinRE  = regexp.MustCompile(`([A-Za-z_])\s*\+\s*([A-Za-z_])`)
	snakeRE = regexp.MustCompile(`\b[A-Za-z][A-Za-z0-9]*(?:_[A-Za-z0-9]+)+\b`)
)

// Words the lexicon has that look like acronyms.
var spoken = map[string]string{
	"OK": "okay", "ID": "I D", "IDs": "I Ds", "PR": "P R", "PRs": "P Rs", "CLI": "C L I", "API": "A P I",
	"TTS": "T T S", "UI": "U I", "URL": "U R L", "GUI": "gooey", "README": "read me", "YAML": "yamel",
	"JSON": "jason", "ESC": "escape", "TAB": "tab", "NEW": "new", "TASK": "task", "SETUP": "setup",
	"ACTIVITY": "activity", "FILES": "files", "MESSAGES": "messages", "OVERVIEW": "overview",
	"YES": "yes", "NO": "no", "ON": "on", "OFF": "off", "MODE": "mode", "ROLES": "roles", "GITHUB": "GitHub",
}

// symbols are read as words.
var symbols = strings.NewReplacer(
	"&", " and ", "+", " plus ", "%", " percent", "@", " at ", "#", " number ",
	"↑↓", " up and down ", "←→", " left and right ", "↑", " up ", "↓", " down ", "←", " left ",
	"⏎", " enter ", "{", "", "}", "", "…,", ",", "….", ".",
	"/", " slash ", "→", ", ", "·", ", ", "—", ", ", "–", ", ", "…", ".",
	"_", " ", "`", "", "*", "", "|", ", ", "[", "", "]", "", "<", "", ">", "",
	"ackn4th", "acknowledge forth",
)

// Normalize turns UI text into something the voice reads naturally.
func Normalize(s string) string {
	for groupRE.MatchString(s) {
		s = groupRE.ReplaceAllString(s, "$1$2")
	}
	for joinRE.MatchString(s) {
		s = joinRE.ReplaceAllString(s, "$1 and $2")
	}
	// A name like TECH_LEAD is words, not an acronym: read it as tech lead.
	s = snakeRE.ReplaceAllStringFunc(s, func(w string) string {
		return strings.ReplaceAll(strings.ToLower(w), "_", " ")
	})
	s = ofRE.ReplaceAllString(s, "$1 of $2")    // round 2/10
	s = rangeRE.ReplaceAllString(s, "$1 to $2") // 1-4 switch tabs
	s = symbols.Replace(s)
	s = mixedRE.ReplaceAllStringFunc(s, spell)
	s = acronymRE.ReplaceAllStringFunc(s, func(w string) string {
		if r, ok := spoken[w]; ok {
			return r
		}
		plural := ""
		if strings.HasSuffix(w, "s") {
			w, plural = w[:len(w)-1], "s"
		}
		return strings.Join(strings.Split(w, ""), " ") + plural
	})
	s = numberRE.ReplaceAllStringFunc(s, func(n string) string {
		whole, frac, ok := strings.Cut(n, ".")
		out := number(whole)
		if ok {
			out += " point " + spell(frac)
		}
		return out
	})
	s = spacesRE.ReplaceAllString(s, " ")
	return strings.TrimSpace(punctRE.ReplaceAllString(s, "$1"))
}

var ones = []string{"zero", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine",
	"ten", "eleven", "twelve", "thirteen", "fourteen", "fifteen", "sixteen", "seventeen", "eighteen", "nineteen"}
var tens = []string{"", "", "twenty", "thirty", "forty", "fifty", "sixty", "seventy", "eighty", "ninety"}

// spell reads each character: "k3f9qz" is "k three f nine q z".
func spell(w string) string {
	var out []string
	for _, r := range w {
		if r >= '0' && r <= '9' {
			out = append(out, ones[r-'0'])
		} else {
			out = append(out, strings.ToUpper(string(r)))
		}
	}
	return " " + strings.Join(out, " ") + " "
}

// number reads digits as a number; very long ones digit by digit.
func number(d string) string {
	n, err := strconv.Atoi(d)
	if err != nil || len(d) > 9 || (len(d) > 1 && d[0] == '0') {
		return spell(d)
	}
	return " " + words(n) + " "
}

func words(n int) string {
	switch {
	case n < 20:
		return ones[n]
	case n < 100:
		if n%10 == 0 {
			return tens[n/10]
		}
		return tens[n/10] + " " + ones[n%10]
	case n < 1000:
		return join(ones[n/100]+" hundred", n%100)
	case n < 1_000_000:
		return join(words(n/1000)+" thousand", n%1000)
	}
	return join(words(n/1_000_000)+" million", n%1_000_000)
}

func join(head string, rest int) string {
	if rest == 0 {
		return head
	}
	return head + " " + words(rest)
}
