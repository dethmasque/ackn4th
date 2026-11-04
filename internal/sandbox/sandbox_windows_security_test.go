//go:build windows && winsandbox

package sandbox

// In-sandbox security tests for the Windows backend. They run real
// commands inside the AppContainer and assert the invariants in
// docs/architecture/sandbox-parity.md (C1 to C10). They must run on a real
// Windows host as a normal (non-elevated) user: cross-compiling proves
// nothing about ACLs. Windows is not released until these pass in CI
// (the windows-sandbox job) and are not skipped.

import (
	"crypto/rand"
	"encoding/hex"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// G1: .git and .ackn4th must not be renamed, moved or deleted away, then
// replaced. A replaced .git would let a command plant hooks that run as the
// user on the next git command outside the sandbox.
func TestProtectedDirsCannotBeReplaced(t *testing.T) {
	cfg, _ := setup(t)
	if err := os.MkdirAll(filepath.Join(cfg.Workspace, ".ackn4th"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Workspace, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{".git", ".ackn4th"} {
		for _, cmd := range []string{
			`ren ` + d + ` ` + d + `.old`,
			`move ` + d + ` ..\gone-` + d,
			`rmdir /s /q ` + d,
			`rmdir ` + d,
			`ren ` + d + ` ` + strings.ToUpper(d) + `.old & mkdir ` + d + ` & echo x> ` + d + `\evil`,
		} {
			run(t, cfg, cmd)
			fi, err := os.Stat(filepath.Join(cfg.Workspace, d))
			if err != nil || !fi.IsDir() {
				t.Fatalf("%q: %s is gone or not a directory (%v)", cmd, d, err)
			}
			for _, bad := range []string{d + ".old", strings.ToUpper(d) + ".old", filepath.Join(d, "evil")} {
				if _, err := os.Lstat(filepath.Join(cfg.Workspace, bad)); err == nil {
					t.Fatalf("%q: %s appeared: %s was replaced", cmd, bad, d)
				}
			}
		}
	}
	if _, err := os.Stat(filepath.Join(cfg.Workspace, ".git", "HEAD")); err != nil {
		t.Errorf(".git lost its contents: %v", err)
	}
	// A hook can't be planted inside it either.
	run(t, cfg, `mkdir .git\hooks & echo calc> .git\hooks\pre-commit`)
	if _, err := os.Stat(filepath.Join(cfg.Workspace, ".git", "hooks", "pre-commit")); err == nil {
		t.Error("planted .git/hooks/pre-commit")
	}
}

// G4: owner-implicit rights must not let a command undo the protected ACLs.
func TestCannotChangeItsOwnACLs(t *testing.T) {
	cfg, _ := setup(t)
	run(t, cfg, `icacls .git /grant *S-1-1-0:(OI)(CI)F /T & icacls .git /reset /T & icacls .env /grant *S-1-1-0:F & icacls .env /reset & icacls . /reset /T`)
	run(t, cfg, `echo x> .git\config`)
	if _, err := os.Stat(filepath.Join(cfg.Workspace, ".git", "config")); err == nil {
		t.Error("wrote .git\\config after changing ACLs")
	}
	if res := run(t, cfg, `type .env`); res.ExitCode == 0 || strings.Contains(res.Output, "hunter2") {
		t.Errorf("read .env after changing ACLs: %s", res.Output)
	}
	// And a command's own re-run still sees the protections (they are put
	// back on every launch).
	if res := run(t, cfg, `echo x> .git\config`); res.ExitCode == 0 {
		t.Error("wrote .git\\config on the next run")
	}
}

// G5: a confined process must not use ackn4th's launcher to start another
// one with capabilities of its choosing.
func TestLauncherRefusesInsideSandbox(t *testing.T) {
	cfg, _ := setup(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Workspace, "probe.exe"), data, 0o700); err != nil {
		t.Fatal(err)
	}
	// Any spec: it must be refused before it is even decoded.
	res := run(t, cfg, `probe.exe `+launcherArg+` e30 cmd.exe /c echo pwned`)
	if res.ExitCode != 125 || !strings.Contains(res.Output, "refusing") || strings.Contains(res.Output, "pwned") {
		t.Errorf("launcher ran inside the sandbox: exit %d: %s", res.ExitCode, res.Output)
	}
}

// C3 / G6: nothing of the user's is readable: not the home directory, not
// AppData, not a canary file in the profile.
func TestUserFilesUnreadable(t *testing.T) {
	cfg, _ := setup(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	var b [6]byte
	rand.Read(b[:])
	dir := filepath.Join(home, ".ackn4th-canary-"+hex.EncodeToString(b[:]))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	canary := filepath.Join(dir, "canary.txt")
	if err := os.WriteFile(canary, []byte("canary-contents"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []string{
		`type "` + canary + `"`,
		`dir "` + dir + `"`,
		`dir "` + home + `"`,
		`dir "` + filepath.Join(home, "AppData", "Roaming") + `"`,
		`dir "` + filepath.Join(home, "Documents") + `"`,
		`type "` + filepath.Join(home, ".ssh", "id_rsa") + `"`,
	} {
		res := run(t, cfg, cmd)
		if res.ExitCode == 0 || strings.Contains(res.Output, "canary-contents") || strings.Contains(res.Output, ".ackn4th-canary") {
			t.Errorf("%s succeeded in the sandbox: %s", cmd, res.Output)
		}
	}
}

// C1: a read-only role's commands can't change the workspace.
func TestReadOnlyWorkspace(t *testing.T) {
	cfg, _ := setup(t)
	os.WriteFile(filepath.Join(cfg.Workspace, "a.txt"), []byte("a"), 0o600)
	cfg.ReadOnly = true
	if res := run(t, cfg, `type a.txt`); res.ExitCode != 0 {
		t.Errorf("a read-only workspace should be readable: %s", res.Output)
	}
	run(t, cfg, `echo x> b.txt & echo x>> a.txt & del a.txt`)
	if _, err := os.Stat(filepath.Join(cfg.Workspace, "b.txt")); err == nil {
		t.Error("created a file in a read-only workspace")
	}
	if got, err := os.ReadFile(filepath.Join(cfg.Workspace, "a.txt")); err != nil || string(got) != "a" {
		t.Errorf("changed a file in a read-only workspace: %q %v", got, err)
	}
}

// G8: a junction in the workspace to a directory outside it reaches
// nothing from the sandbox, and applying the workspace's inheritable
// grants does not follow it.
func TestJunctionOutOfWorkspace(t *testing.T) {
	cfg, root := setup(t)
	outside := filepath.Join(root, "outside-dir")
	os.MkdirAll(outside, 0o700)
	os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("outside-secret"), 0o600)
	// Made before the first grant, so the grant's propagation meets it.
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", filepath.Join(cfg.Workspace, "link"), outside).CombinedOutput(); err != nil {
		t.Skipf("can't make a junction: %v: %s", err, out)
	}
	res := run(t, cfg, `type link\secret.txt & echo x> link\new.txt`)
	if strings.Contains(res.Output, "outside-secret") {
		t.Errorf("read through a junction: %s", res.Output)
	}
	if _, err := os.Stat(filepath.Join(outside, "new.txt")); err == nil {
		t.Error("wrote through a junction")
	}
	// The host's file tools go through os.Root, which must refuse a
	// junction out of the workspace as it does a symlink.
	rt, err := os.OpenRoot(cfg.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	if f, err := rt.Open("link/secret.txt"); err == nil {
		f.Close()
		t.Error("os.Root followed a junction out of the workspace")
	}
}

// C5 / G3: with the network off, a command can't connect to a service
// listening on the host's loopback, whether or not an administrator
// exempted the agent-CLI container for loopback.
func TestNoLoopbackWithoutNetwork(t *testing.T) {
	cfg, _ := setup(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	defer ln.Close()
	var hits atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			hits.Add(1)
			c.Close()
		}
	}()
	cfg.Network = false
	curl := filepath.Join(os.Getenv("SystemRoot"), "System32", "curl.exe")
	if _, err := os.Stat(curl); err != nil {
		t.Skip("no curl.exe")
	}
	run(t, cfg, `"`+curl+`" -s -m 5 http://`+ln.Addr().String()+`/`)
	if hits.Load() != 0 {
		t.Error("a command with the network off reached a host loopback service")
	}
}
