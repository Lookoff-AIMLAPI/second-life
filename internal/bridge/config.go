package bridge

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strings"
)

// Version is set at link time.
var Version = "dev"

const (
	defaultAddr     = "127.0.0.1:8787"
	defaultUpstream = "https://chatgpt.com/backend-api/codex"
	minToken        = 32
)

// Home is where the local bearer lives.
func Home() (string, error) {
	if h := os.Getenv("SLIFE_HOME"); h != "" {
		return h, nil
	}
	h, err := os.UserHomeDir()
	if err != nil {
		// A credential helper may be started with an empty environment.
		u, uerr := user.Current()
		if uerr != nil || u.HomeDir == "" {
			return "", err
		}
		h = u.HomeDir
	}
	return filepath.Join(h, ".second-life"), nil
}

// Addr is the listen address.
func Addr() string {
	if a := os.Getenv("SLIFE_ADDR"); a != "" {
		return a
	}
	return defaultAddr
}

// LocalToken reads the bearer, creating it once with 0600.
func LocalToken(home string) (string, error) {
	if filepath.Base(home) == ".second-life" {
		if err := adopt(home); err != nil {
			return "", err
		}
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return "", err
	}
	p := filepath.Join(home, "token")
	if tok, err := readToken(p); !errors.Is(err, fs.ErrNotExist) {
		return tok, err
	}
	tok := NewToken()
	// A whole file appears under the name or none does: another process may read it at any moment.
	f, err := os.CreateTemp(home, ".token-*") // 0600
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	_, err = f.WriteString(tok)
	if err = errors.Join(err, f.Sync(), f.Close()); err != nil {
		return "", err
	}
	switch err := os.Link(f.Name(), p); {
	case err == nil:
		return tok, nil
	case errors.Is(err, fs.ErrExist): // another process was first
		return readToken(p)
	}
	// No hard links here. A rename could replace a token already in use, so the plain way it is.
	n, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return readToken(p)
	}
	if err != nil {
		return "", err
	}
	_, err = n.WriteString(tok)
	return tok, errors.Join(err, n.Close())
}

func readToken(p string) (string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	tok := strings.TrimSpace(string(b))
	if len(tok) < minToken {
		return "", errors.New("local token file is damaged; delete " + p)
	}
	st, err := os.Stat(p)
	if err != nil {
		return "", err
	}
	if st.Mode().Perm()&0o077 != 0 {
		return tok, os.Chmod(p, 0o600)
	}
	return tok, nil
}

// NewToken makes a local bearer.
func NewToken() string {
	raw := make([]byte, 32)
	rand.Read(raw) // never fails
	return hex.EncodeToString(raw)
}

// Proof answers a health nonce, so a client can tell the real bridge from another program on the port. The address is
// part of it, so a program on another port cannot pass on the real bridge's answer.
func Proof(token, addr, nonce string) string {
	m := hmac.New(sha256.New, []byte(token))
	m.Write([]byte("slife-health:" + DialAddr(addr) + "\n" + nonce))
	return hex.EncodeToString(m.Sum(nil))
}

// DialAddr is how a program on this machine reaches a listen address.
func DialAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	switch host {
	case "", "0.0.0.0", "::", "localhost":
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}
