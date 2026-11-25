package mcpimport

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"strings"
)

// Item is one server for the owner to look at.
type Item struct {
	Server
	Skip  string // why it is not offered
	Twins []Item // the same server in the other source, perhaps with another name
}

// Report is what was found, and what is already there.
type Report struct {
	Items    []Item
	Problems map[string]string // by source
	Code     []string          // names in Claude Code
	Gateway  []string          // names in Claude Desktop's gateway profile
}

// Load reads the sources and compares them with Claude Code. An error means that Claude Code's own list is unknown.
func (m Machine) Load(ctx context.Context) (Report, error) {
	have, err := m.readClaudeCode()
	if err != nil {
		return Report{}, err
	}
	r := Report{Problems: map[string]string{}, Code: sorted(have)}
	if raw, err := m.servers(m.Gateway); err == nil {
		r.Gateway = sorted(raw)
	}
	var all []found
	codex, err := m.readCodex(ctx)
	if err != nil {
		r.Problems[Codex] = err.Error()
	}
	all = append(all, codex...)
	desk, err := m.readDesktop()
	if err != nil {
		r.Problems[Desktop] = err.Error()
	}
	all = append(all, desk...)
	for i := range all {
		f := &all[i]
		if f.skip == "" {
			f.skip = m.Unusable(f.Server)
		}
		if f.skip == "" {
			f.skip = m.there(f.Server, have)
		}
	}
	for _, f := range all {
		it := Item{Server: f.Server, Skip: f.skip}
		i := m.twin(r.Items, f.Server)
		if i < 0 {
			r.Items = append(r.Items, it)
			continue
		}
		old := &r.Items[i]
		if old.Skip != "" && it.Skip == "" { // the one that can be added is the one to show
			it.Twins = append(old.Twins, Item{Server: old.Server, Skip: old.Skip})
			*old = it
		} else {
			old.Twins = append(old.Twins, it)
		}
	}
	return r, nil
}

// Select returns the servers of a source ("" is both) and, with names, only those. missing are names it did not find.
func (r Report) Select(from string, names []string) (items []Item, missing []string) {
	seen := map[string]bool{}
	for _, it := range r.Items {
		for _, one := range append([]Item{it}, it.Twins...) {
			if from != "" && one.Source != from || len(names) > 0 && !slices.Contains(names, one.Name) {
				continue
			}
			seen[one.Name] = true
			items = append(items, one)
			break
		}
	}
	for _, n := range names {
		if !seen[n] {
			missing = append(missing, n)
		}
	}
	return items, missing
}

// twin finds the same server from another source.
func (m Machine) twin(items []Item, s Server) int {
	if s.Command == "" && s.URL == "" {
		return -1
	}
	return slices.IndexFunc(items, func(it Item) bool { return it.Source != s.Source && it.Server.same(s, m) })
}

// There says why a server cannot be added to what Claude Code has now, or "".
func (m Machine) There(s Server) (string, error) {
	have, err := m.readClaudeCode()
	if err != nil {
		return "", err
	}
	return m.there(s, have), nil
}

// there says that Claude Code has it already.
func (m Machine) there(s Server, have map[string]Server) string {
	for _, name := range sorted(have) {
		if strings.EqualFold(name, s.Name) {
			if s.same(have[name], m) {
				return "already in Claude Code"
			}
			return "Claude Code has a different server with this name, which stays as it is"
		}
	}
	for _, name := range sorted(have) {
		if s.same(have[name], m) {
			return "already in Claude Code as " + Clean(name)
		}
	}
	return ""
}

// refusals are phrases of Claude Code's own messages. Its output may repeat the JSON, so nothing else of it is shown.
var refusals = []struct{ phrase, reason string }{
	{"already exists", "Claude Code has a server with this name"},
	{"is reserved", "Claude Code keeps this name for itself"},
	{"enterprise", "an organization's settings do not allow it"},
	{"managed settings", "an organization's settings do not allow it"},
	{"Invalid configuration", "Claude Code does not accept its settings"},
}

// Add puts one server into Claude Code's user scope and checks that it is there.
func (m Machine) Add(ctx context.Context, s Server) error {
	// The list is read again: it may have changed while the owner was reading.
	if why, err := m.There(s); err != nil {
		return err
	} else if why != "" {
		return errors.New(why)
	}
	if !ValidName(s.Name) {
		return errors.New("its name is not valid")
	}
	bin, err := m.Find("claude")
	if err != nil {
		return errors.New("Claude Code is not installed")
	}
	text, err := s.JSON()
	if err != nil {
		return err
	}
	out, rerr := m.Run(ctx, bin, "mcp", "add-json", "--scope", "user", s.Name, text)
	if have, err := m.readClaudeCode(); err == nil {
		if h, ok := have[s.Name]; ok && s.same(h, m) {
			return nil
		}
	}
	if rerr == nil {
		return errors.New("Claude Code reported no error, but the server is not in its list; check with: claude mcp list")
	}
	for _, r := range refusals {
		if strings.Contains(string(out), r.phrase) {
			return errors.New(r.reason)
		}
	}
	return errors.New("Claude Code did not add it")
}

func plainURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Hostname() != ""
}
