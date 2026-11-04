package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func setup(t *testing.T) (Config, string) {
	t.Helper()
	if err := Available(); err != nil {
		t.Skip(err)
	}
	if runtime.GOOS == "windows" {
		// Only the system on PATH: the sandbox would otherwise grant
		// itself every tool folder on a CI runner's long PATH first.
		sys := os.Getenv("SystemRoot")
		t.Setenv("PATH", filepath.Join(sys, "System32")+";"+sys)
	}
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	os.MkdirAll(filepath.Join(ws, ".git"), 0o700)
	os.WriteFile(filepath.Join(ws, ".env"), []byte("TOKEN=hunter2"), 0o600)
	os.WriteFile(filepath.Join(ws, ".env.example"), []byte("TOKEN="), 0o600)
	os.WriteFile(filepath.Join(ws, "Server.PEM"), []byte("hunter2"), 0o600)
	secret := filepath.Join(root, "ackn4th-config")
	os.MkdirAll(secret, 0o700)
	os.WriteFile(filepath.Join(secret, "grant.json"), []byte("{}"), 0o600)
	return Config{Workspace: ws, Scratch: filepath.Join(root, "scratch"), Hide: []string{secret}}, root
}

func run(t *testing.T, cfg Config, cmd string) Result {
	t.Helper()
	res, err := Run(context.Background(), cfg, cmd, 20*time.Second)
	if err != nil {
		t.Fatalf("%s: %v", cmd, err)
	}
	return res
}

// sh picks the command for this OS's shell: /bin/sh, or cmd.exe on
// Windows.
func sh(posix, windows string) string {
	if runtime.GOOS == "windows" {
		return windows
	}
	return posix
}

func TestConfinement(t *testing.T) {
	cfg, root := setup(t)
	t.Setenv("ACKN4TH_TEST_TOKEN", "leaked")
	outside := filepath.Join(root, "outside.txt")
	home, _ := os.UserHomeDir()
	for _, c := range []struct {
		name, cmd string
		ok        bool
	}{
		{"write the workspace", sh("echo hi > out.txt && cat out.txt", "echo hi> out.txt && type out.txt"), true},
		{"write scratch HOME", sh("echo hi > $HOME/x && cat $HOME/x", `echo hi> "%USERPROFILE%\x" && type "%USERPROFILE%\x"`), true},
		{"write TMPDIR", sh("echo hi > $TMPDIR/x", `echo hi> "%TEMP%\x"`), true},
		{"write outside", sh("echo hi > ", "echo hi> ") + outside, false},
		{"write the real home", sh("echo hi > ", "echo hi> ") + filepath.Join(home, ".ackn4th-sandbox-test"), false},
		{"write .git", sh("echo hi > .git/config", `echo hi> .git\config`), false},
		{"write .GIT", sh("echo hi > .GIT/config", `echo hi> .GIT\config`), false},
		{"read .env", sh("cat .env", "type .env"), false},
		{"read .ENV", sh("cat .ENV", "type .ENV"), false},
		{"read .env.example", sh("cat .env.example", "type .env.example"), true},
		{"read Server.PEM", sh("cat Server.PEM", "type Server.PEM"), false},
		{"read hidden config", sh("cat ", "type ") + filepath.Join(root, "ackn4th-config", "grant.json"), false},
		{"read ~/.ssh", sh("ls ", "dir ") + filepath.Join(home, ".ssh"), false},
	} {
		if (c.name == "write .GIT" || c.name == "read .ENV") && runtime.GOOS == "linux" {
			continue // case-sensitive: .GIT is just another directory
		}
		res := run(t, cfg, c.cmd)
		if c.name == "write outside" && runtime.GOOS == "linux" && strings.HasPrefix(outside, "/tmp/") {
			continue // lands in the sandbox's own /tmp; checked below
		}
		if (res.ExitCode == 0) != c.ok {
			t.Errorf("%s: exit %d, want ok=%v: %s", c.name, res.ExitCode, c.ok, res.Output)
		}
	}
	if _, err := os.Stat(outside); err == nil {
		t.Error("a file was written outside the workspace")
	}
	// TEMP is scratch's, not one Windows picked for the container.
	if runtime.GOOS == "windows" {
		sc, _ := filepath.EvalSymlinks(cfg.Scratch)
		res := run(t, cfg, "echo %TEMP%")
		if got := strings.TrimSpace(res.Output); !strings.HasPrefix(strings.ToLower(got), strings.ToLower(sc)) {
			t.Errorf("TEMP is %s, not in scratch (%s)", got, sc)
		}
	}
	if res := run(t, cfg, sh("env", "set")); strings.Contains(res.Output, "leaked") || !strings.Contains(res.Output, "HOME="+cfg.Scratch) && !strings.Contains(res.Output, filepath.FromSlash("/scratch/home")) {
		t.Errorf("environment: %s", res.Output)
	}
}

func TestReadOnlyAndNetwork(t *testing.T) {
	cfg, _ := setup(t)
	cfg.ReadOnly = true
	if res := run(t, cfg, sh("echo hi > out.txt", "echo hi> out.txt")); res.ExitCode == 0 {
		t.Error("a read-only role's command wrote the workspace")
	}
	if res := run(t, cfg, sh("cat .env.example", "type .env.example")); res.ExitCode != 0 {
		t.Errorf("read-only commands should still read files: %s", res.Output)
	}
	if res := run(t, cfg, sh("ls -a", "dir /a /b")); res.ExitCode != 0 || !strings.Contains(res.Output, ".env.example") {
		t.Errorf("read-only commands should still list the workspace: exit %d: %s", res.ExitCode, res.Output)
	}
	curl := sh("/usr/bin/curl", filepath.Join(os.Getenv("SystemRoot"), "System32", "curl.exe"))
	if _, err := os.Stat(curl); err == nil {
		if res := run(t, cfg, curl+sh(" -sS -m 5 -o /dev/null https://example.com", " -sS -m 5 -o NUL https://example.com")); res.ExitCode == 0 {
			t.Error("the network should be off")
		}
	}
}

