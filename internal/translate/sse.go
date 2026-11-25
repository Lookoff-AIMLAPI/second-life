package translate

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"github.com/Lookoff-AIMLAPI/second-life/internal/dto"
)

const stMaxEvent = 32 << 20

// ReadMode changes what ReadSSE accepts.
type ReadMode int

// Lenient is for servers other than Codex: [DONE] is an event of its own, and a JSON body, JSON lines and a stream
// without blank lines are read too. A JSON body of the Responses API becomes its final event.
const Lenient ReadMode = 1

var errStEventSize = errors.New("upstream event is larger than 32 MB")

// ReadSSE calls fn for every JSON event in r, as soon as its blank line arrives. It returns nil at a clean EOF;
// a read failure, an event over 32 MB, or an error from fn ends it. An event that is not a JSON object is skipped, and
// fn gets a stMalformed event in its place, which Stream counts.
func ReadSSE(r io.Reader, fn func(map[string]any) error, mode ...ReadMode) error {
	br := bufio.NewReaderSize(r, 64<<10)
	lenient := len(mode) > 0 && mode[0] == Lenient
	if lenient {
		fn = stWhole(fn)
		if isJSON, err := stStartsJSON(br); err != nil {
			return err
		} else if isJSON {
			return stReadJSON(br, fn)
		}
	}
	var line, data []byte
	var name string
	dispatch := func() error {
		body, event := bytes.TrimSpace(data), name
		data, name = data[:0], ""
		if string(body) == "[DONE]" && lenient {
			return fn(map[string]any{"type": stDoneMark})
		}
		if len(body) == 0 || string(body) == "[DONE]" {
			return nil
		}
		var ev map[string]any
		if err := json.Unmarshal(body, &ev); err != nil || ev == nil {
			return fn(map[string]any{"type": stMalformed})
		}
		if _, ok := ev["type"]; !ok && event != "" {
			ev["type"] = event
		}
		return fn(ev)
	}
	for first := true; ; first = false {
		line = line[:0]
		var err error
		for more := true; more; {
			var chunk []byte
			chunk, err = br.ReadSlice('\n')
			if len(data)+len(line)+len(chunk) > stMaxEvent {
				return errStEventSize
			}
			line = append(line, chunk...)
			more = err == bufio.ErrBufferFull
		}
		if err != nil && err != io.EOF {
			return err
		}
		eof := err == io.EOF
		l := bytes.TrimSuffix(bytes.TrimSuffix(line, []byte("\n")), []byte("\r"))
		if first {
			l = bytes.TrimPrefix(l, []byte("\xef\xbb\xbf"))
		}
		field, value, _ := bytes.Cut(l, []byte(":"))
		value = bytes.TrimPrefix(value, []byte(" "))
		if lenient && len(l) > 0 && l[0] == '{' && json.Valid(l) { // an error without "data:"
			field, value = []byte("data"), l
		}
		now := false // the event is complete: do not wait for its blank line
		switch string(field) {
		case "data":
			now = lenient && len(data) == 0 && (string(bytes.TrimSpace(value)) == "[DONE]" || bytes.HasPrefix(value, []byte("{")) && json.Valid(value))
			data = append(append(data, value...), '\n')
		case "event":
			name = string(value)
		}
		if len(l) == 0 || eof || now {
			if err := dispatch(); err != nil {
				return err
			}
		}
		if eof {
			return nil
		}
	}
}

// WriteEvent writes one Anthropic SSE event in a single Write.
func WriteEvent(w io.Writer, e dto.Event) error {
	var b bytes.Buffer
	b.WriteString("event: " + e.Name + "\ndata: ")
	enc := json.NewEncoder(&b) // ends with a newline
	enc.SetEscapeHTML(false)
	if err := enc.Encode(e.Data); err != nil {
		return err
	}
	b.WriteByte('\n')
	_, err := w.Write(b.Bytes())
	return err
}
