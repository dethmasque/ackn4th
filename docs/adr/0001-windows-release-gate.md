# 0001. Windows ships only when it meets the sandbox contract

Status: proposed (0.1.0 pre-release)

## Context

The Windows AppContainer sandbox could leak secrets or expose sensitive
files, so 0.1.0 must not ship an insecure Windows build. We want Windows
released alongside Linux/macOS with mirrored support. A source review
([sandbox-parity](../architecture/sandbox-parity.md)) found the model is
sound in principle (AppContainer default-deny, per-path capabilities, job
objects) but has gaps against the cross-platform contract: `.git` rename
bypass (G1), profile-wide loopback exemption (G3), unproven DACL/launcher
tamper resistance (G4, G5), no fail-closed canary (G6), junction handling
(G8), and permanent over-broad grants (G7). Nothing has been executed on
Windows by this review.

## Decision

1. Windows is **not released** until every item in the parity doc's
   verification gate passes on a real Windows CI runner. Until then
   goreleaser builds linux and darwin only (packaging/goreleaser.yaml),
   and README/justfile/packaging docs stop advertising Windows.
2. Release gate is mechanical: a `windows-sandbox` CI job running the
   named tests (not skipped), required before the Windows goreleaser
   build is enabled. Cross-compilation alone (linux runner) is a build
   check, not a security check.
3. Fixes required before enabling: G1, G3 (split profiles, or explicitly
   documented opt-in), G6 (canary, fail closed), plus passing VERIFY
   tests G4, G5, G8, and G11/G12 (hide cargo credentials, job UI limits). G2, G7 get documented residuals and a
   `sandbox revoke` command can follow in 0.1.x.
4. Mirrored support means the same invariants (C1 to C10), not identical
   mechanisms. Differences that remain (e.g. tests needing localhost) are
   listed in the security README.
5. arm64: ship only if tests ran on arm64 hardware; otherwise amd64 only.
6. If any gate item can't be met, Windows ships later (0.1.x) or runs
   `run_command`-less: file tools plus API providers only, with CLI
   providers (Codex/Cursor/Copilot) refusing, since they depend on the
   sandbox most.

## Consequences

- 0.1.0 can tag now for Linux/macOS with no Windows artifacts.
- Windows users can still `go install`; the build must refuse
  `run_command` and CLI providers when `available()` fails, which it
  already does (C9). Docs must say so.
- Cost: a Windows CI runner job, and two AppContainer profiles (more
  admin steps are not added: only the CLI profile needs the exemption).

## Alternatives rejected

- Ship Windows as "experimental" with a warning: a secrets leak is not an
  acceptable experiment for a tool that sends repo contents to providers.
- Rely on cross-compile + unit tests on Linux: cannot exercise ACLs.
- Use WSL/containers only on Windows: viable fallback (the container is
  already supported), but loses native support; keep as documented
  alternative meanwhile.
- Restricted tokens / Windows Sandbox / Hyper-V: stronger or heavier, not
  available on all editions; revisit if AppContainer gaps prove structural.
