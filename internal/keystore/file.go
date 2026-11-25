//go:build !windows

package keystore

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// file keeps a key in a file only the owner can read, where the system has no store of its own.
type file struct{ path string }

func (f file) get() (string, error) {
	h, err := os.OpenFile(f.path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return "", ErrNone
	}
	if err != nil {
		return "", errors.New("could not open the key file")
	}
	defer h.Close()
	st, err := h.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return "", errDamaged
	}
	if st.Mode().Perm()&0o077 != 0 {
		return "", errors.New("the key file is open to others; set the key again")
	}
	b, err := io.ReadAll(io.LimitReader(h, maxLen+1))
	if err != nil {
		return "", errors.New("could not read the key file")
	}
	return string(b), nil
}

func (f file) set(key string) error {
	if os.MkdirAll(filepath.Dir(f.path), 0o700) != nil {
		return errSave
	}
	h, err := os.CreateTemp(filepath.Dir(f.path), ".key-*") // 0600
	if err != nil {
		return errSave
	}
	defer os.Remove(h.Name())
	_, err = h.WriteString(key)
	if errors.Join(err, h.Sync(), h.Close()) != nil || os.Rename(h.Name(), f.path) != nil {
		return errSave
	}
	return nil
}

func (f file) delete() error {
	if err := os.Remove(f.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return errors.New("the key file was not deleted")
	}
	return nil
}
