package plugins

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/Lookoff-AIMLAPI/second-life/internal/codex"
)

const maxOutput = 4 << 20

// capped keeps the beginning of the output.
type capped struct{ bytes.Buffer }

func (c *capped) Write(p []byte) (int, error) {
	if room := maxOutput - c.Len(); room > 0 {
		c.Buffer.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

// runError is a command that ended badly; its text is the end of what the command printed on stderr.
type runError struct{ text string }

func (e *runError) Error() string { return e.text }

// start runs claude in the home folder, where no project settings apply. It gets no input and returns stdout; a
// failure carries the end of stderr.
func start(ctx context.Context, bin string, args ...string) ([]byte, error) {
	cmd, err := codex.Command(bin, args...)
	if err != nil {
		return nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	var out, errs capped
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = home, codex.ToolEnv(bin), &out, &errs
	cmd.WaitDelay = 2 * time.Second // git, started by claude, may keep the pipes open
	var tree codex.Tree             // a timeout must also end the git that claude started
	tree.Prepare(cmd)
	if err := cmd.Start(); err != nil {
		return nil, errors.New("could not start Claude Code")
	}
	if err := tree.Adopt(cmd); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, errors.New("could not start Claude Code")
	}
	defer tree.Kill() // whatever it left behind
	stop := context.AfterFunc(ctx, tree.Kill)
	defer stop()
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return out.Bytes(), ctx.Err()
		}
		text := strings.TrimSpace(strings.ReplaceAll(errs.String(), bin, "claude"))
		if len(text) > 300 {
			text = text[len(text)-300:]
		}
		if text == "" {
			text = "Claude Code stopped with an error"
		}
		return out.Bytes(), &runError{text}
	}
	return out.Bytes(), nil
}
