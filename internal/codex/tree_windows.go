package codex

import (
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// tree is a job object that ends with its last handle, so nothing outlives the bridge even when it is killed.
// Its own process group keeps the console's Ctrl-C away from the child.
type tree struct {
	mu  sync.Mutex
	job windows.Handle
}

func (t *tree) prepare(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
}

func (t *tree) adopt(cmd *exec.Cmd) error {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return err
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
	if err == nil {
		var h windows.Handle
		h, err = windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
		if err == nil {
			err = windows.AssignProcessToJobObject(job, h)
			windows.CloseHandle(h)
		}
	}
	if err != nil {
		windows.CloseHandle(job)
		return err
	}
	t.job = job
	return nil
}

// kill is safe to repeat.
func (t *tree) kill() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.job != 0 {
		_ = windows.TerminateJobObject(t.job, 1)
		windows.CloseHandle(t.job)
		t.job = 0
	}
}
