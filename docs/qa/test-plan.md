# QA test plan: how the CLI reaches a daemon

Scope: the `task.Backend` interface (`internal/task/backend.go`), the two
implementations behind it — the file store (`internal/task/task.go`) and the
gRPC controller/client (`internal/task/grpc.go`, `internal/task/ackn.proto`,
`internal/task/acknpb`) — the runner that writes through it
(`internal/task/runner.go`), and the CLI/dashboard commands that read or write
task state (`internal/cli/{tasks,control,dash,github,root}.go`,
`internal/dash`).

The design in one line: the daemon owns the data directory and the processes;
a client anywhere asks it over `ackn://host:port`, and the file store is still
what everything uses when nothing else is configured. `docs/grpc-backend.md`
is the reader-facing version of this.

## How to run

```sh
export GOTOOLCHAIN=auto
go test ./internal/task/ ./internal/cli/ ./internal/dash/ ./internal/workspace/ -count=1   # the suites
go test -race ./internal/task/ ./internal/cli/ ./internal/dash/ -count=1                   # concurrency
go test ./internal/task/ ./internal/cli/ -run '^TestQA' -count=1 -v                        # bug-report invariants
go test ./test/docs/ -count=1                                                              # links, anchors, env vars
```

No test needs a provider key, a terminal, or a network beyond loopback: the
dashboard and watch tests drive the real `app`/`dash.Options` wiring with output
captured in a buffer, and the controller tests host a real gRPC server on
`127.0.0.1:0`. A test that needs a controller picks its port by listening on
`:0`, closing, and retrying if `Serve` loses the race — never by hard-coding a
port, and never with a sleep in place of waiting for `ControllerAlive()`.

**A failing `TestQA*` test is a bug report, not a broken test.** Do not make one
green by loosening it: fix the production code, or argue the expectation is
wrong and change this plan in the same breath.

## Acceptance criteria → tests

| AC | What must hold | Test |
| --- | --- | --- |
| AC1 | One interface, two implementations: the file store and the controller answer the same `task.Backend` calls | `task.TestFileBackend`, `task.TestBothBackendsAreEndpoints`, `task.TestStoreEndpointIsTheDataDir` |
| AC2 | The default stays local: with no `ACKN4TH_BACKEND_URL` every command uses this machine's data dir | `task.TestOpenBackendFileByDefault`, `cli.TestDefaultBackendIsLocal` |
| AC3 | Endpoint parsing accepts `ackn://host:port` and a bare `host:port` (filling in the port), and refuses anything else with a message naming the flag | `task.TestGRPCEndpointParsing`, `task.TestOpenBackendSelectsByScheme`, `cli.TestAnUnsupportedBackendURLIsRefused` |
| AC4 | An endpoint makes the backend remote, so commands stop acting on local processes | `task.TestOpenBackendSelectsTheController`, `cli.TestControllerURLMakesTheBackendRemote`, `cli.TestControllerRequiresASharedBackend` |
| AC5 | A task id is one path segment on disk and one key over the wire; every call rejects empty, `.`, `..`, spaces, `:` and NUL | `task.TestStoreRefusesIDsThatAreNotOnePathSegment`, `task.TestNewIDsAreValid` |
| AC6 | A record crosses the wire without losing a field, and the lifecycle works through the client alone | `task.TestGRPCRecordRoundTripKeepsEveryField`, `task.TestGRPCTaskLifecycleOverTheWire` |
| AC7 | Events are append-only and replayable from a cursor, and a live view streams new ones | `task.TestGRPCEventsAndLivePreview`, `task.TestGRPCWatchReplaysThenStreams`, `task.TestLiveFeedShowsAFileBeingWritten` |
| AC8 | An unreachable or silent controller fails fast with a message a person can act on; nothing waits for ever | `task.TestGRPCDoesNotHangOnASilentServer`, `task.TestGRPCSendWithNoController`, `cli.TestRemoteStartWithoutAControllerFails` |
| AC9 | A client runs tasks under the **daemon's** settings: its workspace roots, providers and models, adopted over the interface | `task.TestGRPCConfigComesFromTheDaemon`, `cli.TestRemoteWorkspaceComesFromTheDaemon`, `cli.TestRemoteWorkspaceWithoutADefaultRootSaysSo` |
| AC10 | A start from a client is carried out by the daemon, which clones what the task needs before it spawns anything | `cli.TestRemoteStartGoesThroughTheController`, `cli.TestTheRecordCarriesTheRepository`, `cli.TestRepoPathIsWhereTheClientSaysTheTaskWillWork` |
| AC11 | A client never signals a pid it did not start; the controller stops a run only when the record's pid is the one holding that task's lock | `cli.TestRemoteStopDoesNotSignalLocally`, `cli.TestRemoteStopWithoutAControllerFails`, `task.TestStoreRunnerPIDNeedsBothTheLockAndThePid`, `task.TestGRPCStopsWithAWatchOpen` |
| AC12 | The daemon's run files reach a client as **text**: an allowlisted name, a validated id, a path asserted to be one file in the task's own directory, and no host path in any refusal | `task.TestStoreArtifactServesOnlyTheAllowlist`, `task.TestGRPCArtifactsComeFromTheDaemon`, `task.TestTheMissingArtifactTextIsSaidOnce`, `cli.TestTheSummaryComesFromTheController` |
| AC13 | The GitHub context travels with the record, so it lands where the runner will read it | `task.TestCreateWithBriefStoresTheGitHubContext`, `cli.TestTheGitHubContextTravelsWithTheRecord` |
| AC14 | A watch started during a controller restart waits for it, while a task the daemon says does not exist is reported at once | `cli.TestWatchWaitsForTheControllerItStartedUnder` |
| AC15 | The file backend stays first-class: with no endpoint, every command still works on this machine's data dir | `task.TestFileBackend` plus the whole `internal/cli` suite, which runs with no controller |
| AC16 | The docs say what the code does: every relative link and `#anchor` resolves, and every documented `ACKN4TH_*` variable exists in the code | `docs.TestRelativeLinksResolve`, `docs.TestDocumentedEnvVarsExist` |
| AC17 | Every state call in the dashboard goes through the backend it was given (`opt.state()`), and no test of that is allowed to rot: the guard reads the sources, so a receiver that no test happens to drive is still caught | `dash.TestTheDashReadsStateThroughItsBackend`, `dash.TestAnUnreachableBackendIsSaidNotShownAsNoTasks` |

