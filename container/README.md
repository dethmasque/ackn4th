# Running in a container

[← README](../README.md)

`ackn4th-container` runs ackn4th in a Debian image, from the directory you
run it in. Agents' commands are sandboxed there by bubblewrap, as on a Linux
host. The image includes the Claude Code, Codex and Copilot CLIs and `ant`
(for Claude API sign-in).

```sh
git clone https://github.com/dethmasque/ackn4th ~/ackn4th
~/ackn4th/container/ackn4th-container --build      # builds the image, once (a few minutes)
alias ackn4th="$HOME/ackn4th/container/ackn4th-container"   # add this to your shell profile
```

With the alias, every `ackn4th …` command in these docs works as written.

```sh
ackn4th-container [command …]      # any ackn4th command; none opens the dashboard
ackn4th-container --shell [cmd]    # a shell (or one command) inside
ackn4th-container --stop           # stop this directory's container (ends the session)
ackn4th-container --build          # rebuild the image, e.g. after git pull
```

From a clone, `just container-build` and `just container <command>` do the
same.

- **One container per directory.** The directory you run it from is mounted
  at `/work/<name>`, and ackn4th runs there. The container stays up, so your
  session and background tasks survive between commands.
- **Secrets stay in memory.** The session and API keys live on a tmpfs
  inside the container (`ACKN4TH_SECRETS_DIR`) and are gone when it stops.
- **What persists.** Config, task records and the agent CLIs' logins are
  kept in the `ackn4th-home` volume. These logins are separate from the ones
  on your machine.
- **Browser sign-ins.** Claude's sign-in (`ant`) calls back to a localhost
  port that the wrapper publishes for each directory, bound to `127.0.0.1`
  only. Codex and Copilot use device codes, and Claude Code a pasted code.
- **Agent CLIs.** The image includes Claude Code, Codex and Copilot; choose
  others with `AGENT_CLIS="@anthropic-ai/claude-code@2.1.316" ackn4th-container --build`
  (npm packages at exact versions, empty for none). The Cursor CLI has no
  npm package. Install it inside, into the persistent home:
  `ackn4th-container --shell 'curl https://cursor.com/install -fsS | bash'`.
  Cursor's install script always fetches its latest release, so that one
  can't be pinned.
- **Permissions.** bubblewrap needs namespaces and a fresh `/proc`, which
  Docker's default seccomp, AppArmor and `/proc` masking forbid. The wrapper
  relaxes those three, drops every capability, and sets
  `no-new-privileges`. `CONTAINER_ENGINE=podman` uses Podman.
- **No audio**, so the dashboard's [voice](../internal/voice/README.md)
  needs ackn4th installed on your machine.

## The image

[`Dockerfile`](Dockerfile) builds it, with the repository root as the
context (where `.dockerignore` is):

```sh
docker build -f container/Dockerfile .
```

Every dependency is pinned: base images by digest, Debian packages to a
snapshot of the archive, `ant` and the npm packages to exact versions. Bump
them together, deliberately, and rebuild; the `ant` and npm versions are
repeated in the install hints in `internal/auth/oauth.go`. The wrapper
passes your user and group IDs as build arguments, so files it writes in
your directory are yours.
