//go:build linux || (windows && winsandbox)

package sandbox

import (
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/dethmasque/ackn4th/internal/workspace"
)

// maxScan bounds the walk for credential files in the workspace. Past it
// the scan gives up with an error rather than a partial list: a credential
// file the walk never reached would otherwise be readable by commands, and
// a repository crafted to push `.env` past a smaller limit would defeat the
// protection.
var maxScan = 250000 // a variable so tests can lower it

// secretFiles lists the credential files in the workspace (.env, *.pem,
// ...), which commands must not read. It fails closed: if the workspace is
// too big to scan completely, it returns an error and the command doesn't
// run.
//
// node_modules, vendor, .venv and .git are not entered: they hold
// third-party files (test certificates and the like), and .git is never
// readable by name through the agent tools either.
func secretFiles(ws string) ([]string, error) {
	var out []string
	n := 0
	err := filepath.WalkDir(ws, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == ws {
				return err
			}
			// A directory ackn4th can't read, a sandboxed command can't
			// either: it runs with no more rights than ackn4th has.
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if n++; n > maxScan {
			return fmt.Errorf("the workspace has more than %d files and folders, too many to scan for credential files: commands are unavailable here", maxScan)
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
		return nil, err
	}
	return out, nil
}
