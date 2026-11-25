//go:build !windows

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Lookoff-AIMLAPI/second-life/internal/bridge"
)

// The test binary is slife itself with SLIFE_RUN, and the fake claude with SLIFE_CLAUDE.
func init() {
	if mode := os.Getenv("SLIFE_CLAUDE"); mode != "" && os.Getenv("SLIFE_RUN") == "" {
		fakeClaude(mode)
		os.Exit(0)
	}
	if os.Getenv("SLIFE_RUN") != "" {
		os.Unsetenv("SLIFE_RUN")
		main()
		os.Exit(0)
	}
}

func fakeClaude(mode string) {
	switch mode {
	case "report":
		out := map[string]any{"args": os.Args[1:], "url": os.Getenv("ANTHROPIC_BASE_URL"), "key": os.Getenv("ANTHROPIC_AUTH_TOKEN"), "sigint_ignored": signal.Ignored(os.Interrupt)}
		r, _ := http.NewRequest("GET", os.Getenv("ANTHROPIC_BASE_URL")+"/v1/models", nil)
		r.Header.Set("Authorization", "Bearer "+os.Getenv("ANTHROPIC_AUTH_TOKEN"))
		if resp, err := (&http.Client{Transport: &http.Transport{}}).Do(r); err == nil {
			out["status"] = resp.StatusCode
		}
		_ = json.NewEncoder(os.Stdout).Encode(out)
		os.Exit(7)
	case "wait":
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM, syscall.SIGHUP)
		fmt.Println("ready")
		select {
		case s := <-ch:
			fmt.Println("got", s)
		case <-time.After(20 * time.Second):
			fmt.Println("left alone")
		}
	}
}

// slife runs the command in a closed world: its own home, a fake claude and codex, nothing else on PATH.
func slife(t *testing.T, mode string, args ...string) *exec.Cmd {
	t.Helper()
	dir := t.TempDir()
	for name, text := range map[string]string{
		"claude": "#!/bin/sh\nexec '" + os.Args[0] + "' \"$@\"\n",
		"codex":  "#!/bin/sh\nwhile read -r l; do :; done\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = []string{"SLIFE_RUN=1", "SLIFE_CLAUDE=" + mode, "PATH=" + dir + ":/usr/bin:/bin", "HOME=" + t.TempDir(), "SLIFE_HOME=" + filepath.Join(dir, "home"),
		"CODEX_HOME=" + t.TempDir(), "SLIFE_ADDR=127.0.0.1:1"}
	return cmd
}

func TestClaudeGetsItsOwnBridge(t *testing.T) {
	cmd := slife(t, "report", "claude", "-p", "hi")
	var out, errs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errs
	err := cmd.Run()
	if cmd.ProcessState.ExitCode() != 7 {
		t.Fatal(err, errs.String())
	}
	var got struct {
		Args          []string
		URL, Key      string
		Status        int
		SigintIgnored bool `json:"sigint_ignored"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err, out.String())
	}
	if got.Status != 200 || got.SigintIgnored || len(got.Key) != 64 || !strings.HasPrefix(got.URL, "http://127.0.0.1:") || strings.HasSuffix(got.URL, ":1") {
		t.Fatalf("%+v", got)
	}
	if strings.Contains(strings.Join(got.Args, " "), got.Key) || strings.Join(got.Args[2:], " ") != "-p hi" {
		t.Fatal(got.Args)
	}
	home := values(cmd.Env)["SLIFE_HOME"]
	if _, err := os.Stat(filepath.Join(home, "token")); err == nil {
		t.Error("a private run made the shared token")
	}
	if c, err := net.Dial("tcp", strings.TrimPrefix(got.URL, "http://")); err == nil {
		c.Close()
		t.Error("the listener outlived the session")
	}
}

func TestClaudeSignals(t *testing.T) {
	for _, s := range []syscall.Signal{syscall.SIGTERM, syscall.SIGHUP} {
		cmd := slife(t, "wait", "claude")
		pipe, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 6)
		if _, err := io.ReadFull(pipe, buf); err != nil {
			t.Fatal(err)
		}
		_ = cmd.Process.Signal(os.Interrupt) // alone: Claude's own matter
		time.Sleep(200 * time.Millisecond)
		_ = cmd.Process.Signal(s)
		rest, _ := io.ReadAll(pipe)
		if err := cmd.Wait(); err != nil || string(rest) != "got "+s.String()+"\n" {
			t.Fatalf("%v %q", err, rest)
		}
	}
}

func TestServeEndsOnSignals(t *testing.T) {
	for _, s := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP} {
		cmd := slife(t, "", "serve")
		cmd.Env = append(cmd.Env, "SLIFE_ADDR=127.0.0.1:0")
		pipe, err := cmd.StderrPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 12)
		if _, err := io.ReadFull(pipe, buf); err != nil {
			t.Fatal(err)
		}
		began := time.Now()
		_ = cmd.Process.Signal(s)
		_, _ = io.Copy(io.Discard, pipe)
		if err := cmd.Wait(); err != nil || time.Since(began) > 10*time.Second {
			t.Fatal(s, err, time.Since(began))
		}
	}
}

func TestToken(t *testing.T) {
	cmd := slife(t, "", "token")
	home := values(cmd.Env)["SLIFE_HOME"]
	for _, env := range [][]string{cmd.Env, {"SLIFE_RUN=1", "SLIFE_HOME=" + home}} { // the second: no PATH, no HOME
		run := exec.Command(os.Args[0], "token")
		run.Env = env
		var errs bytes.Buffer
		run.Stderr = &errs
		out, err := run.Output()
		file, _ := os.ReadFile(filepath.Join(home, "token"))
		if err != nil || errs.Len() > 0 || len(out) != 64 || string(out) != string(file) {
			t.Fatalf("%v out=%d bytes stderr=%q", err, len(out), errs.String())
		}
	}
}

func TestAliveRefusesARelay(t *testing.T) {
	t.Setenv("SLIFE_HOME", t.TempDir())
	tok := bridge.NewToken()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var mode, real string
	go http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nonce := r.URL.Query().Get("nonce")
		switch mode {
		case "self": // the real one
			fmt.Fprintf(w, `{"proof":%q}`, bridge.Proof(tok, ln.Addr().String(), nonce))
		case "relay":
			fmt.Fprintf(w, `{"proof":%q}`, bridge.Proof(tok, real, nonce))
		case "redirect":
			http.Redirect(w, r, "http://"+real+r.URL.String(), http.StatusFound)
		}
	}))
	other, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	real = other.Addr().String()
	go http.Serve(other, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("followed the redirect") }))
	t.Setenv("SLIFE_ADDR", ln.Addr().String())
	for _, m := range []string{"self", "relay", "redirect"} {
		mode = m
		if ok, err := alive(tok); ok != (m == "self") || (err == nil) != ok {
			t.Error(m, ok, err)
		}
	}
}
