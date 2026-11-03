package voice

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// The engine runs in a helper process (`ackn4th __voice`): its library
// writes diagnostics to stderr, which would scribble over the dashboard,
// and a crash in it takes down only the voice. The dashboard sends it one
// JSON command per line on stdin.

// command is one line of the helper's input.
type command struct {
	Say      string `json:"say,omitempty"`      // speak this now, cutting off whatever is playing
	Prefetch string `json:"prefetch,omitempty"` // synthesize this ahead, to say instantly later
	Stop     bool   `json:"stop,omitempty"`     // fall silent
}

// Settings choose how the voice sounds.
type Settings struct {
	Voice string  // one of Voices; empty means DefaultVoice
	Speed float32 // 1 is normal; 0 means 1
}

func (s Settings) sid() int {
	if id := SpeakerID(s.Voice); id >= 0 {
		return id
	}
	return SpeakerID(DefaultVoice)
}

func (s Settings) speed() float32 {
	if s.Speed <= 0 {
		return 1
	}
	return max(0.5, min(2, s.Speed))
}

// settle is how long a new phrase waits for a newer one, so moving
// through a list quickly speaks only where you stop.
const settle = 150 * time.Millisecond

// Serve runs the helper: it loads the voice installed in dir, then speaks
// the commands read from in until in closes. Phrases are cached as WAV
// files in dir/phrases.
func Serve(dir string, s Settings, in io.Reader) error {
	e, err := Load(dir)
	if err != nil {
		return err
	}
	defer e.Close()
	sp := &server{e: e, s: s, cache: filepath.Join(dir, "phrases")}
	if err := os.MkdirAll(sp.cache, 0o700); err != nil {
		return err
	}
	go prune(sp.cache, cacheMax)

	cmds := make(chan command, 64)
	go func() {
		defer close(cmds)
		sc := bufio.NewScanner(in)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			var c command
			if json.Unmarshal(sc.Bytes(), &c) == nil {
				cmds <- c
			}
		}
	}()

	var (
		stop     = func() {}
		speaking <-chan struct{} // open while a phrase plays
		prefetch []string
		pending  string
		due      <-chan time.Time
	)
	idle := func() bool {
		if speaking == nil {
			return true
		}
		select {
		case <-speaking:
			return true
		default:
			return false
		}
	}
	defer func() { stop() }()
	for {
		// Prefetch only while there is nothing to say, so it never holds
		// up what is being said.
		if pending == "" && len(prefetch) > 0 && len(cmds) == 0 && idle() {
			for _, part := range chunks(Normalize(prefetch[0])) {
				sp.cached(part)
			}
			prefetch = prefetch[1:]
			continue
		}
		select {
		case c, ok := <-cmds:
			if !ok {
				return nil
			}
			switch {
			case c.Stop:
				stop()
				pending, due = "", nil
			case c.Say != "":
				stop()
				pending, due = c.Say, time.After(settle)
			case c.Prefetch != "":
				prefetch = append(prefetch, c.Prefetch)
			}
		case <-due:
			text := pending
			pending, due = "", nil
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { defer close(done); sp.speak(ctx, text) }()
			stop, speaking = func() { cancel(); <-done }, done
		case <-time.After(250 * time.Millisecond):
			// Look again at prefetching once the phrase has played.
		}
	}
}

// speak says text a sentence at a time: the first starts playing as soon
// as it is synthesized, and each next one is synthesized while the one
// before plays. Long descriptions start at once instead of after all of
// them is ready. ctx ends it, mid-sentence if need be.
func (sp *server) speak(ctx context.Context, text string) {
	parts := chunks(Normalize(text))
	wavs := make(chan string, 1)
	go func() {
		defer close(wavs)
		for _, p := range parts {
			if ctx.Err() != nil {
				return
			}
			w, err := sp.cached(p)
			if err != nil {
				continue
			}
			select {
			case wavs <- w:
			case <-ctx.Done():
				return
			}
		}
	}()
	for w := range wavs {
		if ctx.Err() != nil {
			continue // let the synthesizer see ctx and stop
		}
		Play(ctx, w)
	}
}

// chunkMax is about how much text, after the first sentence, is
// synthesized and played at once: enough that the player's start-up
// between pieces isn't heard often, little enough to stay ahead of it.
const chunkMax = 180

// chunks splits normalized text into what speak synthesizes at a time: the
// first sentence alone, so speech starts quickly, then sentences grouped
// up to chunkMax characters.
func chunks(text string) []string {
	var sentences []string
	start := 0
	for i, r := range text {
		if strings.ContainsRune(".!?", r) && (i+1 == len(text) || text[i+1] == ' ') {
			if s := strings.TrimSpace(text[start : i+1]); s != "" {
				sentences = append(sentences, s)
			}
			start = i + 1
		}
	}
	if s := strings.TrimSpace(text[start:]); s != "" {
		sentences = append(sentences, s)
	}
	if len(sentences) == 0 {
		return nil
	}
	out := []string{sentences[0]}
	cur := ""
	for _, s := range sentences[1:] {
		if cur != "" && len(cur)+1+len(s) > chunkMax {
			out = append(out, cur)
			cur = ""
		}
		cur = strings.TrimSpace(cur + " " + s)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

type server struct {
	e       *Engine
	s       Settings
	cache   string
	written atomic.Int32 // phrases synthesized, to prune every so often
	pruning atomic.Bool
}

// The phrase cache keeps what was said most recently, up to cacheMax: the
// dashboard's own phrases come back often and stay, while one-off text (a
// task's prompt, an event) ages out. A phrase is about 400 KB.
const (
	cacheMax   = 128 << 20
	pruneEvery = 64 // new phrases between prunes
)

// prune deletes the least recently used phrases until the cache fits in
// max bytes, and leftovers of interrupted writes.
func prune(dir string, max int64) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type phrase struct {
		path string
		size int64
		used time.Time
	}
	var all []phrase
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		p := filepath.Join(dir, e.Name())
		switch filepath.Ext(e.Name()) {
		case ".wav":
			all = append(all, phrase{p, info.Size(), info.ModTime()})
		case ".part":
			if time.Since(info.ModTime()) > time.Hour {
				os.Remove(p)
			}
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].used.After(all[j].used) })
	var total int64
	for _, ph := range all {
		total += ph.size
		if total > max {
			os.Remove(ph.path)
		}
	}
}

// cached returns the WAV for normalized text, synthesizing it first if
// it isn't cached yet.
func (sp *server) cached(text string) (string, error) {
	if text == "" {
		return "", fmt.Errorf("voice: nothing to say")
	}
	h := sha256.Sum256(fmt.Appendf(nil, "%s|%d|%.2f|%s", engineVersion, sp.s.sid(), sp.s.speed(), text))
	p := filepath.Join(sp.cache, hex.EncodeToString(h[:12])+".wav")
	if _, err := os.Stat(p); err == nil {
		now := time.Now()
		os.Chtimes(p, now, now) // used: keep it over older phrases
		return p, nil
	}
	samples, rate, err := sp.e.Synthesize(text, sp.s.sid(), sp.s.speed())
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp(sp.cache, "*.part")
	if err != nil {
		return "", err
	}
	if err := WriteWAV(f, samples, rate); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	f.Close()
	if err := os.Rename(f.Name(), p); err != nil {
		return "", err
	}
	if sp.written.Add(1)%pruneEvery == 0 && sp.pruning.CompareAndSwap(false, true) {
		go func() { defer sp.pruning.Store(false); prune(sp.cache, cacheMax) }()
	}
	return p, nil
}
