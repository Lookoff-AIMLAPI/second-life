package tray

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

const (
	openBin = "/usr/bin/open"
	copyBin = "/usr/bin/pbcopy"
)

func platformActions() actions {
	return addDesktop(actions{
		openURL: func(raw string) error {
			link, err := safeURL(raw)
			if err != nil {
				return err
			}
			return launch(nil, openBin, link)
		},
		openLocal:    func(u string) error { return launch(nil, openBin, u) },
		openClaude:   func() error { return openTerminal(verbClaude) },
		installCodex: func() error { return openTerminal(verbCodex) },
		importMCP:    func() error { return openTerminal(verbMCP) },
		copyText:     func(s string) error { return pipe(copyBin, s) },
		copySecret:   func(s string) (func(), error) { return copySecret("", s) },
		openLog:      openLogWith(func(path string) error { return launch(nil, openBin, path) }),
		autostart: func() bool {
			home, exe, err := homeAndExe()
			return err == nil && launchAgentEnabled(home, exe)
		},
		setAutostart: func(on bool) error {
			home, exe, err := homeAndExe()
			if err != nil {
				return err
			}
			return setLaunchAgent(home, exe, on)
		},
	}, "slife")
}

func homeAndExe() (home, exe string, err error) {
	if home, err = os.UserHomeDir(); err != nil {
		return
	}
	exe, err = executable()
	return
}

// openTerminal runs slife in a terminal window.
func openTerminal(verb string) error {
	exe, err := executable()
	if err != nil {
		return err
	}
	slife, err := siblingCLI(exe, "slife")
	if err != nil {
		return err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	script, err := writeClaudeScript(cache, slife, verb)
	if err != nil {
		return err
	}
	return launch(nil, openBin, script)
}

func acquireInstance() (release func(), ok bool, err error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return nil, false, err
	}
	return lockDir(filepath.Join(dir, appDir))
}

// lockDir takes the instance lock in dir. The kernel drops it with the process,
// so a crash leaves nothing to clean up.
func lockDir(dir string) (release func(), ok bool, err error) {
	if err := privateDir(dir); err != nil {
		return nil, false, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "tray.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		f.Close()
		return nil, false, nil
	}
	if err != nil {
		f.Close()
		return nil, false, err
	}
	return func() { f.Close() }, true, nil
}

// pipe hands text to bin on stdin only, so it shows up in no process list.
func pipe(bin, text string) error {
	_, err := pipeOut(bin, text)
	return err
}

func pipeOut(bin, text string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), launchTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdin = strings.NewReader(text)
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// pbcopy cannot mark what it copies. The scripts take the pasteboard's name
// ("" is the usual one) and, to clear, the change count after the copy; the
// secret comes on stdin.
const (
	osascript  = "/usr/bin/osascript"
	pasteboard = `ObjC.import("AppKit");
function board(name) { return name ? $.NSPasteboard.pasteboardWithName(name) : $.NSPasteboard.generalPasteboard; }
`
	secretScript = pasteboard + `function run(argv) {
	const data = $.NSFileHandle.fileHandleWithStandardInput.readDataToEndOfFile;
	const text = $.NSString.alloc.initWithDataEncoding(data, $.NSUTF8StringEncoding);
	const p = board(argv[0]);
	p.prepareForNewContentsWithOptions(1); // this Mac only: no Universal Clipboard
	p.setStringForType("", "org.nspasteboard.ConcealedType");
	if (!p.setStringForType(text, "public.utf8-plain-text")) { throw new Error("refused"); }
	return String(p.changeCount);
}`
	clearScript = pasteboard + `function run(argv) {
	const p = board(argv[0]);
	if (String(p.changeCount) === argv[1]) { p.clearContents; return "cleared"; }
	return "kept";
}`
)

func copySecret(name, s string) (clear func(), err error) {
	count, err := pipeOut(osascript, s, "-l", "JavaScript", "-e", secretScript, name)
	if err != nil {
		return nil, err
	}
	return func() { pipeOut(osascript, "", "-l", "JavaScript", "-e", clearScript, name, count) }, nil
}
