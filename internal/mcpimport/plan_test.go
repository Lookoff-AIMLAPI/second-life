package mcpimport

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fixture is a machine in a temporary folder. codex prints list, claude stores what it is given.
type fixture struct {
	Machine
	t          *testing.T
	dir        string
	list       string
	calls      [][]string
	claudeSays string // what claude prints when it refuses
	keepsNone  bool
}

func newFixture(t *testing.T) *fixture {
	dir := t.TempDir()
	f := &fixture{t: t, dir: dir, list: "[]"}
	f.Machine = Machine{GOOS: "darwin", ClaudeCode: filepath.Join(dir, ".claude.json"), Usual: filepath.Join(dir, "usual.json"),
		Gateway: filepath.Join(dir, "gateway.json"), CodexDirs: []string{filepath.Join(dir, ".codex")}}
	f.Find = func(name string) (string, error) {
		switch name {
		case "codex", "claude", "npx", "uvx":
			return filepath.Join(dir, "bin", name), nil
		case "inner":
			return filepath.Join(dir, ".codex", "bin", "inner"), nil
		}
		return "", errors.New("SECRET not found")
	}
	f.Run = func(_ context.Context, bin string, args ...string) ([]byte, error) {
		f.calls = append(f.calls, append([]string{filepath.Base(bin)}, args...))
		if filepath.Base(bin) == "codex" {
			return []byte(f.list), nil
		}
		if f.claudeSays != "" {
			return []byte(f.claudeSays), errors.New("exit status 1 SECRET")
		}
		if f.keepsNone {
			return nil, nil
		}
		have := f.code()
		have[args[4]] = json.RawMessage(args[5])
		b, _ := json.Marshal(map[string]any{"mcpServers": have})
		f.put(f.ClaudeCode, string(b))
		return nil, nil
	}
	return f
}

func (f *fixture) put(path, text string) {
	f.t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) code() map[string]json.RawMessage {
	have, err := f.servers(f.ClaudeCode)
	if err != nil {
		f.t.Fatal(err)
	}
	if have == nil {
		have = map[string]json.RawMessage{}
	}
	return have
}

func (f *fixture) load() Report {
	f.t.Helper()
	r, err := f.Load(context.Background())
	if err != nil {
		f.t.Fatal(err)
	}
	return r
}

func stdio(name, rest string) string {
	return `{"name":"` + name + `","enabled":true,"auth_status":"unsupported","transport":{"type":"stdio","command":"npx","args":["-y","` + name + `"],"env":null,"env_vars":[],"cwd":null` + rest + `}}`
}

func remote(name, auth, rest string) string {
	return `{"name":"` + name + `","enabled":true,"auth_status":"` + auth + `","transport":{"type":"streamable_http","url":"https://` + name + `.test/mcp"` + rest + `}}`
}

