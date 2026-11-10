//go:build !windows

package localengine

import (
	"os/exec"
	"syscall"
)

// procGroup is the engine's process group.
type procGroup struct{ pid int }

func configureProcess(cmd *exec.Cmd) {
	// Own process group, so the engine and its model workers stop together.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func attach(cmd *exec.Cmd) (*procGroup, error) {
	return &procGroup{pid: cmd.Process.Pid}, nil
}

func (g *procGroup) interrupt() {
	_ = syscall.Kill(-g.pid, syscall.SIGTERM)
}

// kill ends the whole group at once: killing the launcher alone would leave the interpreter
// and its model workers running.
func (g *procGroup) kill() {
	_ = syscall.Kill(-g.pid, syscall.SIGKILL)
}

func (g *procGroup) close() {}
