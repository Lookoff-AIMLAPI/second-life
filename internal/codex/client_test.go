package codex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var bg = context.Background()

func running(c *Client) *proc {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.proc
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); !ok(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("never: " + what)
		}
	}
}

func TestAccountAndLogin(t *testing.T) {
	c := fake(t, "ok")
	a, err := c.Account(bg)
	if err != nil || a == nil || a.Plan != "plus" {
		t.Fatal(a, err)
	}
	if u, err := c.Login(bg); err != nil || u != "https://auth.openai.com/x" {
		t.Fatal(u, err)
	}
	if got, err := os.Getwd(); err != nil || running(c).cmd.Dir != c.Dir || got == c.Dir {
		t.Fatal("not in its private directory")
	}
}

func TestHungChildIsReplaced(t *testing.T) {
	c := fake(t, "hangs")
	c.limits = timeouts{call: 100 * time.Millisecond, probe: 100 * time.Millisecond}
	p, err := c.get(bg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Account(bg); err == nil {
		t.Fatal("a hung child answered")
	}
	eventually(t, "the hung child is dropped", func() bool { return running(c) == nil })
	eventually(t, "the hung child is gone", func() bool {
		select {
		case <-p.done:
			return true
		default:
			return false
		}
	})
	os.Setenv("SLIFE_FAKE", "ok") // restored by fake
	c.limits = timeouts{}
	if _, err := c.Account(bg); err != nil {
		t.Fatal(err)
	}
}

func TestSlowCallDoesNotKillTheChild(t *testing.T) {
	c := fake(t, "slow")
	if _, err := c.Account(bg); err != nil {
		t.Fatal(err)
	}
	p := running(c)
	c.limits = timeouts{call: 200 * time.Millisecond}
	if _, _, err := c.Limits(bg); err == nil {
		t.Fatal("no timeout")
	}
	eventually(t, "the probe ended", func() bool { return !p.checking.Load() })
	if running(c) != p {
		t.Fatal("a child that still answers was replaced")
	}
}

func TestCancelledCallerReturnsAtOnce(t *testing.T) {
	c := fake(t, "hangs")
	go c.Credentials(bg, "")
	eventually(t, "a fetch runs", func() bool { c.credMu.Lock(); defer c.credMu.Unlock(); return c.flight != nil && running(c) != nil })
	ctx, cancel := context.WithTimeout(bg, 50*time.Millisecond)
	defer cancel()
	began := time.Now()
	if _, err := c.Credentials(ctx, ""); err == nil || time.Since(began) > time.Second {
		t.Fatal(time.Since(began), err)
	}
	// A child that stops reading blocks nobody either.
	ctx, cancel = context.WithTimeout(bg, 50*time.Millisecond)
	defer cancel()
	big := strings.Repeat("x", 1<<20)
	began = time.Now()
	for range 3 {
		_ = running(c).write(ctx, big)
	}
	if time.Since(began) > time.Second {
		t.Fatal("write ignored the context")
	}
}

func TestOneCallersCancelDoesNotEndTheStart(t *testing.T) {
	c := fake(t, "ok")
	ctx, cancel := context.WithTimeout(bg, time.Millisecond)
	defer cancel()
	_, _ = c.Account(ctx)
	if a, err := c.Account(bg); err != nil || a == nil {
		t.Fatal(a, err)
	}
}

func TestCloseIsQuick(t *testing.T) {
	for _, mode := range []string{"mute", "hangs", "ok"} {
		t.Run(mode, func(t *testing.T) {
			c := fake(t, mode)
			go c.Account(bg)
			time.Sleep(300 * time.Millisecond)
			began := time.Now()
			c.Close()
			if d := time.Since(began); d > 2800*time.Millisecond { // the tray waits 3 s
				t.Fatal(d)
			}
			if running(c) != nil {
				t.Fatal("still there")
			}
		})
	}
}

func TestNothingStartsForACancelledCaller(t *testing.T) {
	pid := filepath.Join(t.TempDir(), "pid")
	t.Setenv("SLIFE_FAKE_PID", pid)
	c := fake(t, "ok")
	ctx, cancel := context.WithCancel(bg)
	cancel()
	c.Close()
	_, _ = c.Account(ctx)
	_, _ = c.Login(ctx)
	_, _ = c.Credentials(ctx, "")
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(pid); err == nil || running(c) != nil {
		t.Fatal("a child was started")
	}
}

func TestOneRefreshForManyRejections(t *testing.T) {
	log := filepath.Join(t.TempDir(), "log")
	t.Setenv("SLIFE_FAKE_LOG", log)
	c := fake(t, "ok")
	first, err := c.Credentials(bg, "")
	if err != nil || first.Token != fakeToken(0) || first.AccountID != "acct" {
		t.Fatal(first, err)
	}
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			if got, err := c.Credentials(bg, first.Token); err != nil || got.Token != fakeToken(1) {
				t.Error(got, err)
			}
		})
	}
	wg.Wait()
	// The fresh token rejected as well: no second refresh so soon.
	if got, err := c.Credentials(bg, fakeToken(1)); err != nil || got.Token != fakeToken(1) {
		t.Fatal(got, err)
	}
	// A late rejection of the old one needs no refresh at all.
	if got, err := c.Credentials(bg, first.Token); err != nil || got.Token != fakeToken(1) {
		t.Fatal(got, err)
	}
	b, _ := os.ReadFile(log)
	if n := strings.Count(string(b), "getAuthStatus refresh=true"); n != 1 {
		t.Fatalf("%d forced refreshes", n)
	}
	// A new login forgets the gap.
	c.forget()
	if got, err := c.Credentials(bg, fakeToken(1)); err != nil || got.Token != fakeToken(2) {
		t.Fatal(got, err)
	}
}

