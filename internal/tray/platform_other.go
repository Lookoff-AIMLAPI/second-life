//go:build !darwin && !windows

package tray

import "errors"

var errPlatform = errors.New("the tray app supports macOS and Windows only")

// These keep the package compiling and its tests running elsewhere (Linux CI).

func setIcon(iconState) {}

func platformActions() actions {
	return addDesktop(actions{
		openURL:      func(string) error { return errPlatform },
		openLocal:    func(string) error { return errPlatform },
		openClaude:   func() error { return errPlatform },
		installCodex: func() error { return errPlatform },
		importMCP:    func() error { return errPlatform },
		copyText:     func(string) error { return errPlatform },
		copySecret:   func(string) (func(), error) { return nil, errPlatform },
		openLog:      func() error { return errPlatform },
		autostart:    func() bool { return false },
		setAutostart: func(bool) error { return errPlatform },
	}, "slife")
}

func acquireInstance() (release func(), ok bool, err error) { return nil, false, errPlatform }
