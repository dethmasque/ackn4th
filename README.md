# Local A2A Automation with `ackn4th`

## `)) <==> ((`

Back and forth, forever.

```
   ▓▒░  a c k n 4 t h  ░▒▓
   agent ──▶ ( ✎ ) ──▶ agent
     ▲                   │
     ╰──── ( ✓ ) ◀───────╯
```

`ackn4th` puts a team of AI agents to work on your code. Each agent plays a
software role (backend dev, QA, reviewer, …). They take turns, or work in
parallel, in your project directory until they all agree the task is done.

```sh
cd ~/code/my-project
ackn4th start-task --parallel --roles backend_dev,qa_engineer,code_reviewer \
  "Complete every requirement in https://github.com/example/example/issues/1"
```

Agents run on your existing plans through their CLIs (**Claude Code**,
**Codex**, **Cursor**, **Copilot**) or on API keys (**Claude**, **Gemini**,
**OpenAI**). Every agent can use a different one.

> **A hobby project, for hobbyists.** ackn4th is for quickly putting together
> a team of software-development agents that iterates on a prompt until the
> result is ready to demo to you. It is not a product, a hosted service or
> something to run in production, and it comes with no support or warranty.
> It runs on your own accounts and API keys: what you send, what it costs and
> following each provider's terms are up to you.

## Is it for you?

**What it's for**

- Building an MVP from a skeleton, or from nothing but an idea.
- Tackling a well-defined GitHub issue with a team of roles: it reads the
  issue, works on a branch and opens a draft pull request for you to review.
- Bug smashing on a repository you know well enough to judge the fixes.
- Anyone happy to review AI-drafted pull requests before anything merges.

**Who it's for**

- Developers who want a team of agents on a side project.
- People who live in GUIs and are curious about the command line: the
  dashboard does everything with a form and arrow keys, and can read itself
  aloud.
- Product managers who want to see an idea working before asking anyone to
  build it.
- Anyone with an idea and some tokens.

**Who it's not for**

- Professionals automating their whole development workflow.
- People who don't like reviewing AI output, code included. Everything
  ackn4th makes is a draft for you to check.
- Those without ideas or tokens.

**What it's not for**

- Deploying to production.
- Commercial applications or services.

## Install