func TestTimeoutKillsTheGroup(t *testing.T) {
	cfg, _ := setup(t)
	start := time.Now()
	// On Windows, a loop that never ends (a step of 0).
	res, err := Run(context.Background(), cfg, sh("sleep 30 & sleep 30; echo never", "(for /L %i in (0,0,1) do @rem) & echo never"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut || time.Since(start) > 10*time.Second || strings.Contains(res.Output, "never") {
		t.Errorf("timeout: %+v after %s", res, time.Since(start))
	}
}

func TestOutputIsCapped(t *testing.T) {
	cfg, _ := setup(t)
	res := run(t, cfg, sh("i=0; while [ $i -lt 4000 ]; do echo line $i ................; i=$((i+1)); done; echo LAST",
		"(for /L %i in (0,1,3999) do @echo line %i ................) & echo LAST"))
	if res.Cut == 0 || !strings.Contains(res.Output, "line 0 ") || !strings.HasSuffix(strings.TrimSpace(res.Output), "LAST") || len(res.Output) > headBytes+tailBytes+100 {
		t.Errorf("cut %d, %d bytes", res.Cut, len(res.Output))
	}
}

// Keychain access needs the keychain files readable: macOS reads them in
// the calling process.
func TestKeychainUnhidesKeychainFiles(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	kc := filepath.Join(home, "Library/Keychains")
	if !slices.Contains(hidden(Config{}), kc) {
		t.Error("keychain files must be hidden by default")
	}
	if slices.Contains(hidden(Config{Keychain: true}), kc) {
		t.Error("Keychain must leave the keychain files readable")
	}
}

// Scratch inside a hidden dir (ackn4th's data dir): tools may see that its
// parents exist (Copilot checks), never what's in them.
func TestScratchInsideHiddenDir(t *testing.T) {
	cfg, root := setup(t)
	data := filepath.Join(root, "data")
	os.MkdirAll(filepath.Join(data, "tasks"), 0o700)
	os.WriteFile(filepath.Join(data, "tasks", "secret.json"), []byte("hunter2"), 0o600)
	os.WriteFile(filepath.Join(data, "tasks", "only-in-a-listing"), nil, 0o600)
	cfg.Hide = append(cfg.Hide, data)
	cfg.Scratch = filepath.Join(data, "tasks", "x", "scratch")
	secret, tasks := filepath.Join(data, "tasks", "secret.json"), filepath.Join(data, "tasks")
	res := run(t, cfg, sh(`cd "$TMPDIR" && pwd -P >/dev/null && test -d "$TMPDIR/.." && echo resolved; cat "`+secret+`"; ls "`+tasks+`"`,
		`cd /d "%TEMP%" && cd .. && echo resolved & type "`+secret+`" & dir /b "`+tasks+`"`))
	if !strings.Contains(res.Output, "resolved") {
		t.Errorf("the scratch dir's path should resolve: %s", res.Output)
	}
	if strings.Contains(res.Output, "hunter2") || strings.Contains(res.Output, "only-in-a-listing") {
		t.Errorf("the hidden dir's contents leaked: %s", res.Output)
	}
}

// With the network on, commands reach the internet.
func TestNetworkOn(t *testing.T) {
	cfg, _ := setup(t)
	cfg.Network = true
	curl := sh("/usr/bin/curl", filepath.Join(os.Getenv("SystemRoot"), "System32", "curl.exe"))
	if _, err := os.Stat(curl); err != nil || os.Getenv("ACKN4TH_TEST_NETWORK") == "" {
		t.Skip("set ACKN4TH_TEST_NETWORK=1 to test network access")
	}
	if res := run(t, cfg, curl+sh(" -sS -m 10 -o /dev/null https://example.com", " -sS -m 10 -o NUL https://example.com")); res.ExitCode != 0 {
		t.Errorf("with the network on, curl failed: %s", res.Output)
	}
}

// A Fixed directory inside scratch can be read, not written.
func TestFixedDir(t *testing.T) {
	cfg, _ := setup(t)
	fixed := filepath.Join(cfg.Scratch, "cli")
	os.MkdirAll(fixed, 0o700)
	os.WriteFile(filepath.Join(fixed, "conf.json"), []byte("{}"), 0o600)
	cfg.Fixed = []string{fixed}
	if res := run(t, cfg, sh("cat ", "type ")+filepath.Join(fixed, "conf.json")); res.ExitCode != 0 {
		t.Errorf("the fixed dir should be readable: %s", res.Output)
	}
	for _, p := range []string{filepath.Join(fixed, "new.txt"), filepath.Join(fixed, "conf.json")} {
		if res := run(t, cfg, sh("echo hi > ", "echo hi> ")+p); res.ExitCode == 0 {
			t.Errorf("wrote %s", p)
		}
	}
	if res := run(t, cfg, sh("echo hi > out.txt", "echo hi> out.txt")); res.ExitCode != 0 {
		t.Errorf("the workspace should stay writable: %s", res.Output)
	}
}
