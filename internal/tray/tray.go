// Package tray is the menu bar (macOS) and notification area (Windows) app.
//
// Windows release builds need -ldflags "-H windowsgui"; without it the app
// opens a console window.
package tray

import (
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"fyne.io/systray"

	"github.com/Lookoff-AIMLAPI/second-life/internal/bridge"
)

//go:generate go run ./icongen -out icons

// Controller is the bridge as the menu sees it. Its methods are called from
// several goroutines, never from the UI one.
type Controller interface {
	Start() error
	Stop()
	Status() bridge.Status
	Login() (string, error)
	Mapping() [][2]string
	PickerURL() string // a one-time link to the model picker, empty while off
	SetDesktop(configured func() bool, apply func() error)
	Token() string // the local bearer; only ever copied to the clipboard
}

// Run shows the tray icon and returns after Quit, with the bridge stopped. It
// must be called from the main goroutine. A second copy returns at once.
func Run(c Controller, version string) {
	release, ok, err := acquireInstance()
	if err != nil {
		// The bridge's port still keeps a second copy from working.
		log.Printf("single instance check: %v", err)
	} else if !ok {
		log.Print("second-life is already running")
		return
	} else {
		defer release()
	}

	a := newApp(c, platformActions(), version)
	// The exit callback runs on the UI goroutine and must block there: the
	// process ends when it returns (logout, shutdown). macOS skips it on Quit.
	systray.Run(a.ready, a.shutdown)
	a.shutdown()
}

func (a *app) ready() {
	a.setPower(true) // like a VPN app: on as soon as it runs
	v := buildView(a.snapshot(), time.Now())
	m := newMenu(v)
	a.shown(v)
	a.listen(m)

	go a.warnAtStart()
	a.ctl.SetDesktop(func() bool { return a.snapshot().Desktop.ThirdParty }, a.applyDesktop)
	go a.poll()
	go a.watchSignals()
	go a.draw(m)
}

// draw is the only goroutine that changes the menu after it is built.
func (a *app) draw(m *menu) {
	m.setModels(modelLines(a.ctl.Mapping()))
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-a.changed:
			m.setModels(modelLines(a.ctl.Mapping()))
			v := buildView(a.snapshot(), time.Now())
			m.apply(v)
			a.shown(v)
		}
	}
}

func (a *app) listen(m *menu) {
	on := func(id itemID, fn func()) {
		go func() {
			for {
				select {
				case <-a.ctx.Done():
					return
				case <-m.items[id].ClickedCh:
					fn()
				}
			}
		}()
	}
	on(itemToggle, func() { a.toggle() })
	on(itemPick, func() { a.pick() })
	on(itemCopyAddr, func() { a.copyAddr() })
	on(itemCopyKey, func() { a.copyKey() })
	on(itemSignIn, func() { a.signIn() })
	on(itemInstall, func() { a.install() })
	on(itemClaude, func() { a.claude() })
	on(itemMCP, func() { a.importMCP() })
	on(itemDesktop, func() { a.setupDesktop() })
	on(itemDesktopBack, func() { a.leaveDesktop() })
	on(itemLog, func() { a.openLog() })
	on(itemAutostart, func() { a.toggleAutostart() })
	on(itemQuit, a.quit)
}

func (a *app) warnAtStart() {
	if msg := a.act.startWarning(); msg != "" {
		a.act.alert("Second Life", msg)
	}
}

func (a *app) quit() {
	a.shutdown()
	systray.Quit()
}

// watchSignals covers launchd, which ends login items with SIGTERM.
func (a *app) watchSignals() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(ch)
	select {
	case <-ch:
		a.quit()
	case <-a.ctx.Done():
	}
}
