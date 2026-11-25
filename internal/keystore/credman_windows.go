package keystore

import (
	"errors"
	"fmt"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	credTypeGeneric  = 1
	credPersistLocal = 2 // this user on this machine; it does not roam
	maxBlob          = 5 * 512
)

var (
	advapi32   = windows.NewLazySystemDLL("advapi32.dll")
	credReadW  = advapi32.NewProc("CredReadW")
	credWriteW = advapi32.NewProc("CredWriteW")
	credDelete = advapi32.NewProc("CredDeleteW")
	credFree   = advapi32.NewProc("CredFree")
)

// credential is CREDENTIALW.
type credential struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        windows.Filetime
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         uintptr
	TargetAlias        *uint16
	UserName           *uint16
}

// manager is one entry of Credential Manager.
type manager struct{ account, service string }

func native(account string) (backend, error) {
	return inherited{manager{account, service}, manager{account, former}}, nil
}

func (m manager) target() string { return m.service + "/" + m.account }

func credError(err error) error {
	var no syscall.Errno
	errors.As(err, &no)
	return fmt.Errorf("Credential Manager refused (code %d)", uint32(no))
}

func (m manager) get() (string, error) {
	target, err := windows.UTF16PtrFromString(m.target())
	if err != nil {
		return "", errDamaged
	}
	var c *credential
	ok, _, err := credReadW.Call(uintptr(unsafe.Pointer(target)), credTypeGeneric, 0, uintptr(unsafe.Pointer(&c)))
	if ok == 0 {
		if errors.Is(err, windows.ERROR_NOT_FOUND) {
			return "", ErrNone
		}
		return "", credError(err)
	}
	defer credFree.Call(uintptr(unsafe.Pointer(c)))
	if c.CredentialBlob == nil || c.CredentialBlobSize > maxBlob {
		return "", errDamaged
	}
	return string(unsafe.Slice(c.CredentialBlob, c.CredentialBlobSize)), nil
}

func (m manager) set(key string) error {
	target, err := windows.UTF16PtrFromString(m.target())
	if err != nil {
		return errSave
	}
	name, err := windows.UTF16PtrFromString(m.account)
	if err != nil || key == "" {
		return errSave
	}
	blob := []byte(key)
	c := credential{
		Type: credTypeGeneric, TargetName: target, UserName: name, Persist: credPersistLocal,
		CredentialBlob: &blob[0], CredentialBlobSize: uint32(len(blob)),
	}
	ok, _, err := credWriteW.Call(uintptr(unsafe.Pointer(&c)), 0)
	runtime.KeepAlive(blob)
	if ok == 0 {
		return credError(err)
	}
	return nil
}

func (m manager) delete() error {
	target, err := windows.UTF16PtrFromString(m.target())
	if err != nil {
		return errDamaged
	}
	ok, _, err := credDelete.Call(uintptr(unsafe.Pointer(target)), credTypeGeneric, 0)
	if ok == 0 && !errors.Is(err, windows.ERROR_NOT_FOUND) {
		return credError(err)
	}
	return nil
}
