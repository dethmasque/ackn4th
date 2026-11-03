package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Speaker talks to the helper process. Its methods never block on the
// voice and are safe to call with a nil *Speaker (the voice is off).
type Speaker struct {
	mu   sync.Mutex
	cmd  *exec.Cmd
	out  chan command
	last string
}

// Start launches exe (this program) as the voice helper, with
// `__voice serve dir voice speed`. Its output is discarded.
func Start(exe, dir string, s Settings) (*Speaker, error) {
	if !Installed(dir) {
		return nil, ErrNotInstalled
	}
	if err := CanPlay(); err != nil {
		return nil, err
	}
	cmd := exec.Command(exe, helperArgs("serve", dir, s)...)
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stdout, cmd.Stderr = nil, nil
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	sp := &Speaker{cmd: cmd, out: make(chan command, 32)}
	go func() {
		defer in.Close() // ends the helper
		enc := json.NewEncoder(in)
		for c := range sp.out {
			if enc.Encode(c) != nil {
				return
			}
		}
	}()
	return sp, nil
}

func (sp *Speaker) send(c command) {
	select {
	case sp.out <- c:
	default: // the helper is behind; the next phrase will supersede this one
	}
}

// Say speaks text, cutting off what is playing. Saying the same text twice
// in a row says it once: callers may call it on every redraw.
func (sp *Speaker) Say(text string) {
	if sp == nil || text == "" {
		return
	}
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if text == sp.last || sp.out == nil {
		return
	}
	sp.last = text
	sp.send(command{Say: text})
}

// Repeat says the last phrase again.
func (sp *Speaker) Repeat() {
	if sp == nil {
		return
	}
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if sp.out != nil && sp.last != "" {
		sp.send(command{Say: sp.last})
	}
}

// Prefetch synthesizes phrases ahead of time, so saying them is instant.
func (sp *Speaker) Prefetch(texts ...string) {
	if sp == nil {
		return
	}
	sp.mu.Lock()
	defer sp.mu.Unlock()
	for _, t := range texts {
		if sp.out != nil {
			sp.send(command{Prefetch: t})
		}
	}
}

// Close silences and stops the helper.
func (sp *Speaker) Close() {
	if sp == nil {
		return
	}
	sp.mu.Lock()
	if sp.out == nil {
		sp.mu.Unlock()
		return
	}
	close(sp.out)
	sp.out = nil
	sp.mu.Unlock()
	done := make(chan struct{})
	go func() { sp.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		sp.cmd.Process.Kill()
		<-done
	}
}

func helperArgs(mode, dir string, s Settings) []string {
	return []string{"__voice", mode, dir, s.Voice, strconv.FormatFloat(float64(s.speed()), 'f', 2, 32)}
}

// SayOnce speaks text with a helper and returns when it has finished.
func SayOnce(ctx context.Context, exe, dir string, s Settings, text string) error {
	if !Installed(dir) {
		return ErrNotInstalled
	}
	if err := CanPlay(); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, exe, append(helperArgs("say", dir, s), text)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// The engine's own diagnostics, without its source paths.
		lines := strings.Split(strings.TrimSpace(stderr.String()), "\n")
		return fmt.Errorf("voice: %s", strings.TrimSpace(lines[len(lines)-1]))
	}
	return nil
}

// Main is the helper's entry point: `serve dir voice speed`, reading
// commands from stdin, or `say dir voice speed text`.
func Main(args []string) int {
	if len(args) < 4 {
		return 2
	}
	dir := args[1]
	f, _ := strconv.ParseFloat(args[3], 32)
	s := Settings{Voice: args[2], Speed: float32(f)}
	switch args[0] {
	case "serve":
		if err := Serve(dir, s, os.Stdin); err != nil {
			return 1
		}
		return 0
	case "say":
		if len(args) < 5 {
			return 2
		}
		e, err := Load(dir)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		defer e.Close()
		sp := &server{e: e, s: s, cache: filepath.Join(dir, "phrases")}
		os.MkdirAll(sp.cache, 0o700)
		wav, err := sp.cached(Normalize(args[4]))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err := Play(context.Background(), wav); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	}
	return 2
}
