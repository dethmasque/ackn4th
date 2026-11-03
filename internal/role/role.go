// Package role loads agent role definitions.
//
// A role is a Markdown file with YAML front matter:
//
//	---
//	name: backend_dev
//	title: Backend Developer
//	summary: One line shown in `ackn4th roles`.
//	skills: [Go, SQL, API design]
//	writable: ["**"]        # globs the role may modify; omit for read-only
//	---
//	The body is the role's system prompt.
//
// Built-in roles are embedded from the repository's roles/ directory. Files
// in <config>/roles override or extend them by name; Store manages them.
package role

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/dethmasque/ackn4th/roles"
)

// Role is one agent persona and its permissions.
type Role struct {
	Name     string   `yaml:"name"`
	Title    string   `yaml:"title"`
	Summary  string   `yaml:"summary"`
	Skills   []string `yaml:"skills"`
	Writable []string `yaml:"writable"`
	Prompt   string   `yaml:"-"`
	Source   string   `yaml:"-"`
}

// ReadOnly reports whether the role may not modify any file.
func (r *Role) ReadOnly() bool { return len(r.Writable) == 0 }

var validName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// Parse reads a role definition.
func Parse(data []byte, source string) (*Role, error) {
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	if !bytes.HasPrefix(data, []byte("---\n")) {
		return nil, fmt.Errorf("%s: missing front matter", source)
	}
	front, body, ok := bytes.Cut(data[4:], []byte("\n---\n"))
	if !ok {
		return nil, fmt.Errorf("%s: unterminated front matter", source)
	}
	var r Role
	if err := yaml.Unmarshal(front, &r); err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	if !validName.MatchString(r.Name) {
		return nil, fmt.Errorf("%s: invalid role name %q (use lower_snake_case)", source, r.Name)
	}
	if r.Title == "" {
		r.Title = r.Name
	}
	r.Prompt = strings.TrimSpace(string(body))
	if r.Prompt == "" {
		return nil, fmt.Errorf("%s: empty prompt", source)
	}
	r.Source = source
	return &r, nil
}

// Registry holds roles by name.
type Registry struct {
	roles map[string]*Role
	// Invalid lists user role files that couldn't be read, so one bad
	// file doesn't stop every command; `ackn4th roles` shows them.
	Invalid []error
}

// isRoleFile reports whether a file defines a role: any Markdown file but
// a README, which documents them.
func isRoleFile(name string) bool {
	return filepath.Ext(name) == ".md" && !strings.EqualFold(filepath.Base(name), "README.md")
}

// Load reads the built-in roles, then any overrides in the given directories.
// Missing directories are skipped.
func Load(overrideDirs ...string) (*Registry, error) {
	reg := &Registry{roles: map[string]*Role{}}
	err := fs.WalkDir(roles.FS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !isRoleFile(p) {
			return err
		}
		data, err := fs.ReadFile(roles.FS, p)
		if err != nil {
			return err
		}
		r, err := Parse(data, "builtin:"+p)
		if err != nil {
			return err
		}
		reg.roles[r.Name] = r
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, dir := range overrideDirs {
		entries, err := os.ReadDir(dir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() || !isRoleFile(e.Name()) {
				continue
			}
			p := filepath.Join(dir, e.Name())
			data, err := os.ReadFile(p)
			if err != nil {
				return nil, err
			}
			r, err := Parse(data, p)
			if err != nil {
				reg.Invalid = append(reg.Invalid, err)
				continue
			}
			reg.roles[r.Name] = r
		}
	}
	return reg, nil
}

// Get returns the named role. Hyphens are accepted in place of
// underscores, and roles joined with "+" (backend_dev+qa_engineer) name one
// agent playing all of them; see Combine.
func (g *Registry) Get(name string) (*Role, error) {
	name = strings.ReplaceAll(strings.ToLower(strings.TrimSpace(name)), "-", "_")
	if strings.Contains(name, "+") {
		var parts []*Role
		for _, n := range strings.Split(name, "+") {
			r, err := g.Get(n)
			if err != nil {
				return nil, err
			}
			parts = append(parts, r)
		}
		return Combine(parts...), nil
	}
	if r, ok := g.roles[name]; ok {
		return r, nil
	}
	return nil, fmt.Errorf("unknown role %q (see `ackn4th roles`)", name)
}

// Combine makes one role out of several, for a single agent that does all
// of their jobs: it gets every role's prompt and may write wherever any of
// them may. Repeated roles are dropped; one role is returned as is.
func Combine(roles ...*Role) *Role {
	var uniq []*Role
	for _, r := range roles {
		if !slices.ContainsFunc(uniq, func(u *Role) bool { return u.Name == r.Name }) {
			uniq = append(uniq, r)
		}
	}
	if len(uniq) == 1 {
		return uniq[0]
	}
	c := &Role{Source: "combined"}
	var names, titles, summaries []string
	var prompt strings.Builder
	prompt.WriteString("You play several roles at once. Do each one's job, and when they pull in different directions (shipping versus testing, say), make the trade-off deliberately and say so.\n")
	for _, r := range uniq {
		names = append(names, r.Name)
		titles = append(titles, r.Title)
		summaries = append(summaries, r.Summary)
		for _, s := range r.Skills {
			if !slices.Contains(c.Skills, s) {
				c.Skills = append(c.Skills, s)
			}
		}
		for _, w := range r.Writable {
			if !slices.Contains(c.Writable, w) {
				c.Writable = append(c.Writable, w)
			}
		}
		fmt.Fprintf(&prompt, "\n## As the %s\n\n%s\n", r.Title, strings.TrimSpace(r.Prompt))
	}
	c.Name = strings.Join(names, "+")
	c.Title = strings.Join(titles, " & ")
	c.Summary = strings.Join(summaries, " ")
	c.Prompt = prompt.String()
	return c
}

// All returns every role sorted by name.
func (g *Registry) All() []*Role {
	out := make([]*Role, 0, len(g.roles))
	for _, r := range g.roles {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
