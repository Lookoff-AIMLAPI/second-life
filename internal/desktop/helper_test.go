package desktop

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// A universal build is twice the size of a native one; the check must not depend on size.
func TestHelperCheckIgnoresSize(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "slife")
	f, err := os.OpenFile(big, os.O_CREATE|os.O_WRONLY, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(200 << 20); err != nil { // sparse
		t.Fatal(err)
	}
	f.Close()
	if err := checkHelper(big); err != nil {
		t.Error(err)
	}
	link := filepath.Join(dir, "link")
	if os.Symlink(big, link) == nil && checkHelper(link) == nil {
		t.Error("a symlink passed")
	}
	if checkHelper(dir) == nil || checkHelper(filepath.Join(dir, "none")) == nil {
		t.Error("a directory or a missing file passed")
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(big, 0o644); err != nil {
			t.Fatal(err)
		}
		if checkHelper(big) == nil {
			t.Error("a file that cannot run passed")
		}
	}
}
