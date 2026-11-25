//go:build !windows

package codex

import (
	"os/exec"
	"sync/atomic"
	"syscall"
)

// tree is the child's process group: the npm launcher starts the real binary, and killing the launcher alone would
// leave it behind. Its own group also keeps the terminal's Ctrl-C away from it.
type tree struct{ pgid atomic.Int64 }

func (t *tree) prepare(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

func (t *tree) adopt(cmd *exec.Cmd) error {
	t.pgid.Store(int64(cmd.Process.Pid))
	return nil
}

// kill works once: later the group id could belong to someone else.
func (t *tree) kill() {
	if id := t.pgid.Swap(0); id > 0 {
		_ = syscall.Kill(-int(id), syscall.SIGKILL)
	}
}
