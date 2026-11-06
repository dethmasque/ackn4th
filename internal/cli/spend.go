package cli

import (
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/spf13/cobra"

	"github.com/dethmasque/ackn4th/internal/spend"
)

func spendCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "spend",
		GroupID: "info",
		Short:   "Show what agents spent today and this month, and the caps",
		Long: `Show the estimated spending on billed providers (claude, gemini, openai, deepseek)
today and this month, in all and by provider, beside the caps set under
spend: in the config. Estimates come from the token counts each provider
reports and its prices per million tokens; providers' own bills are the
final word. Agent CLIs on your own plan (claude-code, codex, cursor,
copilot) cost nothing here.`,
		Example: `  ackn4th spend`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := a.ledger.Totals(time.Now())
			if err != nil {
				return err
			}
			l := a.cfg.Spend
			line := func(who string, day, month float64, c spend.Cap) {
				a.term.Println(fmt.Sprintf("%-14s", who), "today ", a.term.Bold(fmt.Sprintf("%-8s", spend.USD(day))), a.term.Gray(fmt.Sprintf("%-14s", capOf(c.Daily))),
					"this month ", a.term.Bold(fmt.Sprintf("%-8s", spend.USD(month))), a.term.Gray(capOf(c.Monthly)))
			}
			line("ackn4th", t.Day, t.Month, l.Cap)
			provs := slices.Sorted(maps.Keys(t.ProviderMonth))
			for p := range l.Providers {
				if !slices.Contains(provs, p) {
					provs = append(provs, p)
				}
			}
			for _, p := range provs {
				line("  "+p, t.ProviderDay[p], t.ProviderMonth[p], l.Providers[p])
			}
			if l.PerTask > 0 {
				a.term.Println(a.term.Gray("each task is capped at " + spend.USD(l.PerTask) + " unless started with --max-cost"))
			}
			return nil
		},
	}
}

func capOf(v float64) string {
	if v <= 0 {
		return "(no cap)"
	}
	return "of " + spend.USD(v)
}

// costCell is a task's estimated spending for a table: "-" when none.
func costCell(usd float64) string {
	if usd <= 0 {
		return "-"
	}
	return "≈" + spend.USD(usd)
}
