package keystore

import (
	"context"
	"crypto/rand"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestKeychain uses a keychain file of its own, never the login one.
func TestKeychain(t *testing.T) {
	run := func(args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, securityBin, args...).Output()
		return string(out), err
	}
	before, err := run("list-keychains")
	if err != nil {
		t.Skip("no Keychain here")
	}
	k := keychain{filepath.Join(t.TempDir(), "a test.keychain-db"), aimlapi, service}
	t.Cleanup(func() { run("delete-keychain", k.path) }) // first, so it goes even when creating half failed
	if _, err := run("create-keychain", "-p", rand.Text(), k.path); err != nil {
		t.Skip("no keychain can be created here")
	}
	if after, _ := run("list-keychains"); after != before {
		run("delete-keychain", k.path) // it also leaves the search list this way
		t.Skip("a new keychain joins the search list here")
	}
	roundTrip(t, k)
	e := keychain{k.path, testID, service}
	endpointTrip(t, e)
	if err := errors.Join(k.set("0123456789abcdef"), e.set("x"), e.delete()); err != nil {
		t.Error(err)
	}
	if got, err := k.get(); err != nil || got != "0123456789abcdef" {
		t.Error("the AIMLAPI key went with the key of an endpoint", err)
	}
	if err := (keychain{k.path + `"x`, aimlapi, service}).set("0123456789abcdef"); err == nil {
		t.Error("a quote in the path")
	}
}

func TestNativeIsTheLoginKeychain(t *testing.T) {
	b, err := native(aimlapi) // no call to the Keychain
	i, ok := b.(inherited)
	k, _ := i.now.(keychain)
	old, _ := i.then.(keychain)
	if err != nil || !ok || !strings.HasSuffix(k.path, "/Library/Keychains/login.keychain-db") || k.account != "aimlapi" || k.service != "second-life" || old.service != "codex-to-claude" || old.account != k.account || old.path != k.path {
		t.Errorf("%v %v", b, err)
	}
}
