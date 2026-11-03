# How a task runs

[← README](../../README.md)

A **task** is a prompt, a **workspace** directory, and a **team** of agents.
Agents take turns in the order given by `--roles`. Each turn an agent reads
and edits files in the workspace through a small set of tools, then ends its
turn with a message that is handed to the next agent: back and forth.

A task **completes** when every agent calls `declare_done` in consecutive
turns. If any agent ends a turn with concerns, the count starts over. It
**stops** when it reaches its round limit (`--rounds`, default
`limits.max_rounds`), or runs until consensus with `--forever`.

```
━━━━ ROUND 1 / 10 ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

 ▶ fullstack_dev · gemini · gemini-3.1-pro-preview
   ├─ list_dir     .
   ├─ write_file   main.go (2.1 KB)
   │ Wrote main.go with add/list/done commands. Product manager: please
   │ check the acceptance criteria.
   ╰─ turn ended · 12.4k in / 2.2k out

 ▶ product_manager · claude · claude-opus-5-5
   ├─ read_file    main.go
   ╞═ ◆ declared done: All five acceptance criteria met.
```

## In parallel

With `--parallel`, every agent takes its turn of a round **at the same
time**, in the same workspace:

```sh
ackn4th start-task --parallel --roles backend_dev,frontend_dev,qa_engineer \
  "Add a settings page backed by a /settings API, with tests"
```

- **Messages mid-turn.** `message_teammate` reaches a teammate who is
  working right now, alongside their next tool results. Each round's closing
  messages go to everyone at the start of the next round.
- **No lost work.** An agent can only change a file whose current version it
  knows (read in full, written itself, or seen as a diff). If a teammate
  changed it in the meantime, the write fails with who changed it, and the
  agent re-reads and redoes its change. Changes are serialized, so two
  agents never write the same file at once. This rule holds in turn-taking
  tasks too.
- **Credit and catch-up.** Every change is credited to the agent that made
  it, and the next round's digest shows each agent its teammates' diffs.
- **Consensus** means every agent declares done in the same round. A task
  stopped mid-round resumes only the agents that hadn't finished it.
- **Going in circles.** Within a turn, a repeat of a lookup the agent
  already made (`list_dir`, `read_file`, `search_files`, `run_command`
  with the same input), with no file changed by it since, isn't run: the
  agent is told the answer is already in its conversation. After three
  such repeats it is asked to hand off. Across turns, a task stops (to
  save cost) when an agent gives the same handoff three turns running, or
  runs out of tool calls two turns running without changing a file. The
  reason names the agent and suggests resuming it on another model or
  provider, or a follow-up; resuming gives it a fresh start. Done votes
  don't count: an agent waiting on teammates may say so every turn.
- **Follow-ups** (`follow-up`, or `a` in the dashboard) give a task that
  isn't running more work. Every agent gets the follow-up once, at the
  start of its next turn, whether the team takes turns or works at once,
  and every agent's done vote is cleared. The follow-up gets as many
  rounds as the task's last run allowed, unless `--rounds` says otherwise.
  In the dashboard, a follow-up can also give agents other roles, models
  or efforts (not other providers). A new role or effort keeps the agent's
  conversation; a new model starts it fresh, with the workspace, notes and
  history carried over. The follow-up tells every agent what changed.

Lines from agents working at once are tagged with the agent:

```
 backend_dev ├─ message_teammate → qa_engineer: /settings handler is in api/settings.go
 qa_engineer ├─ read_file        api/settings.go
 backend_dev ├─ write_file       api/settings.go (2.4 KB)
 qa_engineer │  ✗ api/settings.go changed since you last read it (by backend_dev): read it again…
```

## One provider, many roles (recommended)

By default **the whole team runs on one provider** (`default_provider`,
claude out of the box). This is the fast, cheap way to run a team, not a
compromise:

- **Shared context, paid once.** Each agent's system prompt is split in
  two. The *shared* part (tools, team, rules, and the project brief frozen
  at task start) is byte-identical for every agent; the agent's *own* part
  (its role and the paths it may change) comes after it. Agents on the same
  provider, model and effort read the shared part from **one prompt-cache
  entry**. Cached input costs about a tenth of fresh input on Claude and is
  faster to process.
- **Prewarmed for parallel rounds.** A cache entry can only be read once the
  request writing it finishes, so agents that start together would each
  write their own. Before a parallel round, ackn4th writes the shared part
  once (a `max_tokens: 0` request on Claude) and every agent reads it.
- **Same-provider teams work side by side** with `--parallel`, or in turns
  without it.

```sh
ackn4th start-task --parallel --roles backend_dev,qa_engineer,tech_writer "…"   # all claude
ackn4th start-task --provider gemini --roles backend_dev,code_reviewer "…"      # all gemini
ackn4th start-task --mix --roles backend_dev,code_reviewer "…"                  # claude, gemini, …
```

