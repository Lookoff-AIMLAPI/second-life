// Package keystore keeps the AIMLAPI key and the keys of custom endpoints in the system's secret store. A key never goes
// into a log, an error or a command line.
package keystore

import (
	"errors"
	"os"
	"regexp"
	"strings"
)

const (
	service = "second-life"
	former  = "codex-to-claude" // the service under the first name of the app
	aimlapi = "aimlapi"         // also the account its key has always been stored under
	envVar  = "AIMLAPI_API_KEY"

	minLen, maxLen = 16, 512
)

var (
	// ErrNone means no key is stored.
	ErrNone = errors.New("no AIMLAPI key is stored")
	// ErrShape means the text cannot be a key.
	ErrShape = errors.New("that does not look like an AIMLAPI key")

	errDamaged = errors.New("the stored AIMLAPI key is damaged; set it again")
	errEnv     = errors.New(envVar + " does not look like an AIMLAPI key")
	errSave    = errors.New("the key was not saved")
	errID      = errors.New("there is no key store for that id")

	// The same errors for an endpoint.
	endpointErrors = map[error]error{
		ErrNone:    reworded{"no key is stored for this endpoint", ErrNone},
		ErrShape:   reworded{"a key has 1 to 512 letters, digits or signs and no spaces", ErrShape},
		errDamaged: reworded{"the stored key of this endpoint is damaged; set it again", errDamaged},
	}

	endpointID = regexp.MustCompile(`^ep-[0-9a-f]{8}$`) // as in package route
)

type reworded struct {
	text string
	is   error
}

func (r reworded) Error() string { return r.text }
func (r reworded) Unwrap() error { return r.is }

// backend is one place for one key. get returns ErrNone when there is none, and delete of a missing key is no error.
type backend interface {
	get() (string, error)
	set(key string) error
	delete() error
}

// inherited reads a key saved under the app's first name when there is none under this one. Deleting removes both, or
// the old one would come back.
type inherited struct{ now, then backend }

func (i inherited) get() (string, error) {
	k, err := i.now.get()
	if errors.Is(err, ErrNone) {
		return i.then.get()
	}
	return k, err
}

func (i inherited) set(k string) error { return i.now.set(k) }

func (i inherited) delete() error { return errors.Join(i.now.delete(), i.then.delete()) }

// open returns the place of an account in the store of this system; tests replace it.
var open = native

// Store holds one key.
type Store struct{ id string }

// For returns the store of "aimlapi" or of an endpoint id. With any other id every method fails.
func For(id string) Store { return Store{id} }

// Get returns the stored key. For "aimlapi" AIMLAPI_API_KEY wins; endpoints have no such override.
func (s Store) Get() (string, error) {
	if s.id == aimlapi {
		if env := strings.TrimSpace(os.Getenv(envVar)); env != "" {
			if !s.shape(env) {
				return "", errEnv
			}
			return env, nil
		}
	}
	b, err := s.open()
	if err != nil {
		return "", err
	}
	key, err := b.get()
	if err != nil {
		return "", s.word(err)
	}
	if !s.shape(key) {
		return "", s.word(errDamaged)
	}
	return key, nil
}

// Set stores a key; white space around it is cut.
func (s Store) Set(key string) error {
	b, err := s.open()
	if err != nil {
		return err
	}
	key = strings.TrimSpace(key)
	if !s.shape(key) {
		return s.word(ErrShape)
	}
	if err := b.set(key); err != nil {
		return s.word(err)
	}
	if got, err := b.get(); err != nil || got != key {
		return errSave
	}
	return nil
}

// Delete removes the stored key. It does not touch AIMLAPI_API_KEY.
func (s Store) Delete() error {
	b, err := s.open()
	if err != nil {
		return err
	}
	return s.word(b.delete())
}

// open checks the id: it becomes a file name, or part of a line the Keychain tool reads.
func (s Store) open() (backend, error) {
	if s.id != aimlapi && !endpointID.MatchString(s.id) {
		return nil, errID
	}
	return open(s.id)
}

// shape is what a key may look like. Local servers take any short word.
func (s Store) shape(key string) bool {
	least := 1
	if s.id == aimlapi {
		least = minLen
	}
	return len(key) >= least && len(key) <= maxLen && !strings.ContainsFunc(key, func(r rune) bool { return r <= ' ' || r > '~' })
}

func (s Store) word(err error) error {
	if e, ok := endpointErrors[err]; ok && s.id != aimlapi {
		return e
	}
	return err
}

// Get returns the AIMLAPI key from AIMLAPI_API_KEY, or the stored one.
func Get() (string, error) { return For(aimlapi).Get() }

// Set stores the AIMLAPI key.
func Set(key string) error { return For(aimlapi).Set(key) }

// Delete removes the stored AIMLAPI key.
func Delete() error { return For(aimlapi).Delete() }
