# Packaging and releasing

[← README](../README.md)

Releases are built and published by **GitHub Actions**: pushing a version
tag runs [`.github/workflows/release.yml`](../.github/workflows/release.yml),
which tests on every OS and then runs
[GoReleaser](https://goreleaser.com) v2.19.0 with
[`goreleaser.yaml`](goreleaser.yaml). Everything lands on the tag's
[GitHub release](https://github.com/dethmasque/ackn4th/releases). The
workflow's own `GITHUB_TOKEN` is all it needs: there are no secrets to set
up.

`goreleaser.yaml` isn't at the repository root, where GoReleaser looks by
default, so every command passes `--config packaging/goreleaser.yaml`; run
them from the root. Locally, the `justfile` runs the same pinned GoReleaser
through `go run` (set `GORELEASER=goreleaser` to use an installed copy).

## What a release contains

| File | For |
| --- | --- |
| `ackn4th_darwin_amd64.tar.gz`, `ackn4th_darwin_arm64.tar.gz` | macOS, Intel and Apple silicon |
| `ackn4th_linux_amd64.tar.gz`, `ackn4th_linux_arm64.tar.gz` | any Linux |
| `ackn4th_windows_amd64.zip`, `ackn4th_windows_arm64.zip` | Windows |
| `ackn4th_linux_<arch>.deb` / `.rpm` / `.apk` | Debian/Ubuntu, Fedora/RHEL, Alpine (installs to `/usr/bin`, recommends `gnome-keyring`) |
| `checksums.txt` | SHA-256 of every file |

Archives hold the binary, `LICENSE`, `README.md` and the built-in roles.
File names carry no version, so
`https://github.com/dethmasque/ackn4th/releases/latest/download/<file>`
always fetches the newest release; the root README's install commands use
that. Binaries are static (`CGO_ENABLED=0`), built with `-trimpath` and the
commit's timestamp, so a tag builds the same bytes anywhere. They aren't
code-signed: the README says how to run them on macOS and Windows. The
version is stamped into `ackn4th version`.

`go install github.com/dethmasque/ackn4th/cmd/ackn4th@<tag>` works too, as
soon as the tag is public, through the Go module proxy.

## Recipes

| Recipe | Does |
| --- | --- |
| `just cross` | Static binaries for every target in `bin/<os>_<arch>/` (no packaging) |
| `just release-check` | Validates `goreleaser.yaml` |
| `just snapshot` | Everything a release would contain, in `dist/`, without publishing |
| `just lint` | staticcheck, govulncheck and actionlint (which checks the workflows) |
| `just container-build` | The container image (not part of a release) |

## Before the first release

Once, on GitHub:

1. **Make the repository public** (Settings → General → Danger Zone →
   Change visibility). Release downloads and `go install` need it: a
   private repository's releases are visible only to people with access.
2. **Let workflows write releases.** Settings → Actions → General:
   *Actions permissions* allows all actions (or at least `actions/*`), and
   nothing under *Workflow permissions* needs changing: the release job asks
   for `contents: write` itself. If your account or organization caps the
   workflow token at read-only, the release job fails with
   `403 Resource not accessible by integration`; allow read and write there.
3. **Optional: protect version tags** (Settings → Rules → Rulesets → New tag
   ruleset, target `v*`, restrict deletions and updates), so a published
   version can't be moved or deleted by accident.

## Releasing, step by step

Versions follow [semver](https://semver.org): `vMAJOR.MINOR.PATCH`. Tags
with a suffix (`v0.2.0-rc.1`) are published as pre-releases. The examples
release `v0.1.0`; substitute yours.

**1. Start from a clean, current `main`.**

```sh
git switch main
git pull --ff-only
git status --short          # must print nothing
```

**2. Run every check.** Docker must be running for `test-linux`.

```sh
just check                  # gofmt, vet, race tests
just lint                   # staticcheck, govulncheck, actionlint
just e2e                    # the binary end to end
just test-linux             # Linux, under the container's restrictions
just release-check          # the GoReleaser config
```

CI must be green on the commit too:

```sh
gh run list --branch main --limit 3
```

**3. Try the packaging.**

```sh
just snapshot
ls dist/
tar -tzf dist/ackn4th_linux_amd64.tar.gz      # the binary, LICENSE, README.md, roles/
./dist/ackn4th-macos_darwin_$(go env GOARCH)*/ackn4th version   # on a Mac
```

**4. Name the version in the README.** The `go install` line in the root
`README.md` names the release; the release job refuses to publish until it
matches the tag.

```sh
grep -n 'cmd/ackn4th@' README.md
sed -i.bak 's|cmd/ackn4th@v[0-9.]*|cmd/ackn4th@v0.1.0|' README.md && rm README.md.bak
git commit -am "release: v0.1.0"
git push origin main
```

**5. Tag it and push the tag.** This starts the release.

```sh
git tag -a v0.1.0 -m "v0.1.0"
git push origin v0.1.0
gh run watch "$(gh run list --workflow release.yml --limit 1 --json databaseId --jq '.[0].databaseId')"
```

The workflow tests on Linux, macOS and Windows, then builds everything,
writes a changelog (commits since the previous tag, minus `docs:`, `test:`
and `chore:`), and publishes the release. It takes about ten minutes.

**6. Check it from the outside.**

```sh
gh release view v0.1.0
gh release download v0.1.0 --pattern checksums.txt --pattern 'ackn4th_linux_amd64.tar.gz' --dir /tmp/ackn4th-v0.1.0
(cd /tmp/ackn4th-v0.1.0 && shasum -a 256 --ignore-missing -c checksums.txt)
GOPROXY=https://proxy.golang.org go install github.com/dethmasque/ackn4th/cmd/ackn4th@v0.1.0
```

Then follow the root README's install commands on each OS you have.

## If a release goes wrong

- **The workflow failed before publishing** (tests, the README check, a
  build error): fix it on `main`, then move the tag to the fixed commit and
  push it again. Nothing was published, so nobody has the old tag yet:

  ```sh
  git tag -d v0.1.0 && git push origin :refs/tags/v0.1.0
  # commit the fix, then step 5 again
  ```

- **A bad release went out:** don't move its tag (the Go module proxy has
  already cached it). Delete the release if it is unusable, and release the
  next patch version:

  ```sh
  gh release delete v0.1.0 --yes     # keeps the tag
  # fix, then release v0.1.1 from step 1
  ```

- **Re-running:** a failed release job can be re-run from the Actions tab
  once the cause is fixed outside the repository (e.g. the permissions
  above).

## Pinned versions

GoReleaser, staticcheck, govulncheck and actionlint are pinned in the
`justfile` and the workflows, the GitHub Actions and runner images in
`.github/workflows/`, and the container's dependencies in
[`container/Dockerfile`](../container/Dockerfile). Bump them deliberately
and run steps 2 and 3.