func TestToolEnvHasNoCurrentDirectory(t *testing.T) {
	for _, path := range []string{"", string(os.PathListSeparator) + "x" + string(os.PathListSeparator), "."} {
		t.Setenv("PATH", path)
		for _, kv := range ToolEnv(filepath.Join(t.TempDir(), "codex")) {
			v, ok := strings.CutPrefix(kv, "PATH=")
			if !ok {
				continue
			}
			for _, d := range filepath.SplitList(v) {
				if d == "" || d == "." {
					t.Errorf("PATH %q gives %q", path, v)
				}
			}
		}
	}
}

func TestShimTarget(t *testing.T) {
	for text, want := range map[string]string{
		"IF EXIST \"%dp0%\\node.exe\" (\r\n  SET \"_prog=%dp0%\\node.exe\"\r\n)\r\n\"%_prog%\"  \"%dp0%\\node_modules\\@openai\\codex\\bin\\codex.js\" %*": `node_modules\@openai\codex\bin\codex.js`,
		"\"%dp0%\\node_modules\\@anthropic-ai\\claude-code\\bin\\claude.exe\"   %*":                                                                        `node_modules\@anthropic-ai\claude-code\bin\claude.exe`,
		"@IF EXIST \"%~dp0\\node.exe\" (\r\n  \"%~dp0\\node.exe\"  \"%~dp0\\..\\x\\cli.mjs\" %*\r\n) ELSE (\r\n  node  \"%~dp0\\..\\x\\cli.mjs\" %*\r\n)":  `..\x\cli.mjs`,
		"@echo hello": "",
	} {
		if got := shimTarget(text); got != want {
			t.Errorf("got %q want %q", got, want)
		}
	}
}

func TestUnsafeForBatch(t *testing.T) {
	for arg, want := range map[string]bool{"app-server": false, `C:\Program Files\x.cmd`: false, "-p": false, "a&calc": true, `"`: true, "%PATH%": true, "a\nb": true, "!x!": true} {
		if unsafeForBatch([]string{arg}) != want {
			t.Errorf("%q", arg)
		}
	}
	if !isBatch(`a.CMD`) || !isBatch("a.bat") || isBatch("a.exe") || isBatch("a") {
		t.Error("isBatch")
	}
}