ackn4th is one binary for macOS and Linux, on Intel/AMD (amd64) and ARM
(arm64). **Windows is not part of 0.1.0**: its sandbox is still being
verified, and without a working sandbox agents can't run commands (see the
[security model](internal/sandbox/README.md)). Windows builds will follow
once they pass the same tests. Every release is on the
[releases page](https://github.com/dethmasque/ackn4th/releases), with
`checksums.txt` to verify the downloads.

**macOS and Linux**

```sh
os=$(uname -s | tr A-Z a-z); arch=$(uname -m)
case $arch in x86_64) arch=amd64 ;; aarch64) arch=arm64 ;; esac
url=https://github.com/dethmasque/ackn4th/releases/latest/download
curl -fsSLO "$url/ackn4th_${os}_${arch}.tar.gz"
curl -fsSL "$url/checksums.txt" | shasum -a 256 --ignore-missing -c   # or sha256sum --ignore-missing -c
tar -xzf "ackn4th_${os}_${arch}.tar.gz" ackn4th
sudo mv ackn4th /usr/local/bin/      # or any directory on your PATH
ackn4th version
```

The binaries aren't signed or notarized. Downloaded with `curl` as above,
macOS runs them as they are; if you download the archive in a browser
instead, clear the quarantine flag once: `xattr -d com.apple.quarantine ackn4th`.

**Debian, Ubuntu, Fedora, Alpine:** packages put `ackn4th` in `/usr/bin`.

```sh
url=https://github.com/dethmasque/ackn4th/releases/latest/download
arch=$(uname -m); case $arch in x86_64) arch=amd64 ;; aarch64) arch=arm64 ;; esac
curl -fsSLO "$url/ackn4th_linux_$arch.deb" && sudo apt install "./ackn4th_linux_$arch.deb"    # Debian, Ubuntu
curl -fsSLO "$url/ackn4th_linux_$arch.rpm" && sudo dnf install "./ackn4th_linux_$arch.rpm"    # Fedora, RHEL
curl -fsSLO "$url/ackn4th_linux_$arch.apk" && sudo apk add --allow-untrusted "./ackn4th_linux_$arch.apk"   # Alpine
```

**Windows:** no release yet. The container below is the only supported
route, from inside WSL 2.

**With Go** (1.27.3 or later), on any OS:

```sh
go install github.com/dethmasque/ackn4th/cmd/ackn4th@v0.1.0
```

Agents' commands run in the OS's own sandbox
([security model](internal/sandbox/README.md)):

- **macOS:** built in.
- **Linux:** install [bubblewrap](https://github.com/containers/bubblewrap)
  (`apt install bubblewrap`).
- **Windows:** not released yet (an AppContainer sandbox exists in the
  source but is unverified; builds from source refuse to run commands
  unless it works).

Optional: the [GitHub CLI](https://cli.github.com) (`gh`), for
[GitHub issues, repositories and draft pull requests](internal/github/README.md).

On macOS or Linux you can keep ackn4th in a container instead, with Docker
([container/README.md](container/README.md)):

```sh
git clone https://github.com/dethmasque/ackn4th ~/ackn4th
~/ackn4th/container/ackn4th-container --build      # builds the image, once (a few minutes)
alias ackn4th="$HOME/ackn4th/container/ackn4th-container"   # add this to your shell profile
```

## Providers

You only need the ones you use. A provider's CLI on your plan is usually the
better choice: no API key to manage and no API rate limits. When you don't
name a provider and its CLI is signed in, ackn4th uses the CLI.

| Provider | Runs on |
| --- | --- |
| **Claude Code** (`claude-code`) | your Claude Pro/Max plan |
| **Codex** (`codex`) | your ChatGPT plan |
| **Copilot** (`copilot`) | your GitHub Copilot plan |
| **Cursor** (`cursor`) | your Cursor plan |
| **Claude** (`claude`) | Anthropic API credits |
| **Gemini** (`gemini`) | Google AI Studio (free tier) |
| **OpenAI** (`openai`) | OpenAI API credits |

How to sign in to each, and how to choose models and efforts per agent:
[internal/provider/README.md](internal/provider/README.md).

## First run

Agents work in the current directory. The first time they work in a
directory, ackn4th asks you to **trust** it, since agents read it and send
its contents to the model provider. ackn4th has no accounts of its own:
agents run on the AI providers you sign in to, and those sign-ins last up
to 24 hours; after that, `ackn4th login` signs you in again.

```sh
cd ~/code/my-project
ackn4th
```

`ackn4th` on its own opens the [dashboard](internal/dash/README.md):

1. **A walkthrough** shows a tooltip at a time. `t` (or `ackn4th tour`)
   replays it whenever you like.
2. **Set up providers.** It lists each one as ready, needing sign-in, or not
   installed (with the install command). Pick some with `space`, press `⏎` to
   sign in. `p` brings this back, and `x` there signs out.
3. **Accessibility.** Press **`v`** to have the dashboard read aloud by a
   natural-sounding voice that runs on your computer
   ([details](internal/voice/README.md)). `ackn4th --voice` opens it
   speaking.
4. **Start a task.** Press **`n`**. Write the prompt, name the task, pick
   the workspace (or a GitHub repository and branch), then add agents: a role, 
   provider, model and effort each. **`f1`** explains every field; **`ctrl+s`** 
   launches it in the background.
5. **Roles.** `R` lists the roles agents can play; add your own, edit
   any, or import role files ([roles](roles/README.md)).
6. **Watch.** Each agent works live. `?` lists every key. Quitting the
   dashboard never stops a task.

A command that's missing an argument, value, or flag asks for it, unless
running in non-interactive (`ACKN4TH_NO_INPUT`) mode. 

```sh
ackn4th login                      # pick providers from a numbered list
ackn4th start-task --name "Rate limiting" --roles backend_dev,code_reviewer "Add rate limiting to the API, with tests"
ackn4th watch-task "Rate limiting" # follow a task by name (or ID); ctrl+c detaches
ackn4th logout --provider openai   # sign out of one provider (or all, without --provider)
```

Every command, flag and the scripting (CI) mode:
[internal/cli/README.md](internal/cli/README.md).

## Documentation

Each topic lives next to the code it describes:

| Topic | Where | Code |
| --- | --- | --- |
| Commands, flags, scripts and CI | [internal/cli/README.md](internal/cli/README.md) | `internal/cli/` |
| Providers, sign-in, choosing models | [internal/provider/README.md](internal/provider/README.md) | `internal/provider/`, `internal/auth/` |
| How a task runs: turns, parallel, shared context, onboarding | [internal/task/README.md](internal/task/README.md) | `internal/task/`, `internal/agent/`, `internal/onboard/` |
| The dashboard | [internal/dash/README.md](internal/dash/README.md) | `internal/dash/` |
| The dashboard's voice (accessibility) | [internal/voice/README.md](internal/voice/README.md) | `internal/voice/` |
| GitHub: issues, repositories, branches, draft pull requests | [internal/github/README.md](internal/github/README.md) | `internal/github/` |
| Security model: sign-ins, the workspace jail, OS sandboxes | [internal/sandbox/README.md](internal/sandbox/README.md) | `internal/sandbox/`, `internal/workspace/`, `internal/auth/` |
| Roles: built-in, and adding, editing and importing your own | [roles/README.md](roles/README.md) | `roles/`, `internal/role/` |
| Configuration and where files live | [internal/config/README.md](internal/config/README.md) | `internal/config/` |
| Running in a container | [container/README.md](container/README.md) | `container/` |
| Developing: build, test, the dev sandbox | [sandbox/README.md](sandbox/README.md) | `justfile`, `sandbox/` |
| Packaging and releasing | [packaging/README.md](packaging/README.md) | `packaging/` |

The rest of the tree:

```
cmd/ackn4th/          main package
internal/agent/       one agent's turn loop and system prompt
internal/auth/        OAuth grants (ant / gcloud), keychain API keys (or a tmpfs dir), the 24h sign-in limit
internal/notes/       shared per-project notes
internal/onboard/     project brief, workspace snapshots, change digests with diffs
internal/ratelimit/   shared per-key pacing from rate-limit headers
internal/tips/        first-run tips and the walkthrough (seen-state, opt-out)
internal/tools/       agent tools (list/read/search/write/edit/delete/run_command/…)
internal/ui/          ASCII art: banner, boxes, tables, gauges, animations
internal/workspace/   os.Root confinement, write globs, secret filtering
```

## License

[MIT](LICENSE): use, change and share it freely, with no warranty. The
license covers ackn4th's own code only. The models and agent CLIs it drives
come with their providers' terms, which you agree to when you sign in. The
optional voice downloads sherpa-onnx and Kokoro (both Apache-2.0) from their
own releases.