The task card shows who shares a cache (`context  shared · all 3 agents
read one cached copy`), each turn shows its hit rate (`· 67% cached`), and a
mixed team gets a tip saying how many separate caches it uses. Mixing
providers is still useful for a genuine second opinion (a Gemini reviewer on
Claude's code, say); it just costs more.

**What this is, and isn't.** Claude Projects, Claude's memory, and Gemini's
conversation branching and memory are features of the Claude and Gemini
apps, not of the APIs ackn4th uses. The API equivalents ackn4th uses:

| Want | Claude API | Gemini |
| --- | --- | --- |
| Agents share one copy of common context | prompt caching: breakpoint on the shared system block, prewarmed | implicit caching: automatic for a repeated prompt prefix |
| Each agent keeps its own growing conversation | automatic caching of the conversation prefix | implicit caching |
| Knowledge that persists across chats/tasks | project notes (`update_notes`), like the API memory tool | project notes |
| Long conversations | server-side compaction | client-side summary |

## Getting agents up to speed

Every turn starts with context ackn4th assembles locally, without any model
calls, so agents spend their tool calls on the work instead of
rediscovering the project:

| What | When | Why |
| --- | --- | --- |
| **Project brief**: layout, languages, `AGENTS.md`/`CLAUDE.md`/`GEMINI.md`, README, build manifests, CI | frozen at task start, in the shared system prompt | replaces a dozen exploratory `list_dir`/`read_file` calls (≤ 32 KB, ~8k tokens), cached once for the whole team |
| **From GitHub**: issues and pull requests linked in the prompt, with their comments | fetched at task start, frozen with the brief | agents have no network; see [GitHub](../github/README.md) |
| **Project notes**: the team's shared knowledge, written with the `update_notes` tool | first turn, and whenever they change | knowledge outlives the task: the next task on the same workspace starts from what earlier agents learned |
| **Changes since your last turn**: every file changed, by whom (a teammate or you), with diffs | every later turn | agents read diffs instead of re-reading whole files, and see edits you made between rounds |
| **No re-sends**: re-reading a file that hasn't changed returns a short note | any `read_file` | the earlier copy is still in context; tokens aren't paid twice |
| **Compaction** at `compact_at` input tokens (default 200k) | long tasks | Claude compacts server-side, Gemini and OpenAI between turns, and the agent CLIs on their own; the summary keeps the task, decisions, file states and open questions |

The system prompt and tool list stay identical across turns (and across
resumes: the brief is frozen in the task directory), so the provider's
prompt cache covers the whole conversation prefix. Each turn's footer shows
how much input came from the cache and how full the agent's context is:

```
   ├─ catch-up     shared prompt 21.4 KB incl. brief · notes 1.2 KB · 2 changed file(s)
   ╰─ turn ended · 150.0k in / 9.0k out · 91% cached · context [███████░░░] 143.0k/200.0k
```

Preview the brief with `ackn4th brief [dir]`. Notes live in ackn4th's data
dir, keyed by workspace, never in the project. Read them with
`ackn4th notes [dir]`, or seed them before the first task with
`ackn4th notes --edit`: a few lines on how to build and test save every
agent the discovery. An `AGENTS.md` in the project is quoted in every brief.

## When a task ends

Every run ends with a summary. A finished, stopped or failed task prints its
outcome, then a **FOR YOU** box with the open action items (decisions only
you can make, credentials, manual steps, things the agents couldn't
verify), numbered, each with who raised it. Agents add them with
`add_user_todo` as they work and close them when the team handles them.

The full report is written to `<data>/tasks/<id>/summary.md` (see
[where files live](../config/README.md#where-files-live)): the outcome, the
action items (open and resolved), what each agent last reported, every file
the agents changed and, with `--draft-pr`, the pull request's URL.
`ackn4th show-task <id>` shows it again.

## Code

```
task.go       the task record and its store (<data>/tasks/<id>/)
runner.go     the orchestrator: rounds, turns or parallel lanes, consensus
run.go        one run's shared state: frozen brief, cache prewarming, messages, action items
live.go       what each agent's model is writing right now, for the dashboard
proc_*.go     background runners: start, signal, find
summary.go    the end-of-task report
```

A task's state lives in `<data>/tasks/<id>/`: `task.json`, its `name`
(a file of its own, so a rename while a background runner rewrites
`task.json` isn't lost), the `events.jsonl` transcript (with
each tool call's arguments, what came back, and the models' reasoning
summaries, cut to 4 KB each, 16 KB for failures), each agent's conversation in `agents/N.json`
(what makes resume exact), the frozen project brief (`brief.md`, plus
`github.md`), workspace snapshots for change digests
(`agents/N.snapshot.json`, `workspace.json`), and `runner.log` for
background runs. The agent loop itself is in
[`internal/agent`](../agent/), the brief and digests in
[`internal/onboard`](../onboard/).
