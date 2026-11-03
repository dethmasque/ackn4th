// Package sandbox runs agents' shell commands (builds, tests, linters)
// confined by the operating system, not by trust:
//
//   - writes are allowed only in the task workspace and a private scratch
//     directory that stands in for HOME, TMPDIR and tool caches. Nothing
//     system-wide or user-wide can change. The workspace's .git and
//     .ackn4th stay read-only, and read-only roles can't write the
//     workspace at all;
//   - credentials can't be read: ~/.ssh, cloud and registry configs,
//     keychains, ackn4th's own config and data, and credential files in
//     the workspace (.env, *.pem, ...);
//   - the network is off unless the config turns it on (localhost works
//     either way, for tests that start a server);
//   - the environment is rebuilt from an allowlist, so tokens in the
//     user's shell never reach a command.
//
// macOS uses Seatbelt (sandbox-exec); Linux uses bubblewrap (bwrap);
// Windows uses an AppContainer, with access granted to the workspace,
// scratch and tool directories through their ACLs. Without a working
// sandbox, commands are unavailable rather than unconfined.
package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// Config describes one command's confinement.
type Config struct {
	Workspace string // absolute path of the task workspace
	// Scratch is the command's private writable directory; HOME, TMPDIR
	// and caches point inside it. It is created if missing.
	Scratch string
	// ReadOnly makes the workspace unwritable too.
	ReadOnly bool
	// Network allows outbound connections beyond localhost.
	Network bool
	// Hide lists more paths commands must not read, such as ackn4th's
	// config and data directories.
	Hide []string

	// For agent CLIs (Codex, Cursor, Copilot) that run whole in the
	// sandbox:
	//
	// Keep lists the CLI's own state directories (its login, sessions,
	// logs), readable and writable. They win over Hide and the secrets.
	Keep []string
	// RealHome keeps HOME pointing at the user's home, so the CLI finds
	// its config; it can still only write the workspace, scratch and Keep.
	RealHome bool
	// Keychain lets the process reach macOS's security service: the
	// keychain, for CLIs that keep their login there, and the TLS trust
	// checks that go through it. Keychain items stay guarded by their own
	// access lists.
	Keychain bool
	// Env adds variables to the allowlisted environment.
	Env []string
	// Fixed lists directories inside scratch that stay readable but not
	// writable, such as a CLI's working directory, so that its files can
	// only land in the workspace.
	Fixed []string
}

// Result is a finished command.
type Result struct {
	Output   string // stdout and stderr, interleaved, maybe cut in the middle
	ExitCode int
	TimedOut bool
	Duration time.Duration
	Cut      int // bytes of output dropped from the middle
}

// ErrUnavailable means commands can't be confined on this system.
var ErrUnavailable = errors.New("sandboxed commands are unavailable")

// Available reports whether commands can run here, and if not, why.
func Available() error { return available() }

const (
	headBytes = 8 << 10
	tailBytes = 24 << 10
)

// Shell is the shell Run runs commands with, as agents should be told.
func Shell() string {
	if runtime.GOOS == "windows" {
		return "cmd.exe /c, on Windows"
	}
	return "/bin/sh -c"
}

func shellArgv(command string) []string {
	if runtime.GOOS == "windows" {
		return []string{comspec(), "/d", "/s", "/c", command}
	}
	return []string{"/bin/sh", "-c", command}
}

func comspec() string {
	if c := os.Getenv("ComSpec"); c != "" {
		return c
	}
	return filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
}

