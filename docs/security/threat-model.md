# Threat model

Status: pre-0.1.0 review by the security role. Source review plus
cross-compiles (`go vet` for windows, linux, darwin); **nothing in this
document about Windows has been executed on a Windows host.** Items marked
VERIFY must pass the `windows-sandbox` CI job
(`internal/sandbox/sandbox_windows_security_test.go`) before Windows is
released. See also [sandbox parity](../architecture/sandbox-parity.md) and
[ADR 0001](../adr/0001-windows-release-gate.md).

## What we protect (assets)

| Asset | Where | Why it matters |
| --- | --- | --- |
| Provider credentials: API keys, OAuth grants | OS keychain / private tmpfs, `<config>/oauth/*` | Spend, impersonation |
| The user's other files: `~/.ssh`, cloud and registry tokens, browser profiles, other projects | the user's profile | Lateral compromise |
| Credential files in the workspace (`.env`, `*.pem`, ...) | workspace | Must never reach a model provider or a command's output |
| `.git/` and `.ackn4th/` of the workspace | workspace | Hooks and config run as the user, outside any sandbox, on the next `git` command (ackn4th itself runs `git commit/push` on the host in `internal/github/pr.go`) |
| ackn4th's config and task data | `<config>`, `<data>` | Contains grants, session record |
| Integrity of the host (code execution as the user) | everything | The goal of every attack below |
| The clipboard, the desktop session | the user's session | Passwords copied from a manager |

## Who attacks, and what they control

The main adversary is **content, not a person at the keyboard**: a model
that has been manipulated by prompt injection. Sources: the repository being
worked on, a GitHub issue or PR named in the prompt, a dependency's README
or test output, a web page fetched by an agent CLI. That adversary can:

- make an agent call any tool with any arguments (file tools, `run_command`);
- run arbitrary code in `run_command`, and in the agent CLIs (Codex, Cursor,
  Copilot), with the full power of the sandbox's rules and no more;
- craft workspace contents: huge trees, odd file names, symlinks and (on
  Windows) junctions, files named like Windows devices, alternate data
  streams, 8.3 short names;
- read anything the model provider receives (so anything a tool returns is
  disclosed to the provider).

Not in scope: a hostile local user or malware already running as the user
(they can read the keychain anyway); a compromised provider; physical
access; supply-chain compromise of the Go toolchain.

## Trust boundaries and entry points

```
 model output ──▶ [ file tools ] ──▶ os.Root workspace jail ─────────▶ workspace files
              ├─▶ [ run_command ] ─▶ OS sandbox ───────────────────────▶ workspace + scratch
              └─▶ [ agent CLI ] ──▶ same OS sandbox + localhost MCP ───▶ workspace + scratch + CLI state dir
 ackn4th (unsandboxed, as the user): git commit/push via gh, keychain, providers' HTTPS, MCP bridge on 127.0.0.1
```

1. **File tools → workspace jail** (`internal/workspace`, `os.Root`): path
   traversal, symlinks, role write globs, secret-name denylist, protected
   dirs, Windows aliasing (`:stream`, `~1`, trailing dot/space, and since this
   review, reserved device names such as `NUL`/`CON.txt`).
2. **`run_command` and agent CLIs → OS sandbox** (`internal/sandbox`):
   Seatbelt (macOS), bubblewrap (Linux), AppContainer (Windows). Writes only
   in workspace + scratch (+ Keep); `.git`/`.ackn4th` read-only; secrets
   unreadable; network off by default; environment allowlist.
