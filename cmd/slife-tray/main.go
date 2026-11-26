// Command slife-tray is the menu bar (macOS) and notification area (Windows) app.
//
// Windows release builds need -ldflags "-H windowsgui".
package main

import (
	"errors"

	"github.com/Lookoff-AIMLAPI/second-life/internal/bridge"
	"github.com/Lookoff-AIMLAPI/second-life/internal/tray"
)

func main() {
	var c tray.Controller
	b, err := bridge.New(bridge.Options{})
	if err != nil {
		// A GUI app has nowhere else to say why it cannot work.
		c = broken(err.Error())
	} else {
		c = b
	}
	tray.Run(c, bridge.Version)
}

// broken shows why the bridge could not be prepared.
type broken string

func (e broken) Start() error                         { return errors.New(string(e)) }
func (e broken) Stop()                                {}
func (e broken) Login() (string, error)               { return "", errors.New(string(e)) }
func (e broken) Token() string                        { return "" }
func (e broken) Mapping() [][2]string                 { return nil }
func (e broken) PickerURL() string                    { return "" }
func (e broken) PluginsURL() string                   { return "" }
func (e broken) SetDesktop(func() bool, func() error) {}
func (e broken) Status() bridge.Status {
	return bridge.Status{State: bridge.StateError, LastError: string(e)}
}
