package tray

import (
	"errors"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32                  = windows.NewLazySystemDLL("user32.dll")
	openClipboard           = user32.NewProc("OpenClipboard")
	closeClipboard          = user32.NewProc("CloseClipboard")
	emptyClipboard          = user32.NewProc("EmptyClipboard")
	setClipboardData        = user32.NewProc("SetClipboardData")
	registerClipboardFormat = user32.NewProc("RegisterClipboardFormatW")
	clipboardSequence       = user32.NewProc("GetClipboardSequenceNumber")

	kernel32      = windows.NewLazySystemDLL("kernel32.dll")
	globalAlloc   = kernel32.NewProc("GlobalAlloc")
	globalFree    = kernel32.NewProc("GlobalFree")
	globalLock    = kernel32.NewProc("GlobalLock")
	globalUnlock  = kernel32.NewProc("GlobalUnlock")
	rtlMoveMemory = kernel32.NewProc("RtlMoveMemory")
)

// secretFormats keep the text out of monitors, the history (Win+V) and the
// cloud clipboard. Each holds a DWORD 0.
var secretFormats = []string{
	"ExcludeClipboardContentFromMonitorProcessing",
	"CanIncludeInClipboardHistory",
	"CanUploadToCloudClipboard",
}

var errClipboard = errors.New("clipboard unavailable")

func copyText(s string) error {
	_, err := setClipboard(s, nil)
	return err
}

// copySecret refuses to copy unless every mark was set.
func copySecret(s string) (clear func(), err error) {
	seq, err := setClipboard(s, secretFormats)
	if err != nil {
		return nil, err
	}
	return func() { clearClipboard(seq) }, nil
}

// setClipboard uses the clipboard API: no child process ever sees the text. It
// returns the clipboard's sequence number after the copy.
func setClipboard(s string, marks []string) (seq uintptr, err error) {
	const cfUnicodeText = 13
	text, err := windows.UTF16FromString(s)
	if err != nil {
		return 0, errClipboard
	}
	var ids []uintptr
	for _, m := range marks {
		name, err := windows.UTF16PtrFromString(m)
		if err != nil {
			return 0, errClipboard
		}
		id, _, _ := registerClipboardFormat.Call(uintptr(unsafe.Pointer(name)))
		if id == 0 {
			return 0, errClipboard
		}
		ids = append(ids, id)
	}
	err = withClipboard(func() error {
		if ok, _, _ := emptyClipboard.Call(); ok == 0 {
			return errClipboard
		}
		// The marks come first: nothing may see the text without them.
		for _, id := range ids {
			var zero uint32
			if err := put(id, unsafe.Pointer(&zero), unsafe.Sizeof(zero)); err != nil {
				emptyClipboard.Call()
				return err
			}
		}
		if err := put(cfUnicodeText, unsafe.Pointer(&text[0]), uintptr(len(text))*unsafe.Sizeof(text[0])); err != nil {
			emptyClipboard.Call()
			return err
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	// Read after closing: closing can add formats and raise the number.
	seq, _, _ = clipboardSequence.Call()
	return seq, nil
}

// clearClipboard empties the clipboard if nothing was copied since seq.
func clearClipboard(seq uintptr) {
	withClipboard(func() error {
		if now, _, _ := clipboardSequence.Call(); now == seq && seq != 0 {
			emptyClipboard.Call()
		}
		return nil
	})
}

// put copies size bytes into memory that the clipboard then owns.
func put(format uintptr, src unsafe.Pointer, size uintptr) error {
	const gmemMoveable = 2
	mem, _, _ := globalAlloc.Call(gmemMoveable, size)
	if mem == 0 {
		return errClipboard
	}
	dst, _, _ := globalLock.Call(mem)
	if dst == 0 {
		globalFree.Call(mem)
		return errClipboard
	}
	rtlMoveMemory.Call(dst, uintptr(src), size)
	globalUnlock.Call(mem)
	if h, _, _ := setClipboardData.Call(format, mem); h == 0 {
		globalFree.Call(mem)
		return errClipboard
	}
	return nil
}

// withClipboard opens the clipboard, which belongs to the thread that opened
// it. Another program may hold it for a moment.
func withClipboard(fn func() error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	for range 20 {
		if ok, _, _ := openClipboard.Call(0); ok != 0 {
			defer closeClipboard.Call()
			return fn()
		}
		time.Sleep(25 * time.Millisecond)
	}
	return errClipboard
}
