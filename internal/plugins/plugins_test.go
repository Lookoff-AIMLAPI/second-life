package plugins

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

const catalogJSON = `{"installed":[
 {"id":"context7@claude-plugins-official","version":"0fbf","scope":"user","enabled":true,"mcpServers":{"context7":{"type":"http"},"b-second":{"type":"http"}}},
 {"id":"x@m","version":"1","scope":"project","enabled":true},
 {"id":"vesna@inline","version":"0.2.0","scope":"session","enabled":true,"installPath":"/x"}
],"available":[
 {"pluginId":"linear@claude-plugins-official","name":"linear","description":"Linear issues","marketplaceName":"claude-plugins-official","source":"./external_plugins/linear","installCount":42},
 {"pluginId":"design@anthropic-plugin-directory","name":"design","description":"Design work","marketplaceName":"anthropic-plugin-directory","version":"1.2.0","source":{"source":"git-subdir","url":"https://github.com/x/y","path":"design"}},
 {"pluginId":"runner@claude-plugins-official","name":"runner","description":"Runs a command","marketplaceName":"claude-plugins-official","source":{"source":"command","command":"x"}}
]}`

func TestValidID(t *testing.T) {
	for _, id := range []string{"linear@claude-plugins-official", "a@b", "x.y_z-1@m-2.k"} {
		if !ValidID(id) {
			t.Errorf("%q refused", id)
		}
	}
	for _, id := range []string{"", "a", "-x@y", "a@b@c", "a b@c", "a@b;rm", "a@", "@b", strings.Repeat("n", 129) + "@m"} {
		if ValidID(id) {
			t.Errorf("%q accepted", id)
		}
	}
}

func TestParseCatalog(t *testing.T) {
	c, err := parseCatalog([]byte(catalogJSON))
	if err != nil {
		t.Fatal(err)
	}
	if !c.HasOfficial || len(c.Installed) != 3 || len(c.Available) != 3 {
		t.Fatalf("%+v", c)
	}
	if got := c.Installed[0].MCPServers; !reflect.DeepEqual(got, []string{"b-second", "context7"}) {
		t.Errorf("servers %v", got)
	}
	if c.Installed[1].Scope != "project" || c.Installed[2].Scope != "" || c.Installed[1].MCPServers == nil {
		t.Errorf("installed %+v", c.Installed)
	}
	a := c.Available
	if a[0].ID != "linear@claude-plugins-official" || a[0].Installs != 42 || a[0].SourceKind != "relative" {
		t.Errorf("first %+v", a[0])
	}
	if a[1].Marketplace != "anthropic-plugin-directory" || a[1].SourceKind != "git-subdir" || a[1].Installs != 0 {
		t.Errorf("second %+v", a[1])
	}
	if a[2].SourceKind != "command" {
		t.Errorf("third %+v", a[2])
	}
}

