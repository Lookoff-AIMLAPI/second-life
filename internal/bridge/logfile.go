package bridge

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
)

const maxLog = 5 << 20

// LogPath is the bridge log of a home directory.
func LogPath(home string) string { return filepath.Join(home, "logs", "bridge.log") }

// logFile appends to one file and keeps one older file.
type logFile struct {
	mu   sync.Mutex
	path string
	f    *os.File
	size int64
}

func openLog(home string) *log.Logger {
	p := LogPath(home)
	if os.MkdirAll(filepath.Dir(p), 0o700) != nil {
		return log.New(io.Discard, "", 0)
	}
	return log.New(&logFile{path: p}, "", log.LstdFlags|log.Lmicroseconds)
}

func (l *logFile) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil && l.size+int64(len(b)) > maxLog {
		l.f.Close()
		l.f = nil
		_ = os.Rename(l.path, l.path+".1")
	}
	if l.f == nil {
		f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return len(b), nil // a log problem must not end a request
		}
		if st, err := f.Stat(); err == nil {
			l.size = st.Size()
		}
		l.f = f
	}
	n, err := l.f.Write(b)
	l.size += int64(n)
	return n, err
}
