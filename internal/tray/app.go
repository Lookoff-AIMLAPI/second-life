package tray

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/Lookoff-AIMLAPI/second-life/internal/bridge"
	"github.com/Lookoff-AIMLAPI/second-life/internal/desktop"
)

const (
	pollEvery = 2 * time.Second
	copiedFor = 2 * time.Second
	keyLives  = 60 * time.Second // how long the key stays on the clipboard
	// Windows ends a session after about 5 s, so both together stay below it.
	quitWaitBusy = 1500 * time.Millisecond
	quitWaitStop = 3 * time.Second
)

// actions is what the menu does outside the bridge. Tests replace it.
type actions struct {
	openURL      func(string) error
	openLocal    func(string) error // the model picker
	openClaude   func() error
	installCodex func() error // in a terminal window, where it asks first
	importMCP    func() error // the same
	autostart    func() bool
	setAutostart func(bool) error
	copyText     func(string) error // must not repeat the text in its error
	// copySecret marks the text as not for clipboard histories. clear removes it
	// if nothing else was copied since.
	copySecret   func(string) (clear func(), err error)
	openLog      func() error
	desktop      func() desktopState
	setupDesktop func(ctx context.Context, mapping [][2]string) error
	leaveDesktop func(ctx context.Context) error
	alert        func(title, text string)
	startWarning func() string
}

// gate lets one job run at a time and drops the others.
type gate chan struct{}

func newGate() gate { return make(gate, 1) }

func (g gate) tryEnter() bool {
	select {
	case g <- struct{}{}:
		return true
	default:
		return false
	}
}

func (g gate) leave() { <-g }

func (g gate) busy() bool { return len(g) > 0 }

type app struct {
	ctl     Controller
	act     actions
	version string

	power, login, misc, clip, desk gate
	poke                           chan struct{} // ask the bridge now
	changed                        chan struct{} // redraw the menu
	ctx                            context.Context
	cancel                         context.CancelFunc
	stopOnce                       sync.Once

	mu        sync.Mutex
	status    bridge.Status
	startup   bool
	desktop   desktopState
	clearKey  func() // set while the key may be on the clipboard
	actionErr string
	turnsOn   bool                 // what the toggle on screen says
	want      bool                 // what the owner asked for last; a failed Start clears it
	running   bool                 // the last Start worked and no Stop followed
	copied    map[itemID]time.Time // when the "Copied" label ends
}

func newApp(c Controller, act actions, version string) *app {
	ctx, cancel := context.WithCancel(context.Background())
	return &app{
		ctl: c, act: act, version: version,
		power: newGate(), login: newGate(), misc: newGate(), clip: newGate(), desk: newGate(),
		poke: make(chan struct{}, 1), changed: make(chan struct{}, 1), ctx: ctx, cancel: cancel,
		status: bridge.Status{State: bridge.StateOff}, turnsOn: true,
		copied: map[itemID]time.Time{},
	}
}

func (a *app) snapshot() snapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	return snapshot{
		Status: a.status, Autostart: a.startup, ActionErr: a.actionErr,
		LoginBusy: a.login.busy(), Want: a.want, Version: a.version,
		CopiedAddr: now.Before(a.copied[itemCopyAddr]), CopiedKey: now.Before(a.copied[itemCopyKey]),
		Desktop: a.desktop, DesktopBusy: a.desk.busy(),
	}
}

// poll asks the bridge for its state until ctx ends. It has its own goroutine,
// so a slow Status never holds up the menu.
func (a *app) poll() {
	tick := time.NewTicker(pollEvery)
	defer tick.Stop()
	for {
		st, startup, desk := a.ctl.Status(), a.act.autostart(), a.act.desktop()
		a.mu.Lock()
		a.status, a.startup, a.desktop = st, startup, desk
		a.mu.Unlock()
		notify(a.changed)
		select {
		case <-a.ctx.Done():
			return
		case <-tick.C:
		case <-a.poke:
		}
	}
}

func notify(ch chan<- struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// run starts fn on its own goroutine unless g is taken. It reports whether fn
// was started.
func (a *app) run(g gate, fn func() error) bool {
	if a.ctx.Err() != nil || !g.tryEnter() {
		return false
	}
	notify(a.changed)
	go func() {
		err := fn()
		a.mu.Lock()
		a.setErr(err)
		a.mu.Unlock()
		g.leave()
		notify(a.changed)
		notify(a.poke)
	}()
	return true
}

// setErr needs a.mu.
func (a *app) setErr(err error) {
	a.actionErr = ""
	if err != nil {
		a.actionErr = err.Error()
	}
}

// toggle does what the label on screen says, so a second click before the menu
// is redrawn asks for the same thing again and changes nothing.
func (a *app) toggle() bool {
	a.mu.Lock()
	on := a.turnsOn
	a.mu.Unlock()
	return a.setPower(on)
}

// shown records the toggle that is now on screen.
func (a *app) shown(v view) {
	a.mu.Lock()
	a.turnsOn = v.TurnsOn
	a.mu.Unlock()
}

// setPower is the only way to Start or Stop before shutdown. One goroutine does
// the work and the last request wins: a Turn off during a slow Start is carried
// out when that Start returns. It reports whether the request was new.
func (a *app) setPower(on bool) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ctx.Err() != nil || a.want == on {
		return false
	}
	a.want = on
	if a.power.tryEnter() {
		go a.drivePower()
	}
	notify(a.changed)
	return true
}

