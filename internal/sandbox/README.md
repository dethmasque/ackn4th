# Security model

[← README](../../README.md)

What ackn4th does so that a team of agents working on your code can't reach
the rest of your machine, your credentials, or other projects. The OS
sandbox lives in this package; the workspace jail is in
[`internal/workspace`](../workspace/) and the sign-ins in
[`internal/auth`](../auth/).

## Sign-ins

**Short-lived OAuth where the provider offers it; keychain-only keys where
it doesn't.**

| Provider | Signed in with | Credential | Lifetime |
| --- | --- | --- | --- |
| Claude | `ant auth login --profile ackn4th` | short-lived access token, refreshed by the SDK | refresh token hard-expires server-side |
| Gemini (default) | AI Studio API key, pasted at login | long-lived key, stored **only in the OS keychain** | used only within the 24h sign-in limit; deleted by `logout`; revoke it at AI Studio |
| Gemini (Vertex AI) | `gcloud auth application-default login` | ~1h access token, refreshed in memory | refresh token revoked daily |
| Claude Code | Claude Code's own login (`claude auth login`) | none held by ackn4th | Claude Code's; used only within the 24h sign-in limit |
| OpenAI | API key, pasted at login | long-lived key, stored **only in the OS keychain** | like the Gemini key; revoke it at platform.openai.com |
| Codex, Cursor, Copilot | each CLI's own login | none held by ackn4th | the CLI's; used only within the 24h sign-in limit |
| GitHub (optional) | `gh auth login` | none held by ackn4th | gh's |

The OS keychain is the Keychain on macOS, the Credential Manager on Windows
and the Secret Service (e.g. GNOME Keyring) on Linux. Where there is none (a
container), keys and the session go to a private tmpfs directory instead
(`ACKN4TH_SECRETS_DIR`, which must be mode 0700), so they still never reach
disk.

The Gemini and OpenAI keys are read with hidden input (or from a pipe, e.g.
a password manager), never from a flag, environment variable or file, so
they can't leak into shell history, `ps`, or a child process, and agents
can't read them. Google offers OAuth for Gemini only through a Google Cloud
project, which most people don't have; Vertex AI is there for those who do.

Both OAuth logins go into config dirs private to ackn4th
(`<config>/oauth/{anthropic,gcloud}`), with the user's credential
environment variables stripped. Your everyday `ant` and `gcloud` sessions
are never read or changed. Only user OAuth grants are accepted: a Google
service-account key file, which is a long-lived secret, is refused.

The provider clients ignore `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`,
`ANTHROPIC_BASE_URL`, `GOOGLE_APPLICATION_CREDENTIALS`, `GOOGLE_API_KEY`,
`GEMINI_API_KEY`, `GOOGLE_VERTEX_BASE_URL`, `OPENAI_API_KEY`,
`OPENAI_BASE_URL` and friends. Endpoints are pinned to `api.anthropic.com`,
`api.openai.com`, `generativelanguage.googleapis.com` and Google's
`aiplatform.googleapis.com` hosts, so nothing in the shell can swap
credentials or redirect a token.

**Rate limits.** API-key clients (OpenAI, Gemini with a key) share one gate
per key. It reads the provider's rate-limit headers, holds requests when
fewer than 5% of the window's requests or tokens are left, and makes every
agent wait out a 429's `Retry-After` together. A parallel team slows down
instead of failing mid-turn.

## Daily deauth

ackn4th has no accounts of its own, but it limits how long it uses the
providers you sign in to. `ackn4th login` records which providers are
signed in and until when (in the OS keychain), `session_ttl` from now,
capped at 24h. That record is checked before every model request, not only
when a command starts. When it has expired:

