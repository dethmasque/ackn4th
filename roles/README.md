# Roles

[← README](../README.md)

Each agent plays a role: instructions (its system prompt), skills, and the
paths it may change. 18 built-in roles live here as Markdown files with
YAML front matter, embedded in the binary:

`product_manager` · `tech_lead` · `software_architect` · `backend_dev` ·
`frontend_dev` · `fullstack_dev` · `mobile_dev` · `devops_engineer` · `sre` ·
`qa_engineer` · `security_engineer` · `data_engineer` · `ml_engineer` ·
`database_admin` · `ux_designer` · `tech_writer` · `code_reviewer` ·
`release_manager`

`ackn4th roles` lists every role, built-in and yours; `ackn4th roles
backend_dev` shows one.

**One agent, several roles.** Join roles with `+` (or list several for
`run-agent --role`) and one agent plays them all: it gets every role's
guidance and may write wherever any of them may. That works in teams too:

```sh
ackn4th run-agent --role backend_dev,qa_engineer "Add pagination, with tests"
ackn4th start-task --roles backend_dev+qa_engineer,code_reviewer "Add pagination"
```

## Your own roles

Add, edit and import roles from the command line or the dashboard. Yours
live in `<config>/roles/`, one file each (see
[where files live](../internal/config/README.md#where-files-live)). A role
of yours with a built-in role's name **overrides** it; removing yours brings
the built-in back.

### From the command line

```sh
# add: give it as flags…
ackn4th roles add game_designer --title "Game Designer" \
  --skills "level design,balancing" --writable 'design/**,*.md' \
  --prompt "You design the game's mechanics and levels. …"
# …from another role…
ackn4th roles add strict_reviewer --from code_reviewer --edit
# …or write it in your editor, from a template
ackn4th roles add data_scientist

# edit: in your editor ($VISUAL or $EDITOR; else nano or vi, Notepad on Windows)
ackn4th roles edit game_designer
ackn4th roles edit code_reviewer         # saves your copy, which overrides the built-in

# import: from files or https URLs (GitHub file pages work as they are)
ackn4th roles import ./game_designer.md
ackn4th roles import https://github.com/acme/agent-roles/blob/main/game_designer.md
ackn4th roles import team-roles/*.md --force    # replace yours with the same names

# remove one of yours (an edited built-in goes back to the original)
ackn4th roles remove strict_reviewer
```

| Command | Flags |
| --- | --- |
| `roles add <name>` | `--title`, `--summary`, `--skills a,b`, `--writable 'glob,glob'` (default: everything), `--read-only`, `-p/--prompt` or `--prompt-file f` (`-` for stdin), `--from <role>`, `--edit`, `--force` |
| `roles edit <name>` | needs a terminal; a role that isn't valid when you close the editor can be edited again, or nothing changes |
| `roles import <file\|url>…` | `--force` |
| `roles remove <name>` | `--yes` (needed without a terminal) |

In scripts, `roles add` with `--prompt` or `--prompt-file`, and `roles
import`, never open an editor or ask anything.

### In the dashboard

**`R`** opens the roles screen: every role, marked built-in, yours, or
yours overriding a built-in.

- **`a`** adds a role and **`e`** (or `⏎`) edits one, in an editor inside
  the dashboard: name, title, summary, skills, writable paths and the
  instructions. `tab` moves between fields, `ctrl+s` saves, `esc` cancels.
  Editing a built-in saves your copy.
- **`i`** imports a role from a path or https URL.
- **`d`** removes one of yours (press it twice).

Saved roles appear in the new-task form's role lists at once. With the
[voice](../internal/voice/README.md) on, each field is read as you reach
it.

### Before you import

An imported role is instructions your agents will follow, so read it
first, as you would a script. What it can't do is widen what agents may
reach: `writable` only narrows writes within the workspace, and the
[workspace jail and OS sandbox](../internal/sandbox/README.md) apply to
every role the same way.

## The file format

```markdown
---
name: game_designer           # lower_snake_case; the file is <name>.md
title: Game Designer
summary: "One line for `ackn4th roles`."
skills: [level design, balancing]
writable: ["design/**", "*.md"]   # gitignore-style globs; omit for read-only
---
The instructions: what the agent should do, what it cares about, how it
works with the rest of the team, and what "done" means for it.
```

Every role is checked before it is saved, imported or edited:

- the name is lower_snake_case and isn't a command word (`add`, `edit`,
  `import`, `remove`, `show`, `help`);
- the instructions aren't empty;
- writable paths are relative to the workspace, without `..`;
- the file is under 256 KiB.

A role file you edit by hand that doesn't pass is skipped, with a warning
in `ackn4th roles` and on the roles screen, rather than stopping every
command.

## Code

```
roles/*.md                  the built-in roles, embedded by roles/embed.go
internal/role/role.go       parsing, loading (built-ins, then yours), combining roles
internal/role/store.go      your roles: save, import (Fetch), remove, validate
internal/cli/roles.go       the roles commands
internal/dash/roles.go      the dashboard's roles screen
```
