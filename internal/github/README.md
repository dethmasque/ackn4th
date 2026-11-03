# GitHub

[← README](../../README.md)

ackn4th reaches GitHub through the [GitHub CLI](https://cli.github.com)
(`gh`) and your own `gh auth login`. It covers what plain git can't do, or
can't reach without a GitHub sign-in:

- reading issues and pull requests into a task's brief (agents have no
  network);
- cloning a repository;
- fetching and pushing a branch;
- opening a draft pull request with what the agents changed.

ackn4th stores no GitHub credential. Everything here is optional: without
`gh`, links in a prompt stay plain text, and the rest asks you to install
it.

```sh
gh auth login      # once
```

## Issues and pull requests in the prompt

Link an issue or pull request in the prompt and ackn4th fetches it when the
task starts: title, state, author, labels, body and every comment, plus a
pull request's branches and changed files. It goes into the agents' brief
under **From GitHub**, framed as the requirements' source rather than
instructions, and frozen with the brief so a resume sees the same text. Each
is capped at 24 KiB, with a link to the rest.

```sh
ackn4th start-task --roles backend_dev,qa_engineer \
  "Fix https://github.com/acme/web/issues/12 and add a regression test"
```

## A repository and a branch

`--repo` works in a GitHub repository instead of a local directory, and
`--branch` on a branch of it. The dashboard's new-task form has the same, in
its **GITHUB** row.

```sh
ackn4th start-task --repo acme/web --branch fix/login-safari \
  --roles backend_dev,qa_engineer "Fix https://github.com/acme/web/issues/12"
```

- **`--repo owner/name`** (or a `https://github.com/…` or `git@github.com:…`
  URL) clones the repository with `gh repo clone` into
  `default_workspace_root/<name>`. If that directory is already a clone of
  the same repository, it is reused as is; anything else there is an error,
  never overwritten. It replaces `--workspace` and `--scratch`.
- **`--branch name`** puts the workspace on that branch before the agents
  start: the local branch if there is one, else the branch from GitHub
  (fetched with gh's credentials), else a new branch from the current
  commit. Uncommitted changes come along, as with `git switch`. It works
  with `--workspace` too, in any git repository.

## Draft pull requests

`--draft-pr` (the form's **draft PR** box) proposes the agents' work when
the task completes. `ackn4th open-pr <id>` does it for any finished task.

```sh
ackn4th start-task --repo acme/web --branch fix/login-safari --draft-pr \
  --roles backend_dev,qa_engineer,code_reviewer "Fix https://github.com/acme/web/issues/12"
ackn4th open-pr k3f9qz      # later, for a task that ran without --draft-pr
```

1. Only the files the agents changed are committed, never your own
   uncommitted work. The commit message is the task's prompt and outcome.
2. With `--branch`, the commit goes on that branch and the pull request
   targets the repository's default branch. Without it, the commit goes on
   a new branch, `ackn4th/<task-id>`, targeting the branch the workspace was
   on.
3. The branch is pushed to `origin` with gh's credentials. The credential
   helper is set for that one git command, never in your git config.
4. `gh pr create --draft` opens the pull request. Its URL is in the task's
   summary, the COMPLETE box, and `show-task`.

It's always a **draft**: review it, then mark it ready yourself. Before the
task starts, ackn4th checks that `gh` is signed in, that `origin` is on
GitHub and that a branch is checked out, so a task doesn't run for an hour
and then fail to propose its work.

## Code

```
github.go   gh calls, issue and pull request fetching, repository parsing and cloning
pr.go       branches (Checkout) and draft pull requests (CheckPR, DraftPR)
```

The CLI side (`--repo`, `--branch`, `--draft-pr`, `open-pr`) is in
[`internal/cli/github.go`](../cli/github.go). The tests drive a fake `gh`
and a local bare repository standing in for GitHub, so they publish
nothing.
