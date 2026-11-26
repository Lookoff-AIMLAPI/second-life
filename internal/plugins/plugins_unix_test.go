//go:build !windows

package plugins

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// With SLIFE_FAKE_CLAUDE set, the test binary is a fake claude that prints, sleeps and exits as told.
func init() {
	if os.Getenv("SLIFE_FAKE_CLAUDE") == "" {
		return
	}
	if path := os.Getenv("SLIFE_FAKE_CHILD"); path != "" { // like claude starting git
		child := exec.Command("sleep", "30")
		_ = child.Start()
		_ = os.WriteFile(path, []byte(strconv.Itoa(child.Process.Pid)), 0o600)
	}
	if d, err := time.ParseDuration(os.Getenv("SLIFE_FAKE_SLEEP")); err == nil {
		time.Sleep(d)
	}
	os.Stdout.WriteString(os.Getenv("SLIFE_FAKE_OUT"))
	os.Stderr.WriteString(os.Getenv("SLIFE_FAKE_ERR"))
	if os.Getenv("SLIFE_FAKE_EXIT") == "1" {
		os.Exit(1)
	}
	os.Exit(0)
}

func fakeClaude(t *testing.T, out, errs, sleep, exit string) *Runner {
	t.Setenv("SLIFE_FAKE_CLAUDE", "1")
	t.Setenv("SLIFE_FAKE_OUT", out)
	t.Setenv("SLIFE_FAKE_ERR", errs)
	t.Setenv("SLIFE_FAKE_SLEEP", sleep)
	t.Setenv("SLIFE_FAKE_EXIT", exit)
	r := Here()
	r.Find = func(string) (string, error) { return os.Args[0], nil }
	return r
}

func TestRunReadsStdoutOnly(t *testing.T) {
	r := fakeClaude(t, catalogJSON, "Warning: on stderr {\n", "", "0")
	c, err := r.Catalog(context.Background())
	if err != nil || len(c.Available) != 3 {
		t.Fatal(c, err)
	}
}

func TestRunFailureCarriesStderr(t *testing.T) {
	r := fakeClaude(t, "", "error: unknown option '--available'\n", "", "1")
	if _, err := r.Catalog(context.Background()); err != ErrOldClaude {
		t.Fatal(err)
	}
	r = fakeClaude(t, "", "fatal: could not read from remote repository\n", "", "1")
	err := r.AddCatalog(context.Background())
	if err == nil || !strings.Contains(err.Error(), "could not read from remote repository") || strings.Contains(err.Error(), os.Args[0]) {
		t.Fatal(err)
	}
}

func TestTimeout(t *testing.T) {
	r := fakeClaude(t, "", "", "5s", "0")
	r.Waits.Change = 200 * time.Millisecond
	start := time.Now()
	err := r.UpdateCatalog(context.Background())
	if err == nil || !strings.Contains(err.Error(), "took too long") {
		t.Fatal(err)
	}
	if time.Since(start) > 4*time.Second {
		t.Error("the command was not stopped in time")
	}
}

func TestTimeoutEndsWhatClaudeStarted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pid")
	r := fakeClaude(t, "", "", "30s", "0")
	t.Setenv("SLIFE_FAKE_CHILD", path)
	r.Waits.Change = 500 * time.Millisecond
	if err := r.UpdateCatalog(context.Background()); err == nil {
		t.Fatal("no timeout")
	}
	b, err := os.ReadFile(path)
	pid, _ := strconv.Atoi(string(b))
	if err != nil || pid <= 0 {
		t.Fatal("the fake did not start its child", err)
	}
	for i := 0; syscall.Kill(pid, 0) != syscall.ESRCH; i++ {
		if i > 50 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatal("the child of claude outlived the timeout")
		}
		time.Sleep(100 * time.Millisecond)
	}
}
