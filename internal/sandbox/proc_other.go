//go:build !unix && !windows

package sandbox

import "os/exec"

func ownGroup(*exec.Cmd) {}

func killGroup(cmd *exec.Cmd) { cmd.Process.Kill() }
