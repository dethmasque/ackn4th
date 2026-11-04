# Sandbox parity: macOS, Linux, Windows

Status: review of `internal/sandbox` for the 0.1.0 Windows decision.
Companion to [ADR 0001](../adr/0001-windows-release-gate.md).
Reviewed from source only (no Windows host available to the architect):
every "VERIFY" item must be proven by a test that runs **inside the
sandbox on a real Windows runner** before Windows is released.

## Contract (what every backend must guarantee)

| # | Invariant | Mac (Seatbelt) | Linux (bwrap) | Windows (AppContainer) |
|---|-----------|----------------|---------------|------------------------|
| C1 | Writes only in workspace + scratch (+Keep) | deny-default profile | ro root bind, rw binds | default-deny + allow ACE per capability |
| C2 | `.git`, `.ackn4th` immutable, **including rename/replace of the dir itself** | `subpath` deny, also when absent | ro bind mount (EBUSY on rename) | protected ACL on the existing dir, sandbox read-only, re-checked every launch. **G2** |
| C3 | Home secrets unreadable | deny rules | not mounted | default deny (AC has no user ACL access). Needs canary, **G6** |
| C4 | Workspace credential files unreadable | deny rules | masked | protected ACL with no AppContainer access, on files existing at launch |
| C5 | No network unless `commands.network` | deny network | `--unshare-net` | no `internetClient` caps, **but see G3** |
| C6 | Host loopback services unreachable when network off | reachable (accepted, documented) | **unreachable** (own netns) | **reachable once loopback exempt, G3** |
| C7 | Env rebuilt from allowlist | same code | same code | same code |
| C8 | Whole process tree killed on timeout/exit | pgroup | pgroup / die-with-parent | job object KILL_ON_JOB_CLOSE |
| C9 | No sandbox => `run_command` refused, never unconfined | yes | yes | yes |
| C10 | Sandboxed code cannot weaken its own confinement | no exec outside | namespaces | **unproven, G4, G5** |

## Status (end of round 4; see also ../security/threat-model.md)

| Finding | State |
|---------|-------|
| G1 rename/replace of `.git` | fixed in code, unrun: grants are `DELETE` only (no `FILE_DELETE_CHILD`), and `.git` no longer inherits the grant at all (G15). Test `TestProtectedDirsCannotBeReplaced` |
| G2 paths created after launch | accepted residual, document |
| G3 profile-wide loopback exemption | open, needs owner decision: split profiles (recommended) vs documented opt-in |
| G4 owner rights / DACL tampering | fixed in code, unrun: no grant includes WRITE_DAC, and every launch restores the protected ACLs; test `TestCannotChangeItsOwnACLs` |
| G5 nested launcher | mitigated (launcher refuses inside an AppContainer); test written, unrun |
| G6 canary self-test | open |
| G7 broad/permanent grants | partly mitigated (tool dirs no longer include ancestors of workspace/scratch/Keep); revoke command deferred |
| G8 junctions | test written, unrun |
| G10 (new) secret scan truncation | fixed: `secretFiles` fails closed past 250k entries |
| G11 (new) `~/.cargo`, `.pyenv`, `.nvm` granted whole | fixed in code, unrun: credentials added to the hidden list; hidden paths inside a tool-dir grant get a protected ACL with no AppContainer access |
| G15 (round 5) deny ACEs ignored | CI showed every deny entry for a capability SID had no effect: the AppContainer access check ignores them. Fixed in code, unrun: protected paths (`.git`, `.ackn4th`, credential and hidden files, fixed CLI dirs) get a protected DACL with AppContainer access left out (`restrictDACL`, unit-tested on every OS) |
| G12 (new) job object has no UI limits | fixed in code, unrun: `JobObjectBasicUIRestrictions` set in the launcher |
| G13 (new) Windows device names as file-tool paths | fixed: `winDevice` in `internal/workspace`, unit-tested |

Nothing here has run on Windows. "Written" means the test compiles, not that it passes.

## Findings (Windows), most severe first

**G1. `.git` / `.ackn4th` can be renamed away and replaced (breaks C2).**
`wrap` grants the workspace `FILE_DELETE_CHILD` (`fileDelete`) and puts a
deny-write ACE on `.git`. On NTFS, `DELETE` on an object is *not* required
if the parent grants `FILE_DELETE_CHILD`; a deny ACE on the child does not
override that. So `ren .git .git.old && mkdir .git` (then plant
`.git\hooks\pre-commit`) is expected to work, giving code execution as the
user on the next `git` command: the exact risk the README says is blocked.
Same for `.ackn4th`. Fix options (backend_dev picks, security_engineer
reviews):
  1. Do not grant `FILE_DELETE_CHILD` on the workspace root; grant it per
     top-level entry except the protected names (inheritable ACE on each
     child), or
  2. Deny-delete on the workspace root can't be limited to names, and
     placeholder directories don't help; prefer option 1.
Either way add a VERIFY test (below).

**G2. Protected paths that do not exist at launch are unprotected (C2, C4).**
Seatbelt and bwrap protect by *path*; Windows ACEs protect existing
objects only. A command that runs `git init` or creates `.env`-style files
is not confined. Fix: ackn4th creates `.git`-protection by creating the
directory entry is not an option (`.git` must not appear in non-repos).
Accept for `.env`/keys (agent-authored, not a leak). For `.git` the
residual is that a command can `git init` and plant hooks in a repo-less
workspace; re-apply ACEs before every command (already done by `wrap`)
and document it. Residual risk: medium-low.

