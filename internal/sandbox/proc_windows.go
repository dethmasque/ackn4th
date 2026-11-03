package sandbox

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// ownGroup keeps the launcher from opening a console window when ackn4th
// runs without one (a background task).
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
}

// killGroup kills the launcher; its job object takes everything the
// command started with it.
func killGroup(cmd *exec.Cmd) { cmd.Process.Kill() }
