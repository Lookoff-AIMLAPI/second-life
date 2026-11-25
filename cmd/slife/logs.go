package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"time"

	"github.com/Lookoff-AIMLAPI/second-life/internal/bridge"
)

const (
	logLines = 100
	logTail  = 1 << 20 // how far back to look for them
	logPoll  = 500 * time.Millisecond
)

func logs(follow bool) error {
	home, err := bridge.Home()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), endSignals()...)
	defer stop()
	return showLog(ctx, os.Stdout, bridge.LogPath(home), follow)
}

// showLog prints the last lines and, with follow, what comes after until ctx ends.
func showLog(ctx context.Context, w io.Writer, path string, follow bool) error {
	f, err := os.Open(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if !follow {
			return errors.New("no log yet: " + path)
		}
		f = nil
	}
	// f changes when the log is rotated.
	defer func() {
		if f != nil {
			f.Close()
		}
	}()
	if f != nil {
		if err := tail(w, f); err != nil {
			return err
		}
	}
	for follow {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(logPoll):
		}
		now, err := os.Stat(path)
		if err != nil {
			continue // between two files
		}
		var rotated bool
		if f != nil {
			if err := drain(w, f); err != nil {
				return err
			}
			st, err := f.Stat()
			if err != nil {
				return err
			}
			pos, _ := f.Seek(0, io.SeekCurrent)
			rotated = !os.SameFile(st, now) || st.Size() < pos
		}
		if f == nil || rotated {
			n, err := os.Open(path)
			if err != nil {
				continue
			}
			if f != nil {
				f.Close()
			}
			f = n
			if err := drain(w, f); err != nil {
				return err
			}
		}
	}
	return nil
}

func tail(w io.Writer, f *os.File) error {
	st, err := f.Stat()
	if err != nil {
		return err
	}
	from := max(0, st.Size()-logTail)
	b := make([]byte, st.Size()-from)
	n, err := f.ReadAt(b, from)
	if err != nil && err != io.EOF {
		return err
	}
	b = b[:n]
	if _, err := f.Seek(from+int64(n), io.SeekStart); err != nil {
		return err
	}
	if from > 0 { // the first line is likely cut
		_, b, _ = bytes.Cut(b, []byte("\n"))
	}
	lines := bytes.SplitAfter(b, []byte("\n"))
	if len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > logLines {
		lines = lines[len(lines)-logLines:]
	}
	_, err = fmt.Fprint(w, plain(string(bytes.Join(lines, nil))))
	return err
}

func drain(w io.Writer, f *os.File) error {
	b, err := io.ReadAll(f)
	if err != nil || len(b) == 0 {
		return err
	}
	_, err = fmt.Fprint(w, plain(string(b)))
	return err
}
