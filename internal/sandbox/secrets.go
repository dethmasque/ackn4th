//go:build linux || windows

package sandbox

import (
	"io/fs"
	"path/filepath"

	"github.com/dethmasque/ackn4th/internal/workspace"
)

// maxScan bounds the walk for credential files in the workspace.
const maxScan = 20000

// secretFiles lists the credential files in the workspace (.env, *.pem,
// ...), which commands must not read.
func secretFiles(ws string) []string {
	var out []string
	n := 0
	filepath.WalkDir(ws, func(p string, d fs.DirEntry, err error) error {
		if n++; err != nil || n > maxScan {
			return filepath.SkipDir
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", ".venv":
				return filepath.SkipDir
			}
			return nil
		}
		if rel, err := filepath.Rel(ws, p); err == nil && workspace.IsSecret(filepath.ToSlash(rel)) {
			out = append(out, p)
		}
		return nil
	})
	return out
}
