//go:build !darwin && !windows

package keystore

import (
	"errors"
	"os"
	"os/user"
	"path/filepath"
)

// native is a file in the bridge home. The home is found as package bridge finds it; importing it would be a cycle.
func native(account string) (backend, error) {
	home := os.Getenv("SLIFE_HOME")
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			u, uerr := user.Current()
			if uerr != nil || u.HomeDir == "" {
				return nil, errors.New("the home folder is not known")
			}
			h = u.HomeDir
		}
		home = filepath.Join(h, ".second-life")
	}
	return file{filepath.Join(home, account+"-key")}, nil
}