1. running tasks stop before their next request (resumable after login);
2. the record is deleted;
3. OAuth grants are revoked: Gemini's Vertex refresh token is revoked with
   Google and deleted; Claude's tokens are deleted from disk. API keys
   (Gemini, OpenAI) stay in the keychain, unusable until the next login (or
   you'd paste them daily, which tempts people to keep them in a text file).
   Agent CLIs keep their own logins.

The next `ackn4th login` is a fresh browser sign-in for Claude, and a quick
check for everything else. `ackn4th logout` does the same immediately, and
also deletes the API keys; `logout --provider p` does it for one provider.

Two caveats:
- Expiry is enforced the next time ackn4th runs, since there is no daemon.
  Until then the grants stay on disk (owner-only files), though Claude's
  refresh token also expires on its own.
- Anthropic's local logout deletes the tokens; this release doesn't confirm
  that Anthropic revokes them server-side.

## The workspace jail

Agents can only touch files through ackn4th's tools, and every tool call
goes through an `os.Root` opened on the task workspace:

- absolute paths, `..` traversal, and symlinks that leave the workspace are
  rejected;
- writes are limited to the role's `writable` globs (e.g. `code_reviewer`
  may only write `REVIEW.md` and `reviews/**`);
- `.git/` (hooks would run code on your next git command) and `.ackn4th/`
  are never writable, and writes never pass through a symlink;
- credential files (`.env`, `*.pem`, `*.key`, `id_rsa*`, …) are unreadable,
  so they are never sent to a provider;
- workspaces must resolve inside a trusted directory (`workspace_roots`,
  managed with `ackn4th trust`, plus `default_workspace_root`), checked
  before anything is created and again on every run; the filesystem root
  and your home directory can't be trusted;
- ackn4th's own config (OAuth grants) and data are invisible to agents,
  even inside a workspace;
- an agent can't overwrite or edit a file without knowing its current
  version, so concurrent agents can't clobber each other.

## Commands, sandboxed by the OS

