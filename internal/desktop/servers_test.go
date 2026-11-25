package desktop

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// usual writes the usual profile. %s in the text is a program that exists.
func (f *fixture) usual(servers string) {
	f.t.Helper()
	f.Usual = filepath.Join(f.root, "Claude", modeFile)
	f.put(f.Usual, `{"mcpServers":`+strings.ReplaceAll(servers, "%s", filepath.ToSlash(f.Helper))+`,"deploymentMode":"1p"}`)
}

func (f *fixture) servers() map[string]any {
	f.t.Helper()
	m, _ := f.json(modeFile)[serversKey].(map[string]any)
	return m
}

func TestSetupCopiesServers(t *testing.T) {
	f := newFixture(t)
	f.seed()
	f.usual(`{"a":{"command":"%s","env":{"K":"other"}},"New-1":{"command":"%s","args":["x<&>"],"env":{"KEY":"SECRET"},"note":1},"A":{"command":"%s"},"url":{"url":"https://x.test"},
		"cap":{"Command":"%s"},"ext":{"command":"%s","extensionId":"e"},"a b":{"command":"%s"},"gone":{"command":"/nowhere/at/all"},"node_repl":{"command":"%s"},
		"app":{"command":"/Applications/ChatGPT.app/x"},"bidi":{"command":"%s","args":["‮"]},"two":{"command":"%s","args":[1]},"two":{"command":"%s"}}`)
	before, _ := os.ReadFile(f.Usual)
	if err := f.setup(); err != nil {
		t.Fatal(err)
	}
	got := f.servers()
	want := map[string]any{"a": map[string]any{"command": "x<&>"}, "New-1": map[string]any{"command": filepath.ToSlash(f.Helper), "args": []any{"x<&>"}, "env": map[string]any{"KEY": "SECRET"}},
		"two": map[string]any{"command": filepath.ToSlash(f.Helper)}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%v", got)
	}
	if !strings.Contains(f.sys.text, "2 MCP servers") || !strings.Contains(f.sys.text, ": New-1, two.") || strings.Contains(f.sys.text, "SECRET") {
		t.Error(f.sys.text)
	}
	if after, _ := os.ReadFile(f.Usual); string(after) != string(before) {
		t.Error("the usual profile changed")
	}
	f.perm(f.path(modeFile), 0o600)
	tree := f.tree()
	f.sys.running = true
	if err := f.setup(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.sys.text, "MCP server") || !reflect.DeepEqual(tree, f.tree()) {
		t.Error("the second setup copied again")
	}
	// What the owner changed in gateway mode stays; going back copies nothing.
	f.usual(`{"New-1":{"command":"%s","args":["changed"]},"later":{"command":"%s"}}`)
	f.sys.running = true
	if err := f.back(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.servers(), want) {
		t.Errorf("%v", f.servers())
	}
}

func TestSetupWithoutServers(t *testing.T) {
	for name, c := range map[string]func(f *fixture){
		"no file":          func(f *fixture) { f.Usual = filepath.Join(f.root, "none") },
		"not set":          func(f *fixture) { f.Usual = "" },
		"broken":           func(f *fixture) { f.put(f.Usual, `{"mcpServers":`) },
		"not an object":    func(f *fixture) { f.put(f.Usual, `{"mcpServers":[1]}`) },
		"none":             func(f *fixture) { f.put(f.Usual, `{}`) },
		"gateway is odd":   func(f *fixture) { f.put(f.path(modeFile), `{"mcpServers":[]}`) },
		"features off":     func(f *fixture) { f.put(f.path(modeFile), `{"features":{"isLocalDevMcpEnabled":false}}`) },
		"entry off":        func(f *fixture) { f.ours(`{"isLocalDevMcpEnabled":false}`) },
		"entry off, text":  func(f *fixture) { f.ours(`{"isLocalDevMcpEnabled":"no"}`) },
		"too large":        func(f *fixture) { f.put(f.Usual, `{"x":"`+strings.Repeat("x", maxFile)+`"}`) },
		"folder, not file": func(f *fixture) { os.Remove(f.Usual); os.Mkdir(f.Usual, 0o700) },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.seed()
			f.usual(`{"new":{"command":"%s"}}`)
			c(f)
			if err := f.setup(); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(f.sys.text, "MCP server") || f.servers()["new"] != nil {
				t.Error(f.sys.text)
			}
		})
	}
	f := newFixture(t) // on, explicitly, and a library of one's own
	f.seed()
	f.usual(`{"new":{"command":"%s"}}`)
	f.ours(`{"isLocalDevMcpEnabled":true}`)
	if err := f.setup(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.sys.text, "one MCP server from your usual Claude to this mode: new.") || f.servers()["new"] == nil {
		t.Error(f.sys.text)
	}
}

