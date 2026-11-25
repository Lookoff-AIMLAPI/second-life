package bridge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogFileRotates(t *testing.T) {
	h := t.TempDir()
	lg := openLog(h)
	for range 6 {
		lg.Print(strings.Repeat("x", 1<<20))
	}
	for _, p := range []string{LogPath(h), LogPath(h) + ".1"} {
		st, err := os.Stat(p)
		if err != nil || st.Size() > maxLog {
			t.Fatal(p, err)
		}
		if os.PathSeparator == '/' && st.Mode().Perm() != 0o600 {
			t.Fatal(st.Mode())
		}
	}
	if st, _ := os.Stat(filepath.Dir(LogPath(h))); os.PathSeparator == '/' && st.Mode().Perm() != 0o700 {
		t.Fatal(st.Mode())
	}
}
