package tray

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Lookoff-AIMLAPI/second-life/internal/bridge"
	"github.com/Lookoff-AIMLAPI/second-life/internal/desktop"
)

type fakeController struct {
	starts, stops, logins atomic.Int32
	start                 func() error
	stop                  func()
	status                func() bridge.Status
}

func (f *fakeController) Start() error {
	f.starts.Add(1)
	if f.start != nil {
		return f.start()
	}
	return nil
}

func (f *fakeController) Stop() {
	f.stops.Add(1)
	if f.stop != nil {
		f.stop()
	}
}

func (f *fakeController) Status() bridge.Status {
	if f.status != nil {
		return f.status()
	}
	return bridge.Status{State: bridge.StateOff}
}

func (f *fakeController) Login() (string, error) {
	f.logins.Add(1)
	return "https://example.com/", nil
}

const testKey = "0123456789abcdef-local-bearer"

func (f *fakeController) Token() string { return testKey }

func (f *fakeController) PickerURL() string { return "http://127.0.0.1:8787/picker/#x" }

func (f *fakeController) SetDesktop(func() bool, func() error) {}

func (f *fakeController) Mapping() [][2]string { return nil }

func testActions() actions {
	return actions{
		openURL:      func(string) error { return nil },
		openClaude:   func() error { return nil },
		installCodex: func() error { return nil },
		importMCP:    func() error { return nil },
		autostart:    func() bool { return false },
		setAutostart: func(bool) error { return nil },
		copyText:     func(string) error { return nil },
		copySecret:   func(string) (func(), error) { return func() {}, nil },
		openLog:      func() error { return nil },
		desktop:      func() desktopState { return desktopState{} },
		setupDesktop: func(context.Context, [][2]string) error { return nil },
		leaveDesktop: func(context.Context) error { return nil },
		alert:        func(string, string) {},
		startWarning: func() string { return "" },
	}
}

// redraw does what draw does, without a menu.
func redraw(a *app) view {
	v := buildView(a.snapshot(), time.Now())
	a.shown(v)
	return v
}

func TestDoubleClickStartsOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := &fakeController{start: func() error { time.Sleep(time.Second); return nil }}
		a := newApp(c, testActions(), "")
		redraw(a)
		if !a.toggle() || a.toggle() || a.toggle() {
			t.Error("the guard let a second click through")
		}
		if !a.signIn() {
			t.Error("a start blocks sign in")
		}
		time.Sleep(2 * time.Second)
		if n := c.starts.Load(); n != 1 || c.stops.Load() != 0 {
			t.Errorf("%d starts, %d stops", n, c.stops.Load())
		}
		if v := redraw(a); v.TurnsOn {
			t.Error("still offers to turn on")
		}
		a.toggle()
		a.toggle()
		synctest.Wait()
		if c.stops.Load() != 1 || !redraw(a).TurnsOn {
			t.Error("not stopped once")
		}
	})
}

func TestAutoStart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := &fakeController{}
		a := newApp(c, testActions(), "")
		a.setPower(true)
		if a.setPower(true) {
			t.Error("a second request was taken")
		}
		synctest.Wait()
		if a.toggle(); redraw(a).TurnsOn {
			t.Error("offers to turn on")
		}
		synctest.Wait()
		if n := c.starts.Load(); n != 1 || c.stops.Load() != 0 {
			t.Errorf("%d starts, %d stops", n, c.stops.Load())
		}
	})
}

func TestTurnOffRightAfterAutoStartWins(t *testing.T) {
	for name, wait := range map[string]time.Duration{"at once": 0, "during Start": 300 * time.Millisecond} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var on atomic.Bool
				c := &fakeController{
					start: func() error { time.Sleep(time.Second); on.Store(true); return nil },
					stop:  func() { on.Store(false) },
				}
				a := newApp(c, testActions(), "")
				a.setPower(true)
				if v := redraw(a); v.TurnsOn || !v.Items[itemToggle].Enabled {
					t.Error("cannot turn off while starting")
				}
				time.Sleep(wait)
				if !a.toggle() {
					t.Error("turn off was dropped")
				}
				if !redraw(a).TurnsOn {
					t.Error("the label did not follow")
				}
				time.Sleep(5 * time.Second)
				if on.Load() || a.power.busy() || c.starts.Load() > 1 {
					t.Errorf("on=%v busy=%v starts=%d", on.Load(), a.power.busy(), c.starts.Load())
				}
				// And it can be turned on again.
				a.toggle()
				time.Sleep(2 * time.Second)
				if !on.Load() {
					t.Error("does not turn on again")
				}
			})
		})
	}
}

