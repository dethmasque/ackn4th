// Package notes keeps a project's shared knowledge base: Markdown notes
// that agents maintain with the update_notes tool and that every agent
// reads when it joins a task. Notes live in ackn4th's data dir, keyed by
// the workspace path, so they carry over from task to task on the same
// project without adding files to it.
package notes

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// MaxBytes caps the whole notes file, so it stays cheap to hand every
// agent at the start of every task.
const MaxBytes = 24 << 10

const title = "# Project notes\n"

// Book is one project's notes.
type Book struct {
	dir string
	mu  sync.Mutex // agents working at once update in turn
}

// Open returns the notes for the project in workspace (an absolute path).
// Nothing is created until the first update.
func Open(dataDir, workspace string) *Book {
	sum := sha256.Sum256([]byte(filepath.Clean(workspace)))
	return &Book{dir: filepath.Join(dataDir, "projects", hex.EncodeToString(sum[:8]))}
}

// Path is the notes file.
func (b *Book) Path() string { return filepath.Join(b.dir, "notes.md") }

// Read returns the notes, or "" if there are none.
func (b *Book) Read() (string, error) {
	data, err := os.ReadFile(b.Path())
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	return string(data), err
}

// Version identifies the current contents; "" when there are no notes.
func (b *Book) Version() string {
	text, err := b.Read()
	if err != nil || text == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:8])
}

type section struct {
	name string
	body string
}

func parse(text string) []section {
	var out []section
	for _, line := range strings.SplitAfter(text, "\n") {
		if name, ok := strings.CutPrefix(strings.TrimRight(line, "\n"), "## "); ok {
			out = append(out, section{name: strings.TrimSpace(name)})
			continue
		}
		if len(out) > 0 {
			out[len(out)-1].body += line
		}
	}
	return out
}

func render(secs []section) string {
	var b strings.Builder
	b.WriteString(title)
	for _, s := range secs {
		fmt.Fprintf(&b, "\n## %s\n\n%s\n", s.name, strings.TrimSpace(s.body))
	}
	return b.String()
}

// Update replaces a section's content (adding the section if new); empty
// content deletes it. author is recorded under the section.
func (b *Book) Update(name, content, author string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if strings.ContainsAny(name, "#\r\n") {
		return errors.New("section must be a short heading without '#' or newlines")
	}
	name = strings.Join(strings.Fields(name), " ")
	if name == "" || len(name) > 80 {
		return errors.New("section must be a short heading without '#' or newlines")
	}
	text, err := b.Read()
	if err != nil {
		return err
	}
	secs := parse(text)
	i := -1
	for j, s := range secs {
		if strings.EqualFold(s.name, name) {
			i = j
		}
	}
	content = strings.TrimSpace(content)
	switch {
	case content == "" && i >= 0:
		secs = append(secs[:i], secs[i+1:]...)
	case content == "":
		return fmt.Errorf("no section %q to delete", name)
	default:
		// Section headings inside content would split it on the next read.
		content = strings.ReplaceAll("\n"+content, "\n## ", "\n### ")[1:]
		body := fmt.Sprintf("%s\n\n_— %s, %s_", content, author, time.Now().Format("2006-01-02"))
		if i >= 0 {
			secs[i].body = body
		} else {
			secs = append(secs, section{name: name, body: body})
		}
	}
	out := render(secs)
	if len(secs) == 0 {
		out = ""
	}
	if len(out) > MaxBytes {
		return fmt.Errorf("notes would be %d bytes, over the %d-byte limit: condense or delete a section first", len(out), MaxBytes)
	}
	return b.write(out)
}

// Clear deletes the notes.
func (b *Book) Clear() error {
	err := os.Remove(b.Path())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func (b *Book) write(text string) error {
	if text == "" {
		return b.Clear()
	}
	if err := os.MkdirAll(b.dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(b.dir, ".notes-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(text); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), b.Path())
}
