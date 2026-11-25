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

// Adopt brings the files of the former name into the usual home. Call it before anything there is read.
func Adopt(home string) error {
	if filepath.Base(home) != ".second-life" {
		return nil
	}
	return adopt(home)
}

// adopt copies the owner's files of the former name, so nothing has to be set up again. The old folder stays as it
// is: the old app may still be there. Once there is a token here, this home is in use and nothing is copied. An empty
// folder is not in use: any command may have made it.
func adopt(home string) error {
	if os.Getenv("SLIFE_HOME") != "" {
		return nil
	}
	if _, err := os.Lstat(filepath.Join(home, "token")); !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	old := filepath.Join(filepath.Dir(home), formerHome)
	if st, err := os.Lstat(old); err != nil || !st.IsDir() {
		return nil
	}
	tmp, err := os.MkdirTemp(filepath.Dir(home), ".second-life-*") // 0700
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	names := []string{"models.json"}
	keys, _ := filepath.Glob(filepath.Join(old, "*-key")) // the key files of Linux
	for _, k := range keys {
		names = append(names, filepath.Base(k))
	}
	names = append(names, "token") // last: it marks the home as in use
	var got []string
	for _, n := range names {
		switch err := copyPlain(filepath.Join(old, n), filepath.Join(tmp, n)); {
		case err == nil:
			got = append(got, n)
		case !errors.Is(err, fs.ErrNotExist):
			return err
		}
	}
	if len(got) == 0 {
		return nil
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	for _, n := range got {
		to := filepath.Join(home, n)
		if _, err := os.Lstat(to); !errors.Is(err, fs.ErrNotExist) {
			if err != nil {
				return err
			}
			continue // the owner's own, or another process was first
		}
		if err := os.Rename(filepath.Join(tmp, n), to); err != nil {
			return err
		}
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
