package translate

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
)

const stDoneMark = "slife.done" // ReadSSE saw [DONE]

// stStartsJSON skips what may come before the body and tells whether it is JSON and not an event stream.
func stStartsJSON(br *bufio.Reader) (bool, error) {
	if b, _ := br.Peek(3); bytes.Equal(b, []byte("\xef\xbb\xbf")) {
		br.Discard(3)
	}
	for {
		b, err := br.ReadByte()
		if err == io.EOF {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if b != ' ' && b != '\t' && b != '\r' && b != '\n' {
			return b == '{' || b == '[', br.UnreadByte()
		}
	}
}

// stWhole gives a type to bodies that have none.
func stWhole(fn func(map[string]any) error) func(map[string]any) error {
	return func(ev map[string]any) error {
		if _, ok := ev["type"]; ok {
			return fn(ev)
		}
		if ev["object"] == "response" {
			switch status := stStr(ev["status"]); status {
			case "completed", "incomplete", "failed":
				return fn(map[string]any{"type": "response." + status, "response": ev})
			}
		}
		if chatHasError(ev["error"]) {
			ev["type"] = "error"
		}
		return fn(ev)
	}
}

// stReadJSON reads JSON objects, one after another or in an array, as they arrive. Anything between them is skipped.
func stReadJSON(br *bufio.Reader, fn func(map[string]any) error) error {
	var scan jsonScan
	var value, between []byte
	buf := make([]byte, 32<<10)
	for {
		n, err := br.Read(buf)
		p := buf[:n]
		for len(p) > 0 {
			if !scan.inside() {
				i := bytes.IndexByte(p, '{')
				if i < 0 {
					i = len(p)
				}
				if between = append(between, p[:i]...); len(between) > 64 {
					between = between[len(between)-64:]
				}
				if bytes.Contains(between, []byte("[DONE]")) {
					between = nil
					if err := fn(map[string]any{"type": stDoneMark}); err != nil {
						return err
					}
				}
				if p = p[i:]; len(p) == 0 {
					break
				}
			}
			end, complete := scan.feed(p)
			if len(value)+end > stMaxEvent {
				return errStEventSize
			}
			value, p = append(value, p[:end]...), p[end:]
			if !complete {
				continue
			}
			var ev map[string]any
			if json.Unmarshal(value, &ev) != nil {
				ev = map[string]any{"type": stMalformed}
			}
			value, between, scan = value[:0], nil, jsonScan{}
			if err := fn(ev); err != nil {
				return err
			}
		}
		if err == io.EOF {
			if scan.inside() {
				return fn(map[string]any{"type": stMalformed})
			}
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// jsonScan finds the end of a JSON object that arrives in pieces.
type jsonScan struct {
	depth    int
	str, esc bool
}

func (s *jsonScan) inside() bool { return s.depth > 0 }

// feed returns the length of the part of p that belongs to the object. The first byte of an object must be its brace.
func (s *jsonScan) feed(p []byte) (n int, complete bool) {
	for i, b := range p {
		switch {
		case s.esc:
			s.esc = false
		case s.str:
			s.esc, s.str = b == '\\', b != '"'
		case b == '"':
			s.str = true
		case b == '{' || b == '[':
			s.depth++
		case b == '}' || b == ']':
			if s.depth--; s.depth <= 0 {
				return i + 1, true
			}
		}
	}
	return len(p), false
}
