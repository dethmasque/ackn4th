# The dashboard

[← README](../../README.md)

Run `ackn4th` on its own (or `ackn4th dash`, also `top`) for a full-screen,
keyboard-driven view of every task and where each agent is in its
workflow. Off a terminal, or with `--no-input`, bare `ackn4th` prints the
help instead. The header is drawn in actual pixels (two per character cell,
in truecolor): the wordmark over digital rain, and two agents volleying in
the provider colors of the task's first and last agents. Roughly:

```
▓▒░ ACKN4TH ░▒▓ // DASHBOARD   ·  ｱ   ·  0   ·   ｸ  ·      ◐ 1 running  ✓ 3 completed   15:18:01
╔═[ TASKS ]═══════════════╗┌─[ TASK K3F9QZ ]──────────────────────────────────── ◐ running ─┐
║▶ ◐ k3f9qz r2/10 ⠋1 ◆1/3 ║│ 1 OVERVIEW   2 ACTIVITY   3 FILES   4 MESSAGES                  │
║    Add DELETE /todos/…  ║│ round 2/10 · parallel · 60.0k in / 4.0k out · 75% cached        │
║  ✓ a81bcd r3/10         ║│ LANES ⇉ ⟦⠋ 1 backend_dev⟧ ║ [· 2 qa_engineer] ║ [◆ 3 tech_writer] │
║    Write the README     ║│┌─[ 1 backend_dev ]──────────────┐┌─[ 2 qa_engineer ]────────────┐│
║                         ║││ claude claude-opus-5-5 · high  ││ claude claude-opus-5-5 · high ││
║                         ║││ ⠋ edit_file main.go            ││ ✓ round done, waiting         ││
║                         ║││ using tools for 53s · step 4   ││                               ││
║                         ║││ ctx ██░░░░░░░░ 43.0k/200.0k    ││ ctx █░░░░░░░░░ 21.5k/200.0k   ││
```

Tasks are listed by name, with their ID beside the prompt. It reads the
records runners write, so it shows foreground and background tasks alike, and you can keep it open in a second terminal (or a tmux pane)
while a task runs. Quitting it never stops a task.

## First run

