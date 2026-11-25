//go:build !windows

package codex

import (
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
)

func pidFrom(t *testing.T, path string) int {
	t.Helper()
	pid := 0
	eventually(t, "the child says its pid", func() bool {
		b, _ := os.ReadFile(path)
		pid, _ = strconv.Atoi(string(b))
		return pid > 0
	})
	return pid
}

func gone(pid int) bool { return syscall.Kill(pid, 0) == syscall.ESRCH }

func TestLauncherAndItsChildEnd(t *testing.T) {
	for _, mode := range []string{"wrap:ok", "wrap:hangs", "wrap:mute"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "pid")
			t.Setenv("SLIFE_FAKE_PID", path)
			c := fake(t, mode)
			go c.Account(bg)
			pid := pidFrom(t, path)
			if mode != "wrap:mute" {
				eventually(t, "started", func() bool { return running(c) != nil })
			}
			c.Close()
			eventually(t, "the real child is gone", func() bool { return gone(pid) })
		})
	}
}

func TestKilledLauncherIsReplaced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pid")
	t.Setenv("SLIFE_FAKE_PID", path)
	c := fake(t, "dies:ok")
	p, err := c.get(bg)
	if err != nil {
		t.Fatal(err)
	}
	pid := pidFrom(t, path)
	<-p.done
	eventually(t, "what it left behind is gone", func() bool { return gone(pid) })
	os.Setenv("SLIFE_FAKE", "ok") // restored by fake
	if _, err := c.Account(bg); err != nil {
		t.Fatal(err)
	}
	if running(c) == p {
		t.Fatal("the dead one is still used")
	}
}

func TestChildHasItsOwnGroup(t *testing.T) {
	c := fake(t, "ok")
	p, err := c.get(bg)
	if err != nil {
		t.Fatal(err)
	}
	if g, err := syscall.Getpgid(p.cmd.Process.Pid); err != nil || g == syscall.Getpgrp() {
		t.Fatal("Ctrl-C would reach it", g, err)
	}
}