func TestFailedStartShowsAndRetries(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := &fakeController{start: func() error { return errors.New("port in use") }}
		a := newApp(c, testActions(), "")
		a.setPower(true)
		synctest.Wait()
		if v := redraw(a); !v.TurnsOn || v.Items[itemError].Title != "Last error: port in use" || a.ctx.Err() != nil {
			t.Errorf("%+v", v)
		}
		c.start = nil
		a.toggle()
		synctest.Wait()
		if v := redraw(a); v.TurnsOn || v.Items[itemError].Visible || c.starts.Load() != 2 {
			t.Errorf("%+v", v)
		}
	})
}

func TestCopy(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var got []string
		act := testActions()
		act.copyText = func(s string) error { got = append(got, s); return nil }
		var cleared atomic.Int32
		act.copySecret = func(s string) (func(), error) { got = append(got, s); return func() { cleared.Add(1) }, nil }
		a := newApp(&fakeController{}, act, "")
		a.status = bridge.Status{State: bridge.StateOn, Addr: "127.0.0.1:8787"}
		a.copyAddr()
		synctest.Wait()
		if v := redraw(a); v.Items[itemCopyAddr].Title != "Copied" || v.Items[itemCopyKey].Title != "Copy API key" {
			t.Errorf("%+v", v.Items)
		}
		time.Sleep(time.Second)
		a.copyKey()
		synctest.Wait()
		if !slices.Equal(got, []string{"http://127.0.0.1:8787", testKey}) {
			t.Error("wrong text copied")
		}
		time.Sleep(copiedFor - time.Second - time.Millisecond)
		if redraw(a).Items[itemCopyAddr].Title != "Copied" {
			t.Error("Copied ended early")
		}
		for len(a.changed) > 0 {
			<-a.changed
		}
		time.Sleep(time.Millisecond)
		synctest.Wait()
		if len(a.changed) == 0 {
			t.Error("no redraw asked for at 2 s")
		}
		if v := redraw(a); v.Items[itemCopyAddr].Title != "Copy API address" || v.Items[itemCopyKey].Title != "Copied" {
			t.Errorf("%+v", v.Items)
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if redraw(a).Items[itemCopyKey].Title != "Copy API key" {
			t.Error("Copied stayed")
		}
		time.Sleep(keyLives - 3*time.Second)
		synctest.Wait()
		if cleared.Load() != 0 {
			t.Error("the key went before 60 s")
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if cleared.Load() != 1 {
			t.Errorf("cleared %d times after 60 s", cleared.Load())
		}
	})
}

func TestKeyNeverInTheMenu(t *testing.T) {
	for _, fail := range []bool{false, true} {
		for _, state := range []bridge.State{bridge.StateOff, bridge.StateStarting, bridge.StateOn, bridge.StateNeedsLogin, bridge.StateCodexMissing, bridge.StateError} {
			synctest.Test(t, func(t *testing.T) {
				act := testActions()
				act.copyText = func(string) error { t.Error("the key was copied without its marks"); return nil }
				act.copySecret = func(s string) (func(), error) {
					if fail {
						return nil, errors.New("cannot copy " + s)
					}
					return func() {}, nil
				}
				a := newApp(&fakeController{}, act, "1.0")
				a.status = bridge.Status{State: state, Addr: "127.0.0.1:8787", SignedIn: true, Plan: "pro", LastError: "x"}
				a.copyKey()
				synctest.Wait()
				v := redraw(a)
				if s := fmt.Sprintf("%+v", v); strings.Contains(s, testKey) || strings.Contains(s, testKey[:8]) {
					t.Errorf("%s fail=%v: the key is in the menu", state, fail)
				}
				if fail && v.Items[itemError].Title != "Last error: could not copy to the clipboard" {
					t.Errorf("%+v", v.Items[itemError])
				}
				if (v.Items[itemCopyKey].Title == "Copied") == fail {
					t.Errorf("fail=%v: %+v", fail, v.Items[itemCopyKey])
				}
				time.Sleep(keyLives)
			})
		}
	}
}