func (a *app) drivePower() {
	for a.powerStep() {
		notify(a.changed)
		notify(a.poke)
	}
}

// powerStep makes one Start or Stop. It returns false, with the gate free, once
// nothing is left to do; a request cannot slip in between, both hold a.mu.
func (a *app) powerStep() bool {
	a.mu.Lock()
	on := a.want
	if on == a.running || a.ctx.Err() != nil {
		a.power.leave()
		a.mu.Unlock()
		return false
	}
	a.mu.Unlock()

	var err error
	if on {
		err = a.ctl.Start()
	} else {
		a.ctl.Stop()
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	a.running = on && err == nil
	if err != nil {
		a.want = false
	}
	a.setErr(err)
	return true
}

func (a *app) signIn() bool {
	return a.run(a.login, func() error {
		link, err := a.ctl.Login()
		if err != nil {
			return err
		}
		return a.act.openURL(link)
	})
}

func (a *app) importMCP() bool {
	return a.run(a.misc, func() error { return a.act.importMCP() })
}

func (a *app) install() bool {
	return a.run(a.misc, func() error { return a.act.installCodex() })
}

func (a *app) claude() bool {
	return a.run(a.misc, func() error { return a.act.openClaude() })
}

func (a *app) toggleAutostart() bool {
	return a.run(a.misc, func() error { return a.act.setAutostart(!a.act.autostart()) })
}

func (a *app) copyAddr() bool {
	return a.copy(itemCopyAddr, a.act.copyText, func() string {
		a.mu.Lock()
		defer a.mu.Unlock()
		return apiAddress(a.status.Addr)
	})
}

func (a *app) copyKey() bool {
	return a.copy(itemCopyKey, a.putKey, a.ctl.Token)
}

// putKey copies the key and takes it off the clipboard a minute later.
func (a *app) putKey(key string) error {
	clear, err := a.act.copySecret(key)
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.clearKey = clear
	a.mu.Unlock()
	time.AfterFunc(keyLives, clear) // harmless after a later copy
	return nil
}

// copy puts text on the clipboard. Its errors are fixed strings, so that the key
// cannot reach the menu through one.
func (a *app) copy(id itemID, put func(string) error, text func() string) bool {
	return a.run(a.clip, func() error {
		s := text()
		if s == "" {
			return errors.New("nothing to copy yet")
		}
		if err := put(s); err != nil {
			return errors.New("could not copy to the clipboard")
		}
		a.mu.Lock()
		a.copied[id] = time.Now().Add(copiedFor)
		a.mu.Unlock()
		time.AfterFunc(copiedFor, func() { notify(a.changed) })
		return nil
	})
}

func (a *app) openLog() bool { return a.run(a.misc, a.act.openLog) }

func (a *app) setupDesktop() bool {
	return a.switchDesktop(func() error { return a.act.setupDesktop(a.ctx, a.ctl.Mapping()) })
}

// applyDesktop lets the picker update Claude Desktop; the owner confirms in a window again.
func (a *app) applyDesktop() error {
	if !a.setupDesktop() {
		return errors.New("Claude Desktop is already being changed")
	}
	return nil
}

func (a *app) pick() bool { return a.openPage(a.ctl.PickerURL) }

func (a *app) plugins() bool { return a.openPage(a.ctl.PluginsURL) }

// openPage opens a one-time link of the bridge's own pages.
func (a *app) openPage(link func() string) bool {
	return a.run(a.misc, func() error {
		u := link()
		if !localURL(u) {
			return errors.New("turn the bridge on first")
		}
		return a.act.openLocal(u)
	})
}

func (a *app) leaveDesktop() bool {
	return a.switchDesktop(func() error { return a.act.leaveDesktop(a.ctx) })
}

// switchDesktop also shows the reason of a failure in a window: the owner is
// waiting for Claude, not looking at the menu.
func (a *app) switchDesktop(fn func() error) bool {
	return a.run(a.desk, func() error {
		err := fn()
		if err == nil || errors.Is(err, desktop.ErrCancelled) || a.ctx.Err() != nil {
			return nil
		}
		go a.act.alert("Second Life", err.Error())
		return err
	})
}

// apiAddress is the base URL without /v1.
func apiAddress(addr string) string {
	if addr == "" {
		return ""
	}
	return "http://" + strings.TrimPrefix(addr, "http://")
}

// shutdown stops the bridge once; every caller waits until that is done. A
// running Start gets a moment to finish, so Stop has something to stop.
func (a *app) shutdown() {
	a.stopOnce.Do(func() {
		a.cancel()
		select {
		case a.power <- struct{}{}: // kept: nothing may start again
		case <-time.After(quitWaitBusy):
		}
		done := make(chan struct{})
		go func() {
			a.mu.Lock()
			clear := a.clearKey
			a.mu.Unlock()
			var wg sync.WaitGroup
			wg.Go(a.ctl.Stop)
			if clear != nil {
				wg.Go(clear)
			}
			wg.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(quitWaitStop):
		}
	})
}
