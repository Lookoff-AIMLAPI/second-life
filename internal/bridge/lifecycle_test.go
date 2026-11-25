package bridge

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Lookoff-AIMLAPI/second-life/internal/codex"
)

// hangAuth answers nothing until the context ends.
type hangAuth struct {
	fakeAuth
	closed, logins atomic.Int32
}

func (h *hangAuth) Account(ctx context.Context) (*codex.Account, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (h *hangAuth) Login(ctx context.Context) (string, error) {
	h.logins.Add(1)
	<-ctx.Done()
	return "", ctx.Err()
}

func (h *hangAuth) Close() { h.closed.Add(1) }

func quiet(t *testing.T, a Auth) *Bridge {
	t.Helper()
	b, err := New(Options{Home: t.TempDir(), Addr: "127.0.0.1:0", Auth: a, Log: log.New(io.Discard, "", 0)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Stop)
	return b
}

func TestStopEndsALogin(t *testing.T) {
	a := &hangAuth{}
	b := quiet(t, a)
	if err := b.Start(); err != nil {
		t.Fatal(err)
	}
	got := make(chan error, 1)
	go func() { _, err := b.Login(); got <- err }()
	for a.logins.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	began := time.Now()
	b.Stop()
	if err := <-got; !errors.Is(err, context.Canceled) || time.Since(began) > 2*time.Second {
		t.Fatal(err, time.Since(began))
	}
	if b.Status().State != StateOff || a.closed.Load() != 1 {
		t.Fatal(b.Status().State, a.closed.Load())
	}
	// Off: a login would start an app-server nobody ends.
	if _, err := b.Login(); err == nil || a.logins.Load() != 1 {
		t.Fatal("login while off")
	}
	if b.Stop(); a.closed.Load() != 2 {
		t.Fatal("Stop while off must still close the CLI")
	}
}

func TestStopStaysOff(t *testing.T) {
	for i := range 300 {
		b := quiet(t, &fakeAuth{})
		if err := b.Start(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Duration(i%40) * 10 * time.Microsecond)
		b.Stop()
		if s := b.Status().State; s != StateOff {
			t.Fatal(s)
		}
	}
}

func TestNoWatchWithoutAListener(t *testing.T) {
	b := quiet(t, &fakeAuth{})
	if err := b.Start(); err != nil {
		t.Fatal(err)
	}
	b.srv.Close() // Serve reports ErrServerClosed
	b.tasks.Wait()
	if b.run.Err() == nil {
		t.Fatal("still watching without a listener")
	}
}

func TestServerLimits(t *testing.T) {
	b := quiet(t, &fakeAuth{})
	if err := b.Start(); err != nil {
		t.Fatal(err)
	}
	if b.srv.IdleTimeout <= 0 || b.srv.ReadHeaderTimeout <= 0 || b.srv.ErrorLog != b.log {
		t.Fatal("limits")
	}
}

func TestFixedToken(t *testing.T) {
	home, tok := t.TempDir(), NewToken()
	b, err := New(Options{Home: home, Token: tok, Addr: "127.0.0.1:0", Auth: &fakeAuth{}, Log: log.New(io.Discard, "", 0)})
	if err != nil || b.Token() != tok {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "token")); err == nil {
		t.Fatal("the token of a private run must not reach the disk")
	}
	if err := b.Start(); err != nil {
		t.Fatal(err)
	}
	defer b.Stop()
	for key, want := range map[string]int{tok: 200, NewToken(): 401} {
		r, _ := http.NewRequest("GET", "http://"+b.Status().Addr+"/admin/status", nil)
		r.Header.Set("Authorization", "Bearer "+key)
		resp, err := http.DefaultClient.Do(r)
		if err != nil || resp.StatusCode != want {
			t.Fatal(resp, err)
		}
		resp.Body.Close()
	}
	if _, err := New(Options{Home: home, Token: "short", Auth: &fakeAuth{}}); err == nil {
		t.Fatal("short token")
	}
}

func TestEcho(t *testing.T) {
	var out bytes.Buffer
	home := t.TempDir()
	b, err := New(Options{Home: home, Auth: &fakeAuth{}, Echo: &out})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Stop)
	b.log.Print("line")
	file, _ := os.ReadFile(LogPath(home))
	if !strings.Contains(out.String(), "line") || string(file) != out.String() {
		t.Fatalf("%q %q", out.String(), file)
	}
}

func TestLocalTokenIsWholeOrAbsent(t *testing.T) {
	for range 100 {
		home := t.TempDir()
		var wg sync.WaitGroup
		got := make([]string, 4)
		for i := range got {
			wg.Go(func() {
				tok, err := LocalToken(home)
				if err != nil {
					t.Error(err)
				}
				got[i] = tok
			})
		}
		wg.Wait()
		for _, tok := range got {
			if tok != got[0] || len(tok) != 64 {
				t.Fatalf("%d different tokens or lengths", len(got))
			}
		}
		if left, _ := filepath.Glob(filepath.Join(home, ".token-*")); len(left) > 0 {
			t.Fatal(left)
		}
	}
}

func TestProofIsBoundToTheAddress(t *testing.T) {
	tok := NewToken()
	if Proof(tok, "127.0.0.1:8787", "n") == Proof(tok, "127.0.0.1:8788", "n") {
		t.Fatal("another port can pass on the answer")
	}
	for _, same := range []string{":8787", "0.0.0.0:8787", "[::]:8787", "localhost:8787"} {
		if Proof(tok, same, "n") != Proof(tok, "127.0.0.1:8787", "n") {
			t.Error(same)
		}
	}
	if DialAddr("[::1]:1") != "[::1]:1" || DialAddr("10.0.0.1:1") != "10.0.0.1:1" {
		t.Error("changed a real address")
	}
}

func TestHomeWithoutEnvironment(t *testing.T) {
	t.Setenv("SLIFE_HOME", "")
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	t.Setenv("PATH", "")
	u, err := user.Current()
	if err != nil {
		t.Skip(err)
	}
	if h, err := Home(); err != nil || h != filepath.Join(u.HomeDir, ".second-life") {
		t.Fatal(h, err)
	}
}
