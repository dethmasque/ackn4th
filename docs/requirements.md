# Requirements: issue #20, move the turns info blurb

## Goal
In the dashboard's New Task modal, the hint `0 = until every agent agrees` belongs to the turns/rounds field. Since the spending cap field was added, it renders after the cap field instead of next to turns, which misleads users. Make the hint appear with the turns field, or move it into the help text.

## User story
As a dashboard user creating a task, I want the "0 = until every agent agrees" explanation to sit with the turns field so that I don't read it as describing the spending cap.

## Must-have acceptance criteria
1. Given the New Task modal, when the turns field is shown, the `0 = until every agent agrees` hint is directly adjacent to it (same row, or the line right below it), and not after the spending cap field.
2. The spending cap field is not visually associated with that hint.
3. The hint is still visible when the turns field is focused and at narrow terminal widths (truncated cleanly, no overflow or wrapping glitches).
4. The help text for the rounds/turns field (`internal/dash/formhelp.go`) still explains that 0 runs until every agent agrees.
5. Existing dash tests and snapshots pass, updated only where the layout change requires it.

## Nice to have
- A short test asserting the hint's position relative to the turns and cap fields.

## Out of scope
Changing how rounds or caps work, or other form fields.

## Success
A screenshot of the New Task modal shows the hint next to turns, and `go test ./internal/dash/...` passes.

## Status
Implemented in `internal/dash/form.go` (`renderSettings`). The hint, shortened to `0 = until all agree`, now follows ROUNDS. On narrow widths the PR checkbox shortens first, then the hint drops to its own line labelled `ROUNDS:`. `TestRoundsHintStaysWithRounds` covers criterion 1, and the full wording stays in `formhelp.go`.

Review against criteria (by reading the diff; frontend_dev reports `internal/dash` tests passing under a patched go.mod):
1. Pass. 2. Pass. 3. Pass by design, but the 80-column layout has not been checked visually. 4. Pass. 5. Reported passing.

Remaining: a visual check of the modal in a real terminal.