3. **Sandbox → host**: anything the sandbox can write and the host later
   trusts: `.git/` (hooks, config, `core.fsmonitor`), build files the user
   runs, files committed into the PR. Only `.git`/`.ackn4th` are blocked
   mechanically; the rest is reviewed by the user (draft PR, "everything is
   a draft").
4. **MCP bridge** on `127.0.0.1` for agent CLIs: random per-session token,
   same jail as other providers.
5. **Provider endpoints**: pinned hosts; ambient credential env vars ignored.
6. **Release pipeline**: tag → GitHub Actions → GoReleaser; actions pinned by
   SHA, `contents: write` only in the release job, no long-lived secrets.

## Findings and status

Severity is for the Windows backend unless stated. "Fixed" means a code
change exists and compiles; none is proven on Windows until the CI gate runs.

| ID | Sev | Finding | Exploit | Status |
| --- | --- | --- | --- | --- |
| G1 | **High** | Workspace grant included `FILE_DELETE_CHILD`; a deny on `.git` does not stop the parent's right to rename/delete it | `ren .git x & mkdir .git` then write `.git\hooks\pre-commit`; code runs as the user on the next host `git` | Fixed in `sandbox_windows.go`: grant is `DELETE` only, and `.git` gets a protected ACL giving the sandbox read access only (deny entries are ignored for AppContainers, so they can't do it). Test `TestProtectedDirsCannotBeReplaced`. VERIFY |
| G3 | Medium | `CheckNetIsolation LoopbackExempt` exempts the whole AppContainer profile, so `network:false` commands can reach host localhost services (DB, Docker TCP API, dev servers) | `curl 127.0.0.1:2375` from a command, then mount host paths via Docker | **Open** (backend_dev: split profiles). Test `TestNoLoopbackWithoutNetwork`. macOS has the same reachability (accepted there, documented); Linux isolates |
| G4 | Medium | Owner-implicit `WRITE_DAC` could let a command change a protected path's ACL | `icacls .git /reset /T` | Mitigated: no grant includes `WRITE_DAC`, and every launch restores the protected ACLs; test `TestCannotChangeItsOwnACLs`. VERIFY |
| G5 | Medium | `ackn4th.exe __ackn4th-sandbox <spec>` is on PATH (README installs it in the user PATH, which is granted to commands); a spec chooses capabilities | confined process launches a child with another workspace's capability SID (`sha256(path)`, public) | Mitigated: launcher refuses inside an AppContainer, fail closed. Test `TestLauncherRefusesInsideSandbox`. VERIFY that the kernel also refuses nested capability requests |
| G6 | Medium | Confidentiality of home rests on Windows default-deny; any `ALL APPLICATION PACKAGES` ACE widens it silently | read `%USERPROFILE%` content | **Open**: add a fail-closed canary in `available()`. Test `TestUserFilesUnreadable` is the CI form |
| G10 | Medium (all OS) | Credential scan gave up silently after 20,000 entries | repo with many files sorted before `.env`/`*.pem` → commands could `type .env` and the output goes to the provider | Fixed: `secretFiles` returns an error past 250,000 entries; the command doesn't run. Test `TestSecretFilesFailsClosedWhenTruncated` |
| G11 | Medium (all OS) | Toolchain dirs readable by commands held credentials: `~/.cargo/credentials.toml`, `~/.gem/credentials`, `~/.m2/settings.xml`, `~/.gradle/gradle.properties`, shell history, Windows `AppData` browser/credential stores | agent runs `cat ~/.cargo/credentials.toml` | Fixed: added to the hidden list; on Windows hidden paths inside a tool-directory grant get a protected ACL with no sandbox access (fail closed). VERIFY on Windows |
| G12 | Low | Job object had no UI restrictions: clipboard, other windows' handles, desktop switching | read the clipboard (a copied password) from a command | Fixed: `JobObjectBasicUIRestrictions` in the launcher. VERIFY (AppContainers are already restricted for clipboard; this is defense in depth) |
| G13 | Low | Windows device names (`NUL`, `CON.txt`, `COM1`) were not rejected as file-tool paths | open a device through the file tools, hang or read from a device | Fixed in `internal/workspace` (`winDevice`), test `TestWinAlias` |
| G2 | Low-Med (Linux, Windows) | `.git`/`.env` that don't exist when the command starts aren't protected | in a repo-less workspace, `git init` + hooks; the later PR step runs hooks | Residual, documented. macOS protects by path, so it is not affected |
| G7 | Low | Windows grants are permanent and inheritable, on PATH entries too | a project directory on PATH stays readable to all tasks | Partially fixed: dirs containing the workspace/scratch/Keep are skipped. Residual: no `sandbox revoke`; document, add in 0.1.x |
| G8 | Low-Med | Junctions in the workspace pointing outside | file tool follows it; ACL propagation crosses it | Believed handled (Go's `os.Root` treats mount points as links; the sandbox has no access to the target). `TestJunctionOutOfWorkspace` checks both. VERIFY |
| G14 | Low | Hidden/secret scan skips `node_modules`, `vendor`, `.venv` | a real `.env` inside `vendor/` is readable by commands | Accepted: third-party trees; documented |
| G15 | Info | Network "off" on Windows means no network capabilities; named pipes and ALPC endpoints open to `ALL APPLICATION PACKAGES` remain reachable | only OS-provided endpoints | Accepted |

### Things reviewed and found sound

- **Provider sign-ins**: keys only in the OS keychain or a 0700 tmpfs, read
  with hidden input, never from flags or env; ambient credential env vars
  ignored; endpoints pinned. The Windows Credential Manager path is
  `go-keyring` → `wincred`.
- **Environment**: rebuilt from an allowlist on every platform; the launcher
  starts the confined process with only the standard handles inherited
  (`PROC_THREAD_ATTRIBUTE_HANDLE_LIST`).
- **Command line**: `ComposeCommandLine` / `EscapeArg` for argv; the cmd.exe
  special case passes the agent's own command string, which is the intended
  capability. npm `.cmd` shims are rewritten to `node script.js` because
  cmd.exe cannot carry newlines or quotes safely. `.ps1` and other batch
  files are refused.
- **Randomness**: session tokens and keys use `crypto/rand` (spot-checked
  in the MCP bridge; no homemade crypto found).
- **CI/release**: third-party actions pinned by SHA; `persist-credentials:
  false`; release job is the only one with `contents: write`.

### Not reviewed / needs a person

- Everything marked VERIFY needs a non-elevated Windows 10/11 run.
- **Dependencies**: no network here, so `govulncheck` was not run locally.
  CI runs `govulncheck@v1.9.0` and `staticcheck` on Linux; keep that job
  green and required. `go.sum` is committed.
- `internal/provider/*` (MCP bridge authentication and CLI argument
  construction) got a skim, not a line-by-line audit.

## Windows release gate (security view)

Windows is released only when all of the following hold on a real runner,
with no skipped tests:

1. `go test ./internal/sandbox/` including `sandbox_windows_security_test.go`
   passes on `windows-2025` (run as a normal user, not elevated).
2. G3 is resolved (profile split) or its trade-off is documented in the
   security README as the single intentional difference from Linux, and the
   user has accepted that.
3. G6 canary exists and fails closed, or the README states plainly that
   Windows confidentiality relies on default-deny with no self-check.
4. Docs match reality: README install section and the "localhost works" and
   "workspaces can't reach each other" statements in
   `internal/sandbox/README.md`.
5. arm64 only if tests ran on arm64 hardware.

Until then Windows users can `go install`; `run_command` and the CLI
providers refuse when `sandbox.Available()` fails, rather than running
unconfined.
