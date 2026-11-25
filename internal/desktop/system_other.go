//go:build !darwin && !windows

package desktop

import "errors"

// New keeps the package compiling and its tests running elsewhere (Linux CI).
func New(exe, helper, baseURL, backupDir string) (*Desktop, error) {
	return nil, errors.New("Claude Desktop setup supports macOS and Windows only")
}
