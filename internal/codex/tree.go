package codex

import "os/exec"

// Tree ends a command and everything it started: Prepare before Start, Adopt right after it, Kill to end them all.
type Tree struct{ t tree }

func (x *Tree) Prepare(cmd *exec.Cmd)     { x.t.prepare(cmd) }
func (x *Tree) Adopt(cmd *exec.Cmd) error { return x.t.adopt(cmd) }
func (x *Tree) Kill()                     { x.t.kill() }
