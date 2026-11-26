// Package plugins installs and removes Claude Code plugins by running Claude Code's own plugin commands. It never
// downloads or writes plugin files itself.
package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Lookoff-AIMLAPI/second-life/internal/codex"
)

var (
	ErrNoClaude  = errors.New("Claude Code is not installed")
	ErrOldClaude = errors.New("this Claude Code cannot list plugins; update it")
	ErrBusy      = errors.New("another plugin change is running")
	ErrUnknown   = errors.New("no such plugin")
)

const (
	// Official is Anthropic's own marketplace; Repo is where Claude Code gets it.
	Official = "claude-plugins-official"
	Repo     = "anthropics/claude-plugins-official"
)

// Plugin is one entry of the catalog. Its text is untrusted.
type Plugin struct {
	ID, Name, Description, Marketplace string
	SourceKind                         string // "relative" for a path inside the marketplace, else source.source
	Installs                           int    // 0 when the catalog has no count
}

// Installed is a plugin Claude Code has.
type Installed struct {
	ID, Version string
	Scope       string // user, project or local; "" for a plugin loaded only for a session
	Enabled     bool
	MCPServers  []string // sorted names
}

// Catalog is what Claude Code can install and what it has.
type Catalog struct {
	Installed   []Installed
	Available   []Plugin
	HasOfficial bool   // some Available entry comes from the official marketplace
	ConfigDir   string // the folder Claude Code keeps them in
}

// Result says how an install went. NeedsTerminal, when set, means nothing was installed.
type Result struct {
	MCPServers    []string
	NeedsTerminal string
}

// Runner runs the plugin commands, one change at a time.
type Runner struct {
	GOOS  string
	Find  func(name string) (string, error)
	Run   func(ctx context.Context, bin string, args ...string) ([]byte, error)
	Waits struct{ List, Details, Change time.Duration }

	change sync.Mutex
}

// Here is a runner for this computer.
func Here() *Runner {
	r := &Runner{GOOS: runtime.GOOS, Find: codex.FindTool, Run: start}
	r.Waits.List, r.Waits.Details, r.Waits.Change = 30*time.Second, 30*time.Second, 3*time.Minute
	return r
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}@[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// ValidID tells whether id can be given to Claude Code as a plugin id.
func ValidID(id string) bool { return idPattern.MatchString(id) }

func configDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); filepath.IsAbs(d) {
		return filepath.Clean(d)
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}

// call runs one claude command with its own time limit.
func (r *Runner) call(ctx context.Context, wait time.Duration, args ...string) ([]byte, error) {
	bin, err := r.Find("claude")
	if err != nil {
		return nil, ErrNoClaude
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	out, err := r.Run(ctx, bin, args...)
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return out, errors.New("Claude Code took too long: claude " + strings.Join(args[:min(2, len(args))], " "))
	}
	return out, err
}

// Catalog reads both lists. It never waits for a change.
func (r *Runner) Catalog(ctx context.Context) (Catalog, error) {
	out, err := r.call(ctx, r.Waits.List, "plugin", "list", "--available", "--json")
	if err != nil {
		var re *runError
		if errors.As(err, &re) && strings.Contains(strings.ToLower(re.text), "unknown option") {
			return Catalog{}, ErrOldClaude
		}
		return Catalog{}, err
	}
	return parseCatalog(out)
}

type wireCatalog struct {
	Installed *[]struct {
		ID         string                     `json:"id"`
		Version    string                     `json:"version"`
		Scope      string                     `json:"scope"`
		Enabled    bool                       `json:"enabled"`
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	} `json:"installed"`
	Available *[]struct {
		ID          string          `json:"pluginId"`
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Marketplace string          `json:"marketplaceName"`
		Source      json.RawMessage `json:"source"`
		Installs    int             `json:"installCount"`
	} `json:"available"`
}

// firstObject decodes into v the first JSON object of out for which ok holds; a line printed before it does not matter.
func firstObject[T any](out []byte, ok func(*T) bool) (T, bool) {
	for i := bytes.IndexByte(out, '{'); i >= 0; {
		var v T
		if json.NewDecoder(bytes.NewReader(out[i:])).Decode(&v) == nil && ok(&v) {
			return v, true
		}
		next := bytes.IndexByte(out[i+1:], '{')
		if next < 0 {
			break
		}
		i += 1 + next
	}
	var zero T
	return zero, false
}

// parseCatalog reads the lists out of what claude printed.
func parseCatalog(out []byte) (Catalog, error) {
	w, found := firstObject(out, func(w *wireCatalog) bool { return w.Installed != nil || w.Available != nil })
	if !found {
		return Catalog{}, ErrOldClaude
	}
	c := Catalog{ConfigDir: configDir(), Installed: []Installed{}, Available: []Plugin{}}
	if w.Installed != nil {
		for _, p := range *w.Installed {
			servers := []string{}
			for name := range p.MCPServers {
				servers = append(servers, name)
			}
			slices.Sort(servers)
			scope := p.Scope
			if scope != "user" && scope != "project" && scope != "local" {
				scope = "" // "session" and anything new: nothing to uninstall
			}
			c.Installed = append(c.Installed, Installed{p.ID, p.Version, scope, p.Enabled, servers})
		}
	}
	if w.Available != nil {
		for _, p := range *w.Available {
			kind := ""
			var s struct {
				Source string `json:"source"`
			}
			var path string
			switch {
			case json.Unmarshal(p.Source, &path) == nil:
				kind = "relative"
			case json.Unmarshal(p.Source, &s) == nil:
				kind = s.Source
			}
			c.Available = append(c.Available, Plugin{p.ID, p.Name, p.Description, p.Marketplace, kind, max(p.Installs, 0)})
			c.HasOfficial = c.HasOfficial || p.Marketplace == Official
		}
	}
	return c, nil
}

