//go:build windows && !winsandbox

package sandbox

import "fmt"

// Windows has no sandbox in release builds (see sandbox_windows.go), and
// commands are never run unconfined: agents there read, write and search
// files with ackn4th's own tools, but can't run commands, and agent CLIs
// (Codex, Cursor, Copilot) aren't available.
func available() error {
	return fmt.Errorf("%w on Windows: agents can't run commands there, and Codex, Cursor and Copilot aren't available (use WSL for those)", ErrUnavailable)
}

func wrap(Config, []string) ([]string, error) { return nil, available() }