// Run runs command with the shell (see Shell) in the workspace, confined
// by cfg, for at most timeout.
func Run(ctx context.Context, cfg Config, command string, timeout time.Duration) (Result, error) {
	if err := available(); err != nil {
		return Result{}, err
	}
	ws, err := filepath.EvalSymlinks(cfg.Workspace)
	if err != nil {
		return Result{}, err
	}
	cfg.Workspace = ws
	cmd, err := prepare(cfg, shellArgv(command))
	if err != nil {
		return Result{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out := &capped{}
	cmd.Stdout, cmd.Stderr = out, out
	start := time.Now()
	if err := cmd.Start(); err != nil {
		return Result{}, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var werr error
	select {
	case werr = <-done:
	case <-ctx.Done():
		killGroup(cmd)
		werr = <-done
	}
	res := Result{Output: out.String(), Duration: time.Since(start), Cut: out.cut, TimedOut: ctx.Err() == context.DeadlineExceeded}
	var exit *exec.ExitError
	switch {
	case werr == nil:
	case errors.As(werr, &exit):
		res.ExitCode = exit.ExitCode()
	default:
		return res, werr
	}
	if ctx.Err() != nil && !res.TimedOut {
		return res, ctx.Err() // cancelled: the task is stopping
	}
	return res, nil
}

// prepare makes cfg's directories and wraps argv in the sandbox.
func prepare(cfg Config, argv []string) (*exec.Cmd, error) {
	dirs := []string{"home", "tmp", "cache"}
	if runtime.GOOS == "windows" {
		dirs = append(dirs, filepath.Join("home", "AppData", "Roaming"), filepath.Join("home", "AppData", "Local"))
	}
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(cfg.Scratch, d), 0o700); err != nil {
			return nil, err
		}
	}
	var err error
	if cfg.Scratch, err = filepath.EvalSymlinks(cfg.Scratch); err != nil {
		return nil, err
	}
	var keep []string
	for _, k := range cfg.Keep {
		if err := os.MkdirAll(k, 0o700); err != nil {
			return nil, err
		}
		if r, err := filepath.EvalSymlinks(k); err == nil {
			keep = append(keep, r)
		}
	}
	cfg.Keep = keep
	var fixed []string
	for _, f := range cfg.Fixed {
		if err := os.MkdirAll(f, 0o700); err != nil {
			return nil, err
		}
		r, err := filepath.EvalSymlinks(f)
		if err != nil {
			return nil, err
		}
		fixed = append(fixed, r)
	}
	cfg.Fixed = fixed
	wrapped, err := wrap(cfg, argv)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(wrapped[0], wrapped[1:]...)
	cmd.Dir, cmd.Env = cfg.Workspace, environ(cfg)
	ownGroup(cmd) // so stopping it kills everything it started
	return cmd, nil
}

// Command returns argv confined by cfg, ready to start in the workspace,
// for a long-running process such as an agent CLI. Kill stops it and
// everything it started.
func Command(cfg Config, argv []string) (*exec.Cmd, error) {
	if err := available(); err != nil {
		return nil, err
	}
	ws, err := filepath.EvalSymlinks(cfg.Workspace)
	if err != nil {
		return nil, err
	}
	cfg.Workspace = ws
	return prepare(cfg, argv)
}

// Kill stops a command from Command and its whole process group.
func Kill(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		killGroup(cmd)
	}
}

