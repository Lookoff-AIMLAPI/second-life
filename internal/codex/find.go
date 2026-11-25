// Package codex talks to the official Codex CLI's app-server. The CLI owns the login and every token refresh.
package codex

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// ErrMissing means no Codex CLI was found.
var ErrMissing = errors.New("codex CLI not found")

// searchDirs lists where installers put binaries; GUI apps start with a minimal PATH.
func searchDirs() []string {
	home, _ := os.UserHomeDir()
	var dirs []string
	if runtime.GOOS == "windows" {
		for _, p := range []string{home + `\.local\bin`, os.Getenv("APPDATA") + `\npm`, os.Getenv("LOCALAPPDATA") + `\Programs\codex`, os.Getenv("ProgramFiles") + `\nodejs`} {
			if filepath.IsAbs(p) {
				dirs = append(dirs, p)
			}
		}
	} else {
		dirs = append(dirs, "/opt/homebrew/bin", "/usr/local/bin")
		if home != "" {
			for _, d := range []string{".local/bin", ".npm-global/bin", ".bun/bin", ".volta/bin", "Library/pnpm"} {
				dirs = append(dirs, filepath.Join(home, d))
			}
			nvm, _ := filepath.Glob(filepath.Join(home, ".nvm/versions/node/*/bin"))
			dirs = append(dirs, nvm...)
		}
	}
	return dirs
}

// FindTool locates a CLI by name in PATH and then in the usual install directories.
func FindTool(name string) (string, error) {
	if p, err := exec.LookPath(name); err == nil {
		return filepath.Abs(p)
	}
	names := []string{name}
	if runtime.GOOS == "windows" {
		names = []string{name + ".exe", name + ".cmd"}
	}
	for _, d := range searchDirs() {
		for _, n := range names {
			p := filepath.Join(d, n)
			if st, err := os.Stat(p); err == nil && !st.IsDir() && (runtime.GOOS == "windows" || st.Mode()&0o111 != 0) {
				return p, nil
			}
		}
	}
	return "", exec.ErrNotFound
}

// ToolEnv returns the environment with the tool's own directory and the usual ones on PATH, so npm shims find node.
func ToolEnv(bin string) []string {
	var parts []string
	for _, d := range append(append(filepath.SplitList(os.Getenv("PATH")), filepath.Dir(bin)), searchDirs()...) {
		if d != "" && d != "." { // both mean the current directory
			parts = append(parts, d)
		}
	}
	path := "PATH=" + strings.Join(parts, string(os.PathListSeparator))
	env := []string{}
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); !strings.EqualFold(k, "PATH") {
			env = append(env, kv)
		}
	}
	return append(env, path)
}
