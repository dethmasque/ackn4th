package sandbox

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Program returns argv ready to start on Windows: argv[0] as an absolute
// path, and an npm .cmd shim replaced by node running its script.
func Program(argv []string) ([]string, error) {
	out, _, err := resolveProgram(argv)
	return out, err
}

// resolveProgram makes argv[0] an absolute path to a program Windows can
// start with these arguments. npm's .cmd shims (how Codex and Copilot
// install) run their script with node instead: cmd.exe can't pass
// arguments with newlines or quotes through safely. extra lists
// directories the program needs to read.
func resolveProgram(argv []string) (out, extra []string, err error) {
	if len(argv) == 0 {
		return nil, nil, errors.New("no command")
	}
	p := argv[0]
	if !filepath.IsAbs(p) {
		if p, err = exec.LookPath(p); err != nil {
			return nil, nil, err
		}
	}
	switch strings.ToLower(filepath.Ext(p)) {
	case ".cmd", ".bat":
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, nil, err
		}
		script := npmShimScript(string(data))
		if script == "" {
			return nil, nil, fmt.Errorf("%s is a batch file, which can't take arguments safely: run the program it starts directly", p)
		}
		dir := filepath.Dir(p)
		node := filepath.Join(dir, "node.exe")
		if !exists(node) {
			if node, err = exec.LookPath("node"); err != nil {
				return nil, nil, fmt.Errorf("%s needs node: %w", p, err)
			}
		}
		return append([]string{node, filepath.Join(dir, script)}, argv[1:]...), []string{dir}, nil
	case ".ps1":
		return nil, nil, fmt.Errorf("%s is a PowerShell script: run the program it starts directly", p)
	}
	return append([]string{p}, argv[1:]...), nil, nil
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}
