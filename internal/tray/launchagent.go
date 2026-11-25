package tray

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func launchAgentPath(home string) string {
	return filepath.Join(home, "Library", "LaunchAgents", appID+".plist")
}

// launchAgentPlist starts exe once per login. There is no KeepAlive: Quit must
// stay quit.
func launchAgentPlist(exe string) ([]byte, error) {
	var path bytes.Buffer
	if err := xml.EscapeText(&path, []byte(exe)); err != nil {
		return nil, err
	}
	return []byte(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + appID + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + path.String() + `</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>ProcessType</key>
	<string>Interactive</string>
	<key>LimitLoadToSessionType</key>
	<string>Aqua</string>
</dict>
</plist>
`), nil
}

// launchAgentEnabled is true only when the agent starts this very exe, so a
// moved app shows as off and one click repairs it.
func launchAgentEnabled(home, exe string) bool {
	want, err := launchAgentPlist(exe)
	if err != nil {
		return false
	}
	got, err := os.ReadFile(launchAgentPath(home))
	return err == nil && bytes.Equal(got, want)
}

// setLaunchAgent only writes or removes the file. launchd reads it at the next
// login; loading it now would start a second copy.
func setLaunchAgent(home, exe string, on bool) error {
	path := launchAgentPath(home)
	if !on {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	if strings.Contains(exe, "/AppTranslocation/") {
		return errors.New("move the app to Applications first")
	}
	plist, err := launchAgentPlist(exe)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(path, plist, 0o644)
}

func writeFileAtomic(path string, data []byte, mode fs.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".slife-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name()) // fails harmlessly after the rename
	_, err = f.Write(data)
	if err == nil {
		err = f.Chmod(mode)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// privateDir makes dir usable by this user only and refuses a symlink.
func privateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	st, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return errors.New(dir + " is not a directory")
	}
	return os.Chmod(dir, 0o700)
}

// writeClaudeScript writes the .command file under cache and returns its path.
func writeClaudeScript(cache, slife, verb string) (string, error) {
	dir := filepath.Join(cache, appDir)
	if err := privateDir(dir); err != nil {
		return "", err
	}
	name := "Claude.command"
	switch verb {
	case verbCodex:
		name = "Install Codex.command"
	case verbMCP:
		name = "Import MCP servers.command"
	}
	path := filepath.Join(dir, name)
	return path, writeFileAtomic(path, []byte(claudeScript(slife, verb)), 0o700)
}