func TestCodexServers(t *testing.T) {
	f := newFixture(t)
	for _, c := range []struct{ name, json, skip, text string }{
		{"plain", stdio("plain", ""), "", `{"type":"stdio","command":"npx","args":["-y","plain"]}`},
		{"env", stdio("env", `,"env":{"A":"v<&>"}`), "", `{"type":"stdio","command":"npx","args":["-y","env"],"env":{"A":"v<&>"}}`},
		{"pass", stdio("pass", `,"env_vars":["A",{"name":"B"},{"name":"C","source":"local"}]`), "", `{"type":"stdio","command":"npx","args":["-y","pass"]}`},
		{"far", stdio("far", `,"env_vars":[{"name":"C","source":"remote"}]`), "from a place", ""},
		{"cwd", stdio("cwd", `,"cwd":"/x"`), "working folder", ""},
		{"none", stdio("none", `,"command":"nowhere"`), "not found", ""},
		{"rel", stdio("rel", `,"command":"./x"`), "not found", ""},
		{"abs", stdio("abs", `,"command":"/nowhere/at/all"`), "not found", ""},
		{"node_repl", stdio("node_repl", ""), "only inside Codex", ""},
		{"computer-use", stdio("computer-use", ""), "only inside Codex", ""},
		{"app", stdio("app", `,"command":"/Applications/ChatGPT.app/Contents/x"`), "only inside Codex", ""},
		{"script", stdio("script", `,"args":["/Applications/Codex.app/Contents/x.js"]`), "only inside Codex", ""},
		{"ours", stdio("ours", `,"args":["/Applications/Second Life.app/x.js"]`), "", `{"type":"stdio","command":"npx","args":["/Applications/Second Life.app/x.js"]}`},
		{"home", stdio("home", `,"command":"`+filepath.ToSlash(filepath.Join(f.dir, ".codex", "x"))+`"`), "only inside Codex", ""},
		{"inner", stdio("inner", `,"command":"inner"`), "only inside Codex", ""},
		{"dollar", stdio("dollar", `,"env":{"A":"${HOME}"}`), "${...}", ""},
		{"ctl", stdio("ctl", `,"args":["a\u001bb"]`), "cannot be shown", ""},
		{"bidi", stdio("bidi", `,"args":["a\u202eb"]`), "cannot be shown", ""},
		{"badenv", stdio("badenv", `,"env":{"A=B":"x"}`), "name of a variable", ""},
		{"big", stdio("big", `,"env":{"A":"`+strings.Repeat("x", maxJSON)+`"}`), "too large", ""},
		{"a b", stdio("a b", ""), "name has characters", ""},
		{"-s", stdio("-s", ""), "name has characters", ""},
		{"broken", `{"name":"broken","transport":{"type":"stdio","command":7}}`, "could not be read", ""},
		{"sort", `{"name":"sort","enabled":true,"transport":{"type":"new"}}`, "not known", ""},
		{"off", strings.Replace(stdio("off", ""), "true", "false", 1), "switched off", ""},
		{"open", remote("open", "unsupported", ""), "", `{"type":"http","url":"https://open.test/mcp"}`},
		{"head", remote("head", "unsupported", `,"http_headers":{"X-A":"v"}`), "", `{"type":"http","url":"https://head.test/mcp","headers":{"X-A":"v"}}`},
		{"bearer", remote("bearer", "bearer_token", `,"bearer_token_env_var":"MY_TOKEN","env_http_headers":{"X-B":"OTHER"}`), "",
			`{"type":"http","url":"https://bearer.test/mcp","headers":{"Authorization":"Bearer ${MY_TOKEN}","X-B":"${OTHER}"}}`},
		{"blank", remote("blank", "bearer_token", `,"bearer_token_env_var":"ANTHROPIC_API_KEY"`), "never sends", ""},
		{"odd", remote("odd", "bearer_token", `,"bearer_token_env_var":"A}x"`), "not valid", ""},
		{"twice", remote("twice", "bearer_token", `,"bearer_token_env_var":"A","http_headers":{"authorization":"x"}`), "twice", ""},
		{"oauth", remote("oauth", "o_auth", ""), "OAuth", ""},
		{"login", remote("login", "not_logged_in", ""), "OAuth", ""},
		{"crlf", remote("crlf", "unsupported", `,"http_headers":{"X":"a\r\nb"}`), "header is not valid", ""},
		{"file", remote("file", "unsupported", `,"url":"file:///etc/passwd"`), "address is not valid", ""},
	} {
		f.list = "[" + c.json + "]"
		r := f.load()
		if len(r.Items) != 1 || r.Items[0].Name != c.name {
			t.Fatalf("%s: %v %v", c.name, r.Items, r.Problems)
		}
		it := r.Items[0]
		if (it.Skip == "") != (c.skip == "") || !strings.Contains(it.Skip, c.skip) {
			t.Errorf("%s: %q, want %q", c.name, it.Skip, c.skip)
		}
		if c.skip == "" {
			if got, _ := it.JSON(); got != c.text {
				t.Errorf("%s: %s", c.name, got)
			}
		}
	}
	for _, c := range f.calls {
		if !slices.Equal(c, []string{"codex", "mcp", "list", "--json"}) {
			t.Error(c)
		}
	}
}

