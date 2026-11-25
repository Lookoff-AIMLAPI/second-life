package codex

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// Command prepares a tool run. On Windows an npm .cmd launcher is replaced by what it starts, because cmd.exe would
// read the arguments a second time, by its own rules.
func Command(bin string, args ...string) (*exec.Cmd, error) {
	if runtime.GOOS != "windows" || !isBatch(bin) {
		return exec.Command(bin, args...), nil
	}
	if prog, first, ok := resolveShim(bin); ok {
		return exec.Command(prog, append(first, args...)...), nil
	}
	if unsafeForBatch(append([]string{bin}, args...)) {
		return nil, errors.New(filepath.Base(bin) + " is a batch file and cannot take these arguments safely")
	}
	return exec.Command(bin, args...), nil
}

func isBatch(bin string) bool {
	ext := strings.ToLower(filepath.Ext(bin))
	return ext == ".cmd" || ext == ".bat"
}

// unsafeForBatch reports anything cmd.exe would not pass through as it is.
func unsafeForBatch(args []string) bool {
	for _, a := range args {
		if strings.ContainsAny(a, "\"%!^&|<>()\r\n\x00") {
			return true
		}
	}
	return false
}

var shimLine = regexp.MustCompile(`(?i)"(?:%dp0%|%~dp0)\\?([^"%]+\.(?:exe|js|cjs|mjs))"`)

// shimTarget is the path, relative to the launcher, of what an npm or pnpm launcher starts.
func shimTarget(text string) string {
	target := ""
	for _, m := range shimLine.FindAllStringSubmatch(text, -1) {
		if !strings.EqualFold(m[1], "node.exe") {
			target = m[1]
		}
	}
	return target
}

func resolveShim(bin string) (prog string, first []string, ok bool) {
	text, err := os.ReadFile(bin)
	if err != nil {
		return "", nil, false
	}
	rel := shimTarget(string(text))
	if rel == "" {
		return "", nil, false
	}
	dir := filepath.Dir(bin)
	target := filepath.Join(dir, filepath.FromSlash(rel))
	if st, err := os.Stat(target); err != nil || !st.Mode().IsRegular() {
		return "", nil, false
	}
	if strings.EqualFold(filepath.Ext(target), ".exe") {
		return target, nil, true
	}
	node := filepath.Join(dir, "node.exe")
	if st, err := os.Stat(node); err != nil || !st.Mode().IsRegular() {
		if node, err = FindTool("node"); err != nil || isBatch(node) {
			return "", nil, false
		}
	}
	return node, []string{target}, true
}
