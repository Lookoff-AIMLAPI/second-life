package desktop

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const policyPath = `SOFTWARE\Policies\Claude`

type native struct{ local string }

// New returns the installed Claude Desktop.
func New(exe, helper, baseURL, backupDir string) (*Desktop, error) {
	local := os.Getenv("LOCALAPPDATA")
	if !filepath.IsAbs(local) {
		return nil, errors.New("LOCALAPPDATA is not set")
	}
	usual := ""
	if roaming := os.Getenv("APPDATA"); filepath.IsAbs(roaming) {
		usual = filepath.Join(roaming, "Claude", modeFile)
	}
	return &Desktop{
		Sys: native{local}, Dir: filepath.Join(local, "Claude-3p"), Usual: usual,
		BackupDir: backupDir, BaseURL: baseURL, GOOS: "windows", Exe: exe, Helper: helper,
		Blocked: func() error { return packageCopy(local) },
	}, nil
}

// packageCopy: an app installed as a Windows package may keep its own private
// copy of the folder, which would win over the one written here.
func packageCopy(local string) error {
	found, err := filepath.Glob(filepath.Join(local, "Packages", "Claude_*", "LocalCache", "Local", "Claude-3p"))
	if err != nil {
		return err
	}
	if len(found) > 0 {
		return errors.New("this Claude keeps its settings inside its Windows package, where this app cannot set them up; use Configure Third-Party Inference in Claude instead")
	}
	return nil
}

func (n native) stub() string { return filepath.Join(n.local, "AnthropicClaude", "claude.exe") }

func (n native) packages() []string {
	found, _ := filepath.Glob(filepath.Join(n.local, "Packages", "Claude_*"))
	return found
}

func (n native) Installed() bool {
	_, err := os.Stat(n.stub())
	return err == nil || len(n.packages()) > 0
}

func (native) Managed() (bool, error) {
	for _, root := range []registry.Key{registry.LOCAL_MACHINE, registry.CURRENT_USER} {
		if yes, err := policyIn(root, policyPath); yes || err != nil {
			return yes, err
		}
	}
	return false, nil
}

// policyIn: any value or subkey counts.
func policyIn(root registry.Key, path string) (bool, error) {
	k, err := registry.OpenKey(root, path, registry.QUERY_VALUE|registry.ENUMERATE_SUB_KEYS)
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer k.Close()
	st, err := k.Stat()
	if err != nil {
		return false, err
	}
	return st.ValueCount > 0 || st.SubKeyCount > 0, nil
}

// pids finds Claude Desktop by path: Claude Code is called claude.exe too.
func (n native) pids() (map[uint32]bool, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snap)
	found := map[uint32]bool{}
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if strings.EqualFold(windows.UTF16ToString(e.ExeFile[:]), "claude.exe") && isDesktop(imagePath(e.ProcessID), n.local) {
			found[e.ProcessID] = true
		}
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return nil, err
	}
	return found, nil
}

func imagePath(pid uint32) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if windows.QueryFullProcessImageName(h, 0, &buf[0], &n) != nil {
		return ""
	}
	return windows.UTF16ToString(buf[:n])
}

func (n native) Running() (bool, error) {
	p, err := n.pids()
	return len(p) > 0, err
}

var (
	user32      = windows.NewLazySystemDLL("user32.dll")
	postMessage = user32.NewProc("PostMessageW")

	// Windows callbacks are never freed, so there is one, used under a lock.
	closeMu   sync.Mutex
	closePIDs map[uint32]bool
	closeFn   = syscall.NewCallback(func(hwnd windows.HWND, _ uintptr) uintptr {
		const wmClose = 0x0010
		var pid uint32
		windows.GetWindowThreadProcessId(hwnd, &pid)
		if closePIDs[pid] && windows.IsWindowVisible(hwnd) {
			postMessage.Call(uintptr(hwnd), wmClose, 0, 0)
		}
		return 1
	})
)

// Quit closes Claude's windows. Claude may stay in the notification area, and
// then the owner is asked to quit it there.
func (n native) Quit() error {
	pids, err := n.pids()
	if err != nil {
		return err
	}
	closeMu.Lock()
	defer closeMu.Unlock()
	closePIDs = pids
	return windows.EnumWindows(closeFn, nil)
}

func (n native) Open() error {
	if _, err := os.Stat(n.stub()); err == nil {
		cmd := exec.Command(n.stub())
		if err := cmd.Start(); err != nil {
			return err
		}
		return cmd.Process.Release()
	}
	dir, err := windows.GetSystemDirectory()
	if err != nil {
		return err
	}
	cmd := exec.Command(filepath.Join(dir, "rundll32.exe"), "url.dll,FileProtocolHandler", "claude://")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Run()
}

func (native) Confirm(title, text, button string) (bool, error) {
	const idOK = 1
	// The buttons are fixed, so the text names the action.
	got, err := box(title, text+"\n\nPress OK to continue: "+button+".",
		windows.MB_OKCANCEL|windows.MB_ICONWARNING|windows.MB_DEFBUTTON2)
	return got == idOK, err
}

func (native) Alert(title, text string) {
	box(title, text, windows.MB_OK|windows.MB_ICONINFORMATION)
}

func box(title, text string, kind uint32) (int32, error) {
	t, err := windows.UTF16PtrFromString(title)
	if err != nil {
		return 0, err
	}
	b, err := windows.UTF16PtrFromString(text)
	if err != nil {
		return 0, err
	}
	return windows.MessageBox(0, b, t, kind|windows.MB_SETFOREGROUND|windows.MB_TOPMOST)
}
