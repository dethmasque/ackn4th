package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

var (
	probeOnce sync.Once
	probeErr  error
)

// available checks once that bubblewrap is installed and can create its
// namespaces: inside a container with the default seccomp profile, or on
// hosts that restrict unprivileged user namespaces, it is present but
// can't run.
func available() error {
	probeOnce.Do(func() {
		bwrap, err := exec.LookPath("bwrap")
		if err != nil {
			probeErr = fmt.Errorf("%w: install bubblewrap (bwrap), e.g. `apt install bubblewrap`", ErrUnavailable)
			return
		}
		out, err := exec.Command(bwrap, "--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc",
			"--unshare-pid", "--unshare-net", "--die-with-parent", "true").CombinedOutput()
		if err != nil {
			hint := "allow unprivileged user namespaces"
			if _, cerr := os.Stat("/.dockerenv"); cerr == nil || os.Getenv("ACKN4TH_CONTAINER") != "" {
				hint = "start it with container/ackn4th-container, which passes the --security-opt flags bubblewrap needs (seccomp, apparmor and systempaths unconfined)"
			}
			probeErr = fmt.Errorf("%w: bubblewrap can't create a sandbox here (%s): %s", ErrUnavailable, strings.TrimSpace(string(out)), hint)
		}
	})
	return probeErr
}

// wrap runs the command in a bubblewrap container: the filesystem is
// read-only except the workspace and scratch, secrets are masked, and the
// network, PIDs and IPC are private.
func wrap(cfg Config, argv []string) ([]string, error) {
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		return nil, err
	}
	args := []string{bwrap, "--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc", "--tmpfs", "/tmp",
		"--unshare-pid", "--unshare-ipc", "--unshare-uts", "--die-with-parent", "--new-session"}
	if !cfg.Network {
		args = append(args, "--unshare-net")
	}
	mask := func(p string) {
		fi, err := os.Lstat(p)
		switch {
		case err != nil:
		case fi.IsDir():
			args = append(args, "--tmpfs", p)
		default:
			args = append(args, "--ro-bind", "/dev/null", p)
		}
	}
	for _, h := range hidden(cfg) {
		mask(h)
	}
	bind := "--bind"
	if cfg.ReadOnly {
		bind = "--ro-bind"
	}
	args = append(args, bind, cfg.Workspace, cfg.Workspace)
	for _, d := range []string{".git", ".ackn4th"} {
		if p := filepath.Join(cfg.Workspace, d); isDir(p) {
			args = append(args, "--ro-bind", p, p)
		}
	}
	// Credential files in the workspace read as empty.
	secrets, err := secretFiles(cfg.Workspace)
	if err != nil {
		return nil, err
	}
	for _, p := range secrets {
		args = append(args, "--ro-bind", "/dev/null", p)
	}
	args = append(args, "--bind", cfg.Scratch, cfg.Scratch)
	for _, k := range cfg.Keep {
		args = append(args, "--bind", k, k)
	}
	for _, f := range cfg.Fixed {
		args = append(args, "--ro-bind", f, f)
	}
	// The program itself stays visible, even if it lives under the fresh
	// /tmp.
	if len(argv) > 0 && filepath.IsAbs(argv[0]) {
		args = append(args, "--ro-bind-try", argv[0], argv[0])
	}
	args = append(args, "--chdir", cfg.Workspace)
	args = append(args, argv...)
	return args, nil
}
