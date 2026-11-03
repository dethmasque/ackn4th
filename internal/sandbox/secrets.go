//go:build linux || (windows && winsandbox)

package sandbox

import (
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/dethmasque/ackn4th/internal/workspace"
)

// maxScan bounds the walk for credential files in the workspace.
const maxScan = 20000

// secretFiles lists the credential files in the workspace (.env, *.pem,
// ...), which commands must not read. If the workspace itself can't be
// read, it fails: commands then don't run, rather than run with secrets
// that weren't found.
func secretFiles(ws string) ([]string, error) {
	var out []string
	n := 0
	err := filepath.WalkDir(ws, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == ws {
				return err
			}
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if n++; n > maxScan {
			return filepath.SkipAll
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
	if err != nil {
		return nil, fmt.Errorf("looking for credential files in the workspace: %w", err)
	}
	return out, nil
}
