# Dashboard: keep the rounds hint next to ROUNDS (issue #20)

Goal: in the New Task form (`internal/dash/form.go renderSettings`), the hint `0 = until every agent agrees` explains the ROUNDS field, but since the CAP $ field was added after ROUNDS the hint renders after CAP and reads as if it describes the cap. Make the explanation unambiguously about rounds.

## Must-have acceptance criteria
1. Given the New Task form, the `0 = until every agent agrees` text is never displayed directly after the CAP field.
2. The hint appears adjacent to ROUNDS (e.g. `ROUNDS [ ] 0 = until all agree   CAP $[ ]`) when width allows, OR is moved into help text (`formhelp.go`, shown when ROUNDS is focused). The "rounds" help already says it; ensure it remains.
3. On narrow terminals nothing overflows or wraps, and the `draft PR` checkbox still renders (existing fallback preserved).
4. Snapshot/form tests are updated; `go vet ./...` and `go test ./internal/dash/...` pass.

Nice-to-have: a short CAP hint (e.g. `blank = no cap`) if verified accurate.

Out of scope: changing round/cap semantics, other form layout.

Success: a user can no longer misread the 0-hint as applying to the spending cap.
