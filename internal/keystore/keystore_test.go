package keystore

import (
	"errors"
	"os"
	"strings"
	"testing"
)

type fake struct {
	key  string
	err  error
	lose bool // forgets what it is given
}

func (f *fake) get() (string, error) {
	if f.err != nil {
		return "", f.err
	}
	if f.key == "" {
		return "", ErrNone
	}
	return f.key, nil
}

func (f *fake) set(k string) error {
	if !f.lose {
		f.key = k
	}
	return f.err
}

func (f *fake) delete() error { f.key = ""; return f.err }

// No test may reach the real store.
func TestMain(m *testing.M) {
	open = func(string) (backend, error) { return nil, errors.New("a test reached for the real store") }
	os.Unsetenv(envVar)
	os.Exit(m.Run())
}

func use(t *testing.T, b backend) {
	t.Helper()
	was := open
	open = func(string) (backend, error) { return b, nil }
	t.Cleanup(func() { open = was })
}

// roundTrip is what every store must do.
func roundTrip(t *testing.T, b backend) {
	t.Helper()
	use(t, b)
	if _, err := Get(); !errors.Is(err, ErrNone) {
		t.Fatalf("an empty store: %v", err)
	}
	if err := Delete(); err != nil {
		t.Fatalf("delete of nothing: %v", err)
	}
	every := ""
	for r := '!'; r <= '~'; r++ {
		every += string(r)
	}
	for _, key := range []string{"0123456789abcdef", every, strings.Repeat("k", maxLen), `";delete-keychain'$(id)'\`, "----------------", "deadbeefdeadbeef00"} {
		if err := Set(key); err != nil {
			t.Fatalf("set %d characters: %v", len(key), err)
		}
		if got, err := Get(); err != nil || got != key {
			t.Fatalf("got %d characters back of %d, %v", len(got), len(key), err)
		}
	}
	if err := Delete(); err != nil {
		t.Fatal(err)
	}
	if _, err := Get(); !errors.Is(err, ErrNone) {
		t.Fatalf("after delete: %v", err)
	}
}

func TestFake(t *testing.T) { roundTrip(t, &fake{}); endpointTrip(t, &fake{}) }

const testID = "ep-0123abcd"

// endpointTrip is roundTrip for the key of an endpoint, which may be short.
func endpointTrip(t *testing.T, b backend) {
	t.Helper()
	use(t, b)
	s := For(testID)
	if _, err := s.Get(); !errors.Is(err, ErrNone) || strings.Contains(err.Error(), "AIMLAPI") {
		t.Fatalf("an empty store: %v", err)
	}
	if err := s.Delete(); err != nil {
		t.Fatalf("delete of nothing: %v", err)
	}
	for _, key := range []string{"x", "0", "aa", "ollama", "~", `"'\`, "-w", strings.Repeat("k", maxLen)} {
		if err := s.Set(key); err != nil {
			t.Fatalf("set %d characters: %v", len(key), err)
		}
		if got, err := s.Get(); err != nil || got != key {
			t.Fatalf("got %d characters back of %d, %v", len(got), len(key), err)
		}
	}
	if err := s.Delete(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(); !errors.Is(err, ErrNone) {
		t.Fatalf("after delete: %v", err)
	}
}

func TestEndpointShape(t *testing.T) {
	f := &fake{}
	use(t, f)
	s := For(testID)
	for _, bad := range []string{"", " ", "a b", "a\nb", "é", "a\x00b", "a\x7f", strings.Repeat("k", maxLen+1)} {
		if err := s.Set(bad); !errors.Is(err, ErrShape) || strings.Contains(err.Error(), "AIMLAPI") || f.key != "" {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if err := s.Set(" x\n"); err != nil || f.key != "x" {
		t.Errorf("%q %v", f.key, err)
	}
	if err := Set("x"); !errors.Is(err, ErrShape) {
		t.Error("a short AIMLAPI key:", err)
	}
	f.key = "damaged key"
	if k, err := s.Get(); k != "" || !errors.Is(err, errDamaged) || strings.Contains(err.Error(), "AIMLAPI") || strings.Contains(err.Error(), f.key) {
		t.Errorf("%q %v", k, err)
	}
}

func TestEndpointIgnoresTheEnvironment(t *testing.T) {
	use(t, &fake{})
	t.Setenv(envVar, "env-0123456789abc")
	if k, err := For(testID).Get(); k != "" || !errors.Is(err, ErrNone) {
		t.Errorf("%q %v", k, err)
	}
	if k, err := For("aimlapi").Get(); err != nil || k != "env-0123456789abc" {
		t.Errorf("%q %v", k, err)
	}
}

// Every id has its own place, and the AIMLAPI key stays where it was.
func TestIDs(t *testing.T) {
	places := map[string]*fake{}
	was := open
	open = func(account string) (backend, error) {
		if places[account] == nil {
			places[account] = &fake{}
		}
		return places[account], nil
	}
	t.Cleanup(func() { open = was })
	if err := errors.Join(Set("0123456789abcdef"), For(testID).Set("one"), For("ep-ffffffff").Set("two")); err != nil {
		t.Fatal(err)
	}
	if len(places) != 3 || places["aimlapi"].key != "0123456789abcdef" || places[testID].key != "one" || places["ep-ffffffff"].key != "two" {
		t.Fatalf("%+v", places)
	}
	if err := For(testID).Delete(); err != nil {
		t.Fatal(err)
	}
	if k, _ := Get(); k != "0123456789abcdef" {
		t.Error("the AIMLAPI key went too")
	}
	if k, _ := For("ep-ffffffff").Get(); k != "two" {
		t.Error("the other key went too")
	}
	for _, bad := range []string{"", "codex", "AIMLAPI", "aimlapi ", "ep-0123abc", "ep-0123ABCD", "ep-0123abcde", "ep-0123abcd\n", "../x", "ep-0123abcd -w", "x/ep-0123abcd"} {
		s := For(bad)
		_, err := s.Get()
		for _, err := range []error{err, s.Set("0123456789abcdef"), s.Delete()} {
			if !errors.Is(err, errID) {
				t.Errorf("%q: %v", bad, err)
			}
		}
	}
	if len(places) != 3 {
		t.Error("a bad id reached the store")
	}
}

func TestShape(t *testing.T) {
	f := &fake{}
	use(t, f)
	for _, bad := range []string{"", "short", "0123456789 abcdef", "0123456789abcde\n", "0123456789abcdé", "0123456789abc\x00def", strings.Repeat("k", maxLen+1)} {
		if err := Set(bad); !errors.Is(err, ErrShape) || bad != "" && strings.Contains(err.Error(), bad) || f.key != "" {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if err := Set("  0123456789abcdef\n"); err != nil || f.key != "0123456789abcdef" {
		t.Errorf("%q %v", f.key, err)
	}
	f.key = "damaged key with spaces"
	if k, err := Get(); k != "" || err == nil || strings.Contains(err.Error(), f.key) {
		t.Errorf("%q %v", k, err)
	}
}

func TestEnvWins(t *testing.T) {
	f := &fake{key: "stored-0123456789"}
	use(t, f)
	t.Setenv(envVar, " env-0123456789abc\n")
	if k, err := Get(); err != nil || k != "env-0123456789abc" {
		t.Errorf("%q %v", k, err)
	}
	t.Setenv(envVar, "bad key")
	if k, err := Get(); k != "" || err == nil || strings.Contains(err.Error(), "bad key") {
		t.Errorf("%q %v", k, err)
	}
	t.Setenv(envVar, " ")
	if k, err := Get(); err != nil || k != f.key {
		t.Errorf("%q %v", k, err)
	}
}

func TestFailures(t *testing.T) {
	boom := errors.New("locked")
	use(t, &fake{err: boom})
	if _, err := Get(); !errors.Is(err, boom) {
		t.Error(err)
	}
	if err := Set("0123456789abcdef"); !errors.Is(err, boom) {
		t.Error(err)
	}
	if err := Delete(); !errors.Is(err, boom) {
		t.Error(err)
	}
	use(t, &fake{lose: true})
	if err := Set("0123456789abcdef"); err == nil {
		t.Error("a key that was not saved")
	}
	open = func(string) (backend, error) { return nil, boom }
	for _, err := range []error{Set("0123456789abcdef"), Delete()} {
		if !errors.Is(err, boom) {
			t.Error(err)
		}
	}
}

func TestInherited(t *testing.T) {
	now, then := &fake{}, &fake{key: "old-key-0123456789"}
	i := inherited{now, then}
	if k, err := i.get(); err != nil || k != "old-key-0123456789" {
		t.Fatal(k, err)
	}
	if err := i.set("new-key-0123456789"); err != nil {
		t.Fatal(err)
	}
	if k, _ := i.get(); k != "new-key-0123456789" || then.key != "old-key-0123456789" {
		t.Fatal(k, then.key)
	}
	if err := i.delete(); err != nil {
		t.Fatal(err)
	}
	if k, err := i.get(); !errors.Is(err, ErrNone) {
		t.Fatal("came back:", k, err)
	}
}
