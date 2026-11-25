//go:build !windows

package keystore

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "home")
	f := file{filepath.Join(dir, "aimlapi-key")}
	roundTrip(t, f)
	endpointTrip(t, f)
	if err := f.set("0123456789abcdef"); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(f.path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", st.Mode(), err)
	}
	if left, _ := os.ReadDir(dir); len(left) != 1 {
		t.Errorf("%d files left", len(left))
	}
	if err := os.Chmod(f.path, 0o644); err != nil {
		t.Fatal(err)
	}
	if k, err := f.get(); k != "" || err == nil {
		t.Error("a key others can read was used")
	}
	if err := f.set("0123456789abcdef"); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(f.path); st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", st.Mode())
	}
	link := file{filepath.Join(dir, "link")}
	if err := os.Symlink(f.path, link.path); err != nil {
		t.Fatal(err)
	}
	if k, err := link.get(); k != "" || err == nil || errors.Is(err, ErrNone) {
		t.Error("a link was followed")
	}
	if k, err := (file{dir}).get(); k != "" || err == nil {
		t.Error("a folder was read")
	}
}
