package cli

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/dethmasque/ackn4th/internal/ui"
)

// Command groups, in display order.
var groups = []*cobra.Group{
	{ID: "tasks", Title: "TASKS"},
	{ID: "project", Title: "PROJECT"},
	{ID: "session", Title: "PROVIDERS"},
	{ID: "info", Title: "INFO"},
}

// usageError marks mistakes in how a command was invoked, so the error
// output can point at the command's help.
type usageError struct{ error }

func (e usageError) Unwrap() error { return e.error }

// args returns a positional-argument validator with a readable message,
// e.g. args(1, "<task>").
func args(n int, names string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, a []string) error {
		switch {
		case len(a) < n:
			return usageError{fmt.Errorf("missing %s", names)}
		case len(a) > n:
			return usageError{fmt.Errorf("expected only %s, got %q", names, strings.Join(a[n:], " "))}
		}
		return nil
	}
}

// upTo is args for commands that ask for a missing argument: at most n.
func upTo(n int, names string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, a []string) error {
		if len(a) > n {
			return usageError{fmt.Errorf("expected only %s, got %q", names, strings.Join(a[n:], " "))}
		}
		return nil
	}
}

// installHelp replaces cobra's help and usage output with styled versions.
func installHelp(root *cobra.Command, a *app) {
	root.AddGroup(groups...)
	root.SetHelpCommandGroupID("info")
	root.SetCompletionCommandGroupID("info")
	root.SetHelpFunc(func(cmd *cobra.Command, _ []string) {
		renderHelp(helpTerm(a), cmd, a.bannerShown)
	})
	root.SetUsageFunc(func(cmd *cobra.Command) error {
		renderHelp(helpTerm(a), cmd, true)
		return nil
	})
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return usageError{err}
	})
}

func helpTerm(a *app) *ui.Term {
	if a.term != nil {
		return a.term
	}
	// Help runs before PersistentPreRunE, so the app may not be loaded.
	return ui.NewTerm(os.Stdout, false)
}

func section(t *ui.Term, title string) {
	t.Println()
	t.Println(t.Green("▓▒░ "), t.Bold(t.Lime(title)))
}

func renderHelp(t *ui.Term, cmd *cobra.Command, skipBanner bool) {
	if !cmd.HasParent() && !skipBanner {
		static := *t
		static.Effects = false
		static.Banner()
	}

	desc := cmd.Long
	if desc == "" {
		desc = cmd.Short
	}
	for _, l := range ui.Wrap(reflow(desc), t.Width-4) {
		t.Println("  ", inline(t, l))
	}

	section(t, "USAGE")
	if cmd.Runnable() && cmd.HasParent() {
		t.Println("  ", usageLine(t, cmd))
	}
	if cmd.HasAvailableSubCommands() {
		t.Println("  ", t.Bold(t.Lime(cmd.CommandPath())), " ", t.Green("<command>"), " ", t.Gray("[flags]"))
	}
	if len(cmd.Aliases) > 0 {
		t.Println("  ", t.Gray("aliases: "), t.Lime(strings.Join(cmd.Aliases, ", ")))
	}

	if cmd.HasAvailableSubCommands() {
		renderCommands(t, cmd)
	}
	if fs := cmd.LocalFlags(); fs.HasAvailableFlags() {
		section(t, "FLAGS")
		renderFlags(t, fs)
	}
	if fs := cmd.InheritedFlags(); fs.HasAvailableFlags() {
		section(t, "GLOBAL FLAGS")
		renderFlags(t, fs)
	}
	if cmd.Example != "" {
		section(t, "EXAMPLES")
		for _, l := range strings.Split(strings.TrimRight(cmd.Example, "\n"), "\n") {
			t.Println(example(t, l))
		}
	}
	t.Println()
	if cmd.HasAvailableSubCommands() {
		t.Println("  ", t.Gray("Run "), t.Bold(t.Lime(cmd.CommandPath()+" <command> --help")), t.Gray(" for details on a command."))
		t.Println()
	}
}

func usageLine(t *ui.Term, cmd *cobra.Command) string {
	use := strings.Fields(cmd.Use)
	parts := []string{t.Bold(t.Lime(cmd.CommandPath()))}
	for _, u := range use[1:] {
		parts = append(parts, t.Green(u))
	}
	if cmd.HasAvailableFlags() {
		parts = append(parts, t.Gray("[flags]"))
	}
	return strings.Join(parts, " ")
}

func renderCommands(t *ui.Term, cmd *cobra.Command) {
	width := 0
	for _, c := range cmd.Commands() {
		if c.IsAvailableCommand() && len(c.Name()) > width {
			width = len(c.Name())
		}
	}
	show := func(title, id string) {
		var cmds []*cobra.Command
		for _, c := range cmd.Commands() {
			if c.IsAvailableCommand() && c.GroupID == id {
				cmds = append(cmds, c)
			}
		}
		if len(cmds) == 0 {
			return
		}
		section(t, title)
		for _, c := range cmds {
			t.Println("  ", t.Bold(t.Lime(fmt.Sprintf("%-*s", width, c.Name()))), "   ", inline(t, c.Short))
		}
	}
	for _, g := range cmd.Groups() {
		show(g.Title, g.ID)
	}
	show("COMMANDS", "")
}