// ours makes our entry with this content.
func (f *fixture) ours(entry string) {
	f.put(f.path("configLibrary/_meta.json"), `{"appliedId":"`+idA+`","entries":[{"id":"`+idA+`","name":"Second Life"}]}`)
	f.put(f.path("configLibrary/"+idA+".json"), entry)
}

// Only what the dialog named is added, with the content it had then.
func TestServersChangedAfterTheDialog(t *testing.T) {
	for name, c := range map[string]struct {
		now string
		ok  bool
	}{
		"one more":      {`{"new":{"command":"%s"},"more":{"command":"%s"}}`, false},
		"another":       {`{"more":{"command":"%s"}}`, false},
		"other command": {`{"new":{"command":"%s","args":["x"]}}`, false},
		"other format":  {`{ "new" : { "args":[], "command" : "%s" } }`, false},
		"spaces":        {`{ "new" : { "command" :   "%s" } }`, true},
		"fewer":         {`{}`, true},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.seed()
			f.usual(`{"new":{"command":"%s"}}`)
			f.sys.onQuit = func() { f.usual(c.now) }
			if c.ok {
				if err := f.setup(); err != nil {
					t.Fatal(err)
				}
				if (f.servers()["new"] != nil) != (name == "spaces") {
					t.Error(f.servers())
				}
				return
			}
			f.refuse(f.setup, "changed meanwhile", "confirm", "quit", "open")
			if len(f.backups()) != 0 {
				t.Error("made a backup")
			}
		})
	}
	// Claude drops one of its own while it quits: it was not announced, so it does not come back.
	f := newFixture(t)
	f.seed()
	f.usual(`{"a":{"command":"%s"}}`)
	f.sys.onQuit = func() { f.put(f.path(modeFile), `{"mcpServers":{}}`) }
	if err := f.setup(); err == nil || !strings.Contains(err.Error(), "meanwhile") {
		t.Error(err)
	}
}

func TestServersAreRolledBack(t *testing.T) {
	f := newFixture(t)
	f.seed()
	f.usual(`{"new":{"command":"%s"}}`)
	f.sys.openErr = fmt.Errorf("no app")
	f.refuse(f.setup, "old settings are back", "confirm", "quit", "open", "open")
}

func TestServersLimit(t *testing.T) {
	f := newFixture(t)
	f.seed()
	var list []string
	for i := range maxServers + 5 {
		list = append(list, fmt.Sprintf(`"s%d":{"command":"%%s"}`, i))
	}
	f.usual("{" + strings.Join(list, ",") + "}")
	for _, want := range []int{maxServers, maxServers + 5} {
		f.sys.running = true
		if err := f.setup(); err != nil {
			t.Fatal(err)
		}
		if got := len(f.servers()) - 1; got != want {
			t.Errorf("%d servers, want %d", got, want)
		}
	}
	if !strings.Contains(f.sys.text, "5 MCP servers") || !strings.Contains(f.sys.text, "s34") {
		t.Error(f.sys.text)
	}
}

func TestUsualIsNotFollowed(t *testing.T) {
	f := newFixture(t)
	f.seed()
	f.usual(`{"new":{"command":"%s"}}`)
	real := f.Usual + ".real"
	if os.Rename(f.Usual, real) != nil || os.Symlink(real, f.Usual) != nil {
		t.Skip("no links here")
	}
	if err := f.setup(); err != nil || f.servers()["new"] != nil {
		t.Error(err, f.servers())
	}
}
