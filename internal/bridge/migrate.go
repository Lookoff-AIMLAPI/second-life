package bridge

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// formerHome is where the app kept its files under its first name, Codex to Claude.
const formerHome = ".codex-to-claude"

// adopt copies the owner's files of the former name once, so nothing has to be set up again. The old folder stays as
// it is: the old app may still be there. It does nothing when the new folder already exists.
func adopt(home string) error {
	if os.Getenv("SLIFE_HOME") != "" {
		return nil
	}
	old := filepath.Join(filepath.Dir(home), formerHome)
	if _, err := os.Lstat(home); !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if st, err := os.Lstat(old); err != nil || !st.IsDir() {
		return nil
	}
	tmp, err := os.MkdirTemp(filepath.Dir(home), ".second-life-*") // 0700
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	names := []string{"token", "models.json"}
	keys, _ := filepath.Glob(filepath.Join(old, "*-key")) // the key files of Linux
	for _, k := range keys {
		names = append(names, filepath.Base(k))
	}
	for _, n := range names {
		if err := copyPlain(filepath.Join(old, n), filepath.Join(tmp, n)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	// Whole or not at all; another process may have been first.
	if err := os.Rename(tmp, home); err != nil {
		if _, serr := os.Lstat(home); serr == nil {
			return nil
		}
		return err
	}
	return nil
}

func copyPlain(from, to string) error {
	st, err := os.Lstat(from)
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() || st.Size() > 1<<20 {
		return errors.New(filepath.Base(from) + " in the old folder is not a plain small file")
	}
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	return errors.Join(err, out.Close())
}