func TestNothingToCopy(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		act := testActions()
		act.copyText = func(string) error { t.Error("cleared the clipboard"); return nil }
		a := newApp(&fakeController{}, act, "")
		a.copyAddr()
		synctest.Wait()
		if got := redraw(a).Items[itemError].Title; got != "Last error: nothing to copy yet" {
			t.Error(got)
		}
	})
}

func TestSignInOpensTheLink(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var opened []string
		act := testActions()
		act.openURL = func(u string) error { opened = append(opened, u); return nil }
		act.installCodex = func() error { opened = append(opened, "installer"); return nil }
		a := newApp(&fakeController{}, act, "")
		a.signIn()
		synctest.Wait()
		a.install()
		synctest.Wait()
		if !slices.Equal(opened, []string{"https://example.com/", "installer"}) {
			t.Errorf("%q", opened)
		}
	})
}

func TestPollEveryTwoSeconds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var polls atomic.Int32
		a := newApp(&fakeController{status: func() bridge.Status {
			polls.Add(1)
			return bridge.Status{State: bridge.StateOn}
		}}, testActions(), "")
		go a.poll()
		time.Sleep(5 * time.Second)
		synctest.Wait()
		if n := polls.Load(); n != 3 { // 0 s, 2 s, 4 s
			t.Errorf("%d polls", n)
		}
		if a.snapshot().Status.State != bridge.StateOn {
			t.Error("status not stored")
		}
		a.cancel()
	})
}

// Real time: synctest cannot step over callers waiting in sync.Once.
func TestShutdown(t *testing.T) {
	var log []string // Start ends before Stop begins, so no lock is needed
	c := &fakeController{
		start: func() error { time.Sleep(50 * time.Millisecond); log = append(log, "started"); return nil },
		stop:  func() { log = append(log, "stopped") },
	}
	a := newApp(c, testActions(), "")
	a.setPower(true)
	time.Sleep(10 * time.Millisecond)
	var wg sync.WaitGroup
	for range 3 {
		wg.Go(a.shutdown)
	}
	wg.Wait()
	if !slices.Equal(log, []string{"started", "stopped"}) {
		t.Errorf("%q", log)
	}
	if a.setPower(false) || a.setPower(true) || c.starts.Load() != 1 {
		t.Error("started after shutdown")
	}
}

func TestShutdownDoesNotHang(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		block := make(chan struct{})
		c := &fakeController{
			start: func() error { <-block; return nil },
			stop:  func() { <-block },
		}
		a := newApp(c, testActions(), "")
		a.setPower(true)
		synctest.Wait()
		begin := time.Now()
		a.shutdown()
		if d := time.Since(begin); d != quitWaitBusy+quitWaitStop || d >= 5*time.Second {
			t.Errorf("took %v", d)
		}
		close(block)
	})
}

func TestShutdownClearsTheKey(t *testing.T) {
	var cleared atomic.Int32
	act := testActions()
	act.copySecret = func(string) (func(), error) { return func() { cleared.Add(1) }, nil }
	a := newApp(&fakeController{}, act, "")
	if err := a.putKey(testKey); err != nil {
		t.Fatal(err)
	}
	a.shutdown()
	if cleared.Load() != 1 {
		t.Error("the key stayed on the clipboard")
	}
}

func TestDesktopActions(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var alerts []string
		act := testActions()
		act.alert = func(_, text string) { alerts = append(alerts, text) }
		act.setupDesktop = func(_ context.Context, m [][2]string) error {
			time.Sleep(time.Second)
			return errors.New("Claude did not quit")
		}
		a := newApp(&fakeController{}, act, "")
		a.desktop = desktopState{Installed: true, ThirdParty: true}
		a.status = bridge.Status{State: bridge.StateOn, SignedIn: true}
		if !a.setupDesktop() || a.setupDesktop() || a.leaveDesktop() {
			t.Error("two switches at once")
		}
		synctest.Wait()
		if v := redraw(a); v.Items[itemDesktop].Enabled || v.Items[itemDesktopBack].Enabled {
			t.Error("clickable while busy")
		}
		if !a.toggle() || !a.openLog() {
			t.Error("a switch blocks other items")
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if !slices.Equal(alerts, []string{"Claude did not quit"}) || redraw(a).Items[itemError].Title != "Last error: Claude did not quit" {
			t.Errorf("%q", alerts)
		}

		a.act.leaveDesktop = func(context.Context) error { return desktop.ErrCancelled }
		a.leaveDesktop()
		synctest.Wait()
		if len(alerts) != 1 || redraw(a).Items[itemError].Visible {
			t.Error("cancel is shown as an error")
		}
	})
}

