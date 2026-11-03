package cli

import (
	"os"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/dethmasque/ackn4th/internal/config"
	"github.com/dethmasque/ackn4th/internal/tips"
	"github.com/dethmasque/ackn4th/internal/ui"
)

// tip prints a one-time hint for new users on stderr, so scripted output
// stays clean. It shows only on a terminal, once, while tips are on.
func (a *app) tip(id, text string) {
	if a.tips == nil || !a.tips.Pending(id) || !term.IsTerminal(int(os.Stderr.Fd())) {
		return
	}
	a.tips.Done(id)
	t := ui.NewTerm(os.Stderr, false)
	t.Println("")
	t.Println(t.Amber("  ✦ tip  "), inline(t, text))
	t.Println(t.Gray("          " + tips.Hint + "  (or " + tips.Env + "=1, or tips: false in config.yaml)"))
}

func disableTipsCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "disable-tips",
		GroupID: "info",
		Short:   "Stop showing first-run tips in output and the dashboard",
		Long: `Turn off the hints ackn4th shows new users: the ✦ tip lines after
commands and the tooltips in the dashboard. It sets tips: false in
config.yaml. For one run, set ` + tips.Env + `=1 instead.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := config.SetKey("tips", false)
			if err != nil {
				return err
			}
			a.term.Println(a.term.Green("✓ "), "tips are off ", a.term.Gray("("+path+"; turn them back on with `ackn4th enable-tips`)"))
			return nil
		},
	}
}

func enableTipsCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "enable-tips",
		GroupID: "info",
		Short:   "Show first-run tips again, from the start",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := config.SetKey("tips", true)
			if err != nil {
				return err
			}
			data, err := config.DataDir()
			if err != nil {
				return err
			}
			if err := tips.Reset(data); err != nil {
				return err
			}
			a.term.Println(a.term.Green("✓ "), "tips are on and start over ", a.term.Gray("("+path+")"))
			if os.Getenv(tips.Env) == "1" {
				a.term.Println(a.term.Amber("! "), tips.Env+"=1 is set in this shell, which still hides them")
			}
			return nil
		},
	}
}
