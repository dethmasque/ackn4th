//go:build winsandbox

package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandLine(t *testing.T) {
	got := commandLine([]string{`C:\Windows\System32\cmd.exe`, "/d", "/s", "/c", `go test ./... && echo "done"`})
	if want := `C:\Windows\System32\cmd.exe /d /s /c "go test ./... && echo "done""`; got != want {
		t.Errorf("shell: got %s, want %s", got, want)
	}
	got = commandLine([]string{`C:\Program Files\nodejs\node.exe`, "x.js", "line one\nsay \"hi\""})
	if want := `"C:\Program Files\nodejs\node.exe" x.js "line one` + "\n" + `say \"hi\""`; got != want {
		t.Errorf("program: got %s, want %s", got, want)
	}
}

func TestOtherWorkspaceUnreachable(t *testing.T) {
	cfg, root := setup(t)
	other := filepath.Join(root, "other")
	os.MkdirAll(other, 0o700)
	os.WriteFile(filepath.Join(other, "notes.txt"), []byte("other-workspace"), 0o600)
	cfg2 := cfg
	cfg2.Workspace, cfg2.Scratch = other, filepath.Join(root, "scratch2")
	if res := run(t, cfg2, "type notes.txt"); res.ExitCode != 0 {
		t.Fatalf("a workspace's own files should be readable: %s", res.Output)
	}
	res := run(t, cfg, `type "`+filepath.Join(other, "notes.txt")+`" & echo hi> "`+filepath.Join(other, "new.txt")+`"`)
	if strings.Contains(res.Output, "other-workspace") {
		t.Errorf("read another workspace: %s", res.Output)
	}
	if _, err := os.Stat(filepath.Join(other, "new.txt")); err == nil {
		t.Error("wrote another workspace")
	}
	os.WriteFile(filepath.Join(root, "scratch2", "tmp", "x"), []byte("other-scratch"), 0o600)
	if res := run(t, cfg, `type "`+filepath.Join(root, "scratch2", "tmp", "x")+`"`); strings.Contains(res.Output, "other-scratch") {
		t.Errorf("read another agent's scratch: %s", res.Output)
	}
}

// Agent CLIs keep their state in Keep directories, which are writable.
func TestKeepWritable(t *testing.T) {
	cfg, root := setup(t)
	keep := filepath.Join(root, "cli-state")
	cfg.Keep = []string{keep}
	if res := run(t, cfg, `echo hi> "`+filepath.Join(keep, "session")+`" && type "`+filepath.Join(keep, "session")+`"`); res.ExitCode != 0 {
		t.Errorf("Keep should be writable: %s", res.Output)
	}
}
