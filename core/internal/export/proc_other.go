//go:build !windows

package export

import (
	"os/exec"
	"syscall"
)

const onPath = "blender"

func runnable(string) error { return nil }

func hide(*exec.Cmd) {}

// detach lets Blender outlive the core and the terminal that started it.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

func guiBinary(path string) string { return path }