Agents verify their work with `run_command`: builds, tests, linters, type
checkers, running the code. Each command runs under the operating system's
sandbox (Seatbelt via `sandbox-exec` on macOS,
[bubblewrap](https://github.com/containers/bubblewrap) on Linux), not on
trust. Windows has none in release builds (below), so commands don't run
there at all:

- writes are allowed only in the workspace and the agent's private scratch
  directory (`tasks/<id>/scratch/N`), which serves as the home and temp
  directories (`HOME` and `TMPDIR`) and the tool caches (`GOCACHE`, npm, pip). 
  Nothing system-wide or user-wide can change: no dotfiles, no `/tmp`, no 
  global installs. `.git` and `.ackn4th` stay read-only, and a read-only 
  role's commands can't write the workspace;
- secrets can't be read: `~/.ssh`, `~/.aws`, `~/.kube`, `~/.docker`, gcloud
  and gh configs, `.netrc`/`.npmrc`, keychains, browser profiles,
  `~/.claude`, ackn4th's own config and data, and credential files in the
  workspace (`.env`, `*.pem`, …);
- the network is off unless `commands.network: true`. Localhost still works,
  for tests that start a server;
- the environment is rebuilt from an allowlist (`PATH`, locale, toolchain
  homes), so tokens in your shell never reach a command;
- on macOS, commands can't open apps, send Apple Events or reach the
  keychain, so nothing they start runs outside the sandbox;
- each command has a time limit (`commands.timeout`, default 10m), and
  everything it started is killed when it runs out (its process group).

**On Windows**, release builds have no sandbox: `run_command` is
unavailable, and so are the Codex, Cursor and Copilot providers. An
AppContainer sandbox exists in the code but is built only with
`-tags winsandbox`, because it doesn't yet pass the confinement tests
(CI runs them on Windows in a separate, non-blocking step). A sandbox that
lets commands read `.env` is worse than none. The design, for when it
passes: commands run with `cmd.exe`. An AppContainer can read the
system (Windows, Program Files) and none of your files unless their access
list allows it. So the first time a workspace, scratch directory or tool
folder on your `PATH` is used, ackn4th adds an entry to its access list
granting the sandbox access. That first command takes longer in a large
workspace. Each entry names a capability that ackn4th derives for that one
path and gives only to the commands meant to reach it. A command in one
workspace can't reach another, even though the entries stay. Deny entries
keep `.git`, `.ackn4th` and credential files out of reach. AppContainers run
at low integrity, so the folders they write are labeled low integrity too.
Agent CLIs reach ackn4th's tools over localhost, which Windows blocks for
AppContainers until an administrator runs, once:
`CheckNetIsolation LoopbackExempt -a -p=<SID>` (ackn4th prints it with the
SID).

Agents still change files only through the file tools, so their writes are
checked against their role's globs. Files a command changes (generated
code, formatters) are credited to the agent that ran it, and it must
re-read them before editing. Without a sandbox (Linux where `bwrap` is
missing or can't create namespaces, or Windows),
`run_command` is unavailable rather than unconfined. The
[container](../../container/README.md) always has one.
`commands.enabled: false` turns it off.

## Agent CLIs

**Claude Code (`claude-code`).** This provider runs your local Claude Code
(`claude -p`) as the model loop, so agents use your Claude plan rather than
API credits. ackn4th keeps the tools:

- every built-in Claude Code tool is switched off (`--tools ""`), so the
  model can't run commands, read files or reach the web through Claude
  Code;
- your Claude Code settings, hooks, plugins, skills and MCP servers are not
  loaded (`--setting-sources ""`, `--strict-mcp-config`,
  `--disable-slash-commands`);
- the only tools are ackn4th's own, served over MCP from `127.0.0.1` with a
  random per-session token. Each call goes through the same workspace jail
  as every other provider;
- it runs in the task's workspace, so each agent's session is filed with
  that project (`claude --resume` there lists them) instead of in one place
  for every task;
- `ANTHROPIC_API_KEY` and friends are stripped from its environment, so it
  can't fall back to API billing.

ackn4th stores no credential for it. `ackn4th login --provider claude-code`
only checks that Claude Code is signed in. The 24h sign-in limit still
applies, but `ackn4th logout` leaves Claude Code signed in (use
`claude auth logout`). It is experimental: it depends on Claude Code's
`stream-json` output, and agents share your plan's usage limits. Make sure
the way you use it fits your plan's terms.

**Codex, Cursor and Copilot (`codex`, `cursor`, `copilot`).** These CLIs
come with their own tools (shell, file edits), so instead of switching those
off, ackn4th runs each request's CLI process inside the **same OS sandbox as
`run_command`**: writes only to the workspace (never `.git` or `.ackn4th`)
and a private scratch dir, secrets hidden, nothing launched outside the
sandbox. Where a CLI starts in another directory than the workspace, that
directory is made read-only, so everything it writes lands in the
workspace. The only extra access is the CLI's own state dir (`~/.codex`,
`~/.cursor`, `~/.copilot`) and, on macOS, the security service: Cursor and
Copilot keep their logins in the keychain, and all three verify TLS
certificates through it. Network is on, since the CLI must reach its
provider. ackn4th's tools are added over the same localhost MCP bridge with
a random per-session token, and auto-approved. Files the CLI changes itself
are credited to that agent.

The CLIs' own sandboxes and approval prompts are turned off
(`--dangerously-bypass-approvals-and-sandbox`, `--sandbox disabled`,
`--allow-all-tools`), because a sandbox can't be nested inside another and
nobody is there to approve: ackn4th's sandbox is the boundary. Your custom
instructions and MCP servers are not loaded. Without a working sandbox
(Linux without `bwrap`, or Windows), these providers refuse to run: use the
container, where they work under bubblewrap, or WSL on Windows.

## GitHub

`gh` runs outside the sandbox, as you, and only for what you asked for:
fetching the issues and pull requests linked in a prompt, cloning
`--repo`, fetching `--branch`, and pushing and opening a draft pull request.
Agents never get GitHub access, and the git credential helper is set for
each command only, never in your git config. See
[GitHub](../github/README.md).

## Code

```
sandbox.go            the Config every backend takes: workspace, scratch, read-only dirs, network
sandbox_darwin.go     Seatbelt profile (sandbox-exec)
sandbox_linux.go      bubblewrap arguments
sandbox_windows.go    AppContainer, access-list entries, job objects (winutil.go: Win32 helpers);
                      only with -tags winsandbox
sandbox_windows_off.go  Windows release builds: no sandbox, so run_command is unavailable
sandbox_other.go      elsewhere: no sandbox, so run_command is unavailable
program_*.go          resolving the program to start (Windows: npm .cmd shims run with node)
secrets.go            the credential files hidden from commands (Linux; Windows with the tag);
                      if the workspace can't be read, commands don't run
proc_*.go             process groups and killing a command's whole tree
```
