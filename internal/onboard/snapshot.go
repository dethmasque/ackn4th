package onboard

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/dethmasque/ackn4th/internal/workspace"
)

// Snapshot limits. Snapshots are taken around every turn, so they reuse
// unchanged files from the previous snapshot by size and mtime.
const (
	maxSnapshotFiles = 5000
	maxKeptText      = 64 << 10 // larger files are tracked by hash only
	maxTotalText     = 4 << 20
)

// File is one file's state in a snapshot.
type File struct {
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mtime"`
	// Hash identifies the contents; empty when they could not be read
	// (binary, too large, or a credential file agents may not see).
	Hash string `json:"hash,omitempty"`
	// Text is kept for small files so later changes can be shown as diffs.
	Text string `json:"text,omitempty"`
}

func (f File) sameAs(g File) bool {
	if f.Hash != "" || g.Hash != "" {
		return f.Hash == g.Hash
	}
	return f.Size == g.Size && f.ModTime.Equal(g.ModTime)
}

// Snapshot records the workspace's files at one moment.
type Snapshot struct {
	Files map[string]File `json:"files"`
	// Truncated is set when the workspace had more files than are tracked.
	Truncated bool `json:"truncated,omitempty"`
}

// Take snapshots the workspace. Files unchanged since prev (same size and
// mtime) are not re-read. prev may be nil.
func Take(ws *workspace.Workspace, prev *Snapshot) *Snapshot {
	s := &Snapshot{Files: map[string]File{}}
	ignore := loadIgnore(ws)
	entries, truncated, _ := ws.List(".", 64, maxSnapshotFiles*2)
	s.Truncated = truncated
	kept := 0
	for _, e := range entries {
		if e.IsDir || ignore.match(e.Path) {
			continue
		}
		if len(s.Files) >= maxSnapshotFiles {
			s.Truncated = true
			break
		}
		f := File{Size: e.Size, ModTime: e.ModTime}
		if p, ok := prev.file(e.Path); ok && p.Size == f.Size && p.ModTime.Equal(f.ModTime) {
			f = p
		} else if text, err := ws.ReadFile(e.Path, 0, 0); err == nil {
			sum := sha256.Sum256([]byte(text))
			f.Hash = hex.EncodeToString(sum[:12])
			f.Text = text
		}
		if f.Text != "" && (len(f.Text) > maxKeptText || kept+len(f.Text) > maxTotalText) {
			f.Text = ""
		}
		kept += len(f.Text)
		s.Files[e.Path] = f
	}
	return s
}

func (s *Snapshot) file(p string) (File, bool) {
	if s == nil {
		return File{}, false
	}
	f, ok := s.Files[p]
	return f, ok
}

// Change is one file that differs between two snapshots.
type Change struct {
	Path     string
	Op       byte // 'A' added, 'M' modified, 'D' deleted
	Old, New File
}