func TestExposes(t *testing.T) {
	for _, c := range []struct {
		s    Server
		want bool
	}{
		{Server{Command: "npx", Args: []string{"-y", "x"}}, false},
		{Server{Command: "npx", Env: map[string]string{"A": "b"}}, true},
		{Server{Command: "npx", Args: []string{"--token", "b"}}, true},
		{Server{URL: "https://x.test/"}, false},
		{Server{URL: "https://x.test/?key=1"}, true},
		{Server{URL: "https://x.test/", Headers: map[string]string{"A": "Bearer ${B}", "C": "${D}"}}, false},
		{Server{URL: "https://x.test/", Headers: map[string]string{"A": "b"}}, true},
	} {
		if c.s.Exposes() != c.want {
			t.Error(c.s.Line())
		}
	}
}

func TestBrokenSources(t *testing.T) {
	f := newFixture(t)
	for _, out := range []string{"", "SECRET", `{"a":1}`, `["SECRET`} {
		f.list = out
		if p := f.load().Problems[Codex]; p == "" || strings.Contains(p, "SECRET") {
			t.Errorf("%q: %q", out, p)
		}
	}
	f.list = "[]"
	f.put(f.Usual, `{"mcpServers":{"SECRET`)
	if p := f.load().Problems[Desktop]; p == "" || strings.Contains(p, "SECRET") {
		t.Error(p)
	}
	f.put(f.ClaudeCode, `{"mcpServers":"SECRET"}`)
	if _, err := f.Load(context.Background()); err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Error(err)
	}
	old := f.Run
	f.put(f.ClaudeCode, `{}`)
	f.Run = func(ctx context.Context, bin string, args ...string) ([]byte, error) {
		return []byte("SECRET"), errors.New("SECRET")
	}
	if p := f.load().Problems[Codex]; p == "" || strings.Contains(p, "SECRET") {
		t.Error(p)
	}
	f.Run, f.Find = old, func(string) (string, error) { return "", errors.New("no") }
	if p := f.load().Problems[Codex]; !strings.Contains(p, "not found") {
		t.Error(p)
	}
}

func TestDesktopServers(t *testing.T) {
	f := newFixture(t)
	f.put(f.Usual, `{"mcpServers":{"ok":{"command":"uvx","args":["a"],"env":{"K":"v"}},"url":{"url":"https://x.test"},"ext":{"command":"uvx","extensionId":"x"},
		"num":{"command":"uvx","args":[1]},"nul":{"command":"uvx","env":{"A":null}},"a b":{"command":"uvx","args":["b"]}}}`)
	got := map[string]string{}
	for _, it := range f.load().Items {
		if it.Source != Desktop {
			t.Error(it.Source)
		}
		got[it.Name] = it.Skip
	}
	if len(got) != 6 || got["ok"] != "" || got["url"] == "" || got["ext"] == "" || got["num"] == "" || got["nul"] == "" || !strings.Contains(got["a b"], "name") {
		t.Error(got)
	}
	f.Usual = "" // Linux
	if r := f.load(); len(r.Items) != 0 || len(r.Problems) != 0 {
		t.Error(r)
	}
}

func TestWhatIsThereAlready(t *testing.T) {
	f := newFixture(t)
	f.list = "[" + stdio("one", "") + "," + stdio("two", "") + "," + stdio("three", "") + "," + stdio("New", "") + "]"
	f.put(f.ClaudeCode, `{"mcpServers":{"one":{"type":"stdio","command":"npx","args":["-y","one"],"env":{}},"two":{"command":"npx","args":["other"]},
		"drei":{"command":"`+filepath.ToSlash(filepath.Join(f.dir, "bin", "npx"))+`","args":["-y","three"]},"new":{"url":"https://x.test"}}}`)
	skips := map[string]string{}
	for _, it := range f.load().Items {
		skips[it.Name] = it.Skip
	}
	if skips["one"] != "already in Claude Code" || !strings.Contains(skips["two"], "different") || skips["three"] != "already in Claude Code as drei" || !strings.Contains(skips["New"], "different") {
		t.Error(skips)
	}
}

