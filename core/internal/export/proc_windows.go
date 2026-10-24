//go:build windows

package export

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// onPath is the program to look for on PATH. Only blender.exe itself: a .cmd or .bat that
// starts it runs through cmd.exe, which breaks the script's lines apart.
const onPath = "blender.exe"

func runnable(path string) error {
	if !strings.EqualFold(filepath.Ext(path), ".exe") {
		return fmt.Errorf("%s is not blender.exe: set the path of blender.exe itself, not a script that starts it", path)
	}
	return nil
}

// hide keeps a background Blender from opening a console window: the core runs without one.
func hide(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}

// detach lets Blender outlive the core, without a console of its own.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS}
}

// guiBinary is blender-launcher.exe when it sits beside blender.exe: it starts Blender's
// window without flashing a console first.
func guiBinary(path string) string {
	launcher := filepath.Join(filepath.Dir(path), "blender-launcher.exe")
	if st, err := os.Stat(launcher); err == nil && st.Mode().IsRegular() {
		return launcher
	}
	return path
}
