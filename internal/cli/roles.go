package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dethmasque/ackn4th/internal/config"
	"github.com/dethmasque/ackn4th/internal/role"
	"github.com/dethmasque/ackn4th/internal/ui"
)

// roleStore is where the user's roles live: <config>/roles.
func roleStore() (role.Store, error) {
	dir, err := config.Dir()
	if err != nil {
		return role.Store{}, err
	}
	return role.Store{Dir: filepath.Join(dir, "roles")}, nil
}

// editorCmd opens path in the user's editor: $VISUAL, $EDITOR, else
// Notepad on Windows, nano or vi elsewhere.
func editorCmd(path string) (*exec.Cmd, error) {
	for _, env := range []string{"VISUAL", "EDITOR"} {
		if f := strings.Fields(os.Getenv(env)); len(f) > 0 {
			return exec.Command(f[0], append(f[1:], path)...), nil
		}
	}
	if runtime.GOOS == "windows" {
		return exec.Command("notepad", path), nil
	}
	for _, e := range []string{"nano", "vi"} {
		if p, err := exec.LookPath(e); err == nil {
			return exec.Command(p, path), nil
		}
	}
	return nil, errors.New("no editor found: set $EDITOR")
}

// editRole opens a role file in the editor until it is valid or the user
// gives up; then the file is restored to before (or removed if it was
// new). name is the role the file must keep defining.
func (a *app) editRole(path, name string, before []byte) (*role.Role, error) {
	restore := func() {
		if before == nil {
			os.Remove(path)
		} else {
			os.WriteFile(path, before, 0o600)
		}
	}
	for {
		ed, err := editorCmd(path)
		if err != nil {
			restore()
			return nil, err
		}
		ed.Stdin, ed.Stdout, ed.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := ed.Run(); err != nil {
			restore()
			return nil, fmt.Errorf("the editor (%s) failed: %w", ed.Path, err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		r, err := role.Parse(data, path)
		if err == nil {
			err = role.Validate(r)
		}
		if err == nil && r.Name != name {
			err = fmt.Errorf("the file must keep name: %s (to make a copy under another name, use `ackn4th roles add <new> --from %s`)", name, name)
		}
		if err == nil {
			return r, nil
		}
		if a.confirm(err, "edit it again?") != nil {
			restore()
			return nil, fmt.Errorf("%w (nothing was changed)", err)
		}
	}
}

func rolesCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "roles [name]",
		GroupID: "info",
		Short:   "List, show, add, edit and import agent roles",
		Long: `List the agent roles, or show one role's skills, write permissions and
prompt. Add your own, edit any (editing a built-in role saves your copy,
which overrides it), or import role files from a path or URL. Your roles
live in <config>/roles/, one Markdown file each. The dashboard's R screen
does the same.`,
		Example: `  ackn4th roles
  ackn4th roles code_reviewer
  ackn4th roles add game_designer --writable 'design/**' --prompt "You design the game's mechanics…"
  ackn4th roles edit code_reviewer
  ackn4th roles import https://github.com/acme/roles/blob/main/game_designer.md`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := a.roles()
			if err != nil {
				return err
			}
			store, err := roleStore()
			if err != nil {
				return err
			}
			if len(args) == 1 {
				r, err := reg.Get(args[0])
				if err != nil {
					return err
				}
				a.showRole(r)
				return nil
			}
			a.listRoles(reg, store)
			return nil
		},
	}
	cmd.AddCommand(roleAddCmd(a), roleEditCmd(a), roleImportCmd(a), roleRemoveCmd(a))
	return cmd
}

