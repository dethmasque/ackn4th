version := `git describe --tags --always --dirty 2>/dev/null || echo dev`
ldflags := "-s -w -X github.com/dethmasque/ackn4th/internal/cli.Version=" + version

# List recipes
default:
    #!/usr/bin/env bash
    # just prints descriptions in dark blue, which is hard to read on a dark
    # background; recolor them cyan, which reads on dark and light alike.
    if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
        esc=$(printf '\033')
        "{{just_executable()}}" --justfile "{{justfile()}}" --list --color always | sed "s/${esc}\[34m/${esc}[36m/g"
    else
        "{{just_executable()}}" --justfile "{{justfile()}}" --list --color never
    fi

# Build bin/ackn4th
build:
    go build -ldflags "{{ldflags}}" -o bin/ackn4th ./cmd/ackn4th

# Install ackn4th into Go's bin directory ($GOBIN, else $GOPATH/bin)
install:
    go install -ldflags "{{ldflags}}" ./cmd/ackn4th

# Run the tests
test:
    go test ./...

# gofmt, go vet, and tests with the race detector
check:
    @test -z "$(gofmt -l cmd internal roles sandbox/fixtures)" || (gofmt -l cmd internal roles sandbox/fixtures && exit 1)
    go vet ./...
    go test -race ./...

# Build and run ackn4th with arguments, e.g. `just run roles`
[positional-arguments]
run *args: build
    ./bin/ackn4th "$@"

# ── sandbox: try ackn4th from a clone without touching a normal install ──
# Everything lives in sandbox/: config, logins and task data in .ackn4th-home/,
# playground copies of the fixtures in play/. See sandbox/README.md.

sandbox_home := justfile_directory() / "sandbox/.ackn4th-home"
sandbox_play := justfile_directory() / "sandbox/play"

# Build, then set up the sandbox (config, playground projects)
sandbox: build
    #!/usr/bin/env bash
    set -euo pipefail
    mkdir -p "{{sandbox_home}}/config" "{{sandbox_play}}"
    chmod 700 "{{sandbox_home}}"
    if [ ! -f "{{sandbox_home}}/config/config.yaml" ]; then
        printf 'workspace_roots: ["%s"]\n' "{{sandbox_play}}" > "{{sandbox_home}}/config/config.yaml"
        chmod 600 "{{sandbox_home}}/config/config.yaml"
    fi
    for f in sandbox/fixtures/*/; do
        name=$(basename "$f")
        [ -d "{{sandbox_play}}/$name" ] || cp -R "$f" "{{sandbox_play}}/$name"
    done
    echo "sandbox ready: $(ls "{{sandbox_play}}" | tr '\n' ' ')"
    echo "try: just demo   ·   just play todo-api --help"

# Delete the sandbox's data and playground copies, then set it up again
sandbox-reset:
    rm -rf "{{sandbox_home}}" "{{sandbox_play}}"
    "{{just_executable()}}" --justfile "{{justfile()}}" sandbox

# Run ackn4th inside a playground project, e.g. `just play todo-api list-tasks`
[positional-arguments]
play project *args: sandbox
    cd "{{sandbox_play}}/{{project}}" && shift && ACKN4TH_HOME="{{sandbox_home}}" "{{justfile_directory()}}/bin/ackn4th" "$@"

# Run ackn4th in the sandbox from the repo root, e.g. `just dev notes sandbox/play/todo-api`
[positional-arguments]
dev *args: sandbox
    ACKN4TH_HOME="{{sandbox_home}}" ./bin/ackn4th "$@"

# Watch three mock agents work on todo-api in parallel (offline, no login)
demo: sandbox
    cd "{{sandbox_play}}/todo-api" && ACKN4TH_HOME="{{sandbox_home}}" "{{justfile_directory()}}/bin/ackn4th" \
        start-task --parallel --provider mock --rounds 3 \
        --roles backend_dev,qa_engineer,tech_writer \
        "Add DELETE /todos/{id} with tests, and document it"

# End-to-end smoke test: real binary, mock agents, throwaway sandbox (CI-safe)
e2e: build
    #!/usr/bin/env bash
    set -euo pipefail
    tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
    export ACKN4TH_HOME="$tmp/home" ACKN4TH_MOCK_DELAY=0 ACKN4TH_NO_FX=1 NO_COLOR=1
    bin=$PWD/bin/ackn4th
    cp -R sandbox/fixtures/todo-api "$tmp/proj" && cd "$tmp/proj"
    "$bin" trust . > /dev/null
    check() { grep -q "$2" <<<"$1" || { echo "e2e: expected '$2' in output of: $3"; echo "$1"; exit 1; }; }
    out=$("$bin" start-task --provider mock --roles backend_dev,code_reviewer "turns")
    check "$out" "every agent declared the task done" "sequential task"
    check "$out" "FOR YOU · 2 OPEN" "action items for the user"
    check "$out" "summary.md" "end-of-task summary"
    if [ "$(uname)" = Darwin ] || command -v bwrap >/dev/null; then check "$out" '\$ ls' "sandboxed run_command"; fi
    out=$("$bin" start-task --parallel --provider mock --roles backend_dev,qa_engineer,tech_writer "parallel")
    check "$out" "every agent declared the task done" "parallel task"
    check "$out" "message_teammate" "parallel task"
    out=$("$bin" run-agent --role tech_writer --provider mock "solo")
    check "$out" "every agent declared the task done" "solo task"
    out=$("$bin" run-agent --role tech_writer --provider mock --name "Docs pass" "solo, named")
    check "$out" "DOCS PASS" "a named task's header"
    out=$("$bin" list-tasks); check "$out" "Docs pass" "names in list-tasks"
    "$bin" rename-task "docs PASS" "Docs, again" > /dev/null
    out=$("$bin" show-task "Docs, again"); check "$out" "every agent declared the task done" "finding a task by name"
    out=$("$bin" notes); check "$out" "Mock qa_engineer" "notes"
    log=$(cat "$ACKN4TH_HOME"/data/tasks/*/events.jsonl)
    check "$log" '"kind":"reasoning"' "reasoning kept in the log"
    check "$log" '"detail":"command: ' "tool call arguments kept in the log"
    out=$("$bin" list-tasks); check "$out" "completed" "list-tasks"
    out=$("$bin" dash </dev/null 2>&1 || true); check "$out" "interactive terminal" "dash refuses non-interactive use"
    out=$("$bin" </dev/null 2>&1); check "$out" "start-task" "bare ackn4th prints help off a terminal"
    [ -f mock/mock-backend_dev.md ] || { echo "e2e: agent file missing"; exit 1; }
    echo "e2e: ok"

