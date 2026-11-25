package desktop

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"time"
)

const (
	bundleID     = "com.anthropic.claudefordesktop"
	osascript    = "/usr/bin/osascript"
	openBin      = "/usr/bin/open"
	toolTimeout  = 30 * time.Second
	askTimeout   = 11 * time.Minute // longer than the dialog's own limit
	managedPrefs = "/Library/Managed Preferences"
)

// NSRunningApplication needs no permission to control Claude, unlike a scripted
// "quit". Both scripts print how many copies run.
const (
	countScript = `ObjC.import("AppKit");
function run(argv) {
	return String($.NSRunningApplication.runningApplicationsWithBundleIdentifier(argv[0]).count);
}`
	quitScript = `ObjC.import("AppKit");
function run(argv) {
	const apps = $.NSRunningApplication.runningApplicationsWithBundleIdentifier(argv[0]);
	for (let i = 0; i < apps.count; i++) { apps.objectAtIndex(i).terminate; }
	return String(apps.count);
}`
	// Text comes in as arguments, so nothing in it is read as script.
	confirmScript = `on run argv
	try
		set answer to display dialog (item 2 of argv) with title (item 1 of argv) buttons {"Cancel", item 3 of argv} default button "Cancel" cancel button "Cancel" with icon caution giving up after 600
	on error number -128
		return "cancel"
	end try
	if gave up of answer then return "cancel"
	return "ok"
end run`
	alertScript = `on run argv
	display dialog (item 2 of argv) with title (item 1 of argv) buttons {"OK"} default button "OK" giving up after 600
end run`
)

type native struct{ apps []string }

// New returns the installed Claude Desktop.
func New(exe, helper, baseURL, backupDir string) (*Desktop, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return &Desktop{
		Sys:       native{[]string{"/Applications/Claude.app", filepath.Join(home, "Applications", "Claude.app")}},
		Dir:       filepath.Join(home, "Library", "Application Support", "Claude-3p"),
		Usual:     filepath.Join(home, "Library", "Application Support", "Claude", modeFile),
		BackupDir: backupDir, BaseURL: baseURL, GOOS: "darwin", Exe: exe, Helper: helper,
	}, nil
}

func (n native) app() string {
	for _, a := range n.apps {
		if st, err := os.Stat(a); err == nil && st.IsDir() {
			return a
		}
	}
	return ""
}

func (n native) Installed() bool { return n.app() != "" }

func (native) Managed() (bool, error) {
	u, err := user.Current()
	if err != nil {
		return false, err
	}
	return managedIn(managedPrefs, u.Username)
}

// managedIn looks where Claude does: for the computer and for the user.
func managedIn(root, username string) (bool, error) {
	if username == "" || username != filepath.Base(username) {
		return false, errors.New("unexpected user name")
	}
	for _, p := range []string{
		filepath.Join(root, bundleID+".plist"),
		filepath.Join(root, username, bundleID+".plist"),
	} {
		if _, err := os.Lstat(p); err == nil {
			return true, nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return false, err
		}
	}
	return false, nil
}

func (native) Running() (bool, error) {
	out, err := tool(toolTimeout, osascript, "-l", "JavaScript", "-e", countScript, bundleID)
	if err != nil {
		return false, err
	}
	return out != "0", nil
}

func (native) Quit() error {
	_, err := tool(toolTimeout, osascript, "-l", "JavaScript", "-e", quitScript, bundleID)
	return err
}

func (n native) Open() error {
	app := n.app()
	if app == "" {
		return errors.New("Claude is not installed")
	}
	_, err := tool(toolTimeout, openBin, app)
	return err
}

func (native) Confirm(title, text, button string) (bool, error) {
	out, err := tool(askTimeout, osascript, "-e", confirmScript, title, text, button)
	return out == "ok", err
}

func (native) Alert(title, text string) {
	tool(askTimeout, osascript, "-e", alertScript, title, text)
}

func tool(limit time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(ee.Stderr) > 0 {
		err = errors.New(strings.TrimSpace(string(ee.Stderr)))
	}
	return strings.TrimSpace(string(out)), err
}
