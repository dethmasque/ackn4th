package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dethmasque/ackn4th/internal/github"
	"github.com/dethmasque/ackn4th/internal/task"
	"github.com/dethmasque/ackn4th/internal/workspace"
)

// GitHub, through the GitHub CLI's own login: issues and pull requests the
// prompt links to go into the brief, --repo clones with gh's credentials,
// and --draft-pr (or open-pr) proposes the agents' changes.

// repoWorkspace clones owner/name into the default workspace root, or
// reuses an existing clone there, and returns it.
func (a *app) repoWorkspace(ctx context.Context, repo string, allowed []string, quiet bool) (string, error) {
	owner, name, err := github.ParseRepo(repo)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(allowed[0], name)
	say := func(s string) {
		if !quiet {
			a.term.Println(a.term.Gray("  " + s))
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		if !github.IsClone(ctx, dir, owner, name) {
			return "", fmt.Errorf("%s is a clone of another repository, not %s/%s: move it, or pass --workspace", dir, owner, name)
		}
		say("using the clone of " + owner + "/" + name + " at " + tildeHome(dir))
	} else {
		if _, err := os.Stat(dir); err == nil {
			return "", fmt.Errorf("%s exists and isn't a git clone: move it, or pass --workspace", dir)
		}
		say("cloning " + owner + "/" + name + " with gh into " + tildeHome(dir) + " …")
		cctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
		if err := github.Clone(cctx, owner, name, dir); err != nil {
			return "", err
		}
	}
	return workspace.Resolve(dir, allowed, true)
}

// useBranch puts the workspace on branch, when one is given, and says
// what it did.
func (a *app) useBranch(ctx context.Context, ws, branch string, quiet bool) error {
	if branch == "" {
		return nil
	}
	if _, err := os.Stat(filepath.Join(ws, ".git")); err != nil {
		return fmt.Errorf("--branch: %s isn't a git repository", ws)
	}
	bctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	created, err := github.Checkout(bctx, ws, branch)
	if err != nil {
		return err
	}
	if !quiet {
		what := "on branch "
		if created {
			what = "created branch "
		}
		a.term.Println(a.term.Gray("  " + what + branch))
	}
	return nil
}

// fetchGitHub saves the issues and pull requests the prompt links to, for
// the brief. Without gh it says so and goes on: the agents then see only
// the links. A link gh can't read stops the task from starting.
func (a *app) fetchGitHub(ctx context.Context, tk *task.Task, quiet bool) error {
	refs := github.Refs(tk.Prompt)
	if len(refs) == 0 {
		return nil
	}
	t := a.term
	if _, err := github.Status(ctx); err != nil {
		if !quiet {
			t.Println(t.Amber("  ! "), inline(t, "the prompt links to GitHub, but "+strings.TrimPrefix(err.Error(), "github: ")+"; agents will see only the links, as they have no network"))
		}
		return nil
	}
	fctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	md, err := github.Context(fctx, refs)
	if err != nil {
		return err
	}
	if err := os.WriteFile(a.store.GitHubPath(tk.ID), []byte(md), 0o600); err != nil {
		return err
	}
	if !quiet {
		var names []string
		for _, r := range refs {
			names = append(names, fmt.Sprintf("%s/%s#%d", r.Owner, r.Repo, r.Number))
		}
		t.Println(t.Green("  ✓ "), t.Gray("read from GitHub into the brief: "+strings.Join(names, ", ")))
	}
	return nil
}

// openPR proposes the agents' changes as a draft pull request and records
// its URL on the task.
func (a *app) openPR(ctx context.Context, tk *task.Task, files []string) error {
	// The task's name, when it has one, is the title; else the prompt's
	// first line.
	title := tk.Name
	if title == "" {
		title = strings.TrimSpace(strings.SplitN(tk.Prompt, "\n", 2)[0])
	}
	if r := []rune(title); len(r) > 72 {
		title = string(r[:71]) + "…"
	}
	var body strings.Builder
	fmt.Fprintf(&body, "Drafted by [ackn4th](https://github.com/dethmasque/ackn4th) task %s (`%s`): AI agents wrote these changes. Review them before merging.\n\n", tk.Title(), tk.ID)
	fmt.Fprintf(&body, "## Prompt\n\n> %s\n", strings.ReplaceAll(strings.TrimSpace(tk.Prompt), "\n", "\n> "))
	if tk.Reason != "" {
		fmt.Fprintf(&body, "\n## Outcome\n\n%s\n", tk.Reason)
	}
	if open := tk.OpenTodos(); len(open) > 0 {
		body.WriteString("\n## Left for a person\n\n")
		for _, td := range open {
			fmt.Fprintf(&body, "- [ ] %s\n", td.Text)
		}
	}
	pctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	pr := github.PR{Branch: "ackn4th/" + tk.ID, Title: title, Body: body.String(), Files: files}
	if tk.Branch != "" {
		// Commit on the task's own branch and propose it for the default
		// one, unless that is the default branch itself.
		def, err := github.DefaultBranch(pctx, tk.Workspace)
		if err != nil {
			return err
		}
		if tk.Branch != def {
			pr.Branch, pr.Existing, pr.Base = tk.Branch, true, def
		}
	}
	url, err := github.DraftPR(pctx, tk.Workspace, pr)
	if err != nil {
		return err
	}
	tk.PR = url
	return nil
}

// completed is the runner's hook: a task started with --draft-pr opens its
// pull request when every agent is done.
func (a *app) completed(r *task.Runner) func(*task.Task) {
	return func(tk *task.Task) {
		if !tk.DraftPR || tk.PR != "" {
			return
		}
		if err := a.openPR(context.Background(), tk, r.ChangedFiles(tk.ID)); err != nil {
			tk.Reason = strings.TrimSpace(tk.Reason + " · the draft pull request wasn't opened: " + err.Error() + " (fix that, then `ackn4th open-pr " + tk.ID + "`)")
		}
	}
}

func openPRCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "open-pr <task>",
		GroupID: "tasks",
		Short:   "Propose a task's changes as a draft GitHub pull request",
		Long: `Commit the files the task's agents changed (only those), push them to
origin and open a draft pull request, with the prompt and outcome as its
description:

  - a task started with --branch: the commit goes on that branch, proposed
    for the repository's default branch;
  - otherwise: on a new branch, ackn4th/<task-id>, proposed for the branch
    the workspace is on.

It uses the GitHub CLI's login (` + "`gh auth login`" + `), so it works even
where plain git isn't signed in to GitHub. start-task --draft-pr does this
when the task completes.`,
		Example: `  ackn4th open-pr k3f9qz`,
		Args:    upTo(1, "<task>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			tk, err := a.taskArg(args, func(tk *task.Task) bool { return tk.PR == "" && tk.Status != task.Running })
			if err != nil {
				return err
			}
			a.store.Reconcile(tk)
			if tk.PR != "" {
				return fmt.Errorf("task %s already has a draft pull request: %s", tk.Label(), tk.PR)
			}
			if tk.Status == task.Running {
				return errors.New("the task is still running: stop it, or let it finish, first")
			}
			r, err := a.runner()
			if err != nil {
				return err
			}
			if err := a.openPR(cmd.Context(), tk, r.ChangedFiles(tk.ID)); err != nil {
				return err
			}
			if err := a.store.Save(tk); err != nil {
				return err
			}
			a.term.Println(a.term.Green("✓ "), "draft pull request: ", a.term.Lime(tk.PR))
			return nil
		},
	}
}