func (a *app) showRole(r *role.Role) {
	t := a.term
	writes := t.Gray("read-only")
	if !r.ReadOnly() {
		writes = strings.Join(r.Writable, "  ")
	}
	lines := t.KV(
		[2]string{"name", t.Lime(r.Name)},
		[2]string{"writes", writes},
		[2]string{"source", t.Gray(r.Source)},
	)
	lines = append(lines, "")
	for _, l := range ui.Wrap(r.Summary, t.Width-8) {
		lines = append(lines, t.Bold(l))
	}
	lines = append(lines, "", t.Bold("skills"))
	for _, l := range ui.Wrap(strings.Join(r.Skills, " · "), t.Width-8) {
		lines = append(lines, t.Green(l))
	}
	lines = append(lines, "", t.Bold("prompt"))
	for _, l := range ui.Wrap(r.Prompt, t.Width-8) {
		lines = append(lines, t.Gray(l))
	}
	t.Box(strings.ToUpper(r.Title), lines)
}

// roleKind says where a role comes from.
func roleKind(r *role.Role, store role.Store) string {
	_, builtin := role.Builtin(r.Name)
	switch {
	case !store.Custom(r.Name):
		return "built-in"
	case builtin:
		return "yours, overrides built-in"
	}
	return "yours"
}

func (a *app) listRoles(reg *role.Registry, store role.Store) {
	t := a.term
	var rows [][]string
	for _, r := range reg.All() {
		writes := t.Gray("read-only")
		switch {
		case len(r.Writable) == 1 && r.Writable[0] == "**":
			writes = t.Green("everything")
		case !r.ReadOnly():
			writes = t.Amber(fmt.Sprintf("%d path rules", len(r.Writable)))
		}
		kind := roleKind(r, store)
		if kind != "built-in" {
			kind = t.Lime(kind)
		} else {
			kind = t.Gray(kind)
		}
		rows = append(rows, []string{t.Lime(r.Name), r.Title, writes, kind, ui.Truncate(r.Summary, 44)})
	}
	t.Table([]string{"ROLE", "TITLE", "WRITES", "SOURCE", "SUMMARY"}, rows)
	for _, err := range reg.Invalid {
		t.Println(t.Red("  ✗ skipped "), inline(t, err.Error()))
	}
	t.Println(t.Gray("  details: "), t.Lime("ackn4th roles <name>"), t.Gray("   yours: "+store.Dir))
	t.Println(t.Gray("  change:  "), t.Lime("ackn4th roles add | edit | import | remove"), t.Gray("   (or R in the dashboard)"))
}