# ── container: ackn4th in Docker, on macOS or Linux ──

# Build the ackn4th image; AGENT_CLIS="@openai/codex@0.166.0" picks the agent CLIs baked in
container-build:
    container/ackn4th-container --build

# Run ackn4th in a container for the directory you call it from, e.g. `just container login`
[positional-arguments]
container *args:
    cd "{{invocation_directory()}}" && "{{justfile_directory()}}/container/ackn4th-container" "$@"

# Run the tests on Linux in a container, under the ackn4th container's restrictions
test-linux:
    printf '%s\n' \
        'FROM golang:1.27.3-bookworm@sha256:d9b6a1beb9474adc2a1ce842573639b44b2a0dd49b6669f51d3821baf9ec37c9' \
        'RUN sed -i -e "s|http://deb.debian.org/debian-security|http://snapshot.debian.org/archive/debian-security/20261101T000000Z|" -e "s|http://deb.debian.org/debian|http://snapshot.debian.org/archive/debian/20261101T000000Z|" /etc/apt/sources.list.d/debian.sources && apt-get -o Acquire::Check-Valid-Until=false update -qq && apt-get install -y -qq bubblewrap=0.8.0-2+deb12u1 >/dev/null && useradd -m t' \
        | docker build -q -t ackn4th-linuxtest - >/dev/null
    docker run --rm --user t --security-opt seccomp=unconfined --security-opt apparmor=unconfined \
        --security-opt systempaths=unconfined --security-opt no-new-privileges --cap-drop ALL \
        -v "{{justfile_directory()}}:/src:ro" ackn4th-linuxtest sh -c 'cp -r /src /tmp/src && cd /tmp/src && go test ./...'

# Static analysis, known-vulnerability scan and workflow lint, at pinned versions
lint:
    go run honnef.co/go/tools/cmd/staticcheck@v0.8.2 ./...
    go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
    go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12

# Pinned so local and CI packaging match. Set GORELEASER=goreleaser to use
# an installed copy instead of compiling it with go run.
goreleaser := env("GORELEASER", "go run github.com/goreleaser/goreleaser/v2@v2.19.0")

# Cross-compile every release target into bin/<os>_<arch>/ (no packaging)
cross:
    #!/usr/bin/env bash
    set -euo pipefail
    for target in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64; do
        os=${target%/*}; arch=${target#*/}; ext=""; [ "$os" = windows ] && ext=.exe
        echo "→ $os/$arch"
        CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags "{{ldflags}}" -o "bin/${os}_${arch}/ackn4th$ext" ./cmd/ackn4th
    done

# Validate packaging/goreleaser.yaml
release-check:
    {{goreleaser}} check --config packaging/goreleaser.yaml

# Build all archives and Linux packages into dist/ without publishing
snapshot:
    {{goreleaser}} release --config packaging/goreleaser.yaml --snapshot --clean

# Remove build output (the sandbox stays; see sandbox-reset)
clean:
    rm -rf bin dist
