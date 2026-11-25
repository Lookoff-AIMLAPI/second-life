// Package mcpimport copies MCP servers the owner already has, in Codex and in the usual Claude Desktop, to Claude Code.
//
// Everything in a source is untrusted text. The values of variables and headers are kept in memory on their way to the
// target and are never shown, logged or put in an error.
package mcpimport

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Where a server comes from.
const (
	Codex   = "Codex"
	Desktop = "Claude Desktop"
)

// Server is one MCP server. Either Command or URL is set.
type Server struct {
	Name, Source string

	Command string
	Args    []string
	Env     map[string]string

	URL     string
	Headers map[string]string // ${VAR} where Codex reads the value from a variable
	Refs    []string          // the variables Headers refers to

	Inherits []string // variables Codex passes on from its own environment
}

// Format keeps a server out of logs: only its name prints.
func (s Server) Format(f fmt.State, _ rune) { io.WriteString(f, Clean(s.Name)) }

func (s Server) remote() bool { return s.URL != "" }

var namePattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_-]{0,63}$`)

// ValidName: a name that starts with a dash would be read as a flag, and the others mean something to JavaScript.
func ValidName(s string) bool {
	switch s {
	case "__proto__", "constructor", "prototype", "toString", "valueOf", "hasOwnProperty", "isPrototypeOf",
		"propertyIsEnumerable", "toLocaleString", "__defineGetter__", "__defineSetter__", "__lookupGetter__", "__lookupSetter__":
		return false
	}
	return namePattern.MatchString(s)
}

// Line is the command or the address, for the owner to read. Secrets in it are masked.
func (s Server) Line() string {
	var line string
	if s.remote() {
		line = MaskURL(s.hide(s.URL))
	} else {
		words := []string{s.hide(s.Command)}
		for _, a := range s.Args {
			words = append(words, s.hide(a))
		}
		shown := append([]string{maskText(words[0])}, maskArgs(words[1:])...)
		for i, w := range shown {
			if w == "" || strings.ContainsAny(w, " \t\"'") {
				shown[i] = `"` + strings.ReplaceAll(w, `"`, `\"`) + `"`
			}
		}
		line = strings.Join(shown, " ")
	}
	return Clean(line)
}

// hide masks the values of variables and headers wherever else they appear.
func (s Server) hide(text string) string {
	for _, m := range []map[string]string{s.Env, s.Headers} {
		for _, v := range m {
			if len(v) >= 4 && !reference.MatchString(v) {
				text = strings.ReplaceAll(text, v, hidden)
			}
		}
	}
	return text
}

// EnvNames and HeaderNames are sorted and safe to show.
func (s Server) EnvNames() []string    { return names(s.Env) }
func (s Server) HeaderNames() []string { return names(s.Headers) }

func names(m map[string]string) []string {
	out := []string{}
	for _, k := range slices.Sorted(maps.Keys(m)) {
		out = append(out, Clean(k))
	}
	return out
}

// Exposes says whether the JSON for Claude Code holds something that is not shown here.
func (s Server) Exposes() bool {
	if len(s.Env) > 0 || strings.Contains(s.Line(), hidden) {
		return true
	}
	for _, v := range s.Headers {
		if strings.TrimSpace(reference.ReplaceAllString(v, "")) != "" && !strings.HasPrefix(v, "Bearer ${") {
			return true
		}
	}
	return false
}

// JSON is what Claude Code stores.
func (s Server) JSON() (string, error) {
	var v any
	if s.remote() {
		v = struct {
			Type    string            `json:"type"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers,omitempty"`
		}{"http", s.URL, s.Headers}
	} else {
		args := s.Args
		if args == nil {
			args = []string{}
		}
		v = struct {
			Type    string            `json:"type"`
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env,omitempty"`
		}{"stdio", s.Command, args, s.Env}
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", fmt.Errorf("could not write the server as JSON")
	}
	return strings.TrimSpace(b.String()), nil
}

// same compares what two servers do, not what they are called.
func (s Server) same(o Server, m Machine) bool {
	return s.URL == o.URL && m.place(s.Command) == m.place(o.Command) && slices.Equal(s.Args, o.Args) &&
		maps.Equal(s.Env, o.Env) && maps.Equal(s.Headers, o.Headers)
}

// place is the program a command means, or the command as it is.
func (m Machine) place(command string) string {
	if command == "" || strings.ContainsAny(command, `/\`) {
		return filepath.Clean(command)
	}
	if p, err := m.Find(command); err == nil {
		return filepath.Clean(p)
	}
	return command
}
