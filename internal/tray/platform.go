package tray

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Lookoff-AIMLAPI/second-life/internal/bridge"
	"github.com/Lookoff-AIMLAPI/second-life/internal/desktop"
)

var errNoCLI = errors.New("slife was not found next to the app")

const launchTimeout = 15 * time.Second

func executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

// siblingCLI is the command line tool installed beside exe.
func siblingCLI(exe, name string) (string, error) {
	path := filepath.Join(filepath.Dir(exe), name)
	if st, err := os.Stat(path); err != nil || !st.Mode().IsRegular() {
		return "", errNoCLI
	}
	return path, nil
}

// addDesktop fills in what is the same on both systems.
func addDesktop(act actions, slifeName string) actions {
	none := errors.New("Claude Desktop is not available")
	act.desktop = func() desktopState { return desktopState{} }
	act.setupDesktop = func(context.Context, [][2]string) error { return none }
	act.leaveDesktop = func(context.Context) error { return none }
	act.alert = func(string, string) {}
	act.startWarning = func() string { return "" }

	home, err := bridge.Home()
	if err != nil {
		return act
	}
	exe, err := executable()
	if err != nil {
		return act
	}
	d, err := desktop.New(exe, filepath.Join(filepath.Dir(exe), slifeName), apiAddress(bridge.Addr()), filepath.Join(home, "backups", "claude-desktop"))
	if err != nil {
		return act
	}
	act.desktop = func() desktopState {
		if !d.Sys.Installed() {
			return desktopState{}
		}
		return desktopState{Installed: true, ThirdParty: d.ThirdParty()}
	}
	act.setupDesktop = d.Setup
	act.leaveDesktop = d.Back
	act.alert = d.Sys.Alert
	act.startWarning = func() string { return desktop.StartWarning(runtime.GOOS, exe) }
	return act
}

// openLog hands the log to open, if there is one.
func openLog(home string, open func(path string) error) error {
	path := bridge.LogPath(home)
	if st, err := os.Lstat(path); err != nil || !st.Mode().IsRegular() {
		return errors.New("there is no log yet")
	}
	return open(path)
}

func openLogWith(open func(path string) error) func() error {
	return func() error {
		home, err := bridge.Home()
		if err != nil {
			return err
		}
		return openLog(home, open)
	}
}

// launch runs a short helper such as open and returns what it complained about.
func launch(setup func(*exec.Cmd), name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), launchTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	if setup != nil {
		setup(cmd)
	}
	out, err := cmd.CombinedOutput()
	if msg := strings.TrimSpace(string(out)); err != nil && msg != "" && !strings.Contains(msg, "https://") {
		return errors.New(msg)
	}
	return err
}
