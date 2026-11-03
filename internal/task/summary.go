package task

import (
	"fmt"
	"sort"
	"strings"
)

// Summary renders the task's end-of-run report as Markdown: the outcome,
// the action items for the operator, what each agent last reported, and
// the files agents changed.
func (r *Runner) Summary(t *Task) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s · %s\n\nTask `%s`\n\n", t.Title(), t.Status, t.ID)
	fmt.Fprintf(&b, "> %s\n\n", strings.ReplaceAll(strings.TrimSpace(t.Prompt), "\n", "\n> "))
	// Round is the next round to run once one has finished.
	used := t.Round
	if t.Status == Completed || (t.MaxRounds > 0 && t.Round > t.MaxRounds) {
		used = t.Round - 1
	}
	fmt.Fprintf(&b, "Workspace: `%s` · %d round(s)", t.Workspace, used)
	if t.MaxRounds > 0 {
		fmt.Fprintf(&b, " of %d", t.MaxRounds)
	}
	b.WriteString("\n")
	if t.PR != "" {
		fmt.Fprintf(&b, "\nDraft pull request: %s\n", t.PR)
	}
	switch t.Status {
	case Completed:
		b.WriteString("\nEvery agent declared the task done.\n")
	case Stopped, Failed:
		fmt.Fprintf(&b, "\n**%s:** %s\n\nResume with `ackn4th resume-task %s`.\n", strings.ToUpper(string(t.Status[:1]))+string(t.Status[1:]), t.Reason, t.ID)
	}

	open := t.OpenTodos()
	fmt.Fprintf(&b, "\n## For you (%d open)\n\n", len(open))
	if len(open) == 0 {
		b.WriteString("Nothing: the agents didn't leave any action items.\n")
	}
	for _, td := range open {
		fmt.Fprintf(&b, "- [ ] **#%d** %s _(%s, round %d)_\n", td.ID, td.Text, td.By, td.Round)
	}
	for _, td := range t.Todos {
		if td.Done {
			fmt.Fprintf(&b, "- [x] ~~#%d %s~~ %s\n", td.ID, td.Text, td.Note)
		}
	}

	b.WriteString("\n## What each agent reported\n")
	for i, a := range t.Agents {
		fmt.Fprintf(&b, "\n### %d. %s (%s, %s)\n\n", i+1, a.Role, a.Provider, a.Model)
		switch {
		case a.Done && a.DoneSummary != "":
			b.WriteString("Declared done: " + a.DoneSummary + "\n")
		case a.Message != "":
			b.WriteString(a.Message + "\n")
		default:
			b.WriteString("_No turns yet._\n")
		}
	}

	var changed []string
	sh := r.loadShared(t.ID)
	for _, p := range r.ChangedFiles(t.ID) {
		who := sh.Authors[p]
		changed = append(changed, fmt.Sprintf("- `%s` (%s)", p, who))
	}
	fmt.Fprintf(&b, "\n## Files the agents changed (%d)\n\n", len(changed))
	if len(changed) > 100 {
		changed = append(changed[:100], fmt.Sprintf("- … and %d more", len(changed)-100))
	}
	if len(changed) == 0 {
		b.WriteString("None.\n")
	}
	b.WriteString(strings.Join(changed, "\n") + "\n")
	return b.String()
}

// ChangedFiles lists, sorted, the workspace paths the agents created,
// changed or deleted: not the operator's own edits.
func (r *Runner) ChangedFiles(id string) []string {
	var out []string
	for p, who := range r.loadShared(id).Authors {
		if who != operator {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}
