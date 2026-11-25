package mcpimport

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

const maxFile = 64 << 20 // ~/.claude.json grows with every project

// found is a server as it was found, with the reason why it stays where it is.
type found struct {
	Server
	skip string
}

type codexServer struct {
	Name      string `json:"name"`
	Enabled   *bool  `json:"enabled"`
	Auth      string `json:"auth_status"`
	Transport struct {
		Type       string            `json:"type"`
		Command    string            `json:"command"`
		Args       []string          `json:"args"`
		Env        map[string]string `json:"env"`
		EnvVars    []json.RawMessage `json:"env_vars"`
		Cwd        string            `json:"cwd"`
		URL        string            `json:"url"`
		Bearer     string            `json:"bearer_token_env_var"`
		Headers    map[string]string `json:"http_headers"`
		EnvHeaders map[string]string `json:"env_http_headers"`
	} `json:"transport"`
}

// readCodex asks the Codex CLI. Its output holds values, so an error never repeats any of it.
func (m Machine) readCodex(ctx context.Context) ([]found, error) {
	bin, err := m.Find("codex")
	if err != nil {
		return nil, errors.New("the Codex CLI was not found")
	}
	out, err := m.Run(ctx, bin, "mcp", "list", "--json")
	if err != nil {
		return nil, errors.New("the Codex CLI could not list its servers")
	}
	var list []json.RawMessage
	if json.Unmarshal(out, &list) != nil {
		return nil, errors.New("the Codex CLI gave a list this version does not understand")
	}
	var servers []found
	for _, raw := range list {
		var c codexServer
		if json.Unmarshal(raw, &c) != nil {
			var only struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal(raw, &only)
			servers = append(servers, found{Server{Name: only.Name, Source: Codex}, "its settings could not be read"})
			continue
		}
		servers = append(servers, c.found(m))
	}
	return servers, nil
}

func (c codexServer) found(m Machine) found {
	t := c.Transport
	f := found{Server: Server{Name: c.Name, Source: Codex}}
	switch t.Type {
	case "stdio":
		f.Command, f.Args, f.Env = t.Command, t.Args, t.Env
	case "streamable_http", "http":
		f.URL, f.Headers = t.URL, map[string]string{}
		for k, v := range t.Headers {
			f.Headers[k] = v
		}
	}
	// The order is the order of what matters to the owner.
	switch {
	case c.Enabled != nil && !*c.Enabled:
		f.skip = "switched off in Codex"
	case m.codexOwn(f.Server):
		f.skip = "works only inside Codex"
	case f.Command == "" && f.URL == "":
		f.skip = "its kind of connection is not known"
	case t.Cwd != "":
		f.skip = "it needs its own working folder, which Claude Code cannot set"
	case !f.remote():
		for _, raw := range t.EnvVars {
			name, ok := envVar(raw)
			if !ok {
				f.skip = "it takes variables from a place Claude Code does not have"
				break
			}
			f.Inherits = append(f.Inherits, name)
		}
	case strings.ReplaceAll(c.Auth, "_", "") == "oauth" || c.Auth == "not_logged_in":
		f.skip = "it signs in with OAuth, and a sign-in cannot be copied; add it in Claude and sign in there"
	}
	if f.skip == "" {
		f.skip = f.unsafe(true)
	}
	if f.skip != "" || !f.remote() {
		return f
	}
	if t.Bearer != "" {
		f.refer("Authorization", "Bearer ", t.Bearer)
	}
	for k, v := range t.EnvHeaders {
		f.refer(k, "", v)
	}
	slices.Sort(f.Refs)
	if f.skip == "" {
		f.skip = f.unsafe(false)
	}
	return f
}

// refer puts ${name} in a header: Claude Code fills it in when it connects.
func (f *found) refer(header, prefix, name string) {
	switch {
	case !envPattern.MatchString(name):
		f.skip = "the name of a variable it uses is not valid"
	case neverExpanded(name):
		f.skip = "it uses the variable " + Clean(name) + ", which Claude Code never sends to a server"
	}
	for k := range f.Headers {
		if strings.EqualFold(k, header) {
			f.skip = "it sets the header " + Clean(header) + " twice"
		}
	}
	f.Headers[header] = prefix + "${" + name + "}"
	f.Refs = append(f.Refs, name)
}

// envVar is a name, or an object with a name that is taken from this computer.
func envVar(raw json.RawMessage) (string, bool) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, envPattern.MatchString(s)
	}
	var o struct {
		Name   string `json:"name"`
		Source string `json:"source"`
	}
	if json.Unmarshal(raw, &o) != nil || o.Source != "" && o.Source != "local" {
		return "", false
	}
	return o.Name, envPattern.MatchString(o.Name)
}

// Local reads one entry of Claude Desktop's mcpServers. Claude itself takes nothing but a command there.
func Local(name string, raw json.RawMessage) (Server, bool) {
	var e struct {
		Command   *string            `json:"command"`
		Args      []*string          `json:"args"`
		Env       map[string]*string `json:"env"`
		Extension json.RawMessage    `json:"extensionId"`
	}
	s := Server{Name: name, Source: Desktop}
	if json.Unmarshal(raw, &e) != nil || e.Command == nil || strings.TrimSpace(*e.Command) == "" || e.Extension != nil {
		return s, false
	}
	s.Command = *e.Command
	for _, a := range e.Args {
		if a == nil {
			return s, false
		}
		s.Args = append(s.Args, *a)
	}
	for k, v := range e.Env {
		if v == nil {
			return s, false
		}
		if s.Env == nil {
			s.Env = map[string]string{}
		}
		s.Env[k] = *v
	}
	return s, true
}

// readDesktop reads the usual profile of Claude Desktop.
func (m Machine) readDesktop() ([]found, error) {
	raw, err := m.servers(m.Usual)
	if err != nil {
		return nil, err
	}
	var out []found
	for _, name := range sorted(raw) {
		s, ok := Local(name, raw[name])
		f := found{Server: s}
		if !ok {
			f.skip = "Claude Desktop does not run it from this file either"
		} else if f.skip = m.Unusable(s); f.skip == "" {
			f.skip = f.unsafe(true)
		}
		out = append(out, f)
	}
	return out, nil
}

// readClaudeCode is what user scope has.
func (m Machine) readClaudeCode() (map[string]Server, error) {
	raw, err := m.servers(m.ClaudeCode)
	if err != nil {
		return nil, err
	}
	out := map[string]Server{}
	for name, r := range raw {
		var e struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		}
		_ = json.Unmarshal(r, &e)
		out[name] = Server{Name: name, Command: e.Command, Args: e.Args, Env: e.Env, URL: e.URL, Headers: e.Headers}
	}
	return out, nil
}

// servers reads mcpServers of a settings file; a missing file has none.
func (m Machine) servers(path string) (map[string]json.RawMessage, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	base := filepath.Base(path)
	if err != nil {
		return nil, errors.New(base + " could not be opened")
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() {
		return nil, errors.New(base + " is not a plain file")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxFile+1))
	if err != nil || len(data) > maxFile {
		return nil, errors.New(base + " could not be read")
	}
	var top struct {
		Servers map[string]json.RawMessage `json:"mcpServers"`
	}
	if json.Unmarshal(data, &top) != nil {
		return nil, errors.New(base + " is not valid")
	}
	return top.Servers, nil
}

func sorted[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
