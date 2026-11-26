package picker

import (
	"context"
	"errors"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/Lookoff-AIMLAPI/second-life/internal/plugins"
)

// PluginStore installs and removes Claude Code plugins.
type PluginStore interface {
	Catalog(ctx context.Context) (plugins.Catalog, error)
	Details(ctx context.Context, id string) (string, error)
	Install(ctx context.Context, id string) (plugins.Result, error)
	Remove(ctx context.Context, id string) error
	AddCatalog(ctx context.Context) error
	UpdateCatalog(ctx context.Context) error
}

const (
	pluginsWait = 35 * time.Second // the store stops at 30 s, so its own message comes first
	changeWait  = 3*time.Minute + 5*time.Second
	detailsMax  = 4000
)

type wireInstalled struct {
	ID         string   `json:"id"`
	Version    string   `json:"version"`
	Scope      string   `json:"scope"`
	Enabled    bool     `json:"enabled"`
	MCPServers []string `json:"mcpServers"`
}

type wirePlugin struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	Marketplace   string `json:"marketplace"`
	Installs      int    `json:"installs"`
	NeedsTerminal bool   `json:"needsTerminal"`
}

func installHint() string {
	if runtime.GOOS == "windows" {
		return "irm https://claude.ai/install.ps1 | iex"
	}
	return "curl -fsSL https://claude.ai/install.sh | bash"
}

// store answers 503 when this build has no plugin store.
func (p *Picker) store(w http.ResponseWriter) PluginStore {
	s := p.d.Plugins()
	if s == nil {
		fail(w, http.StatusServiceUnavailable, "plugins are not available here")
	}
	return s
}

// pluginID reads {"id": …} and refuses an id Claude Code would not take.
func pluginID(w http.ResponseWriter, r *http.Request) (string, bool) {
	var in struct {
		ID string `json:"id"`
	}
	if !read(w, r, &in) {
		return "", false
	}
	if !plugins.ValidID(in.ID) {
		fail(w, http.StatusBadRequest, "that is not a plugin id")
		return "", false
	}
	return in.ID, true
}

func pluginFail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, plugins.ErrBusy):
		fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, plugins.ErrUnknown):
		fail(w, http.StatusNotFound, err.Error())
	case errors.Is(err, plugins.ErrNoClaude), errors.Is(err, plugins.ErrOldClaude):
		fail(w, http.StatusServiceUnavailable, err.Error())
	default:
		fail(w, http.StatusBadGateway, clean(err.Error(), 300))
	}
}

func (p *Picker) pluginList(w http.ResponseWriter, r *http.Request) {
	s := p.store(w)
	if s == nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), pluginsWait)
	defer cancel()
	c, err := s.Catalog(ctx)
	out := map[string]any{"state": "ok", "installHint": installHint(), "configDir": text(c.ConfigDir, 300), "hasOfficial": c.HasOfficial}
	switch {
	case errors.Is(err, plugins.ErrNoClaude):
		out["state"] = "no-claude"
	case errors.Is(err, plugins.ErrOldClaude):
		out["state"] = "old-claude"
	case err != nil:
		pluginFail(w, err)
		return
	}
	installed := []wireInstalled{}
	for _, i := range c.Installed {
		servers := []string{}
		for _, s := range i.MCPServers {
			servers = append(servers, text(s, 100))
		}
		installed = append(installed, wireInstalled{text(i.ID, 300), text(i.Version, 100), text(i.Scope, 40), i.Enabled, servers})
	}
	available := []wirePlugin{}
	for _, a := range c.Available {
		if !plugins.ValidID(a.ID) { // the page shows the id in a Terminal command
			continue
		}
		name := text(a.Name, 100)
		if name == "" {
			name = text(a.ID, 100)
		}
		available = append(available, wirePlugin{text(a.ID, 300), name, text(a.Description, 300), text(a.Marketplace, 100), max(a.Installs, 0), a.SourceKind == "command"})
	}
	out["installed"], out["available"] = installed, available
	reply(w, http.StatusOK, out)
}

func (p *Picker) pluginInstall(w http.ResponseWriter, r *http.Request) {
	s := p.store(w)
	if s == nil {
		return
	}
	id, ok := pluginID(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), changeWait) // a closed tab must not cut a change short
	defer cancel()
	res, err := s.Install(ctx, id)
	if err != nil {
		pluginFail(w, err)
		return
	}
	if res.NeedsTerminal != "" {
		reply(w, http.StatusOK, map[string]any{"ok": false, "needsTerminal": clean(res.NeedsTerminal, 300)})
		return
	}
	servers := []string{}
	for _, s := range res.MCPServers {
		servers = append(servers, text(s, 100))
	}
	reply(w, http.StatusOK, map[string]any{"ok": true, "servers": servers})
}

func (p *Picker) pluginRemove(w http.ResponseWriter, r *http.Request) {
	s := p.store(w)
	if s == nil {
		return
	}
	id, ok := pluginID(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), changeWait) // a closed tab must not cut a change short
	defer cancel()
	if err := s.Remove(ctx, id); err != nil {
		pluginFail(w, err)
		return
	}
	reply(w, http.StatusOK, map[string]bool{"ok": true})
}

func (p *Picker) pluginDetails(w http.ResponseWriter, r *http.Request) {
	s := p.store(w)
	if s == nil {
		return
	}
	id, ok := pluginID(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), pluginsWait)
	defer cancel()
	t, err := s.Details(ctx, id)
	if errors.Is(err, plugins.ErrUnknown) {
		reply(w, http.StatusOK, map[string]string{"text": ""})
		return
	}
	if err != nil {
		pluginFail(w, err)
		return
	}
	reply(w, http.StatusOK, map[string]string{"text": lines(t, detailsMax)})
}

// lines cleans each line on its own, keeps the line breaks and keeps at most n bytes.
func lines(s string, n int) string {
	var out []string
	size := 0
	for _, l := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		l = clean(l, n)
		if size+len(l)+1 > n {
			break
		}
		out = append(out, l)
		size += len(l) + 1
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func (p *Picker) catalogChange(add bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s := p.store(w)
		if s == nil {
			return
		}
		var in struct{}
		if !read(w, r, &in) {
			return
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), changeWait) // a closed tab must not cut a change short
		defer cancel()
		run := s.UpdateCatalog
		if add {
			run = s.AddCatalog
		}
		if err := run(ctx); err != nil {
			pluginFail(w, err)
			return
		}
		reply(w, http.StatusOK, map[string]bool{"ok": true})
	}
}
