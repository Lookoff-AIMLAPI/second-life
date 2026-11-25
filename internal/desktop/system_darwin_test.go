package desktop

import (
	"os"
	"path/filepath"
	"testing"
)

func TestManagedIn(t *testing.T) {
	root := t.TempDir()
	check := func(want bool) {
		t.Helper()
		if got, err := managedIn(root, "stan"); got != want || err != nil {
			t.Errorf("%v %v", got, err)
		}
	}
	check(false)
	user := filepath.Join(root, "stan")
	os.Mkdir(user, 0o755)
	os.WriteFile(filepath.Join(user, "com.example.other.plist"), nil, 0o644)
	check(false)
	os.WriteFile(filepath.Join(user, bundleID+".plist"), nil, 0o644)
	check(true)
	os.RemoveAll(user)
	os.WriteFile(filepath.Join(root, bundleID+".plist"), nil, 0o644)
	check(true)
	if _, err := managedIn(root, "../x"); err == nil {
		t.Error("odd user name accepted")
	}
	os.Remove(filepath.Join(root, bundleID+".plist"))
	os.Mkdir(user, 0)
	defer os.Chmod(user, 0o700)
	if os.Getuid() != 0 {
		if _, err := managedIn(root, "stan"); err == nil {
			t.Error("an unreadable policy folder must not count as no policy")
		}
	}
}

// The scripts run against an app that does not exist: nothing can quit.
func TestScripts(t *testing.T) {
	for _, script := range []string{countScript, quitScript} {
		if out, err := tool(toolTimeout, osascript, "-l", "JavaScript", "-e", script, "com.example.no-such-app"); err != nil || out != "0" {
			t.Errorf("%q %v", out, err)
		}
	}
	// Compiling does not run: no dialog opens.
	for _, script := range []string{confirmScript, alertScript} {
		out := filepath.Join(t.TempDir(), "x.scpt")
		if _, err := tool(toolTimeout, "/usr/bin/osacompile", "-o", out, "-e", script); err != nil {
			t.Error(err)
		}
	}
	if _, err := tool(toolTimeout, osascript, "-e", "error \"no\""); err == nil {
		t.Error("a failing script is not an error")
	}
}

func TestInstalled(t *testing.T) {
	app := filepath.Join(t.TempDir(), "Claude.app")
	n := native{[]string{app}}
	if n.Installed() || n.Open() == nil {
		t.Error("installed before it exists")
	}
	os.Mkdir(app, 0o755)
	if !n.Installed() {
		t.Error("not found")
	}
}