// lock refuses a second change instead of queueing it.
func (r *Runner) lock() (func(), error) {
	if !r.change.TryLock() {
		return nil, ErrBusy
	}
	return r.change.Unlock, nil
}

// Install installs id for the user, in all projects.
func (r *Runner) Install(ctx context.Context, id string) (Result, error) {
	if _, err := r.Find("claude"); err != nil {
		return Result{}, ErrNoClaude
	}
	unlock, err := r.lock()
	if err != nil {
		return Result{}, err
	}
	defer unlock()
	if !ValidID(id) {
		return Result{}, ErrUnknown
	}
	c, err := r.Catalog(ctx)
	if err != nil {
		return Result{}, err
	}
	i := slices.IndexFunc(c.Available, func(p Plugin) bool { return p.ID == id })
	if i < 0 {
		return Result{}, ErrUnknown
	}
	terminal := "In Terminal: claude plugin install " + id
	if c.Available[i].SourceKind == "command" {
		return Result{NeedsTerminal: "It runs a command that you need to see and accept first. " + terminal}, nil
	}
	out, err := r.call(ctx, r.Waits.Change, "plugin", "install", id, "--scope", "user", "--json")
	if err != nil {
		type outcome struct{ Outcome, Message, FailureCode string }
		if res, ok := firstObject(out, func(o *outcome) bool { return o.Outcome == "failed" && o.Message != "" }); ok {
			if needsSettings(res.Message + " " + res.FailureCode) {
				return Result{NeedsTerminal: "It needs settings first. " + terminal}, nil
			}
			return Result{}, errors.New(res.Message)
		}
		var re *runError
		if errors.As(err, &re) && needsSettings(re.text) {
			return Result{NeedsTerminal: "It needs settings first. " + terminal}, nil
		}
		return Result{}, err
	}
	res := Result{MCPServers: []string{}}
	if after, err := r.Catalog(ctx); err == nil {
		for _, p := range after.Installed {
			if p.ID == id {
				res.MCPServers = p.MCPServers
			}
		}
	}
	return res, nil
}

func needsSettings(s string) bool {
	s = strings.ToLower(s)
	return strings.Contains(s, "userconfig") || strings.Contains(s, "--config") || strings.Contains(s, "config_required")
}

// Remove uninstalls id at the scope it was installed with. Its saved data stays.
func (r *Runner) Remove(ctx context.Context, id string) error {
	if _, err := r.Find("claude"); err != nil {
		return ErrNoClaude
	}
	unlock, err := r.lock()
	if err != nil {
		return err
	}
	defer unlock()
	if !ValidID(id) {
		return ErrUnknown
	}
	c, err := r.Catalog(ctx)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(c.Installed, func(p Installed) bool { return p.ID == id })
	if i < 0 || c.Installed[i].Scope == "" {
		return ErrUnknown
	}
	_, err = r.call(ctx, r.Waits.Change, "plugin", "uninstall", id, "--scope", c.Installed[i].Scope, "--keep-data")
	return err
}

// Details describes an installed plugin; Claude Code knows nothing more about others.
func (r *Runner) Details(ctx context.Context, id string) (string, error) {
	if !ValidID(id) {
		if _, err := r.Find("claude"); err != nil {
			return "", ErrNoClaude
		}
		return "", ErrUnknown
	}
	c, err := r.Catalog(ctx)
	if err != nil {
		return "", err
	}
	if !slices.ContainsFunc(c.Installed, func(p Installed) bool { return p.ID == id }) {
		return "", ErrUnknown
	}
	out, err := r.call(ctx, r.Waits.Details, "plugin", "details", id)
	return string(out), err
}

// AddCatalog adds Anthropic's marketplace.
func (r *Runner) AddCatalog(ctx context.Context) error {
	return r.changeCatalog(ctx, "add", Repo)
}

// UpdateCatalog fetches Anthropic's marketplace again.
func (r *Runner) UpdateCatalog(ctx context.Context) error {
	return r.changeCatalog(ctx, "update", Official)
}

func (r *Runner) changeCatalog(ctx context.Context, verb, what string) error {
	if _, err := r.Find("claude"); err != nil {
		return ErrNoClaude
	}
	unlock, err := r.lock()
	if err != nil {
		return err
	}
	defer unlock()
	_, err = r.call(ctx, r.Waits.Change, "plugin", "marketplace", verb, what)
	return err
}
