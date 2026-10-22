//go:build windows

package localengine

import (
	"os/exec"
	"syscall"
)

func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | 0x08000000} // CREATE_NO_WINDOW
}

func interrupt(cmd *exec.Cmd) {
	_ = cmd.Process.Kill()
}