func TestDesktopSwitchStopsAtQuit(t *testing.T) {
	act := testActions()
	ended := make(chan error, 1)
	act.setupDesktop = func(ctx context.Context, _ [][2]string) error {
		<-ctx.Done()
		ended <- ctx.Err()
		return ctx.Err()
	}
	act.alert = func(string, string) { t.Error("alert while quitting") }
	a := newApp(&fakeController{}, act, "")
	a.setupDesktop()
	a.shutdown()
	if err := <-ended; !errors.Is(err, context.Canceled) {
		t.Error(err)
	}
	if a.leaveDesktop() {
		t.Error("started after shutdown")
	}
}

func TestWarnAtStart(t *testing.T) {
	var shown []string
	act := testActions()
	act.alert = func(_, text string) { shown = append(shown, text) }
	a := newApp(&fakeController{}, act, "")
	a.warnAtStart()
	a.act.startWarning = func() string { return "move me" }
	a.warnAtStart()
	if !slices.Equal(shown, []string{"move me"}) {
		t.Errorf("%q", shown)
	}
}

func TestOpenLog(t *testing.T) {
	home := t.TempDir()
	var opened []string
	open := func(p string) error { opened = append(opened, p); return nil }
	if err := openLog(home, open); err == nil || len(opened) != 0 {
		t.Error("opened a log that is not there")
	}
	path := filepath.Join(home, "logs", "bridge.log")
	os.Mkdir(filepath.Dir(path), 0o700)
	os.WriteFile(path, nil, 0o600)
	if err := openLog(home, open); err != nil || !slices.Equal(opened, []string{path}) {
		t.Errorf("%q %v", opened, err)
	}
}

type fakeItem struct{ calls []string }

func (f *fakeItem) SetTitle(s string) { f.calls = append(f.calls, "title "+s) }
func (f *fakeItem) Enable()           { f.calls = append(f.calls, "enable") }
func (f *fakeItem) Disable()          { f.calls = append(f.calls, "disable") }
func (f *fakeItem) Check()            { f.calls = append(f.calls, "check") }
func (f *fakeItem) Uncheck()          { f.calls = append(f.calls, "uncheck") }
func (f *fakeItem) Show()             { f.calls = append(f.calls, "show") }
func (f *fakeItem) Hide()             { f.calls = append(f.calls, "hide") }

func TestWidgetSendsOnlyChanges(t *testing.T) {
	item := &fakeItem{}
	w := &widget{item: item, cur: entry{Title: "a", Enabled: true, Visible: true}}
	steps := []struct {
		e    entry
		want []string
	}{
		{entry{Title: "a", Enabled: true, Visible: true}, nil},
		{entry{Title: "b", Enabled: true, Visible: true}, []string{"title b"}},
		{entry{Title: "b", Visible: true, Checked: true}, []string{"disable", "check"}},
		// A hidden item gets nothing but the hide, and only once.
		{entry{Title: "c", Enabled: true}, []string{"hide"}},
		{entry{Title: "d"}, nil},
		{entry{Title: "b", Visible: true, Checked: true}, []string{"show"}},
		{entry{}, []string{"hide"}},
		{entry{Title: "e", Enabled: true, Visible: true}, []string{"title e", "enable", "uncheck", "show"}},
	}
	for i, s := range steps {
		item.calls = nil
		w.apply(s.e)
		if !slices.Equal(item.calls, s.want) {
			t.Errorf("step %d: %q, want %q", i, item.calls, s.want)
		}
	}
}

func TestLocalURL(t *testing.T) {
	for u, want := range map[string]bool{
		"http://127.0.0.1:8787/picker/#abc": true, "http://[::1]:8787/picker/#abc": true,
		"": false, "https://127.0.0.1/picker/": false, "http://evil.example/picker/": false, "http://127.0.0.1.evil.example/picker/": false,
		"http://user@127.0.0.1/picker/": false, "http://127.0.0.1/other": false, "http://127.0.0.1/picker/?x=1": false, "file:///picker/": false,
	} {
		if localURL(u) != want {
			t.Error(u)
		}
	}
}
