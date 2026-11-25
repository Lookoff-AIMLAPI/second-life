package keystore

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"time"
)

const (
	securityBin = "/usr/bin/security"
	notFound    = 44 // errSecItemNotFound

	// macOS may ask the owner to unlock the keychain. Reading happens on the way to an answer, so it waits as long
	// as a provider may.
	readLimit  = 15 * time.Minute
	writeLimit = 2 * time.Minute
)

// keychain is a login item in one keychain file.
type keychain struct{ path, account, service string }

func native(account string) (backend, error) {
	// Not $HOME: a helper may be started without it.
	u, err := user.Current()
	if err != nil || u.HomeDir == "" {
		return nil, errors.New("the home folder is not known")
	}
	file := filepath.Join(u.HomeDir, "Library", "Keychains", "login.keychain-db")
	return inherited{keychain{file, account, service}, keychain{file, account, former}}, nil
}

// security runs the tool and returns its output only: what it prints about a failure may repeat its input.
func (k keychain) security(limit time.Duration, stdin string, args ...string) (out string, code int, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, securityBin, args...)
	cmd.WaitDelay = time.Second
	cmd.Stdin = strings.NewReader(stdin)
	for _, v := range []string{"HOME", "USER", "LOGNAME"} {
		if s, ok := os.LookupEnv(v); ok {
			cmd.Env = append(cmd.Env, v+"="+s)
		}
	}
	if cmd.Env == nil {
		cmd.Env = []string{} // nil would pass everything on
	}
	var b bytes.Buffer
	cmd.Stdout = &b
	err = cmd.Run()
	var exit *exec.ExitError
	switch {
	case ctx.Err() != nil:
		return "", -1, errors.New("the Keychain did not answer")
	case errors.As(err, &exit):
		return "", exit.ExitCode(), fmt.Errorf("the Keychain refused (code %d)", exit.ExitCode())
	case err != nil:
		return "", -1, errors.New("the Keychain tool did not start")
	}
	return b.String(), 0, nil
}

func (k keychain) get() (string, error) {
	out, code, err := k.security(readLimit, "", "find-generic-password", "-s", k.service, "-a", k.account, "-w", k.path)
	if code == notFound {
		return "", ErrNone
	}
	return strings.TrimSuffix(out, "\n"), err
}

// set passes the key as hex on stdin: arguments are visible to every process, and hex needs no quoting.
func (k keychain) set(key string) error {
	if strings.ContainsAny(k.path, "\"\\\r\n") {
		return errors.New("the Keychain path has characters that are not allowed")
	}
	line := fmt.Sprintf("add-generic-password -U -s %s -a %s -X %s \"%s\"\n", k.service, k.account, hex.EncodeToString([]byte(key)), k.path)
	_, _, err := k.security(writeLimit, line, "-i")
	return err
}

func (k keychain) delete() error {
	_, code, err := k.security(writeLimit, "", "delete-generic-password", "-s", k.service, "-a", k.account, k.path)
	if code == notFound {
		return nil
	}
	return err
}
