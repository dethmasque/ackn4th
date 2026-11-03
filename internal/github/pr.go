package github

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// PR is a draft pull request to open from a workspace.
type PR struct {
	// Branch holds the commit. With Existing it is the branch the
	// workspace is on (the task's --branch); otherwise it is created from
	// the current one, e.g. ackn4th/k3f9qz.
	Branch   string
	Existing bool
	// Base is the branch to merge into; empty means the branch the
	// workspace is on (for a new Branch) or the repository's default.
	Base  string
	Title string   // also the commit message's subject
	Body  string   // Markdown; also the commit message's body
	Files []string // workspace-relative paths to commit (changed or deleted)
}

// git runs git in dir.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", args[0], msg)
	}
	return strings.TrimSpace(string(out)), nil
}

// withGH runs a git command that talks to the remote with gh supplying
// the credentials, for that command only: it works where plain git isn't
// signed in to GitHub.
func withGH(ctx context.Context, dir string, args ...string) (string, error) {
	ghBin, err := bin()
	if err != nil {
		return "", errors.New("github: the GitHub CLI isn't installed: " + Install)
	}
	helper := "!" + shellQuote(ghBin) + " auth git-credential"
	return git(ctx, dir, append([]string{"-c", "credential.helper=", "-c", "credential.helper=" + helper}, args...)...)
}

// githubOrigin checks that ws's origin remote is on GitHub.
func githubOrigin(ctx context.Context, ws string) error {
	origin, err := git(ctx, ws, "config", "--get", "remote.origin.url")
	if err != nil || !strings.Contains(origin, "github.com") {
		return fmt.Errorf("github: %s has no GitHub remote named origin", ws)
	}
	return nil
}

// CurrentBranch is the branch ws has checked out.
func CurrentBranch(ctx context.Context, ws string) (string, error) {
	b, err := git(ctx, ws, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return "", errors.New("github: the workspace isn't on a branch (detached HEAD): check out a branch first")
	}
	return b, nil
}

// DefaultBranch is the repository's default branch on GitHub.
func DefaultBranch(ctx context.Context, ws string) (string, error) {
	out, err := gh(ctx, ws, nil, "repo", "view", "--json", "defaultBranchRef", "--jq", ".defaultBranchRef.name")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Checkout puts the workspace on branch: the local one if it exists,
// else the one on origin (fetched with gh's credentials, when origin is on
// GitHub), else a new branch from the current commit. It reports whether
// it created the branch. Uncommitted changes come along, as with git
// switch; one that would be overwritten stops it.
func Checkout(ctx context.Context, ws, branch string) (created bool, err error) {
	if _, err := git(ctx, ws, "check-ref-format", "--branch", branch); err != nil {
		return false, fmt.Errorf("github: %q isn't a valid branch name", branch)
	}
	if cur, err := git(ctx, ws, "symbolic-ref", "--short", "HEAD"); err == nil && cur == branch {
		return false, nil
	}
	if _, err := git(ctx, ws, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
		_, err := git(ctx, ws, "switch", branch)
		return false, err
	}
	if githubOrigin(ctx, ws) == nil {
		ref := "+refs/heads/" + branch + ":refs/remotes/origin/" + branch
		if _, err := withGH(ctx, ws, "fetch", "--quiet", "origin", ref); err == nil {
			_, err := git(ctx, ws, "switch", "--track", "origin/"+branch)
			return false, err
		}
	}
	_, err = git(ctx, ws, "switch", "-c", branch)
	return err == nil, err
}

// CheckPR reports whether a draft pull request could be opened from ws:
// gh is signed in, origin is on GitHub and a branch is checked out.
func CheckPR(ctx context.Context, ws string) error {
	if _, err := Status(ctx); err != nil {
		return err
	}
	if err := githubOrigin(ctx, ws); err != nil {
		return err
	}
	_, err := CurrentBranch(ctx, ws)
	return err
}

// DraftPR commits pr.Files, pushes the branch to origin with gh's
// credentials and opens a draft pull request. A new pr.Branch is left
// checked out. It returns the pull request's URL. Nothing outside
// pr.Files is committed.
func DraftPR(ctx context.Context, ws string, pr PR) (string, error) {
	if len(pr.Files) == 0 {
		return "", errors.New("github: the agents changed no files, so there is nothing to propose")
	}
	if err := githubOrigin(ctx, ws); err != nil {
		return "", err
	}
	cur, err := CurrentBranch(ctx, ws)
	if err != nil {
		return "", err
	}
	base := pr.Base
	if pr.Existing {
		if cur != pr.Branch {
			return "", fmt.Errorf("github: the workspace is on %s, not %s", cur, pr.Branch)
		}
		if base == "" {
			if base, err = DefaultBranch(ctx, ws); err != nil {
				return "", err
			}
		}
		if base == pr.Branch {
			return "", fmt.Errorf("github: %s is the branch the pull request would merge into", base)
		}
	} else {
		if base == "" {
			base = cur
		}
		if _, err := git(ctx, ws, "rev-parse", "--verify", "--quiet", "refs/heads/"+pr.Branch); err == nil {
			return "", fmt.Errorf("github: branch %s already exists", pr.Branch)
		}
		if _, err := git(ctx, ws, "switch", "-c", pr.Branch); err != nil {
			return "", err
		}
	}
	if _, err := git(ctx, ws, append([]string{"add", "-A", "--"}, pr.Files...)...); err != nil {
		return "", err
	}
	if _, err := git(ctx, ws, "commit", "-m", pr.Title, "-m", pr.Body); err != nil {
		return "", err
	}
	if _, err := withGH(ctx, ws, "push", "--set-upstream", "origin", pr.Branch); err != nil {
		return "", err
	}
	out, err := gh(ctx, ws, []byte(pr.Body), "pr", "create", "--draft",
		"--base", base, "--head", pr.Branch, "--title", pr.Title, "--body-file", "-")
	if err != nil {
		return "", err
	}
	lines := strings.Fields(string(out))
	if len(lines) == 0 {
		return "", errors.New("github: gh pr create printed no URL")
	}
	return lines[len(lines)-1], nil
}

// shellQuote quotes s for the shell git runs credential helpers with.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
