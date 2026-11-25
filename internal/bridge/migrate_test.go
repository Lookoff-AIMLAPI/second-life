package bridge

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAdopt(t *testing.T) {
	t.Setenv("SLIFE_HOME", "")
	root := t.TempDir()
	home, old := filepath.Join(root, ".second-life"), filepath.Join(root, formerHome)
	if err := adopt(home); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(home); err == nil {
		t.Fatal("made a folder out of nothing")
	}
	if err := os.MkdirAll(filepath.Join(old, "logs"), 0o700); err != nil {
		t.Fatal(err)
	}
	for n, s := range map[string]string{"token": "tok", "models.json": "{}", "aimlapi-key": "k", "logs/bridge.log": "x"} {
		if err := os.WriteFile(filepath.Join(old, n), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := adopt(home); err != nil {
		t.Fatal(err)
	}
	for n, s := range map[string]string{"token": "tok", "models.json": "{}", "aimlapi-key": "k"} {
		p := filepath.Join(home, n)
		b, err := os.ReadFile(p)
		st, _ := os.Stat(p)
		if err != nil || string(b) != s || os.PathSeparator == '/' && st.Mode().Perm() != 0o600 {
			t.Error(n, string(b), err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "logs")); err == nil {
		t.Error("the old log came along")
	}
	if b, _ := os.ReadFile(filepath.Join(old, "token")); string(b) != "tok" {
		t.Error("the old folder changed")
	}
	// Once only: what the owner changed since then stays.
	_ = os.WriteFile(filepath.Join(home, "token"), []byte("new"), 0o600)
	if err := adopt(home); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(home, "token")); string(b) != "new" {
		t.Error("overwritten")
	}
	if l, _ := filepath.Glob(filepath.Join(root, ".second-life-*")); len(l) != 0 {
		t.Error(l)
	}
}

func TestAdoptRefusesLinks(t *testing.T) {
	t.Setenv("SLIFE_HOME", "")
	root := t.TempDir()
	home, old := filepath.Join(root, ".second-life"), filepath.Join(root, formerHome)
	if err := os.Mkdir(old, 0o700); err != nil {
		t.Fatal(err)
	}
	if os.Symlink("/etc/hosts", filepath.Join(old, "token")) != nil {
		t.Skip("no links here")
	}
	if adopt(home) == nil {
		t.Error("followed a link")
	}
	if _, err := os.Lstat(home); err == nil {
		t.Error("left half a folder")
	}
}
