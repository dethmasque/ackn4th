package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveNPMShim(t *testing.T) {
	dir := t.TempDir()
	shim := filepath.Join(dir, "tool.cmd")
	os.WriteFile(shim, []byte("@ECHO off\r\n\"%_prog%\"  \"%dp0%\\node_modules\\tool\\cli.js\" %*\r\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "node.exe"), nil, 0o600)
	argv, extra, err := resolveProgram([]string{shim, "-p", "multi\nline"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(dir, "node.exe"), filepath.Join(dir, `node_modules\tool\cli.js`), "-p", "multi\nline"}
	if strings.Join(argv, "|") != strings.Join(want, "|") || len(extra) != 1 || extra[0] != dir {
		t.Errorf("got %q (extra %q)", argv, extra)
	}
	plain := filepath.Join(dir, "plain.bat")
	os.WriteFile(plain, []byte("@echo off\r\nother.exe %*\r\n"), 0o600)
	if _, _, err := resolveProgram([]string{plain, "x"}); err == nil {
		t.Error("a batch file that isn't an npm shim should be refused")
	}
}

// Grants stay on ACLs, so one workspace's commands must not reach another
// workspace that was granted before.
