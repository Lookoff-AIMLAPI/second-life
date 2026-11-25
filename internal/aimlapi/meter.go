package aimlapi

import (
	"bytes"
	"encoding/json"
	"io"

	"github.com/Lookoff-AIMLAPI/second-life/internal/dto"
)

const (
	maxEvent = 32 << 20 // one SSE event that the meter reads
	maxJSON  = 64 << 20 // one whole answer
	maxHold  = 4 << 20  // what the model rewrite may hold back of a stream
)

// Meter reads a copy of an answer, SSE or JSON, cut anywhere. It is not safe for concurrent use.
type Meter struct {
	// Seen, when set, is called with the name of every stream event, as it comes.
	Seen func(event string)

	kind  byte // 0 until the first byte, then 's' or 'j'
	line  []byte
	name  string // of the current event
	data  []byte
	over  bool // the current event is too big to read
	body  []byte
	stale bool // body has bytes that were not parsed

	usage    dto.Usage
	done     bool
	failed   *dto.APIError
	reported bool
}

// NewMeter returns a meter for one answer: io.TeeReader(resp.Body, meter).
func NewMeter() *Meter { return &Meter{} }

// Write never fails.
func (m *Meter) Write(p []byte) (int, error) {
	n := len(p)
	if m.kind == 0 {
		p = bytes.TrimPrefix(p, []byte("\xef\xbb\xbf"))
		if p = bytes.TrimLeft(p, " \t\r\n"); len(p) == 0 {
			return n, nil
		}
		m.kind = 's'
		if p[0] == '{' {
			m.kind = 'j'
		}
	}
	if m.kind == 'j' {
		if len(m.body)+len(p) > maxJSON {
			m.over = true
		} else if !m.over {
			m.body, m.stale = append(m.body, p...), true
		}
		return n, nil
	}
	for {
		i := bytes.IndexByte(p, '\n')
		part := p
		if i >= 0 {
			part = p[:i]
		}
		if len(m.line)+len(part) <= maxEvent {
			m.line = append(m.line, part...)
		} else {
			m.over = true
		}
		if i < 0 {
			return n, nil
		}
		p = p[i+1:]
		m.endLine()
	}
}

func (m *Meter) endLine() {
	l := bytes.TrimSuffix(m.line, []byte("\r"))
	if len(l) == 0 {
		m.dispatch()
	} else {
		f, v, _ := bytes.Cut(l, []byte(":"))
		v = bytes.TrimPrefix(v, []byte(" "))
		switch string(f) {
		case "event":
			m.name = string(v)
		case "data":
			if len(m.data)+len(v) > maxEvent {
				m.over = true
			} else if !skipped(m.name) {
				m.data = append(append(m.data, v...), '\n')
			}
		}
	}
	if cap(m.line) > 1<<20 {
		m.line = nil
	}
	m.line = m.line[:0]
}

// skipped events are the bulk of a stream and hold nothing to count.
func skipped(name string) bool {
	switch name {
	case "content_block_start", "content_block_delta", "content_block_stop", "ping":
		return true
	}
	return false
}

func (m *Meter) dispatch() {
	name, data, over := m.name, bytes.TrimSpace(m.data), m.over
	m.name, m.data, m.over = "", m.data[:0], false
	if name == "" && len(data) == 0 {
		return
	}
	var ev map[string]any
	if !over && !skipped(name) {
		_ = json.Unmarshal(data, &ev)
	}
	if t, ok := ev["type"].(string); ok {
		name = t
	}
	if name == "" && ev["error"] != nil {
		name = "error"
	}
	if m.Seen != nil && name != "" {
		m.Seen(name)
	}
	switch name {
	case "message_start":
		msg, _ := ev["message"].(map[string]any)
		m.count(msg["usage"])
	case "message_delta":
		m.count(ev["usage"])
	case "message_stop":
		m.done = true
	case "error":
		if m.failed == nil {
			m.failed = classify(0, ev, "")
			m.reported = ev["type"] == "error"
		}
	}
}

// count takes the fields that are there: a message_delta may repeat all of them or only the output.
func (m *Meter) count(v any) {
	u, _ := v.(map[string]any)
	for k, to := range map[string]*int{
		"input_tokens": &m.usage.InputTokens, "output_tokens": &m.usage.OutputTokens,
		"cache_creation_input_tokens": &m.usage.CacheCreationInputTokens, "cache_read_input_tokens": &m.usage.CacheReadInputTokens,
	} {
		if f, ok := u[k].(float64); ok && f >= 0 && f < 1<<40 {
			*to = int(f)
		}
	}
}

