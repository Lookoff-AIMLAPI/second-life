package tray

import (
	"testing"

	"golang.org/x/sys/windows/registry"
)

func TestRunKey(t *testing.T) {
	const path = `Software\second-life-test`
	t.Cleanup(func() { registry.DeleteKey(registry.CURRENT_USER, path) })
	const exe = `C:\My Apps\slife-tray.exe`
	root := registry.CURRENT_USER
	if runKeyEnabled(root, path, exe) {
		t.Error("enabled before it was set")
	}
	for _, on := range []bool{true, true, false, false} {
		if err := setRunKey(root, path, exe, on); err != nil {
			t.Fatal(err)
		}
		if runKeyEnabled(root, path, exe) != on {
			t.Errorf("not %v", on)
		}
	}
}

func TestLockMutex(t *testing.T) {
	const name = `Local\second-life-test`
	release, ok, err := lockMutex(name)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if _, ok, err := lockMutex(name); ok || err != nil {
		t.Errorf("second lock: %v %v", ok, err)
	}
	release()
}
