package desktop

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"unicode/utf8"
)

const maxFile = 8 << 20

// object is a JSON object that keeps the order and the bytes of what it does
// not change.
type object []member

type member struct {
	key string
	val json.RawMessage
}

func parseObject(data []byte) (object, error) {
	if !utf8.Valid(data) || !json.Valid(data) {
		return nil, errors.New("not valid JSON")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, errors.New("not a JSON object")
	}
	o := object{}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, err
		}
		m := member{key: t.(string)}
		if err := dec.Decode(&m.val); err != nil {
			return nil, err
		}
		o = append(o, m)
	}
	return o, nil
}

// get returns the last value, as JavaScript does for a repeated key.
func (o object) get(key string) json.RawMessage {
	for _, m := range slices.Backward(o) {
		if m.key == key {
			return m.val
		}
	}
	return nil
}

func (o object) str(key string) (string, bool) {
	v := o.get(key)
	if len(v) == 0 || v[0] != '"' {
		return "", false
	}
	var s string
	err := json.Unmarshal(v, &s)
	return s, err == nil
}

func (o object) without(key string) object {
	return slices.DeleteFunc(slices.Clone(o), func(m member) bool { return m.key == key })
}

// with puts v where key was, or at the end.
func (o object) with(key string, v any) (object, error) {
	raw, err := marshal(v)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(o, func(m member) bool { return m.key == key })
	if i < 0 {
		i = len(o)
	}
	return slices.Insert(o.without(key), i, member{key, raw}), nil
}

// marshal leaves <, > and & as they are, unlike json.Marshal.
func marshal(v any) (json.RawMessage, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	err := enc.Encode(v)
	return bytes.TrimSpace(b.Bytes()), err
}

// encode writes the object the way Claude does: two spaces.
func (o object) encode() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, m := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		key, err := marshal(m.key)
		if err != nil {
			return nil, err
		}
		b.Write(key)
		b.WriteByte(':')
		b.Write(m.val)
	}
	b.WriteByte('}')
	var out bytes.Buffer
	err := json.Indent(&out, b.Bytes(), "", "  ")
	return out.Bytes(), err
}

// readFile reads a plain file and refuses anything else, symlinks included.
func readFile(path string) (data []byte, existed bool, mode fs.FileMode, err error) {
	st, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, 0o600, nil
	}
	if err != nil {
		return nil, false, 0, err
	}
	name := filepath.Base(path)
	if !st.Mode().IsRegular() {
		return nil, true, 0, fmt.Errorf("%s is not a plain file", name)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, true, 0, err
	}
	defer f.Close()
	if opened, err := f.Stat(); err != nil || !os.SameFile(st, opened) {
		return nil, true, 0, fmt.Errorf("%s changed while it was read", name)
	}
	data, err = io.ReadAll(io.LimitReader(f, maxFile+1))
	if err == nil && len(data) > maxFile {
		err = fmt.Errorf("%s is too large", name)
	}
	return data, true, st.Mode().Perm(), err
}

// checkDir allows a real directory or nothing at all.
func checkDir(path string) error {
	st, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("%s is not a plain folder", filepath.Base(path))
	}
	return nil
}

func writeAtomic(path string, data []byte, mode fs.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".slife-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name()) // fails harmlessly after the rename
	_, err = f.Write(data)
	if err == nil {
		err = f.Chmod(mode)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// change replaces one file. old is what the new content was made from.
type change struct {
	path    string
	old     []byte
	existed bool
	mode    fs.FileMode
	data    []byte
}

// edit reads path, lets fn rewrite the object and returns the change. A missing
// file counts as an empty object.
func edit(path string, fn func(object) (object, error)) (change, error) {
	c := change{path: path}
	var err error
	if c.old, c.existed, c.mode, err = readFile(path); err != nil {
		return c, err
	}
	o := object{}
	if c.existed {
		if o, err = parseObject(c.old); err != nil {
			return c, fmt.Errorf("%s: %w", filepath.Base(path), err)
		}
	}
	if o, err = fn(o); err != nil {
		return c, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	c.data, err = o.encode()
	return c, err
}

type writeFunc func(path string, data []byte, mode fs.FileMode) error

// apply makes all changes or none. The returned undo puts everything back.
func apply(changes []change, write writeFunc) (undo func() error, err error) {
	var done []change
	var dirs []string
	undo = func() error {
		var errs []error
		for _, c := range slices.Backward(done) {
			if c.existed {
				errs = append(errs, writeAtomic(c.path, c.old, c.mode))
			} else if err := os.Remove(c.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, err)
			}
		}
		for _, d := range slices.Backward(dirs) {
			os.Remove(d) // only when empty
		}
		return errors.Join(errs...)
	}
	step := func(c change) error {
		made, err := mkdirs(filepath.Dir(c.path))
		dirs = append(dirs, made...)
		if err != nil {
			return err
		}
		now, existed, _, err := readFile(c.path)
		if err != nil {
			return err
		}
		if existed != c.existed || !bytes.Equal(now, c.old) {
			return fmt.Errorf("%s changed meanwhile", filepath.Base(c.path))
		}
		done = append(done, c)
		return write(c.path, c.data, 0o600)
	}
	for _, c := range changes {
		if err := step(c); err != nil {
			if uerr := undo(); uerr != nil {
				return nil, fmt.Errorf("%w; and putting the old files back failed: %v", err, uerr)
			}
			return nil, err
		}
	}
	return undo, nil
}

// mkdirs creates missing directories, 0700, and returns those it made.
func mkdirs(dir string) (made []string, err error) {
	var missing []string
	for d := dir; ; d = filepath.Dir(d) {
		_, err := os.Lstat(d)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrNotExist) || d == filepath.Dir(d) {
			return nil, err
		}
		missing = append(missing, d)
	}
	for _, d := range slices.Backward(missing) {
		if err := os.Mkdir(d, 0o700); err != nil {
			return made, err
		}
		made = append(made, d)
	}
	return made, nil
}

const keepBackups = 5

var backupName = regexp.MustCompile(`^\d{8}-\d{6}\.\d{3}(-\d+)?$`)

// backup copies the old content of every changed file to a new folder.
func backup(root, stamp, base string, changes []change) error {
	if err := checkDir(root); err != nil {
		return err
	}
	if _, err := mkdirs(root); err != nil {
		return err
	}
	dir := filepath.Join(root, stamp)
	err := os.Mkdir(dir, 0o700)
	for i := 1; errors.Is(err, fs.ErrExist) && i < 100; i++ {
		dir = filepath.Join(root, fmt.Sprintf("%s-%d", stamp, i))
		err = os.Mkdir(dir, 0o700)
	}
	if err != nil {
		return err
	}
	for _, c := range changes {
		if !c.existed {
			continue
		}
		rel, err := filepath.Rel(base, c.path)
		if err != nil || !filepath.IsLocal(rel) {
			return errors.New("unexpected path " + c.path)
		}
		to := filepath.Join(dir, rel)
		if _, err := mkdirs(filepath.Dir(to)); err != nil {
			return err
		}
		if err := writeAtomic(to, c.old, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// prune keeps the newest backups and touches nothing it did not name.
func prune(root string) {
	list, err := os.ReadDir(root)
	if err != nil {
		return
	}
	var names []string
	for _, e := range list {
		if e.IsDir() && backupName.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	slices.Sort(names)
	for len(names) > keepBackups {
		os.RemoveAll(filepath.Join(root, names[0]))
		names = names[1:]
	}
}