func (m *Meter) parse() {
	if !m.stale {
		return
	}
	m.stale = false
	var ev map[string]any
	if json.Unmarshal(m.body, &ev) != nil {
		return // not complete yet
	}
	if ev["type"] == "error" || ev["error"] != nil {
		m.failed, m.reported = classify(0, ev, ""), ev["type"] == "error"
		return
	}
	m.done = true
	m.count(ev["usage"])
}

// Usage is what the answer has reported so far.
func (m *Meter) Usage() dto.Usage { m.parse(); return m.usage }

// Done reports that the answer is complete: message_stop, or a whole JSON message.
func (m *Meter) Done() bool { m.parse(); return m.done }

// Failed is the error the answer ended with, or nil.
func (m *Meter) Failed() *dto.APIError { m.parse(); return m.failed }

// Reported tells whether the failure was an error event that Anthropic clients read. The gateway also ends a native
// stream with {"error":{...}} and no type; Claude needs a proper event after that one.
func (m *Meter) Reported() bool { m.parse(); return m.reported }

// RewriteModel replaces the model name of an answer, in message_start or at the top of a JSON message, and nothing
// else. A stream is not held back, except a line that is not complete before message_start.
func RewriteModel(r io.Reader, id string) io.Reader { return &rewriter{src: r, id: quote(id)} }

type rewriter struct {
	src       io.Reader
	id        []byte
	kind      byte // 0 unknown, 's' stream, 'j' JSON, 'p' pass
	hold, out []byte
	err       error
}

func (w *rewriter) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for len(w.out) == 0 {
		if w.err != nil {
			return 0, w.err
		}
		if w.kind == 'p' {
			return w.src.Read(p)
		}
		buf := make([]byte, 32<<10)
		n, err := w.src.Read(buf)
		w.hold, w.err = append(w.hold, buf[:n]...), err
		w.step()
	}
	n := copy(p, w.out)
	w.out = w.out[n:]
	return n, nil
}

func (w *rewriter) step() {
	if w.kind == 0 {
		switch b := bytes.TrimLeft(bytes.TrimPrefix(w.hold, []byte("\xef\xbb\xbf")), " \t\r\n"); {
		case len(b) == 0:
		case b[0] == '{':
			w.kind = 'j'
		default:
			w.kind = 's'
		}
	}
	for w.kind == 's' {
		i := bytes.IndexByte(w.hold, '\n')
		if i < 0 {
			break
		}
		line := w.hold[:i+1]
		w.hold = w.hold[i+1:]
		if now, ok := w.stream(line); ok {
			w.out, w.kind = append(w.out, now...), 'p'
		} else {
			w.out = append(w.out, line...)
		}
	}
	if w.err != nil {
		switch w.kind {
		case 's':
			if now, ok := w.stream(w.hold); ok {
				w.hold = now
			}
		case 'j':
			w.hold = w.replace(w.hold, 0, w.hold)
		}
	}
	if w.err != nil || w.kind == 'p' || w.kind == 's' && len(w.hold) > maxHold || len(w.hold) > maxJSON {
		w.out, w.hold, w.kind = append(w.out, w.hold...), nil, 'p'
	}
}

// stream rewrites a line of an event stream; ok when it is the data of message_start.
func (w *rewriter) stream(line []byte) (out []byte, ok bool) {
	data, ok := bytes.CutPrefix(line, []byte("data:"))
	if !ok || !bytes.Contains(data, []byte("message_start")) || !json.Valid(data) {
		return line, false
	}
	ms, _ := members(data)
	if fieldText(ms, "type") != "message_start" {
		return line, false
	}
	msg, _ := field(ms, "message")
	return w.replace(line, len("data:")+msg.at, msg.val), true
}

// replace changes the model in obj, which starts at index at of all, give or take white space it has none of.
func (w *rewriter) replace(all []byte, at int, obj []byte) []byte {
	if !json.Valid(obj) {
		return all
	}
	ms, _ := members(obj)
	var out []byte
	end := 0
	for _, m := range ms {
		if _, ok := text(m.val); m.key == "model" && ok {
			out = append(append(out, all[end:at+m.at]...), w.id...)
			end = at + m.at + len(m.val)
		}
	}
	return append(out, all[end:]...)
}