func TestOneServerInTwoSources(t *testing.T) {
	f := newFixture(t)
	f.list = "[" + stdio("here", "") + "," + strings.Replace(stdio("off", ""), "true", "false", 1) + "," + stdio("key", `,"env":{"A":"1"}`) + "]"
	f.put(f.Usual, `{"mcpServers":{"there":{"command":"npx","args":["-y","here"]},"on":{"command":"npx","args":["-y","off"]},"key":{"command":"npx","args":["-y","key"],"env":{"A":"2"}}}}`)
	r := f.load()
	show := func(items []Item) string {
		var out []string
		for _, it := range items {
			out = append(out, it.Source+"/"+it.Name)
		}
		return strings.Join(out, " ")
	}
	for _, c := range []struct {
		from  string
		names []string
		want  string
	}{
		{"", nil, "Codex/here Claude Desktop/on Codex/key Claude Desktop/key"},
		{Codex, nil, "Codex/here Codex/off Codex/key"},
		{Desktop, nil, "Claude Desktop/there Claude Desktop/on Claude Desktop/key"},
		{"", []string{"there"}, "Claude Desktop/there"},
		{Codex, []string{"there"}, ""},
	} {
		items, missing := r.Select(c.from, c.names)
		if show(items) != c.want || (len(missing) > 0) != (c.want == "") {
			t.Errorf("%v: %s %v", c, show(items), missing)
		}
	}
	// The same name twice: once one is in, the other is not offered.
	items, _ := r.Select("", []string{"key"})
	if err := f.Add(context.Background(), items[0].Server); err != nil {
		t.Fatal(err)
	}
	if err := f.Add(context.Background(), items[1].Server); err == nil || !strings.Contains(err.Error(), "different") {
		t.Error(err)
	}
}

func TestAdd(t *testing.T) {
	f := newFixture(t)
	s := Server{Name: "x", Command: "npx", Args: []string{"a b;$(c)"}, Env: map[string]string{"A": "SECRET"}}
	if err := f.Add(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	want := []string{"claude", "mcp", "add-json", "--scope", "user", "x", `{"type":"stdio","command":"npx","args":["a b;$(c)"],"env":{"A":"SECRET"}}`}
	if len(f.calls) != 1 || !slices.Equal(f.calls[0], want) {
		t.Error(f.calls)
	}
	if err := f.Add(context.Background(), s); err == nil || len(f.calls) != 1 {
		t.Error("added twice")
	}
	for says, reason := range map[string]string{
		"MCP server y already exists in user config SECRET": "has a server",
		`Cannot add MCP server "y": this name is reserved.`: "for itself",
		"Invalid configuration: SECRET":                     "does not accept",
		"SECRET":                                            "did not add",
	} {
		f.claudeSays = says
		err := f.Add(context.Background(), Server{Name: "y", Command: "npx", Env: map[string]string{"A": "SECRET"}})
		if err == nil || !strings.Contains(err.Error(), reason) || strings.Contains(err.Error(), "SECRET") {
			t.Errorf("%s: %v", says, err)
		}
	}
	f.claudeSays, f.keepsNone = "", true
	if err := f.Add(context.Background(), Server{Name: "z", Command: "npx"}); err == nil || !strings.Contains(err.Error(), "not in its list") {
		t.Error(err)
	}
	n := len(f.calls)
	for _, name := range []string{"--scope", "a b", ""} {
		if err := f.Add(context.Background(), Server{Name: name, Command: "uvx"}); err == nil || len(f.calls) != n {
			t.Error(name)
		}
	}
	f.put(f.ClaudeCode, "{")
	if err := f.Add(context.Background(), Server{Name: "w", Command: "uvx"}); err == nil || len(f.calls) != n {
		t.Error("added without knowing what is there")
	}
}