func renderFlags(t *ui.Term, fs *pflag.FlagSet) {
	type row struct{ plain, styled, usage string }
	var rows []row
	width := 0
	fs.VisitAll(func(f *pflag.Flag) {
		if f.Hidden {
			return
		}
		varname, usage := pflag.UnquoteUsage(f)
		name := "    "
		if f.Shorthand != "" {
			name = "-" + f.Shorthand + ", "
		}
		name += "--" + f.Name
		r := row{plain: name, styled: t.Green(name), usage: inline(t, usage)}
		if varname != "" {
			r.plain += " " + varname
			r.styled += " " + t.Gray(varname)
		}
		if d := f.DefValue; d != "" && d != "false" && d != "[]" && d != "0" && !strings.Contains(usage, "default") {
			r.usage += t.Gray(" (default " + d + ")")
		}
		width = max(width, len(r.plain))
		rows = append(rows, r)
	})
	for _, r := range rows {
		t.Println("  ", r.styled, strings.Repeat(" ", width-len(r.plain)), "   ", r.usage)
	}
}

var (
	backticks = regexp.MustCompile("`([^`]+)`")
	longFlag  = regexp.MustCompile(`(^|[\s(])(--[a-z][a-z-]*)`)
)

// inline styles `code` spans as commands and --flags as flags.
func inline(t *ui.Term, s string) string {
	s = longFlag.ReplaceAllString(s, "$1"+t.Green("$2"))
	return backticks.ReplaceAllStringFunc(s, func(m string) string {
		return t.Bold(t.Lime(strings.Trim(m, "`")))
	})
}

// example syntax-colors one example line: the program and subcommand,
// flags, quoted strings, and trailing # comments.
func example(t *ui.Term, line string) string {
	code, comment := line, ""
	if i := commentStart(line); i >= 0 {
		code, comment = line[:i], line[i:]
	}
	var b strings.Builder
	var quote byte
	word := 0
	for i := 0; i < len(code); {
		c := code[i]
		switch {
		case quote != 0 || c == '"' || c == '\'':
			// Emit the whole quoted span.
			q := c
			if quote != 0 {
				q = quote
			}
			j := i + 1
			for j < len(code) && code[j] != q {
				j++
			}
			end := min(j+1, len(code))
			b.WriteString(t.Amber(code[i:end]))
			i, quote = end, 0
		case c == ' ' || c == '\\':
			b.WriteByte(c)
			i++
		default:
			j := i
			for j < len(code) && code[j] != ' ' {
				j++
			}
			tok := code[i:j]
			switch {
			case strings.HasPrefix(tok, "-"):
				if k := strings.IndexByte(tok, '='); k > 0 {
					tok = t.Green(tok[:k]) + "=" + t.Amber(tok[k+1:])
				} else {
					tok = t.Green(tok)
				}
			case tok == "ackn4th" || word == 1 && !strings.Contains(tok, "/"):
				tok = t.Bold(t.Lime(tok))
			case strings.Contains(tok, "=") && word == 0:
				tok = t.Gray(tok) // VAR=value prefix
				word--
			}
			b.WriteString(tok)
			word++
			i = j
		}
	}
	return b.String() + t.Gray(comment)
}

// commentStart finds a # that begins a shell comment outside quotes.
func commentStart(s string) int {
	var quote byte
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '#' && (i == 0 || s[i-1] == ' '):
			return i
		}
	}
	return -1
}

// printError renders a command error, pointing at help for usage mistakes.
func printError(t *ui.Term, cmd *cobra.Command, err error) {
	fmt.Fprintln(os.Stderr, t.Red("✗ ")+inline(t, err.Error()))
	var ue usageError
	if errors.As(err, &ue) || strings.HasPrefix(err.Error(), "unknown command") || strings.HasPrefix(err.Error(), "unknown flag") {
		path := "ackn4th"
		if cmd != nil {
			path = cmd.CommandPath()
		}
		fmt.Fprintln(os.Stderr, t.Gray("  see ")+t.Bold(t.Lime(path+" --help")))
	}
}

var listItem = regexp.MustCompile(`^(\s+|[-*•] |\d+[.)] )`)

// reflow joins the lines of each paragraph of help text, so wrapping to
// the terminal doesn't leave a stray word on a line of its own. Blank
// lines, list items and indented lines keep their own lines.
func reflow(s string) string {
	lines := strings.Split(s, "\n")
	var out []string
	for i, l := range lines {
		prev := ""
		if i > 0 {
			prev = lines[i-1]
		}
		if len(out) > 0 && strings.TrimSpace(l) != "" && strings.TrimSpace(prev) != "" &&
			!listItem.MatchString(l) && !listItem.MatchString(prev) {
			out[len(out)-1] += " " + l
			continue
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}