1. **Configuration.** With no tasks yet, the dashboard opens on its
   [configuration](#configuration), so you can see how ackn4th is set up
   and change anything first. `esc` continues; `C` brings it back.
2. **The walkthrough** shows a tooltip at a time, pointing at what it
   explains. `⏎` (or space, or esc) shows the next one; any other key
   dismisses it and does its usual job, so `n` still opens the form.
   `ctrl+t` turns tips off. **`t`** (or `ackn4th walkthrough`, alias
   `tour`) replays it from the start whenever you like, even with tips off.
3. **Set up providers.** If no provider is signed in, it asks to set them
   up. **LATER** skips this; `p` brings it back.
4. **Accessibility.** The same welcome dialog offers the voice: press
   **`v`** to have the dashboard read aloud. To hear it from the first
   screen, open it with `ackn4th --voice` (or `dash --voice`, `tour --voice`).

## Tabs and panels

- **1 Overview:** the workflow (turn order with who's up next, or parallel
  lanes), consensus so far, and a live card per agent: what it's doing
  right now and for how long, its model, context use and cache hit rate.
  The prompt and how the task ended are cut to fit. Like an agent card,
  that summary can be selected (`↑` from the first agent) and **enter**
  opens the task in full, scrollable; `e` does the same from anywhere.
- **Enter** zooms into an agent: settings, usage, its latest handoff, recent
  tool calls and the files it changed. While it works, **LIVE** shows what
  its model is writing right now: the end of a file as it streams into a
  `write_file`, its reply, or its reasoning. Background runners share this
  through `agents/N.live.json`, which is removed once each reply is done.
  The agent's card says the same in one line (`✎ write_file src/trace.js ·
  8.2 KB so far`). Gemini replies all at once, so it has nothing live to
  show.
- **2 Activity** is the full log, following new events (`f`). `←→` filter
  it: everything, **you** (your follow-ups and messages to the team),
  **ackn4th** (the task's own events: starts, rounds, notices), or one
  agent. Your lines are labelled `you`, ackn4th's `ackn4th`. A long line wraps onto up to three. `↑↓` select a line and **enter** opens it with everything
  the log has on it: for a tool call or a failure, the agent's reasoning
  just before, the call's arguments in full (a whole command, say) and
  what came back to the model. `←→` step to the previous or next event,
  esc closes. **3 Files** lists every file agents changed, by whom and
  how. **4 Messages** is every handoff, by round, in full.

**Copying.** The dashboard uses the mouse, which turns off the terminal's
own text selection, so it has its own: drag over text to select it, and
it is copied when you let go (ctrl+c or a right-click copy it again). A
right-click with nothing selected copies the line under the pointer.
**`c`** copies whatever is selected in full rather than what fits on
screen: an activity line with all its details, the open panel (which also
has a **⧉ copy** button), the task's summary, an agent's latest handoff,
or, from the task list, the task's ID. Text goes to the system clipboard
(`pbcopy`, `wl-copy`, `xclip`, `xsel` or `clip.exe`) and to the
terminal's own (OSC 52), which is what works over SSH. Holding Option
(macOS) or Shift usually brings back the terminal's own selection.

Text wraps between words and keeps the shape agents and people give it:
list items wrap under their own text, indentation and blank lines stay,
and fenced code keeps its spacing. Agents' markdown is drawn, not shown
as markup: **bold**, *italic*, `code`, headings and links (with their
address after them), in handoffs, details and the activity log.
- **FOR YOU** lists the action items agents left for you as they worked:
  decisions only you can make, credentials, manual steps, things they
  couldn't verify.
- When a task isn't running and its workspace has uncommitted changes, the
  overview says how many files and where (pointing out a clone, or a
  directory other than the current one), and how to get them out: commit
  there, or `ackn4th open-pr <task>`. ackn4th asks git, with the
  workspace's hooks and fsmonitor off so nothing in it runs.

## Keys

| Key | Does |
| --- | --- |
| `↑ ↓` `j k` | select a task · select an agent (overview) · scroll |
| `tab` | switch between the task list and the task panel |
| `← →` `h l` | in a task: select an agent; in activity and messages, filter (the task list ignores them) |
| `⏎` | open the task · zoom into an agent · an activity line's details |
| `e` | the task in full: its whole prompt, why it stopped, every to-do |
| `c` | copy what's selected, in full (drag to select text; right-click copies a line) |
| `esc` | back |
| `1` – `4` | overview · activity · files · messages |
| `[` `]` | previous / next task from anywhere |
| `pgup` `pgdn` `g` `G` | page · top · bottom (and follow) |
| `f` | follow new activity on / off |
| `s` | stop the task (it saves state; resume any time) |
| `r` | resume a stopped task in the background |
| `a` | on a running task: message its team without stopping it (start with `@role` for one agent). Otherwise a follow-up: give a task that isn't running more work, and optionally other roles, models or efforts for its agents (the same agents continue; all must declare done again) |
| `x` | delete a task's records (its workspace files stay) |
| `n` | new task |
| `p` | providers: see which are ready, sign in or out |
| `R` | roles: add, edit, import or remove agent roles |
| `C` | [configuration](#configuration): spending caps, limits, models, commands, the voice |
| `t` | the walkthrough again, from the start |
| `v` `V` | the voice on / off · repeat the last phrase |
| `w` `i` `o` | with the voice on: where you are · more about what's selected · what you can do (`F2` `F3` `F4`, or `alt+` them, in forms and dialogs) |
| `?` | every key |
| `q` | quit; tasks keep running |

`s`, `r` and `x` ask first, in a dialog. The resume dialog can also move the
agents to another provider (**Run on**, `↑↓`); it suggests claude-code when
a task ran out of API credits.

## New task

`n`, or the **+ NEW TASK** button at the top of the task list (click it),
opens the new-task form, no flags needed:

- **Prompt.** Link GitHub issues or pull requests and they are fetched into
  the agents' brief.
- **Name**, what the task is called everywhere (and what commands accept
  instead of its ID). Left empty, it is named after the prompt's first line.
- **Workspace**, ticking “trust it” if it is new.
- **GitHub** repository and branch, both optional. A repository is
  `owner/name`, `https://github.com/owner/name` or
  `git@github.com:owner/name.git`; it is cloned with `gh` into
  `default_workspace_root` (or reused) and takes the workspace's place:
  the form shows the path it will clone to, or the clone it will reuse, as
  you type. The agents' changes are made there, not in the directory
  ackn4th was started in. A branch is checked out, fetched, or created. See
  [GitHub](../github/README.md).
- **Mode** (turns or parallel), **rounds**, **CAP $** (the most the task
  may spend, estimated; it starts at `spend.per_task`, and empty uses
  that; see [spending caps](../task/README.md#spending-caps)), and **draft
  PR**: open a draft pull request when the task completes.
- **Agents**, as many as you like: each row has dropdowns for the role,
  provider, model and effort. Pick several roles for one agent with space,
  or choose “type a model id…” for any model. `ctrl+n` adds a row, and `x` or
  `del` on any of a row's fields (or the row's ✕) removes it. The form shows whether the team shares one
  cached context.

A line under the agents explains whichever field is focused, and `f1` (or
`?` when the focus isn't in a text field) opens help for the whole form.

**LAUNCH** (`ctrl+s`) checks every model, starts the task in the background
and selects it. If a provider needs signing in, `ctrl+l` runs
`ackn4th login` and brings you back.

## Providers

`p` lists every provider as ✓ ready, ○ needing sign-in or a key, or ✗ not
installed (with the install command). `space` picks providers, `⏎` signs in
to the picked ones (or the highlighted one), `x` signs out of them (press it
twice: API keys are deleted), `r` rechecks.

## Roles

`R` lists every role, marked built-in, yours, or yours overriding a
built-in. `a` adds one and `e` (or `⏎`) edits one in an editor inside the
dashboard: name, title, summary, skills, writable paths and instructions
(`tab` moves, `ctrl+s` saves, `esc` cancels). `i` imports a role file from
a path or https URL, and `d` (twice) removes one of yours. Saved roles show
up in the new-task form at once. See [roles](../../roles/README.md).

## Configuration

`C` opens the everyday settings of `config.yaml`, by section:

| Section | Settings |
| --- | --- |
| General | default provider, prefer signed-in CLIs, where new workspaces go |
| Limits | rounds per task, tool calls per turn |
| Spending caps | each task; all of ackn4th a day and a month; claude, gemini and openai a day and a month |
| Models | each provider's model, and effort where it has one |
| Commands | agents run commands, commands reach the internet, longest command |
| Dashboard | animations, tips, read aloud, the voice and its speaking rate |

`↑↓` choose a setting and a line under the list says what it does. `⏎`
or `space` turns a switch on or off, `←→` (or `⏎`) step through choices,
and `⏎` edits a typed value (`⏎` saves it, `esc` cancels; empty means
what the field says, such as “no cap”). Each change is checked, then
saved on its own, keeping the file's comments and every other key
([config](../config/README.md)). It applies at once: tasks started from
then on use it; animations, tips and the voice change on the spot, and a
new voice or rate is heard straight away. A value that can't be used
(a negative cap, an effort a model doesn't have) is refused, with the
reason. Everything else in `config.yaml`, like profiles and prices, is
edited in the file.

## The voice

With the voice on (`v`, or `ackn4th voice on`), the dashboard says little
on its own. After each key it names what you landed on, briefly: a task
and its status, a tab, an agent and what it is doing, an event, a form
field and its value, a dialog's question and selected button. Between
keys it says what changed: a task finished or failed, a message flashed.
It doesn't echo typing: a text field is read when you reach it. In the
configuration it reads each setting with its value and unit (“All of
ackn4th, a day: 20 dollars”), and after a change, the new value and
“Saved.”, or why it wasn't.

It says things in words rather than reading the screen: an activity line
like `12:07:05 r4 software_arch… ╰─ turn ended · 2.1k in / 812 out`
becomes "at twelve oh seven PM, in round 4, software architect's turn
ended. It read about 2100 tokens of input… and wrote 812 tokens": the
time as people say it, the whole role, numbers with their units.

The rest is on request:

| Key | Says |
| --- | --- |
| `w` | where you are: the screen, the task and tab, what is selected; in the configuration, the section and setting |
| `i` | everything about what's selected, more than the screen shows: a task's round, agents, to-dos open for you, why it stopped, uncommitted changes and prompt; an agent's work and latest handoff; an event in full, how long ago, and the agent's provider and model; the handoffs; the files changed; a tip on screen; a setting, what it does, its choices and its key in `config.yaml` |
| `o` | what you can do there, for that task's status (`s` only while running, `r` only when it can resume), or for the setting selected |
| `?` | every key (the help panel, read aloud) |
| `V` | the last phrase again |

Plain `w`, `i` and `o` work on the dashboard and in the panels over it.
In the new-task and follow-up forms and dialogs, where letters are
typed or mean something else, use `alt+w`, `alt+i`, `alt+o`, or `F2`,
`F3`, `F4`; those work everywhere. Turning the voice on says all this
first. A newer phrase cuts off an older one, and holding an arrow key
speaks only where you stop. How it works:
[internal/voice](../voice/README.md).

## Code

```
model.go    the Bubble Tea model: keys, polling task records, dialogs
views.go    the task list, tabs, help panel and footer
detail.go   an activity line's details, and the task in full (e)
clipboard.go copying: c, the ⧉ button, mouse selection
draw.go     panels, buttons and colors; pixel.go the header art
state.go    each task's live state, derived from its events
form.go     the new-task form
setup.go    provider setup and sign-out
roles.go    the roles screen and role editor
settings.go the configuration screen (C)
tour.go     the walkthrough's tooltips
speak.go    what the voice says on its own
voicekeys.go what it says when asked (w, i, o)
spokenevent.go an activity event in words
```
