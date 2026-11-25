package tray

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	runKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`
	// The uninstaller (packaging/windows/installer.nsi) deletes this name.
	runValueName = "SecondLife"
	mutexName    = `Local\` + appID
)

func hidden(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}

// systemExe avoids PATH: rundll32 and cmd must be the system ones.
func systemExe(name string) (string, error) {
	dir, err := windows.GetSystemDirectory()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

func platformActions() actions {
	return addDesktop(actions{
		openURL: func(raw string) error {
			link, err := safeURL(raw)
			if err != nil {
				return err
			}
			rundll, err := systemExe("rundll32.exe")
			if err != nil {
				return err
			}
			return launch(hidden, rundll, "url.dll,FileProtocolHandler", link)
		},
		openLocal: func(u string) error {
			rundll, err := systemExe("rundll32.exe")
			if err != nil {
				return err
			}
			return launch(hidden, rundll, "url.dll,FileProtocolHandler", u)
		},
		openClaude:   func() error { return openTerminal(verbClaude) },
		installCodex: func() error { return openTerminal(verbCodex) },
		importMCP:    func() error { return openTerminal(verbMCP) },
		copyText:     copyText,
		copySecret:   copySecret,
		openLog:      openLogWith(shellOpen),
		autostart: func() bool {
			exe, err := executable()
			return err == nil && runKeyEnabled(registry.CURRENT_USER, runKeyPath, exe)
		},
		setAutostart: func(on bool) error {
			exe, err := executable()
			if err != nil {
				return err
			}
			return setRunKey(registry.CURRENT_USER, runKeyPath, exe, on)
		},
	}, "slife.exe")
}

// shellOpen starts the program for a file and does not wait for it.
func shellOpen(path string) error {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, p, nil, nil, windows.SW_SHOWNORMAL)
}

// openTerminal runs slife in a terminal window.
func openTerminal(verb string) error {
	exe, err := executable()
	if err != nil {
		return err
	}
	slife, err := siblingCLI(exe, "slife.exe")
	if err != nil {
		return err
	}
	cmdExe, err := systemExe("cmd.exe")
	if err != nil {
		return err
	}
	line, err := claudeCmdLine(cmdExe, slife, verb)
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	return launch(func(cmd *exec.Cmd) {
		cmd.Dir = home
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CmdLine: line}
	}, cmdExe)
}

func runKeyEnabled(root registry.Key, path, exe string) bool {
	k, err := registry.OpenKey(root, path, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	got, _, err := k.GetStringValue(runValueName)
	return err == nil && got == runValue(exe)
}

func setRunKey(root registry.Key, path, exe string, on bool) error {
	k, _, err := registry.CreateKey(root, path, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if on {
		return k.SetStringValue(runValueName, runValue(exe))
	}
	if err := k.DeleteValue(runValueName); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}

func acquireInstance() (release func(), ok bool, err error) {
	return lockMutex(mutexName)
}

func lockMutex(name string) (release func(), ok bool, err error) {
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, false, err
	}
	h, err := windows.CreateMutex(nil, false, p)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		if h != 0 {
			windows.CloseHandle(h)
		}
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return func() { windows.CloseHandle(h) }, true, nil
}
