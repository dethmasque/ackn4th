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
		prefetch []string
		pending  string
		due      <-chan time.Time
	)
	defer func() { stop() }()
	for {
		// Prefetch only while there is nothing to say.
		if pending == "" && len(prefetch) > 0 && len(cmds) == 0 {
			sp.wav(prefetch[0])
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
			wav, err := sp.wav(text)
			if err != nil {
				continue
			}
			// A newer phrase may have come in while this one was synthesized.
			if len(cmds) > 0 {
				continue
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { defer close(done); Play(ctx, wav) }()
			stop = func() { cancel(); <-done }
		}
	}
}

type server struct {
	e     *Engine
	s     Settings
	cache string
}

// wav returns the cached WAV for text, synthesizing it first if needed.
func (sp *server) wav(text string) (string, error) {
	text = Normalize(text)
	if text == "" {
		return "", fmt.Errorf("voice: nothing to say")
	}
	h := sha256.Sum256(fmt.Appendf(nil, "%s|%d|%.2f|%s", engineVersion, sp.s.sid(), sp.s.speed(), text))
	p := filepath.Join(sp.cache, hex.EncodeToString(h[:12])+".wav")
	if _, err := os.Stat(p); err == nil {
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
	return p, os.Rename(f.Name(), p)
}