**G3. Loopback exemption is profile-wide (C5/C6).**
`CheckNetIsolation LoopbackExempt -a -p=<SID>` exempts the AppContainer
*profile* `ackn4th.sandbox`, not a capability. After the admin step needed
for Codex/Cursor/Copilot, every `run_command` (even `network: false`) can
reach any service on the user's localhost (databases, Docker TCP API,
Jupyter, dev servers). Linux isolates loopback; macOS does not (accepted).
Fix: two AppContainer profiles: `ackn4th.sandbox` (never exempted, used by
`run_command`) and `ackn4th.cli` (exempted, used only for agent CLIs, and
only with `Network: true`). `Loopback()` then reports on the CLI profile.
Trade-off: tests that start a local server fail in `run_command` on
Windows until a user opts in. Document it as the one intentional
difference (or make it a documented opt-in `commands.loopback`).

**G4. Owner implicit rights (VERIFY).** Files and dirs are owned by the
user. If the AppContainer access check grants the owner's implicit
`WRITE_DAC`/`READ_CONTROL`, a command could `icacls`-remove the deny ACEs
on `.git`/`.env`. Test: from inside the sandbox run
`icacls .env /remove:d <cap>` and `icacls .git /grant`. Mitigation if it
succeeds: add an `OWNER RIGHTS` (S-1-3-4) deny of WRITE_DAC|WRITE_OWNER on
protected objects (and on the workspace root).

**G5. Nested launch (VERIFY).** From inside the sandbox, try to start a
child with `PROC_THREAD_ATTRIBUTE_SECURITY_CAPABILITIES` carrying another
workspace's capability SID (they are `sha256(path)`-derived, so public),
via the readable `ackn4th.exe __ackn4th-sandbox <b64>` launcher or custom
code. Expect failure. Also try breakaway from the job. If the launcher is
usable, treat it as a finding and gate it (e.g. require a per-process
secret in the env the sandbox never sees).

**G6. No fail-closed self-test of "AC cannot read user files" (C3).**
Confidentiality of `~/.ssh` etc. rests entirely on default-deny, but any
ACE for `ALL APPLICATION PACKAGES` (installers, GPO, AV products,
`C:\Users\Public`-style shares) widens it, and there is no allowlist like
macOS/Linux have. Fix: on first use, `available()` runs a canary: create
a file in a ackn4th-private dir under the user's profile, run a probe
inside the sandbox that tries to read it, list `%USERPROFILE%` and
`%APPDATA%`, and refuse (ErrUnavailable) if any succeed. Re-run when the
binary version changes.

**G7. Persistent, over-broad grants (hygiene).**
- Grants are never removed and are inheritable on whole trees. Tool
  directories are granted read to one global capability (`ackn4th.tools`)
  used by every task; a PATH entry inside the home (e.g. a project dir,
  `~\go\bin`) exposes its whole subtree forever. Restrict tool dirs to
  known toolchain roots plus the single program directory, never recurse
  into dirs that contain a `.git`, and never grant PATH entries under the
  user's documents/desktop/downloads.
- Nested workspaces: if workspace B is under workspace A, A's inheritable
  ACE reaches B, contradicting "can't reach another workspace". Reject
  nested trusted workspaces on Windows, or document.
- Provide `ackn4th sandbox revoke` (and run it on uninstall docs) that
  removes every `ackn4th.*` ACE and low-integrity label it added. Without
  it, the user's folders carry ACEs and labels with no way back.
- Low-integrity label on the workspace is permanent; note that other
  low-IL processes (browser renderers) still need the DACL, so this is
  hygiene, not a hole.

**G8. Reparse points (VERIFY).** The sandbox can create directory
junctions in the workspace (`mklink /J`). Host-side file tools use
`os.Root`; confirm Go's Root rejects junction/mount-point escapes (not
only symlinks) and that `secretFiles`/`filepath.WalkDir` do not follow
junctions during ACE application (a junction to `C:\Users\me\.ssh`
reached while applying an inheritable ACE would be catastrophic:
SetNamedSecurityInfo propagation must not traverse into it, and
`ensureACE` on a symlink path must use the resolved target only for
intended paths). Test both.

**G9. Docs overstate.** README says "network off ... localhost still works"
and "a command in one workspace can't reach another"; both need the
qualifications above until fixed.

## Verification gate (Windows runner, real, not cross-compiled)

`sandbox_windows_test.go` already exists; the release gate requires these
named tests to exist and pass on `windows-2025` (amd64) and, if a runner
is available, `windows-11-arm`; arm64 otherwise ships as "untested" or not at all:

1. read of `%USERPROFILE%\.ssh\canary`, `%APPDATA%`, another workspace,
   and another task's scratch: all denied;
2. rename/delete/replace of `.git` and `.ackn4th`: denied (G1);
3. DACL tampering on protected paths: denied (G4);
4. nested capability grant / launcher abuse: denied (G5);
5. junction to a secret dir: not followable by sandbox nor host tools (G8);
6. `network:false`: no external connect **and no loopback connect to a
   host listener** (G3);
7. timeout kills grandchildren; parent death kills the job;
8. ReadOnly role: workspace write denied;
9. canary self-test refuses to run when a deliberately widened ACL exists (G6).

Tests need the process to run as a normal user (not elevated, no
`SeBackupPrivilege` use) to be meaningful; assert that in the test.
