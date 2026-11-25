package mcpimport

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/Lookoff-AIMLAPI/second-life/internal/codex"
)

// Machine is everything the importer needs from the computer.
type Machine struct {
	GOOS       string
	ClaudeCode string   // Claude Code's settings file, read only
	Usual      string   // Claude Desktop's settings file, read only
	Gateway    string   // the same of its gateway profile, read only here
	CodexDirs  []string // where Codex keeps its own programs

	Find func(name string) (string, error)
	// Run returns what the program printed, both streams together when it failed.
	Run func(ctx context.Context, bin string, args ...string) ([]byte, error)
}

// Here is this computer.
func Here() (Machine, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Machine{}, err
	}
	m := Machine{GOOS: runtime.GOOS, Find: codex.FindTool, CodexDirs: []string{filepath.Join(home, ".codex")}}
	if d := os.Getenv("CODEX_HOME"); filepath.IsAbs(d) {
		m.CodexDirs = append(m.CodexDirs, filepath.Clean(d))
	}
	m.ClaudeCode = filepath.Join(home, ".claude.json")
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); filepath.IsAbs(d) {
		m.ClaudeCode = filepath.Join(d, ".claude.json")
	}
	const file = "claude_desktop_config.json"
	switch runtime.GOOS {
	case "darwin":
		base := filepath.Join(home, "Library", "Application Support")
		m.Usual, m.Gateway = filepath.Join(base, "Claude", file), filepath.Join(base, "Claude-3p", file)
	case "windows":
		if d := os.Getenv("APPDATA"); filepath.IsAbs(d) {
			m.Usual = filepath.Join(d, "Claude", file)
		}
		if d := os.Getenv("LOCALAPPDATA"); filepath.IsAbs(d) {
			m.Gateway = filepath.Join(d, "Claude-3p", file)
		}
	}
	m.Run = func(ctx context.Context, bin string, args ...string) ([]byte, error) {
		return start(ctx, home, bin, args)
	}
	return m, nil
}

const maxOutput = 4 << 20

// capped keeps the beginning of the output.
type capped struct{ bytes.Buffer }

func (c *capped) Write(p []byte) (int, error) {
	if room := maxOutput - c.Len(); room > 0 {
		c.Buffer.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

// start starts a tool in the home folder, where no project settings apply. The tool gets no input: the answers on
// standard input are ours.
func start(ctx context.Context, home, bin string, args []string) ([]byte, error) {
	cmd, err := codex.Command(bin, args...)
	if err != nil {
		return nil, err
	}
	var out, errs capped
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = home, codex.ToolEnv(bin), &out, &errs
	cmd.WaitDelay = 2 * time.Second // a child may keep the pipes open
	if err := cmd.Start(); err != nil {
		return nil, errors.New("could not start")
	}
	stop := context.AfterFunc(ctx, func() { _ = cmd.Process.Kill() })
	defer stop()
	if err := cmd.Wait(); err != nil {
		return append(out.Bytes(), errs.Bytes()...), err
	}
	return out.Bytes(), nil
}