// environ builds the command's environment from an allowlist: nothing
// from the user's shell is passed on unless it is needed to find tools.
func environ(cfg Config) []string {
	home, _ := os.UserHomeDir()
	sc := cfg.Scratch
	h := filepath.Join(sc, "home")
	if cfg.RealHome && home != "" {
		h = home
	}
	tmp := filepath.Join(sc, "tmp")
	env := []string{
		"HOME=" + h,
		"XDG_CACHE_HOME=" + filepath.Join(sc, "cache"),
		"TERM=dumb", "NO_COLOR=1", "CI=1",
		// Toolchain caches in scratch; never fetch a different Go.
		"GOCACHE=" + filepath.Join(sc, "cache", "go-build"),
		"GOPATH=" + filepath.Join(sc, "go"),
		"GOTOOLCHAIN=local",
		"npm_config_cache=" + filepath.Join(sc, "cache", "npm"),
		"npm_config_update_notifier=false",
		"PIP_CACHE_DIR=" + filepath.Join(sc, "cache", "pip"),
		"PYTHONDONTWRITEBYTECODE=1",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_CONFIG_NOSYSTEM=1",
	}
	pass := []string{"PATH", "LANG", "LC_ALL", "LC_CTYPE", "USER", "LOGNAME", "GOROOT", "JAVA_HOME"}
	if runtime.GOOS == "windows" {
		appdata, local := filepath.Join(h, "AppData", "Roaming"), filepath.Join(h, "AppData", "Local")
		if cfg.RealHome {
			appdata, local = os.Getenv("APPDATA"), os.Getenv("LOCALAPPDATA")
		}
		env = append(env, "USERPROFILE="+h, "APPDATA="+appdata, "LOCALAPPDATA="+local, "TEMP="+tmp, "TMP="+tmp)
		// What Windows programs need to start and find the system.
		pass = append(pass, "SystemRoot", "windir", "SystemDrive", "ComSpec", "PATHEXT", "OS",
			"PROCESSOR_ARCHITECTURE", "PROCESSOR_IDENTIFIER", "NUMBER_OF_PROCESSORS",
			"ProgramFiles", "ProgramFiles(x86)", "ProgramW6432", "CommonProgramFiles",
			"CommonProgramFiles(x86)", "CommonProgramW6432", "ProgramData", "PUBLIC",
			"USERNAME", "USERDOMAIN", "COMPUTERNAME")
	} else {
		env = append(env, "TMPDIR="+tmp, "SHELL=/bin/sh")
	}
	if !cfg.RealHome {
		// An agent CLI keeps its config where it always does.
		env = append(env, "XDG_CONFIG_HOME="+filepath.Join(sc, "home", ".config"), "XDG_DATA_HOME="+filepath.Join(sc, "home", ".local", "share"))
	}
	for _, k := range pass {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	for k, v := range toolchains(home) {
		env = append(env, k+"="+v)
	}
	return append(env, cfg.Env...)
}

// toolchains finds toolchains installed under the real home, which keep
// working read-only, by the variable that points at each.
func toolchains(home string) map[string]string {
	out := map[string]string{}
	for dir, k := range map[string]string{"go/pkg/mod": "GOMODCACHE", ".rustup": "RUSTUP_HOME", ".cargo": "CARGO_HOME", ".pyenv": "PYENV_ROOT", ".nvm": "NVM_DIR"} {
		if v, ok := os.LookupEnv(k); ok {
			out[k] = v
		} else if p := filepath.Join(home, filepath.FromSlash(dir)); home != "" && isDir(p) {
			out[k] = p
		}
	}
	return out
}

// secretsIn are paths under the user's home that commands can't read.
var secretsIn = []string{
	".ssh", ".gnupg", ".aws", ".azure", ".kube", ".docker", ".config/gcloud", ".config/gh",
	".netrc", ".npmrc", ".pypirc", ".git-credentials", ".config/git/credentials",
	".claude", ".claude.json", ".password-store", ".local/share/keyrings",
	"Library/Keychains", "Library/Cookies", "Library/Mail", "Library/Messages",
	"Library/Safari", "Library/Application Support/Google/Chrome", "Library/Application Support/Firefox",
	"Library/Application Support/BraveSoftware", "Library/Application Support/Microsoft Edge",
	".mozilla", ".config/google-chrome", ".config/chromium",
}

// hidden lists every path commands can't read: the user's secrets and
// cfg.Hide, as absolute paths.
func hidden(cfg Config) []string {
	home, _ := os.UserHomeDir()
	var out []string
	if home != "" {
		for _, p := range secretsIn {
			// macOS reads keychain files inside the calling process, so
			// keychain access needs them visible; the items stay encrypted
			// and guarded by their access lists.
			if cfg.Keychain && p == "Library/Keychains" {
				continue
			}
			out = append(out, filepath.Join(home, p))
		}
	}
	for _, p := range cfg.Hide {
		if abs, err := filepath.Abs(p); err == nil {
			out = append(out, abs)
		}
	}
	// Symlinked locations (macOS /var → /private/var) are matched as both.
	for _, p := range out {
		if r, err := filepath.EvalSymlinks(p); err == nil && r != p {
			out = append(out, r)
		}
	}
	return out
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// capped keeps the start and the end of a command's output.
type capped struct {
	mu         sync.Mutex
	head, tail bytes.Buffer
	cut        int
}

func (c *capped) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(p)
	if room := headBytes - c.head.Len(); room > 0 {
		k := min(room, len(p))
		c.head.Write(p[:k])
		p = p[k:]
	}
	c.tail.Write(p)
	if extra := c.tail.Len() - tailBytes; extra > 0 {
		c.cut += extra
		b := bytes.Clone(c.tail.Bytes()[extra:])
		c.tail.Reset()
		c.tail.Write(b)
	}
	return n, nil
}

func (c *capped) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cut == 0 {
		return c.head.String() + c.tail.String()
	}
	return fmt.Sprintf("%s\n… %d bytes of output cut …\n%s", c.head.String(), c.cut, c.tail.String())
}
