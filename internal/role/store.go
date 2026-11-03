package role

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/dethmasque/ackn4th/roles"
)

// Store keeps the user's own roles: one Markdown file per role in Dir
// (<config>/roles). A file named like a built-in role overrides it.
type Store struct{ Dir string }

// ErrExists means a user role by that name is already saved.
var ErrExists = errors.New("role already exists")

// reserved are names that would be shadowed by `ackn4th roles` subcommands.
var reserved = []string{"add", "edit", "import", "remove", "show", "help"}

// MaxSize bounds a role file, imported or written.
const MaxSize = 256 << 10

// Path is where the user role name lives.
func (s Store) Path(name string) string { return filepath.Join(s.Dir, name+".md") }

// Custom reports whether the user has a role file for name.
func (s Store) Custom(name string) bool {
	_, err := os.Stat(s.Path(name))
	return err == nil
}

// Builtin returns the built-in role name, if there is one.
func Builtin(name string) (*Role, bool) {
	data, err := fs.ReadFile(roles.FS, name+".md")
	if err != nil {
		return nil, false
	}
	r, err := Parse(data, "builtin:"+name+".md")
	return r, err == nil
}

// Validate checks a role before it is saved, beyond what Parse checks.
func Validate(r *Role) error {
	if !validName.MatchString(r.Name) {
		return fmt.Errorf("invalid role name %q: use lower_snake_case, starting with a letter", r.Name)
	}
	if slices.Contains(reserved, r.Name) {
		return fmt.Errorf("%q is reserved for a command: pick another name", r.Name)
	}
	if strings.TrimSpace(r.Prompt) == "" {
		return errors.New("the prompt is empty: it is the role's instructions to the agent")
	}
	for _, w := range r.Writable {
		if w == "" || strings.HasPrefix(w, "/") || filepath.IsAbs(w) || slices.Contains(strings.Split(filepath.ToSlash(w), "/"), "..") {
			return fmt.Errorf("writable path %q must be relative to the workspace, with no .. in it", w)
		}
	}
	return nil
}

// Marshal writes r as a role file.
func Marshal(r *Role) ([]byte, error) {
	front := struct {
		Name     string   `yaml:"name"`
		Title    string   `yaml:"title,omitempty"`
		Summary  string   `yaml:"summary,omitempty"`
		Skills   []string `yaml:"skills,omitempty,flow"`
		Writable []string `yaml:"writable,omitempty,flow"`
	}{r.Name, r.Title, r.Summary, r.Skills, r.Writable}
	y, err := yaml.Marshal(front)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.WriteString("---\n")
	b.Write(y)
	if len(r.Writable) == 0 {
		b.WriteString("# no writable: the role is read-only\n")
	}
	b.WriteString("---\n")
	b.WriteString(strings.TrimSpace(r.Prompt) + "\n")
	return b.Bytes(), nil
}

// Save writes r as a user role. It refuses to replace an existing user
// role unless overwrite is set. It returns the file's path.
func (s Store) Save(r *Role, overwrite bool) (string, error) {
	if err := Validate(r); err != nil {
		return "", err
	}
	data, err := Marshal(r)
	if err != nil {
		return "", err
	}
	return s.write(r.Name, data, overwrite)
}

// SaveFile writes a role file as given, after checking it parses and
// validates; the role's name comes from its front matter.
func (s Store) SaveFile(data []byte, source string, overwrite bool) (*Role, string, error) {
	if len(data) > MaxSize {
		return nil, "", fmt.Errorf("%s: larger than %d KiB", source, MaxSize>>10)
	}
	r, err := Parse(data, source)
	if err != nil {
		return nil, "", err
	}
	if err := Validate(r); err != nil {
		return nil, "", fmt.Errorf("%s: %w", source, err)
	}
	p, err := s.write(r.Name, bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n")), overwrite)
	return r, p, err
}

func (s Store) write(name string, data []byte, overwrite bool) (string, error) {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return "", err
	}
	p := s.Path(name)
	if !overwrite && s.Custom(name) {
		return p, fmt.Errorf("%w: %s", ErrExists, p)
	}
	tmp, err := os.CreateTemp(s.Dir, "."+name+"-*.part")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	return p, os.Rename(tmp.Name(), p)
}

// Remove deletes a user role. For an override, the built-in role comes
// back.
func (s Store) Remove(name string) error {
	err := os.Remove(s.Path(name))
	if errors.Is(err, fs.ErrNotExist) {
		if _, ok := Builtin(name); ok {
			return fmt.Errorf("%s is built in and not edited: there is nothing to remove", name)
		}
		return fmt.Errorf("no role %q of yours", name)
	}
	return err
}

var blobRE = regexp.MustCompile(`^https://github\.com/([^/]+)/([^/]+)/blob/(.+)$`)

// Fetch reads a role file to import: a local path, or an https URL. A
// github.com page for a file (…/blob/…) is read from its raw form.
func Fetch(ctx context.Context, src string) ([]byte, error) {
	if !strings.Contains(src, "://") {
		f, err := os.Open(src)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return readCapped(f, src)
	}
	u, err := url.Parse(src)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("%s: import from a file or an https:// URL", src)
	}
	if m := blobRE.FindStringSubmatch(src); m != nil {
		src = "https://raw.githubusercontent.com/" + m[1] + "/" + m[2] + "/" + m[3]
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", src, resp.Status)
	}
	return readCapped(resp.Body, src)
}

func readCapped(r io.Reader, src string) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxSize {
		return nil, fmt.Errorf("%s: larger than %d KiB, so not a role file", src, MaxSize>>10)
	}
	return data, nil
}

// Template is a starting point for a new role.
func Template(name string) *Role {
	return &Role{
		Name:     name,
		Title:    titleCase(name),
		Summary:  "One line on what this role does, shown in `ackn4th roles`.",
		Skills:   []string{},
		Writable: []string{"**"},
		Prompt: "You are the team's " + strings.ReplaceAll(name, "_", " ") + ".\n\n" +
			"Describe what this role cares about, how it works with the rest of the team,\n" +
			"and what \"done\" means for it. Agents read this as their instructions.",
	}
}

// titleCase turns data_scientist into Data Scientist.
func titleCase(name string) string {
	words := strings.Split(name, "_")
	for i, w := range words {
		if w != "" {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}
