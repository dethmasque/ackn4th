package voice

import (
	"bufio"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ackn4th turns text into Kokoro's phonemes itself (sherpa-onnx v2 has no
// phonemizer of its own): it looks words up in the model's US English
// lexicon, derives regular inflections the lexicon lacks ("agents",
// "opened": it has "agent" and "open"), and spells out anything else
// letter by letter.

// phonemizer maps words to their phonemes.
type phonemizer struct {
	dict map[string]string
}

func loadPhonemizer(lexicon string) (*phonemizer, error) {
	f, err := os.Open(lexicon)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	p := &phonemizer{dict: make(map[string]string, 180_000)}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		word, phones, ok := strings.Cut(sc.Text(), " ")
		if ok {
			p.dict[word] = strings.ReplaceAll(phones, " ", "")
		}
	}
	for w, ph := range extraWords {
		if _, ok := p.dict[w]; !ok {
			p.dict[w] = ph
		}
	}
	return p, sc.Err()
}

// extraWords are software words the lexicon lacks, as Kokoro phonemes.
var extraWords = map[string]string{
	"acknforth": "ˌækənfˈɔɹθ", // ack-'n-forth, like back-and-forth
	"dev":       "dˈɛv",
	"devops":    "dˈɛvˌɑps",
	// compound would split it as fulls tack.
	"fullstack": "fˈʊlstˌæk",
	"backend":   "back,end",
	"repo":      "ɹˈipO",
	"config":    "kˈɑnfɪɡ",
	"async":     "ˈAsˌɪŋk",
	"enum":      "ˈinəm",
}

var (
	voiceless = map[rune]bool{'p': true, 't': true, 'k': true, 'f': true, 'θ': true}
	sibilant  = map[rune]bool{'s': true, 'z': true, 'ʃ': true, 'ʒ': true, 'ʧ': true, 'ʤ': true}
)

// lastPhone is the final phoneme, skipping stress marks.
func lastPhone(ph string) rune {
	for len(ph) > 0 {
		r, n := utf8.DecodeLastRuneInString(ph)
		if r != 'ˈ' && r != 'ˌ' {
			return r
		}
		ph = ph[:len(ph)-n]
	}
	return 0
}

// plural adds -s as English says it: tasks, agents, fixes.
func plural(ph string) string {
	switch l := lastPhone(ph); {
	case sibilant[l]:
		return ph + "ᵻz"
	case voiceless[l]:
		return ph + "s"
	}
	return ph + "z"
}

// past adds -ed: worked, opened, wanted.
func past(ph string) string {
	switch l := lastPhone(ph); {
	case l == 't' || l == 'd':
		return ph + "ᵻd"
	case voiceless[l] || l == 's' || l == 'ʃ' || l == 'ʧ':
		return ph + "t"
	}
	return ph + "d"
}

func isVowel(b byte) bool { return strings.IndexByte("aeiou", b) >= 0 }

// word returns w's phonemes: from the lexicon, else as a regular
// inflection of a word in it, else spelled out.
func (p *phonemizer) word(w string) string {
	w = strings.ToLower(w)
	if ph, ok := p.dict[w]; ok {
		return ph
	}
	if ph, ok := p.inflected(w); ok {
		return ph
	}
	if ph, ok := p.compound(w); ok {
		return ph
	}
	if strings.Contains(w, "-") {
		var parts []string
		for _, part := range strings.Split(w, "-") {
			if part != "" {
				parts = append(parts, p.word(part))
			}
		}
		return strings.Join(parts, " ")
	}
	var letters []string
	for _, r := range w {
		if ph, ok := p.dict[string(r)]; ok {
			letters = append(letters, ph)
		}
	}
	return strings.Join(letters, " ")
}

// compound reads w as two words the lexicon has: backend is back end,
// fullstack full stack. The second is stressed less, as in English. Parts
// of three letters or more only, so a name isn't split into nonsense.
func (p *phonemizer) compound(w string) (string, bool) {
	for i := len(w) - 3; i >= 3; i-- {
		a, aok := p.dict[w[:i]]
		if !aok {
			continue
		}
		b, bok := p.dict[w[i:]]
		if !bok {
			b, bok = p.inflected(w[i:])
		}
		if bok {
			return a + strings.ReplaceAll(b, "ˈ", "ˌ"), true
		}
	}
	return "", false
}

// inflected finds w as base+suffix, for a base in the lexicon.
func (p *phonemizer) inflected(w string) (string, bool) {
	try := func(base string, f func(string) string) (string, bool) {
		if ph, ok := p.dict[base]; ok && len(base) >= 2 {
			return f(ph), true
		}
		return "", false
	}
	// A doubled final consonant: stopped, running.
	undouble := func(stem string) string {
		n := len(stem)
		if n >= 3 && stem[n-1] == stem[n-2] && !isVowel(stem[n-1]) {
			return stem[:n-1]
		}
		return ""
	}
	ing := func(ph string) string { return ph + "ɪŋ" }
	type rule struct {
		suffix string
		bases  func(stem string) []string
		f      func(string) string
	}
	rules := []rule{
		{"'s", func(s string) []string { return []string{s} }, plural},
		{"ies", func(s string) []string { return []string{s + "y"} }, plural},
		{"es", func(s string) []string { return []string{s, s + "e"} }, plural},
		{"s", func(s string) []string { return []string{s} }, plural},
		{"ied", func(s string) []string { return []string{s + "y"} }, past},
		{"ed", func(s string) []string { return []string{s, s + "e", undouble(s)} }, past},
		{"ying", func(s string) []string { return []string{s + "ie", s + "y"} }, ing},
		{"ing", func(s string) []string { return []string{s, s + "e", undouble(s)} }, ing},
	}
	for _, r := range rules {
		stem, ok := strings.CutSuffix(w, r.suffix)
		if !ok {
			continue
		}
		for _, b := range r.bases(stem) {
			if ph, ok := try(b, r.f); ok {
				return ph, true
			}
		}
	}
	return "", false
}

// maxSentence bounds one synthesis call's phonemes (Kokoro takes 510
// tokens).
const maxSentence = 400

// sentences turns normalized text into phoneme sentences.
func (p *phonemizer) sentences(text string) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			out = append(out, s)
		}
		cur.Reset()
	}
	var word strings.Builder
	endWord := func() {
		if word.Len() == 0 {
			return
		}
		ph := p.word(word.String())
		word.Reset()
		if ph == "" {
			return
		}
		if cur.Len()+len(ph) > maxSentence*2 { // bytes, roughly 2 per phoneme
			flush()
		}
		if cur.Len() > 0 && !strings.HasSuffix(cur.String(), " ") {
			cur.WriteByte(' ')
		}
		cur.WriteString(ph)
	}
	for _, r := range text {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '\'' || r == '-':
			word.WriteRune(r)
		case strings.ContainsRune(",;:", r):
			endWord()
			cur.WriteRune(r)
			cur.WriteByte(' ')
		case strings.ContainsRune(".!?", r):
			endWord()
			cur.WriteRune(r)
			flush()
		default:
			endWord()
		}
	}
	endWord()
	flush()
	return out
}
