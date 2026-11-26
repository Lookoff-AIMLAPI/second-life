//go:build demo

// Command demo shows the tray with a fake bridge that walks through every
// state: go run -tags demo ./cmd/slife-tray/demo. It shares the real app's
// instance lock, and its Start at login is real too.
package main

import (
	"sync"
	"time"

	"github.com/Lookoff-AIMLAPI/second-life/internal/bridge"
	"github.com/Lookoff-AIMLAPI/second-life/internal/tray"
)

type fake struct {
	mu sync.Mutex
	st bridge.Status
}

func (f *fake) set(s bridge.State) {
	f.mu.Lock()
	f.st.State = s
	f.st.SignedIn = s == bridge.StateOn
	f.st.LastError = ""
	if s == bridge.StateError {
		f.st.LastError = "listen tcp 127.0.0.1:8787: bind: address already in use"
	}
	f.mu.Unlock()
}

func (f *fake) Start() error {
	f.set(bridge.StateStarting)
	time.Sleep(time.Second)
	f.set(bridge.StateNeedsLogin)
	return nil
}

func (f *fake) Stop() { f.set(bridge.StateOff) }

func (f *fake) Status() bridge.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.st
}

func (f *fake) Login() (string, error) {
	go func() {
		for _, s := range []bridge.State{bridge.StateOn, bridge.StateCodexMissing, bridge.StateError, bridge.StateOn} {
			f.set(s)
			time.Sleep(8 * time.Second)
		}
	}()
	return "https://example.com/", nil
}

func (f *fake) Token() string { return "demo-key" }

func (f *fake) PickerURL() string  { return "http://127.0.0.1:8787/picker/#x" }
func (f *fake) PluginsURL() string { return "http://127.0.0.1:8787/picker/plugins#x" }

func (f *fake) SetDesktop(func() bool, func() error) {}

func (f *fake) Mapping() [][2]string {
	return [][2]string{{"opus", "gpt-5.6-terra"}, {"fable", "gpt-6-astra"}, {"haiku", "gpt-5.6-luna"}}
}

func main() {
	now := time.Now()
	tray.Run(&fake{st: bridge.Status{
		State: bridge.StateOff, Plan: "pro", Addr: "127.0.0.1:8787",
		Primary:     &bridge.Limit{UsedPercent: 34, WindowMins: 300, ResetsAt: now.Add(2 * time.Hour).Unix()},
		Secondary:   &bridge.Limit{UsedPercent: 61, WindowMins: 10080, ResetsAt: now.Add(80 * time.Hour).Unix()},
		InputTokens: 48210, OutputTokens: 3977,
	}}, "demo")
}
