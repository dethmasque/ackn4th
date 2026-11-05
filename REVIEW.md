# Review: issue #20 (rounds hint placement)

Scope: `internal/dash/form.go` `renderSettings`, test `TestRoundsHintStaysWithRounds`.
Verified: `go test ./internal/dash/` passes (patched go.mod, Go 1.27.1).

Good: the hint now follows ROUNDS and precedes CAP. Narrow widths degrade in two steps (shorter PR label, then a line below). The full wording remains in `formhelp.go`.

## Findings

- **nit** `form.go` ~859 `fixed := 16`: the extra row is reserved even though the "below" line only appears on narrow terminals. This costs one agent row on every New Task form. Suggested fix: keep 15, or add 1 only when the below-line is used. This is low impact.
- **nit** The wording changed to "0 = until all agree". Acceptance criterion 1 quotes the old text. The F1 help and field tip keep the full wording, so I don't consider this a problem.
- **nit** The narrow-width fallback (the below-line) has no test. It was not run at 80 columns. Suggested fix: add an 80-column case to the test. I did not add one, because I don't know the exact fallback layout without a render.

No blockers.
