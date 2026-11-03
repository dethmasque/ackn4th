#!/usr/bin/env bash
# Release gate for the Windows sandbox (docs/adr/0001-windows-release-gate.md).
# Runs the in-sandbox security tests and fails if any is skipped or missing:
# a skipped test is not evidence. Run on a real, non-elevated Windows runner.
set -euo pipefail

pattern='TestProtectedDirs|TestCannotChange|TestLauncher|TestUserFiles|TestReadOnly|TestJunction|TestNoLoopback|TestConfinement|TestOtherWorkspace|TestKeep'
out=$(mktemp)
trap 'rm -f "$out"' EXIT

go test -count=1 -v -run "$pattern" ./internal/sandbox/ 2>&1 | tee "$out"

if grep -E -- '--- SKIP' "$out"; then
  echo "gate failed: sandbox security tests were skipped" >&2
  exit 1
fi
for t in TestProtectedDirsCannotBeReplaced TestCannotChangeItsOwnACLs TestLauncherRefusesInsideSandbox \
  TestUserFilesUnreadable TestReadOnlyWorkspace TestJunctionOutOfWorkspace TestNoLoopbackWithoutNetwork \
  TestConfinement TestOtherWorkspaceUnreachable TestKeepWritable; do
  if ! grep -qE -- "^--- PASS: $t " "$out"; then
    echo "gate failed: $t did not pass (missing, failed or skipped)" >&2
    exit 1
  fi
done
passed=$(grep -cE -- '^--- PASS' "$out" || true)
if [ "$passed" -lt 1 ]; then
  echo "gate failed: no sandbox security tests ran" >&2
  exit 1
fi
echo "gate ok: $passed top-level tests passed, none skipped"