func TestParseCatalogWithoutOfficial(t *testing.T) {
	c, err := parseCatalog([]byte(`{"installed":[],"available":[{"pluginId":"d@anthropic-plugin-directory","name":"d","marketplaceName":"anthropic-plugin-directory"}]}`))
	if err != nil || c.HasOfficial {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestCatalogWithNoise(t *testing.T) {
	c, err := parseCatalog([]byte("Warning: something {odd}\n" + catalogJSON + "\n"))
	if err != nil || len(c.Available) != 3 {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestParseCatalogChanged(t *testing.T) {
	for _, in := range []string{`{"plugins":[]}`, `not json`, ``} {
		if _, err := parseCatalog([]byte(in)); !errors.Is(err, ErrOldClaude) {
			t.Errorf("%q: %v", in, err)
		}
	}
}

// script is a fake claude: it records each call and answers by the first two arguments.
type script struct {
	mu      sync.Mutex
	calls   [][]string
	catalog string
	answers map[string]answer // "install", "uninstall", "marketplace add", ...
	gate    chan struct{}     // when set, install waits on it
}

type answer struct {
	out string
	err error
}

func (s *script) run(ctx context.Context, bin string, args ...string) ([]byte, error) {
	s.mu.Lock()
	s.calls = append(s.calls, args)
	gate := s.gate
	s.mu.Unlock()
	if bin != "/bin/claude" {
		return nil, errors.New("wrong binary " + bin)
	}
	key := args[1]
	if key == "marketplace" {
		key += " " + args[2]
	}
	if key == "list" {
		return []byte(s.catalog), nil
	}
	if key == "install" && gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	a := s.answers[key]
	return []byte(a.out), a.err
}

func (s *script) commands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, c := range s.calls {
		out = append(out, strings.Join(c, " "))
	}
	return out
}

func newRunner(s *script) *Runner {
	r := Here()
	r.Find = func(string) (string, error) { return "/bin/claude", nil }
	r.Run = s.run
	return r
}

func TestInstallArgs(t *testing.T) {
	s := &script{catalog: catalogJSON, answers: map[string]answer{"install": {out: `{"command":"install","outcome":"ok"}`}}}
	res, err := newRunner(s).Install(context.Background(), "linear@claude-plugins-official")
	if err != nil || res.NeedsTerminal != "" {
		t.Fatal(res, err)
	}
	want := []string{"plugin list --available --json", "plugin install linear@claude-plugins-official --scope user --json", "plugin list --available --json"}
	if got := s.commands(); !reflect.DeepEqual(got, want) {
		t.Errorf("%q", got)
	}
}

func TestInstallReportsServers(t *testing.T) {
	after := strings.Replace(catalogJSON, `"id":"x@m"`, `"id":"linear@claude-plugins-official","mcpServers":{"linear":{}}`, 1)
	s := &script{catalog: catalogJSON, answers: map[string]answer{"install": {out: `{"outcome":"ok"}`}}}
	r := newRunner(s)
	inner := r.Run
	r.Run = func(ctx context.Context, bin string, args ...string) ([]byte, error) {
		out, err := inner(ctx, bin, args...)
		if args[1] == "install" {
			s.mu.Lock()
			s.catalog = after
			s.mu.Unlock()
		}
		return out, err
	}
	res, err := r.Install(context.Background(), "linear@claude-plugins-official")
	if err != nil || !reflect.DeepEqual(res.MCPServers, []string{"linear"}) {
		t.Fatal(res, err)
	}
}

func TestInstallFailureMessage(t *testing.T) {
	s := &script{catalog: catalogJSON, answers: map[string]answer{"install": {
		out: `{"command":"install","outcome":"failed","message":"Plugin \"linear\" not found in marketplace","failureCode":"not_found"}`,
		err: errors.New("exit status 1")}}}
	_, err := newRunner(s).Install(context.Background(), "linear@claude-plugins-official")
	if err == nil || !strings.Contains(err.Error(), `Plugin "linear" not found in marketplace`) {
		t.Fatal(err)
	}
}

func TestInstallNeedsSettings(t *testing.T) {
	s := &script{catalog: catalogJSON, answers: map[string]answer{"install": {
		out: `{"outcome":"failed","message":"Missing required userConfig option: api_key","failureCode":"config_required"}`,
		err: errors.New("exit status 1")}}}
	res, err := newRunner(s).Install(context.Background(), "linear@claude-plugins-official")
	if err != nil || !strings.Contains(res.NeedsTerminal, "claude plugin install linear@claude-plugins-official") {
		t.Fatal(res, err)
	}
}

func TestRemoveUsesItsScope(t *testing.T) {
	s := &script{catalog: catalogJSON}
	if err := newRunner(s).Remove(context.Background(), "x@m"); err != nil {
		t.Fatal(err)
	}
	if got := s.commands(); got[len(got)-1] != "plugin uninstall x@m --scope project --keep-data" {
		t.Errorf("%q", got)
	}
}

func TestRefusesUnknownIDs(t *testing.T) {
	s := &script{catalog: catalogJSON}
	r := newRunner(s)
	if _, err := r.Install(context.Background(), "nope@claude-plugins-official"); !errors.Is(err, ErrUnknown) {
		t.Error("install:", err)
	}
	if err := r.Remove(context.Background(), "linear@claude-plugins-official"); !errors.Is(err, ErrUnknown) {
		t.Error("remove:", err)
	}
	if _, err := r.Install(context.Background(), "a b@c"); !errors.Is(err, ErrUnknown) {
		t.Error("bad id:", err)
	}
	for _, c := range s.commands() {
		if !strings.HasPrefix(c, "plugin list") {
			t.Errorf("ran %q", c)
		}
	}
}

func TestSessionOnlyNotRemovable(t *testing.T) {
	s := &script{catalog: catalogJSON}
	if err := newRunner(s).Remove(context.Background(), "vesna@inline"); !errors.Is(err, ErrUnknown) {
		t.Fatal(err)
	}
}

func TestCommandSourceNeedsTerminal(t *testing.T) {
	s := &script{catalog: catalogJSON}
	res, err := newRunner(s).Install(context.Background(), "runner@claude-plugins-official")
	if err != nil || !strings.Contains(res.NeedsTerminal, "claude plugin install runner@claude-plugins-official") {
		t.Fatal(res, err)
	}
	for _, c := range s.commands() {
		if strings.Contains(c, "install runner") {
			t.Errorf("ran %q", c)
		}
	}
}

func TestBusy(t *testing.T) {
	s := &script{catalog: catalogJSON, gate: make(chan struct{}), answers: map[string]answer{"install": {out: `{"outcome":"ok"}`}}}
	r := newRunner(s)
	done := make(chan error)
	go func() {
		_, err := r.Install(context.Background(), "linear@claude-plugins-official")
		done <- err
	}()
	for i := 0; ; i++ {
		if c := s.commands(); len(c) >= 2 {
			break
		}
		if i > 200 {
			t.Fatal("first install did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := r.Install(context.Background(), "linear@claude-plugins-official"); !errors.Is(err, ErrBusy) {
		t.Error("second install:", err)
	}
	if err := r.Remove(context.Background(), "x@m"); !errors.Is(err, ErrBusy) {
		t.Error("remove:", err)
	}
	if _, err := r.Catalog(context.Background()); err != nil {
		t.Error("catalog must not wait:", err)
	}
	close(s.gate)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestNoClaude(t *testing.T) {
	r := Here()
	r.Find = func(string) (string, error) { return "", exec.ErrNotFound }
	ctx := context.Background()
	_, e1 := r.Catalog(ctx)
	_, e2 := r.Install(ctx, "a@b")
	e3 := r.Remove(ctx, "a@b")
	e4 := r.AddCatalog(ctx)
	e5 := r.UpdateCatalog(ctx)
	_, e6 := r.Details(ctx, "a@b")
	for i, err := range []error{e1, e2, e3, e4, e5, e6} {
		if !errors.Is(err, ErrNoClaude) {
			t.Errorf("%d: %v", i, err)
		}
	}
}

func TestCatalogCommands(t *testing.T) {
	s := &script{catalog: catalogJSON}
	r := newRunner(s)
	if err := r.AddCatalog(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.UpdateCatalog(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"plugin marketplace add anthropics/claude-plugins-official", "plugin marketplace update claude-plugins-official"}
	if got := s.commands(); !reflect.DeepEqual(got, want) {
		t.Errorf("%q", got)
	}
}

func TestDetailsOnlyForInstalled(t *testing.T) {
	s := &script{catalog: catalogJSON, answers: map[string]answer{"details": {out: "context7\n  MCP servers (1)\n"}}}
	r := newRunner(s)
	if _, err := r.Details(context.Background(), "linear@claude-plugins-official"); !errors.Is(err, ErrUnknown) {
		t.Error(err)
	}
	text, err := r.Details(context.Background(), "context7@claude-plugins-official")
	if err != nil || !strings.Contains(text, "MCP servers (1)") {
		t.Fatal(text, err)
	}
}

func TestConfigDir(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/x/y")
	if got := configDir(); got != "/x/y" {
		t.Error(got)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", "rel")
	if got := configDir(); !strings.HasSuffix(got, filepath.Join("", ".claude")) || !filepath.IsAbs(got) {
		t.Error(got)
	}
}

func TestInstallFailureAfterWarning(t *testing.T) {
	s := &script{catalog: catalogJSON, answers: map[string]answer{"install": {
		out: "Warning: {cache} is stale\n" + `{"outcome":"failed","message":"Missing required userConfig option: api_key","failureCode":"config_required"}` + "\n",
		err: errors.New("exit status 1")}}}
	res, err := newRunner(s).Install(context.Background(), "linear@claude-plugins-official")
	if err != nil || !strings.Contains(res.NeedsTerminal, "claude plugin install linear@claude-plugins-official") {
		t.Fatal(res, err)
	}
	s.answers["install"] = answer{out: "Note: x\n" + `{"outcome":"failed","message":"Plugin \"linear\" not found"}`, err: errors.New("exit status 1")}
	if _, err := newRunner(s).Install(context.Background(), "linear@claude-plugins-official"); err == nil || err.Error() != `Plugin "linear" not found` {
		t.Fatal(err)
	}
}