func roleAddCmd(a *app) *cobra.Command {
	var title, summary, prompt, promptFile, from string
	var skills, writable []string
	var readOnly, edit, force bool
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Add a role of your own",
		Long: `Create a role in <config>/roles/<name>.md. Give its settings as flags,
start from another role with --from, or, on a terminal without --prompt,
write it in your editor ($VISUAL or $EDITOR) from a template.

Names are lower_snake_case. Using a built-in role's name overrides it.`,
		Example: `  ackn4th roles add game_designer --title "Game Designer" --skills "level design,balancing" \
    --writable 'design/**' --prompt "You design the game's mechanics and levels…"
  ackn4th roles add strict_reviewer --from code_reviewer --edit
  ackn4th roles add data_scientist            # opens your editor on a template`,
		Args: upTo(1, "<name>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := ""
			if len(args) == 1 {
				name = args[0]
			} else {
				var err error
				name, err = a.ask(errors.New("name the role, e.g. `ackn4th roles add game_designer`"), "role name", func(v string) error {
					return role.Validate(&role.Role{Name: v, Prompt: "x"})
				})
				if err != nil {
					return err
				}
			}
			store, err := roleStore()
			if err != nil {
				return err
			}
			if store.Custom(name) && !force {
				return fmt.Errorf("you already have a role %s: `ackn4th roles edit %s`, or --force to replace it", name, name)
			}
			// The template's placeholder text is only for writing in an
			// editor; a role made from flags starts bare.
			r := role.Template(name)
			if !edit && (prompt != "" || promptFile != "") {
				r = &role.Role{Name: name, Title: r.Title, Writable: []string{"**"}}
			}
			if from != "" {
				reg, err := a.roles()
				if err != nil {
					return err
				}
				src, err := reg.Get(from)
				if err != nil {
					return err
				}
				c := *src
				c.Name, c.Source, c.Title = name, "", r.Title
				r = &c
			}
			f := cmd.Flags()
			if f.Changed("title") {
				r.Title = title
			}
			if f.Changed("summary") {
				r.Summary = summary
			}
			if f.Changed("skills") {
				r.Skills = skills
			}
			if f.Changed("writable") {
				r.Writable = writable
			}
			if readOnly {
				r.Writable = nil
			}
			switch {
			case promptFile != "":
				data, err := readPromptFile(promptFile)
				if err != nil {
					return err
				}
				r.Prompt = data
			case prompt != "":
				r.Prompt = prompt
			case from == "":
				edit = true // a template needs writing
			}
			if edit && !a.interactive() {
				return errors.New("give the role's instructions with --prompt or --prompt-file (or start from another role with --from)")
			}
			before, _ := os.ReadFile(store.Path(name)) // with --force, restored if editing fails
			path, err := store.Save(r, true)
			if err != nil {
				return err
			}
			if edit {
				if r, err = a.editRole(path, name, before); err != nil {
					return err
				}
			}
			a.term.Println(a.term.Green("✓ "), "added role ", a.term.Lime(r.Name), a.term.Gray(" ("+path+")"))
			if _, ok := role.Builtin(name); ok {
				a.term.Println(a.term.Gray("  it overrides the built-in " + name + "; `ackn4th roles remove " + name + "` brings that back"))
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&title, "title", "", "display `name`, e.g. \"Game Designer\"")
	f.StringVar(&summary, "summary", "", "one `line` describing the role, shown in the roles list")
	f.StringSliceVar(&skills, "skills", nil, "comma-separated `skills`")
	f.StringSliceVar(&writable, "writable", nil, "comma-separated `globs` the role may change, e.g. 'src/**,*.md' (default: everything)")
	f.BoolVar(&readOnly, "read-only", false, "the role may not change any file")
	f.StringVarP(&prompt, "prompt", "p", "", "the role's instructions: the `text` of its system prompt")
	f.StringVar(&promptFile, "prompt-file", "", "read the instructions from a `file` ('-' for stdin)")
	f.StringVar(&from, "from", "", "start from a copy of this `role`")
	f.BoolVar(&edit, "edit", false, "open the result in your editor before saving")
	f.BoolVar(&force, "force", false, "replace a role of yours with the same name")
	return cmd
}

func readPromptFile(p string) (string, error) {
	var data []byte
	var err error
	if p == "-" {
		data, err = readAllStdin()
	} else {
		data, err = os.ReadFile(p)
	}
	return strings.TrimSpace(string(data)), err
}

func readAllStdin() ([]byte, error) {
	var b bytes.Buffer
	_, err := b.ReadFrom(os.Stdin)
	return b.Bytes(), err
}

func roleEditCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "edit <name>",
		Short: "Edit a role in your editor",
		Long: `Open a role's file in your editor ($VISUAL or $EDITOR; else nano or vi,
Notepad on Windows). It is checked when you close the editor; if it isn't
valid you can edit it again, or nothing changes.

Editing a built-in role saves your copy in <config>/roles/, which
overrides it; ` + "`ackn4th roles remove <name>`" + ` brings the built-in back.`,
		Example: `  ackn4th roles edit code_reviewer
  EDITOR="code --wait" ackn4th roles edit game_designer`,
		Args: upTo(1, "<name>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !a.interactive() {
				return errors.New("roles edit needs a terminal: in scripts, use `roles add <name> --force` or `roles import`")
			}
			reg, err := a.roles()
			if err != nil {
				return err
			}
			name, err := a.roleArg(args, reg)
			if err != nil {
				return err
			}
			store, err := roleStore()
			if err != nil {
				return err
			}
			path := store.Path(name)
			before, err := os.ReadFile(path)
			if errors.Is(err, os.ErrNotExist) {
				b, ok := role.Builtin(name)
				if !ok {
					return fmt.Errorf("unknown role %q (see `ackn4th roles`)", name)
				}
				before = nil
				if _, err := store.Save(b, false); err != nil {
					return err
				}
				a.term.Println(a.term.Gray("  editing your copy of the built-in " + name + ": it will override it"))
			} else if err != nil {
				return err
			}
			start, _ := os.ReadFile(path)
			r, err := a.editRole(path, name, before)
			if err != nil {
				return err
			}
			if after, _ := os.ReadFile(path); bytes.Equal(start, after) {
				if before == nil {
					os.Remove(path) // an untouched copy of a built-in isn't an override
				}
				a.term.Println(a.term.Gray("  no changes"))
				return nil
			}
			a.term.Println(a.term.Green("✓ "), "saved role ", a.term.Lime(r.Name), a.term.Gray(" ("+path+")"))
			return nil
		},
	}
}

