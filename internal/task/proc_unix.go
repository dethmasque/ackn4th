//go:build unix

package task

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
)

// Lock takes the task's run lock, held until release is called or the
// process exits. It fails if another runner holds it.
func (s *Store) Lock(id string) (release func(), err error) {
	f, err := os.OpenFile(filepath.Join(s.Dir(id), "run.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("task is already running")
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// Locked reports whether a runner currently holds the task's lock.
func (s *Store) Locked(id string) bool {
	release, err := s.Lock(id)
	if err != nil {
		_, statErr := os.Stat(s.Dir(id))
		return statErr == nil
	}
	release()
	return false
}

// Spawn starts a detached background runner for the task: it survives the
// terminal closing, and its output goes to runner.log.
func (s *Store) Spawn(id string) (int, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, err
	}
	logf, err := os.OpenFile(s.LogPath(id), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return 0, err
	}
	defer logf.Close()
	cmd := exec.Command(exe, "__run", id)
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	return pid, cmd.Process.Release()
}

// Signal asks a runner to stop gracefully.
func Signal(pid int) error {
	if pid <= 0 {
		return errors.New("no runner process recorded")
	}
	return syscall.Kill(pid, syscall.SIGTERM)
}

// StopContext is cancelled when the process is interrupted or asked to
// stop (Signal).
func StopContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
}
