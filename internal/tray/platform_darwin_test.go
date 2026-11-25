package tray

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLockDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), appDir)
	release, ok, err := lockDir(dir)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if _, ok, err := lockDir(dir); ok || err != nil {
		t.Errorf("second lock: %v %v", ok, err)
	}
	release()
	release, ok, err = lockDir(dir)
	if err != nil || !ok {
		t.Fatalf("after release: %v %v", ok, err)
	}
	release()
	if st, err := os.Stat(dir); err != nil || st.Mode().Perm() != 0o700 {
		t.Errorf("%v %v", st.Mode(), err)
	}
}

// A stand-in for pbcopy records what it got; the real clipboard stays alone.
func TestPipe(t *testing.T) {
	dir := t.TempDir()
	bin, out := filepath.Join(dir, "copy"), filepath.Join(dir, "out")
	script := "#!/bin/sh\ncat > " + shQuote(out) + "\necho \"$#\" > " + shQuote(out+".argc") + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := pipe(bin, testKey); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(out); string(got) != testKey {
		t.Errorf("stdin %q", got)
	}
	if got, _ := os.ReadFile(out + ".argc"); string(got) != "0\n" {
		t.Errorf("arguments: %q", got)
	}
	err := pipe(filepath.Join(dir, "missing"), testKey)
	if err == nil || strings.Contains(err.Error(), testKey) {
		t.Error("a failed copy must fail without the key in its error")
	}
}

// The copy goes to a pasteboard of its own; the owner's clipboard stays alone.
func TestCopySecret(t *testing.T) {
	name := fmt.Sprintf("io.github.lookoff-aimlapi.second-life.test.%d.%d", os.Getpid(), time.Now().UnixNano())
	js := func(body string) string {
		t.Helper()
		out, err := pipeOut(osascript, "", "-l", "JavaScript", "-e", pasteboard+"function run(argv) { const p = board(argv[0]); "+body+" }", name)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	t.Cleanup(func() { js("p.releaseGlobally; return ''") })
	const text = `return ObjC.unwrap(p.stringForType("public.utf8-plain-text")) || "none";`

	clear, err := copySecret(name, testKey)
	if err != nil {
		t.Fatal(err)
	}
	if got := js(text); got != testKey {
		t.Errorf("pasteboard holds %q", got)
	}
	if got := js("return ObjC.deepUnwrap(p.types).join();"); !strings.Contains(got, "org.nspasteboard.ConcealedType") {
		t.Errorf("not marked: %s", got)
	}
	clear()
	if got := js(text); got != "none" {
		t.Errorf("not cleared: %q", got)
	}

	if clear, err = copySecret(name, testKey); err != nil {
		t.Fatal(err)
	}
	js(`p.clearContents; p.setStringForType("other", "public.utf8-plain-text"); return "";`)
	clear()
	if got := js(text); got != "other" {
		t.Errorf("a later copy was cleared: %q", got)
	}
}
