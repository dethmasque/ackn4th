// Package tips walks new users through ackn4th: one-time hints in command
// output and tooltips in the dashboard. Each tip shows until it is
// dismissed once. Tips can be turned off with `tips: false` in
// config.yaml, ACKN4TH_NO_TIPS=1, or `ackn4th disable-tips`.
package tips

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Env turns tips off when set to 1.
const Env = "ACKN4TH_NO_TIPS"

// Hint tells users how to turn tips off; shown with every tip.
const Hint = "tips off: ackn4th disable-tips"

// Book records which tips have been seen.
type Book struct {
	mu   sync.Mutex
	path string
	on   bool
	seen map[string]bool
}

// Load reads the seen tips from dataDir. enabled is the config's tips
// setting; the environment can still turn tips off.
func Load(dataDir string, enabled bool) *Book {
	b := &Book{path: filepath.Join(dataDir, "tips.json"), on: enabled && os.Getenv(Env) != "1", seen: map[string]bool{}}
	if data, err := os.ReadFile(b.path); err == nil {
		_ = json.Unmarshal(data, &b.seen)
	}
	return b
}

// Off is a book that never shows a tip.
func Off() *Book { return &Book{seen: map[string]bool{}} }

// Enabled reports whether tips are shown at all.
func (b *Book) Enabled() bool { return b != nil && b.on }

// Pending reports whether tip id should still be shown.
func (b *Book) Pending(id string) bool {
	if !b.Enabled() {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return !b.seen[id]
}

// Done marks tip id seen, so it is not shown again.
func (b *Book) Done(id string) {
	if !b.Enabled() {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.seen[id] {
		return
	}
	b.seen[id] = true
	data, err := json.Marshal(b.seen)
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(b.path), 0o700) == nil {
		_ = os.WriteFile(b.path, data, 0o600)
	}
}

// Disable stops tips for the rest of this run; persisting that is the
// caller's job (config.yaml).
func (b *Book) Disable() {
	if b != nil {
		b.on = false
	}
}

// Replay shows the tips whose ids start with prefix again, from the first,
// even when tips are off: the walkthrough on request.
func (b *Book) Replay(prefix string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.on = true
	for id := range b.seen {
		if strings.HasPrefix(id, prefix) {
			delete(b.seen, id)
		}
	}
}

// Reset forgets every seen tip, so the walkthrough starts over.
func Reset(dataDir string) error {
	err := os.Remove(filepath.Join(dataDir, "tips.json"))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Seen reports whether one-time step id has happened, whether or not tips
// are on: for things like the provider-setup offer, which isn't a tip.
func (b *Book) Seen(id string) bool {
	if b == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.seen[id]
}

// Mark records one-time step id, whether or not tips are on.
func (b *Book) Mark(id string) {
	if b == nil || b.path == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.seen[id] {
		return
	}
	b.seen[id] = true
	data, err := json.Marshal(b.seen)
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(b.path), 0o700) == nil {
		_ = os.WriteFile(b.path, data, 0o600)
	}
}