// roleArg returns the role named in args, or asks which one.
func (a *app) roleArg(args []string, reg *role.Registry) (string, error) {
	if len(args) == 1 {
		r, err := reg.Get(args[0])
		if err != nil {
			return "", err
		}
		return r.Name, nil
	}
	var opts []option
	for _, r := range reg.All() {
		opts = append(opts, option{r.Name, r.Title})
	}
	picked, err := a.choose(errors.New("name a role, e.g. `ackn4th roles edit code_reviewer`"), "ROLES", opts, false)
	if err != nil {
		return "", err
	}
	return picked[0], nil
}

func roleImportCmd(a *app) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "import <file|url>...",
		Short: "Import role files from paths or https URLs",
		Long: `Copy role files into <config>/roles/. Each is a Markdown file with YAML
front matter, like ` + "`ackn4th roles add`" + ` writes (the format:
https://github.com/dethmasque/ackn4th/blob/main/roles/README.md). Its name
comes from the front matter, and it is checked before anything is saved. A GitHub file page
(github.com/…/blob/…) is read from its raw form.

An imported role is instructions your agents will follow: read it first.`,
		Example: `  ackn4th roles import ./game_designer.md
  ackn4th roles import https://github.com/acme/roles/blob/main/game_designer.md
  ackn4th roles import roles/*.md --force`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := roleStore()
			if err != nil {
				return err
			}
			failed := 0
			for _, src := range args {
				data, err := role.Fetch(cmd.Context(), src)
				var r *role.Role
				var path string
				if err == nil {
					r, path, err = store.SaveFile(data, src, force)
				}
				if errors.Is(err, role.ErrExists) && a.confirm(err, "replace it?") == nil {
					r, path, err = store.SaveFile(data, src, true)
				}
				if err != nil {
					a.term.Println(a.term.Red("✗ "), inline(a.term, err.Error()))
					failed++
					continue
				}
				note := ""
				if _, ok := role.Builtin(r.Name); ok {
					note = ", overriding the built-in"
				}
				a.term.Println(a.term.Green("✓ "), "imported ", a.term.Lime(r.Name), a.term.Gray(" ("+path+note+")"))
			}
			if failed > 0 {
				return fmt.Errorf("%d of %d imports failed", failed, len(args))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "replace roles of yours with the same names")
	return cmd
}

func roleRemoveCmd(a *app) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Delete a role of yours (an edited built-in goes back to the original)",
		Args:  upTo(1, "<name>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := roleStore()
			if err != nil {
				return err
			}
			// A file too broken to load can still be removed by name.
			name := ""
			if len(args) == 1 && store.Custom(args[0]) {
				name = args[0]
			} else {
				reg, err := a.roles()
				if err != nil {
					return err
				}
				if name, err = a.roleArg(args, reg); err != nil {
					return err
				}
			}
			if !store.Custom(name) {
				return store.Remove(name) // says why there's nothing to remove
			}
			if !yes {
				if err := a.confirm(errors.New("pass --yes to delete "+store.Path(name)), "delete your role "+name+"?"); err != nil {
					return err
				}
			}
			if err := store.Remove(name); err != nil {
				return err
			}
			msg := "removed role "
			if _, ok := role.Builtin(name); ok {
				msg = "removed your edit of "
			}
			a.term.Println(a.term.Green("✓ "), msg, a.term.Lime(name))
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "don't ask first")
	return cmd
}
