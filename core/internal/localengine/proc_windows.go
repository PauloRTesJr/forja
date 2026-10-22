//go:build windows

package localengine

import (
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var ntResumeProcess = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtResumeProcess")

// procGroup is a job object holding the engine and every process it spawns. Killing the
// launcher alone would orphan the Python interpreter and its model workers, because
// `uv run` and venv launchers start the real interpreter as a child.
type procGroup struct {
	mu  sync.Mutex
	job windows.Handle
	cmd *exec.Cmd
}

func configureProcess(cmd *exec.Cmd) {
	// Suspended, so it joins the job before it can spawn anything.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW | windows.CREATE_SUSPENDED,
	}
}

// attach puts the started, still-suspended engine in a kill-on-close job and resumes it.
// The core holds the only job handle, so the engine tree also dies if the core crashes.
func attach(cmd *exec.Cmd) (*procGroup, error) {
	g := &procGroup{cmd: cmd}
	proc, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_SUSPEND_RESUME, false, uint32(cmd.Process.Pid))
	if err != nil {
		return nil, fmt.Errorf("opening engine process: %w", err)
	}
	defer windows.CloseHandle(proc)
	job, jobErr := newKillOnCloseJob()
	if jobErr == nil {
		if jobErr = windows.AssignProcessToJobObject(job, proc); jobErr != nil {
			windows.CloseHandle(job)
		} else {
			g.job = job
		}
	}
	if r, _, _ := ntResumeProcess.Call(uintptr(proc)); r != 0 {
		_ = cmd.Process.Kill()
		g.close()
		return nil, fmt.Errorf("resuming engine process: NTSTATUS 0x%x", r)
	}
	if jobErr != nil {
		return g, fmt.Errorf("engine runs outside a job object, so stopping it may leave model workers behind: %w", jobErr)
	}
	return g, nil
}

func newKillOnCloseJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}

// interrupt stops the whole tree. The engine runs without a console, so there is no
// Ctrl+Break to send; it shuts down as abruptly as a crash, which it must survive anyway.
func (g *procGroup) interrupt() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.job != 0 {
		_ = windows.TerminateJobObject(g.job, 1)
		return
	}
	_ = g.cmd.Process.Kill()
}

// close releases the job, killing anything the engine left running.
func (g *procGroup) close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.job != 0 {
		_ = windows.CloseHandle(g.job)
		g.job = 0
	}
}
