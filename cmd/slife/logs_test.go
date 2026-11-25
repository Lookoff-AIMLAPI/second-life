package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type safeBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestLogsTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bridge.log")
	if err := showLog(context.Background(), &bytes.Buffer{}, path, false); err == nil {
		t.Error("no log, no error")
	}
	for _, n := range []int{0, 1, 100, 250, 60000} {
		var in strings.Builder
		for i := range n {
			fmt.Fprintf(&in, "line %d with some text \x1b[2J\n", i)
		}
		if err := os.WriteFile(path, []byte(in.String()), 0o600); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := showLog(context.Background(), &out, path, false); err != nil {
			t.Fatal(err)
		}
		if got := strings.Count(out.String(), "\n"); got != min(n, 100) || strings.Contains(out.String(), "\x1b") {
			t.Errorf("%d lines of %d", got, n)
		}
		if n > 0 && !strings.Contains(out.String(), fmt.Sprintf("line %d ", n-1)) {
			t.Error("the last line is missing")
		}
	}
}

func TestLogsFollow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bridge.log")
	ctx, cancel := context.WithCancel(context.Background())
	var out safeBuffer
	done := make(chan error, 1)
	go func() { done <- showLog(ctx, &out, path, true) }()
	see := func(want string) {
		t.Helper()
		for end := time.Now().Add(5 * time.Second); !strings.Contains(out.String(), want); time.Sleep(20 * time.Millisecond) {
			if time.Now().After(end) {
				t.Fatalf("no %q in %q", want, out.String())
			}
		}
	}
	add := func(text string) {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteString(text)
		f.Close()
	}
	add("one\n")
	see("one\n")
	add("two\n")
	see("two\n")
	if err := os.Rename(path, path+".1"); err == nil { // Windows may refuse while it is open
		add("three\n")
		see("three\n")
	}
	if strings.Count(out.String(), "one\n") != 1 {
		t.Error("repeated")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