### The state/artifact split, and where a direct `store` call is correct

`a.store` and `m.opt.Store` are **local artifacts**: run logs, clones,
snapshots, the run lock, `GitHubPath`, `SummaryPath`, `RunnerPID`, `Spawn`.
State — the record, the event log, the live preview, deletes, switches —
belongs to `a.backend()` / `opt.state()`, which is the controller when one is
configured. Four direct store calls are deliberate and must not be "fixed":

- `internal/cli/tasks.go` `artifact`/`summaryOf`: behind `a.remote()`, which is
  the local branch of the two.
- `internal/cli/tasks.go` `internalRunCmd`: the detached runner runs on the host
  that holds the clone, so it loads the record from that host on purpose.
- `internal/cli/dash.go` `firstRun`: nil-checked local store, and it lists
  through `a.backend()`.

Changing any of them to a backend call makes a remote task read the wrong
machine; the guard in `dash` would not catch a CLI-side slip, so read this
before touching `a.store`.

## Where the evidence for the findings lives

This branch was built against a review, and every finding that shaped it has a
test. The ones still worth reading as evidence (rather than as coverage) are kept
with the review notes, outside the repository: the shipped tests cited above are
what CI runs, and a path covered only by evidence is a gap to close with a test
here, not a pass. Nothing in this plan needs those files to run.

## What a passing gate looks like

1. `git apply` both patches on a fresh `git archive <base>` extraction that has
   been `git init`ed — `git apply` outside a repository is a silent no-op that
   exits 0, so assert the tracked file count moved.
2. No `.rej`, no `.orig`, and no patch line that carries a timestamp.
3. **The patched tree equals the tree that was verified**, file for file
   (`diff -r` with only the excluded paths). Two cuts of this patch shipped
   with a file that had moved on after the cut — `internal/dash/backend_test.go`
   twice — so "it applied cleanly" is not the check; identity is.
4. **The patch reverts nothing it did not mean to.** Every path the patch
   *deletes* must be a path the base tree no longer needs; a patch cut from a
   copy of the tree that predates a base commit silently undoes that commit.
   The one that shipped deleted both README screenshots from `ddc5371`.
   `git diff --stat base..<branch>` should show no deletion of a file that
   exists in `base` unless the change is about deleting it.
5. `go build ./internal/... ./cmd/...`, `go vet`, `gofmt -l` all clean.
6. `GOOS=windows` and `GOOS=plan9 GOARCH=amd64` builds of `internal/task`.
7. The nine packages above plus `./test/docs/` green, then `-race`.
8. Any extra review tests you add: no unexpected failure. A failure here is a
   finding with a test, not a flake.
9. The tree leaves nothing behind: no controller process, no temp data dir.
