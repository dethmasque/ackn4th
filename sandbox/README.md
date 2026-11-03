# Developing ackn4th

[← README](../README.md)

Everything needed to build, try and test ackn4th lives in the repo. Nothing
touches a normal install: the dev sandbox here keeps its own config, logins
and data. You need Go 1.27.3+ and [just](https://github.com/casey/just).
`just build` builds for the machine you're on, and that binary works as an
install by itself. The recipes run in bash (on Windows, Git Bash's). CI
(`.github/workflows/ci.yml`) runs the checks on macOS, Linux and Windows.

```sh
just build       # → bin/ackn4th
just check       # gofmt, go vet, race-enabled tests
just lint        # staticcheck and govulncheck, at pinned versions
just test-linux  # the tests on Linux, in a container, under the container's restrictions
just e2e         # the real binary end to end, with offline mock agents (no network)
just sandbox     # build, then set up sandbox/.ackn4th-home and sandbox/play
just demo        # watch three mock agents work on a fixture project in parallel
just play todo-api start-task --provider mock --roles backend_dev,qa_engineer "Add DELETE /todos/{id}"
just sandbox-reset   # start the sandbox over
just container login # the container, for the directory you run it from
```

**Every dependency is pinned** to an exact version: Go modules in `go.mod`,
tools run with `go run …@vX.Y.Z`, GitHub Actions by commit SHA, container
images by digest, Debian packages to a snapshot, npm packages and the
voice's downloads by version and SHA-256. Nothing uses `@latest`. Bump them
deliberately, together, and run everything above.

## The dev sandbox

- **`ACKN4TH_HOME=sandbox/.ackn4th-home`**: the `just` recipes set this,
  which points ackn4th's config, OAuth logins, keychain session and task
  data into `sandbox/.ackn4th-home/`. Agents can never read that directory,
  even when they work on this repo. (The voice still downloads to the OS's
  cache folder, shared with a normal install.)
- **`fixtures/`**: small committed projects. `just sandbox` copies each one
  into `play/<name>/`, which is trusted, so `.` works as the workspace
  there, as it would in a real project. Agents can change the copies
  freely; `just sandbox-reset` restores them.
- **The `mock` provider**: offline agents that follow a fixed script
  (explore, write a file in their role's paths, update the project notes,
  message teammates, revise, declare done). Use `--provider mock` or
  `--agent role,provider=mock`. It exists only in a sandbox and needs no
  login. `ACKN4TH_MOCK_DELAY=0` makes it instant.

Real agents work here too: `just play todo-api login` signs in with the
sandbox's own session and OAuth grants (agent CLIs use their usual logins),
then drop `--provider mock`.

## Using ackn4th on ackn4th

From the repo root (the first run asks to trust it):

```sh
just build
./bin/ackn4th notes --edit        # seed: "just check builds, vets and tests; just e2e runs the binary"
./bin/ackn4th start-task --parallel --roles backend_dev,qa_engineer,code_reviewer \
  "Implement <feature> in internal/, with tests"
```

Agents run `just check` themselves through the sandboxed `run_command`.
`just e2e` builds and runs the binary, so run it between rounds and pass
what you find back with `resume-task --note`.

## Docs

Each topic's README sits next to its code; the root
[README](../README.md#documentation) links them all. A change to how
something works updates the README beside it.

Packaging and releasing: [packaging/README.md](../packaging/README.md).