// Diff lists the files that differ from old to new, sorted by path.
func Diff(old, new *Snapshot) []Change {
	var out []Change
	if new == nil {
		return nil
	}
	for p, nf := range new.Files {
		of, ok := old.file(p)
		switch {
		case !ok:
			out = append(out, Change{Path: p, Op: 'A', New: nf})
		case !of.sameAs(nf):
			out = append(out, Change{Path: p, Op: 'M', Old: of, New: nf})
		}
	}
	if old != nil {
		for p, of := range old.Files {
			if _, ok := new.Files[p]; !ok {
				out = append(out, Change{Path: p, Op: 'D', Old: of})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// Digest limits.
const (
	maxDigestFiles = 80
	maxFileDiff    = 6 << 10
	maxNewFileShow = 80 // lines of a new file shown in full
)

// Digest describes changes for an agent catching up: a list of changed
// files with who changed them, then diffs while they fit in maxBytes.
// authors maps a path to whoever last changed it; changes whose author is
// self are left out, since the agent made them. It also returns the
// changes shown in full (as diffs or whole new files), which the agent
// now knows the current version of.
func Digest(changes []Change, authors map[string]string, self string, maxBytes int) (string, []Change) {
	var others []Change
	for _, c := range changes {
		if self == "" || authors[c.Path] != self {
			others = append(others, c)
		}
	}
	if len(others) == 0 {
		return "", nil
	}
	var list, diffs strings.Builder
	var skipped []string
	var shown []Change
	fmt.Fprintf(&list, "%d file(s) changed in the workspace since your last turn:\n\n", len(others))
	for i, c := range others {
		if i == maxDigestFiles {
			fmt.Fprintf(&list, "  … and %d more\n", len(others)-i)
			break
		}
		who := authors[c.Path]
		if who == "" {
			who = "unknown"
		}
		body, stat := changeDiff(c)
		fmt.Fprintf(&list, "  %c %s%s  (%s)\n", c.Op, c.Path, stat, who)
		switch {
		case body == "":
			if c.Op != 'D' {
				skipped = append(skipped, c.Path)
			}
		case diffs.Len()+len(body) > maxBytes-list.Len()-512:
			skipped = append(skipped, c.Path)
		default:
			diffs.WriteString(body)
			shown = append(shown, c)
		}
	}
	out := list.String()
	if diffs.Len() > 0 {
		out += "\nDiffs (any earlier copy of these files in your context is now stale):\n\n```diff\n" + diffs.String() + "```\n"
	}
	if len(skipped) > 0 {
		out += "\nNo diff shown for: " + strings.Join(skipped, ", ") + ". Read them if they matter to your work.\n"
	}
	return out, shown
}

// changeDiff renders one change as a unified diff, or "" when it cannot be
// shown compactly, plus a short +/- summary.
func changeDiff(c Change) (string, string) {
	head := fmt.Sprintf("--- a/%s\n+++ b/%s\n", c.Path, c.Path)
	switch c.Op {
	case 'D':
		return "", ""
	case 'A':
		if c.New.Hash == "" {
			return "", " binary or unreadable"
		}
		lines := splitLines(c.New.Text)
		if c.New.Text == "" && c.New.Size > 0 {
			return "", fmt.Sprintf(" %d bytes", c.New.Size)
		}
		stat := fmt.Sprintf(" +%d", len(lines))
		if len(lines) > maxNewFileShow {
			return "", stat
		}
		var b strings.Builder
		fmt.Fprintf(&b, "--- /dev/null\n+++ b/%s\n@@ -0,0 +1,%d @@\n", c.Path, len(lines))
		for _, l := range lines {
			b.WriteString("+" + l + "\n")
		}
		return b.String(), stat
	}
	if c.Old.Text == "" && c.Old.Size > 0 || c.New.Text == "" && c.New.Size > 0 {
		return "", ""
	}
	edits, ok := diffLines(splitLines(c.Old.Text), splitLines(c.New.Text))
	if !ok {
		return "", " rewritten"
	}
	body, adds, dels := unified(edits, 3)
	stat := fmt.Sprintf(" +%d -%d", adds, dels)
	if len(body) > maxFileDiff {
		return "", stat
	}
	return head + body, stat
}

// ignore holds the workspace's root .gitignore patterns, so build output
// and other generated files do not flood snapshots.
type ignore []string

func loadIgnore(ws *workspace.Workspace) ignore {
	text, err := ws.ReadFile(".gitignore", 0, 0)
	if err != nil {
		return nil
	}
	var out ignore
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") {
			continue
		}
		out = append(out, strings.TrimPrefix(line, "/"))
	}
	return out
}

func (ig ignore) match(p string) bool {
	for _, pat := range ig {
		if workspace.MatchGlob(pat, p) || workspace.MatchGlob(strings.TrimSuffix(pat, "/")+"/", p) {
			return true
		}
		// A pattern without a slash ignores a directory of that name, and
		// everything in it, at any depth.
		if !strings.Contains(strings.TrimSuffix(pat, "/"), "/") {
			for d := p; d != "."; d = path.Dir(d) {
				if workspace.MatchGlob(strings.TrimSuffix(pat, "/"), d) {
					return true
				}
			}
		}
	}
	return false
}
