package aimlapi

import (
	"bytes"
	"encoding/json"
)

// The helpers here cut JSON that json.Valid accepted into raw pieces, so what is not changed keeps its bytes: big
// numbers, key order, escapes.

// member is one field of a raw object; at is where val starts in it.
type member struct {
	key         string
	rawKey, val []byte
	at          int
}

func (m member) null() bool { return string(m.val) == "null" }

func skipSpace(raw []byte, i int) int {
	for i < len(raw) && (raw[i] == ' ' || raw[i] == '\t' || raw[i] == '\n' || raw[i] == '\r') {
		i++
	}
	return i
}

// skipString returns the index after the string that starts at i.
func skipString(raw []byte, i int) int {
	for i++; i < len(raw); i++ {
		n := bytes.IndexByte(raw[i:], '"')
		if n < 0 {
			return len(raw)
		}
		end := i + n
		slashes := 0
		for end-1-slashes >= i && raw[end-1-slashes] == '\\' {
			slashes++
		}
		i = end
		if slashes%2 == 0 {
			return end + 1
		}
	}
	return len(raw)
}

// skipValue returns the index after the value that starts at i.
func skipValue(raw []byte, i int) int {
	for depth := 0; i < len(raw); {
		switch raw[i] {
		case '"':
			i = skipString(raw, i)
		case '{', '[':
			depth++
			i++
			continue
		case '}', ']':
			depth--
			i++
		case ',', ':', ' ', '\t', '\n', '\r':
			i++
			continue
		default:
			for i < len(raw) && !bytes.ContainsRune([]byte(",:]} \t\n\r"), rune(raw[i])) {
				i++
			}
		}
		if depth == 0 {
			break
		}
	}
	return i
}

// members splits an object; false for anything else.
func members(raw []byte) ([]member, bool) {
	i := skipSpace(raw, 0)
	if i >= len(raw) || raw[i] != '{' {
		return nil, false
	}
	var out []member
	for i = skipSpace(raw, i+1); i < len(raw) && raw[i] == '"'; {
		end := skipString(raw, i)
		m := member{rawKey: raw[i:end]}
		m.key, _ = text(m.rawKey)
		m.at = skipSpace(raw, skipSpace(raw, end)+1) // past the colon
		end = skipValue(raw, m.at)
		m.val = raw[m.at:end]
		out = append(out, m)
		if i = skipSpace(raw, end); i < len(raw) && raw[i] == ',' {
			i = skipSpace(raw, i+1)
		}
	}
	return out, true
}

// elements splits an array; false for anything else.
func elements(raw []byte) ([][]byte, bool) {
	i := skipSpace(raw, 0)
	if i >= len(raw) || raw[i] != '[' {
		return nil, false
	}
	var out [][]byte
	for i = skipSpace(raw, i+1); i < len(raw) && raw[i] != ']'; {
		end := skipValue(raw, i)
		out = append(out, raw[i:end])
		if i = skipSpace(raw, end); i < len(raw) && raw[i] == ',' {
			i = skipSpace(raw, i+1)
		}
	}
	return out, true
}

// field returns the last member named key, the one every JSON parser keeps.
func field(ms []member, key string) (member, bool) {
	for i := len(ms) - 1; i >= 0; i-- {
		if ms[i].key == key {
			return ms[i], true
		}
	}
	return member{}, false
}

// fieldText is the string in a field, or "".
func fieldText(ms []member, key string) string {
	m, _ := field(ms, key)
	s, _ := text(m.val)
	return s
}

func text(raw []byte) (string, bool) {
	var s string
	if len(raw) < 2 || raw[0] != '"' || json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

func without(ms []member, drop func(member) bool) []member {
	var out []member
	for _, m := range ms {
		if !drop(m) {
			out = append(out, m)
		}
	}
	return out
}

func object(ms []member) []byte {
	out := []byte{'{'}
	for i, m := range ms {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(append(append(out, m.rawKey...), ':'), m.val...)
	}
	return append(out, '}')
}

func array(els [][]byte) []byte {
	out := []byte{'['}
	for i, e := range els {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, e...)
	}
	return append(out, ']')
}

// quote writes a string without the HTML escapes of json.Marshal.
func quote(s string) []byte {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	_ = e.Encode(s) // a string always encodes
	return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
}
