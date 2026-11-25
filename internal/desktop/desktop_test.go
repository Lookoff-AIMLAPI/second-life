package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

const (
	idA = "11111111-1111-4111-8111-111111111111"
	idB = "22222222-2222-4222-8222-222222222222"
)

var mapping = [][2]string{{"fable", "gpt-6-astra"}, {"opus", "gpt-5.6-sol"}, {"sonnet", "gpt-5.6-terra"}, {"haiku", "gpt-5.6-luna"}}

type fakeSystem struct {
	running, stays, managed, decline        bool
	managedErr, confirmErr, runErr, quitErr error
	openErr                                 error
	onConfirm, onQuit                       func()
	log                                     []string
	stopsAt                                 time.Time // when set, says when Claude ends
	text                                    string
}

func (f *fakeSystem) Installed() bool { return true }

func (f *fakeSystem) Managed() (bool, error) { return f.managed, f.managedErr }

func (f *fakeSystem) Running() (bool, error) {
	if !f.stopsAt.IsZero() {
		return time.Now().Before(f.stopsAt), f.runErr
	}
	return f.running, f.runErr
}

func (f *fakeSystem) Quit() error {
	f.log = append(f.log, "quit")
	if f.quitErr != nil {
		return f.quitErr
	}
	if f.onQuit != nil {
		f.onQuit()
	}
	if !f.stays {
		f.running = false
	}
	return nil
}

func (f *fakeSystem) Open() error {
	f.log = append(f.log, "open")
	return f.openErr
}

func (f *fakeSystem) Confirm(title, text, button string) (bool, error) {
	f.log = append(f.log, "confirm")
	f.text = text
	if f.onConfirm != nil {
		f.onConfirm()
	}
	return !f.decline, f.confirmErr
}

func (f *fakeSystem) Alert(string, string) {}

type fixture struct {
	*Desktop
	t    *testing.T
	sys  *fakeSystem
	root string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(root, "My App's", "slife")
	f := &fixture{t: t, root: root, sys: &fakeSystem{running: true}}
	f.Desktop = &Desktop{
		Sys: f.sys, Dir: filepath.Join(root, "Claude-3p"), BackupDir: filepath.Join(root, "home", "backups"),
		BaseURL: "http://127.0.0.1:8787", GOOS: "darwin", Exe: "/Applications/Second Life.app/Contents/MacOS/slife-tray",
		Helper: helper, quitWait: 40 * time.Millisecond, poll: time.Millisecond,
	}
	f.put(helper, "#!/bin/sh\n")
	if err := os.Chmod(helper, 0o755); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) path(rel string) string { return filepath.Join(f.Dir, filepath.FromSlash(rel)) }

func (f *fixture) put(path, data string) {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

// seed writes a library with two entries, as the owner has.
func (f *fixture) seed() {
	f.put(f.path("configLibrary/_meta.json"), `{"appliedId":"`+idA+`","future":{"x":[1,2]},"entries":[{"id":"`+idA+`","name":"Default"},{"id":"`+idB+`","name":"OpenRouter","extra":12345678901234567890}]}`)
	f.put(f.path("configLibrary/"+idA+".json"), `{}`)
	f.put(f.path("configLibrary/"+idB+".json"), `{"inferenceGatewayApiKey":"secret"}`)
	f.put(f.path("claude_desktop_config.json"), `{"mcpServers":{"a":{"command":"x<&>"}},"deploymentMode":"1p","big":12345678901234567890}`)
	f.put(f.path("developer_settings.json"), `{"allowDevTools":false,"other":"kept"}`)
}

// tree is every file and folder under the settings folder with its content.
func (f *fixture) tree() map[string]string {
	out := map[string]string{}
	filepath.WalkDir(f.Dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(f.Dir, p)
		if d.IsDir() {
			out[rel+"/"] = ""
		} else if d.Type()&fs.ModeSymlink != 0 {
			out[rel] = "link"
		} else {
			b, _ := os.ReadFile(p)
			out[rel] = string(b)
		}
		return nil
	})
	return out
}

func (f *fixture) json(rel string) map[string]any {
	f.t.Helper()
	b, err := os.ReadFile(f.path(rel))
	if err != nil {
		f.t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		f.t.Fatal(err)
	}
	return v
}

// refuse runs fn and demands an error, untouched settings and these calls.
func (f *fixture) refuse(fn func() error, want string, calls ...string) {
	f.t.Helper()
	before := f.tree()
	err := fn()
	if err == nil || !strings.Contains(err.Error(), want) {
		f.t.Errorf("error %v, want %q", err, want)
	}
	if after := f.tree(); !reflect.DeepEqual(before, after) {
		f.t.Errorf("settings changed:\n%v\n%v", before, after)
	}
	if !slices.Equal(f.sys.log, calls) {
		f.t.Errorf("calls %q, want %q", f.sys.log, calls)
	}
}

func (f *fixture) setup() error { return f.Setup(context.Background(), mapping) }
func (f *fixture) back() error  { return f.Back(context.Background()) }

func (f *fixture) backups() []string {
	list, _ := os.ReadDir(f.BackupDir)
	var names []string
	for _, e := range list {
		names = append(names, e.Name())
	}
	return names
}

func (f *fixture) perm(path string, want fs.FileMode) {
	f.t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != want {
		f.t.Errorf("%s: %v %v", path, st, err)
	}
}

func TestSetupTwoEntries(t *testing.T) {
	f := newFixture(t)
	f.seed()
	if err := f.setup(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.sys.log, []string{"confirm", "quit", "open"}) {
		t.Errorf("%q", f.sys.log)
	}
	for _, s := range []string{"quit and reopen in gateway mode", "sessions stop", "developer mode"} {
		if !strings.Contains(f.sys.text, s) {
			t.Errorf("the dialog does not say %q", s)
		}
	}

	meta := f.json("configLibrary/_meta.json")
	entries := meta["entries"].([]any)
	if len(entries) != 3 || entries[0].(map[string]any)["id"] != idA || entries[1].(map[string]any)["id"] != idB {
		t.Fatalf("%v", entries)
	}
	ours := entries[2].(map[string]any)
	id := ours["id"].(string)
	if ours["name"] != "Second Life" || meta["appliedId"] != id || !idPattern.MatchString(id) {
		t.Errorf("%v", meta)
	}
	raw, _ := os.ReadFile(f.path("configLibrary/_meta.json"))
	if !strings.Contains(string(raw), "12345678901234567890") || meta["future"] == nil {
		t.Errorf("other content was lost: %s", raw)
	}
	if b, _ := os.ReadFile(f.path("configLibrary/" + idB + ".json")); string(b) != `{"inferenceGatewayApiKey":"secret"}` {
		t.Errorf("another entry changed: %s", b)
	}

	want := map[string]any{
		"inferenceProvider": "gateway", "inferenceGatewayBaseUrl": "http://127.0.0.1:8787",
		"inferenceCredentialKind": "helper-script", "inferenceCredentialHelper": f.Helper,
		"inferenceCredentialHelperArgs": []any{"token"},
		"modelDiscoveryEnabled":         false, "toolSearchEnabled": true, "defaultModelEffort": "medium",
		"claudeAiImport":                map[string]any{"enabled": true, "bannerBehavior": "show"},
		"coworkEgressAllowedHosts":      []any{"*"},
		"inferenceStreamIdleTimeoutSec": 900.0, "deploymentDisplayName": "Second Life",
		"artifactsEnabled": true, "artifactsDesignEnabled": true, "artifactsSlidesEnabled": true,
		"inferenceModels": []any{
			map[string]any{"name": "claude-opus-5-5", "labelOverride": "Opus (GPT-5.6 Sol)", "anthropicFamilyTier": "opus", "isFamilyDefault": true},
			map[string]any{"name": "claude-sonnet-5-5", "labelOverride": "Sonnet (GPT-5.6 Terra)", "anthropicFamilyTier": "sonnet", "isFamilyDefault": true},
			map[string]any{"name": "claude-fable-5-2", "labelOverride": "Fable (GPT-6 Astra)", "anthropicFamilyTier": "fable", "isFamilyDefault": true},
			map[string]any{"name": "claude-haiku-5-5", "labelOverride": "Haiku (GPT-5.6 Luna)", "anthropicFamilyTier": "haiku", "isFamilyDefault": true},
		},
	}
	if got := f.json("configLibrary/" + id + ".json"); !reflect.DeepEqual(got, want) {
		t.Errorf("%v", got)
	}

	raw, _ = os.ReadFile(f.path("claude_desktop_config.json"))
	var compact object
	if compact, _ = parseObject(raw); len(compact) != 3 || compact[0].key != "mcpServers" || compact[1].key != "deploymentMode" ||
		string(compact[1].val) != `"3p"` || string(compact[2].val) != "12345678901234567890" || !strings.Contains(string(raw), "x<&>") {
		t.Errorf("%s", raw)
	}
	if got := f.json("developer_settings.json"); !reflect.DeepEqual(got, map[string]any{"allowDevTools": true, "other": "kept"}) {
		t.Errorf("%v", got)
	}
	if !f.ThirdParty() {
		t.Error("not in gateway mode")
	}
	for _, rel := range []string{"configLibrary/_meta.json", "configLibrary/" + id + ".json", "claude_desktop_config.json", "developer_settings.json"} {
		f.perm(f.path(rel), 0o600)
	}

	names := f.backups()
	if len(names) != 1 {
		t.Fatalf("%q", names)
	}
	dir := filepath.Join(f.BackupDir, names[0])
	f.perm(dir, 0o700)
	f.perm(f.BackupDir, 0o700)
	for rel, old := range map[string]string{"developer_settings.json": `{"allowDevTools":false,"other":"kept"}`, "configLibrary/_meta.json": idA} {
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil || !strings.Contains(string(b), old) {
			t.Errorf("%s: %s %v", rel, b, err)
		}
		f.perm(filepath.Join(dir, filepath.FromSlash(rel)), 0o600)
	}
	if _, err := os.Stat(filepath.Join(dir, "configLibrary", idB+".json")); err == nil {
		t.Error("a file that did not change was copied")
	}
}

func TestSetupFromNothing(t *testing.T) {
	f := newFixture(t)
	f.sys.running = false
	if err := f.setup(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.sys.log, []string{"confirm", "open"}) {
		t.Errorf("%q", f.sys.log)
	}
	f.perm(f.Dir, 0o700)
	f.perm(f.path("configLibrary"), 0o700)
	meta := f.json("configLibrary/_meta.json")
	if len(meta["entries"].([]any)) != 1 || f.json("claude_desktop_config.json")["deploymentMode"] != "3p" {
		t.Errorf("%v", meta)
	}
}

func TestSetupAgainReusesTheEntry(t *testing.T) {
	f := newFixture(t)
	f.seed()
	if err := f.setup(); err != nil {
		t.Fatal(err)
	}
	id := f.json("configLibrary/_meta.json")["appliedId"]
	before := f.tree()
	f.sys.running = true
	if err := f.setup(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.sys.text, "developer mode") {
		t.Error("the dialog announces a change that is not made")
	}
	if !reflect.DeepEqual(before, f.tree()) {
		t.Error("a second run changed something")
	}
	// Another entry in use and an old helper path: both are repaired.
	f.put(f.path("configLibrary/"+id.(string)+".json"), `{"inferenceGatewayApiKey":"old","inferenceCredentialHelper":"/old"}`)
	raw, _ := os.ReadFile(f.path("configLibrary/_meta.json"))
	o, _ := parseObject(raw)
	o, _ = o.with("appliedId", idB)
	o, _ = o.with("hybridPointer", map[string]string{"bootstrapUrl": "https://x"})
	b, _ := o.encode()
	f.put(f.path("configLibrary/_meta.json"), string(b))
	if err := f.setup(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, f.tree()) {
		t.Errorf("not repaired: %v", f.tree())
	}
}

func TestBack(t *testing.T) {
	f := newFixture(t)
	f.seed()
	if err := f.setup(); err != nil {
		t.Fatal(err)
	}
	before := f.tree()
	f.sys.running, f.sys.log = true, nil
	f.Exe = "/Volumes/x/slife-tray" // leaving must work from anywhere
	if err := f.back(); err != nil {
		t.Fatal(err)
	}
	after := f.tree()
	if after["claude_desktop_config.json"] != strings.Replace(before["claude_desktop_config.json"], `"3p"`, `"1p"`, 1) {
		t.Errorf("%s", after["claude_desktop_config.json"])
	}
	delete(before, "claude_desktop_config.json")
	delete(after, "claude_desktop_config.json")
	if !reflect.DeepEqual(before, after) {
		t.Error("more than the mode changed")
	}
	if f.ThirdParty() || !slices.Equal(f.sys.log, []string{"confirm", "quit", "open"}) || !strings.Contains(f.sys.text, "claude.ai") {
		t.Errorf("%q %q", f.sys.log, f.sys.text)
	}
}

func TestRefusalsBeforeTheQuestion(t *testing.T) {
	for name, c := range map[string]struct {
		prepare func(f *fixture)
		want    string
	}{
		"outside Applications": {func(f *fixture) { f.Exe = "/Users/x/Downloads/A.app/Contents/MacOS/slife-tray" }, "Applications folder"},
		"no helper":            {func(f *fixture) { f.Helper = filepath.Join(f.root, "none") }, "not found"},
		"relative helper":      {func(f *fixture) { f.Helper = "slife" }, "own disk"},
		"percent in helper":    {func(f *fixture) { f.Helper = filepath.Join(f.root, "100%", "slife") }, "characters"},
		"managed":              {func(f *fixture) { f.sys.managed = true }, "company policy"},
		"policy unknown":       {func(f *fixture) { f.sys.managedErr = errors.New("denied") }, "denied"},
		"blocked":              {func(f *fixture) { f.Blocked = func() error { return errors.New("package") } }, "package"},
		"broken meta":          {func(f *fixture) { f.put(f.path("configLibrary/_meta.json"), `{"appliedId":`) }, "_meta.json"},
		"empty meta":           {func(f *fixture) { f.put(f.path("configLibrary/_meta.json"), ``) }, "_meta.json"},
		"meta is a list":       {func(f *fixture) { f.put(f.path("configLibrary/_meta.json"), `[]`) }, "not a JSON object"},
		"meta trailing text":   {func(f *fixture) { f.put(f.path("configLibrary/_meta.json"), `{"entries":[]} x`) }, "_meta.json"},
		"entries not a list":   {func(f *fixture) { f.put(f.path("configLibrary/_meta.json"), `{"entries":{}}`) }, "not a list"},
		"entries null":         {func(f *fixture) { f.put(f.path("configLibrary/_meta.json"), `{"entries":null}`) }, "not a list"},
		"entries missing":      {func(f *fixture) { f.put(f.path("configLibrary/_meta.json"), `{"appliedId":""}`) }, "missing"},
		"entry not an object":  {func(f *fixture) { f.put(f.path("configLibrary/_meta.json"), `{"entries":["x"]}`) }, "not an object"},
		"entry without id":     {func(f *fixture) { f.put(f.path("configLibrary/_meta.json"), `{"entries":[{"name":"x"}]}`) }, "no id"},
		"applied id a number":  {func(f *fixture) { f.put(f.path("configLibrary/_meta.json"), `{"appliedId":1,"entries":[]}`) }, "not text"},
		"our id escapes": {func(f *fixture) {
			f.put(f.path("configLibrary/_meta.json"), `{"entries":[{"id":"../../x","name":"Second Life"}]}`)
		}, "not valid"},
		"our entry broken": {func(f *fixture) {
			f.put(f.path("configLibrary/_meta.json"), `{"entries":[{"id":"`+idA+`","name":"Second Life"}]}`)
			f.put(f.path("configLibrary/"+idA+".json"), `{`)
		}, idA},
		"broken mode file":      {func(f *fixture) { f.put(f.path(modeFile), `{"a":1,}`) }, modeFile},
		"mode file not UTF-8":   {func(f *fixture) { f.put(f.path(modeFile), "{\"a\":\"\xff\"}") }, modeFile},
		"broken developer file": {func(f *fixture) { f.put(f.path(developer), `nope`) }, developer},
		"too large": {func(f *fixture) {
			f.put(f.path(modeFile), `{"a":"`+strings.Repeat("x", maxFile)+`"}`)
		}, "too large"},
		"mode file is a folder": {func(f *fixture) {
			os.Remove(f.path(modeFile))
			os.Mkdir(f.path(modeFile), 0o700)
		}, "not a plain file"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.seed()
			c.prepare(f)
			f.refuse(f.setup, c.want)
			if len(f.backups()) != 0 {
				t.Error("made a backup")
			}
		})
	}
}

func TestSymlinks(t *testing.T) {
	for _, rel := range []string{".", "configLibrary", "configLibrary/_meta.json", modeFile, developer} {
		t.Run(rel, func(t *testing.T) {
			f := newFixture(t)
			f.seed()
			real := filepath.Join(f.root, "elsewhere")
			if err := os.Rename(f.path(rel), real); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(real, f.path(rel)); err != nil {
				t.Skip(err)
			}
			f.refuse(f.setup, "not a plain")
			if rel == modeFile {
				f.refuse(f.back, "not a plain")
				if f.ThirdParty() {
					t.Error("followed the link")
				}
			}
		})
	}
	t.Run("helper", func(t *testing.T) {
		f := newFixture(t)
		link := filepath.Join(f.root, "slife-link")
		if err := os.Symlink(f.Helper, link); err != nil {
			t.Skip(err)
		}
		f.Helper = link
		f.refuse(f.setup, "not a plain file")
	})
	t.Run("backup folder", func(t *testing.T) {
		f := newFixture(t)
		f.seed()
		os.MkdirAll(filepath.Dir(f.BackupDir), 0o700)
		if err := os.Symlink(t.TempDir(), f.BackupDir); err != nil {
			t.Skip(err)
		}
		f.refuse(f.setup, "back up", "confirm", "quit", "open")
	})
}

func TestDeclined(t *testing.T) {
	f := newFixture(t)
	f.seed()
	f.sys.decline = true
	f.refuse(f.setup, ErrCancelled.Error(), "confirm")
	if err := f.setup(); !errors.Is(err, ErrCancelled) {
		t.Error(err)
	}
}

func TestSystemFailures(t *testing.T) {
	boom := errors.New("boom")
	for name, c := range map[string]struct {
		prepare func(s *fakeSystem)
		calls   []string
	}{
		"dialog":  {func(s *fakeSystem) { s.confirmErr = boom }, []string{"confirm"}},
		"running": {func(s *fakeSystem) { s.runErr = boom }, []string{"confirm"}},
		"quit":    {func(s *fakeSystem) { s.quitErr = boom }, []string{"confirm", "quit"}},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.seed()
			c.prepare(f.sys)
			f.refuse(f.setup, "boom", c.calls...)
		})
	}
}

func TestClaudeStays(t *testing.T) {
	f := newFixture(t)
	f.seed()
	f.sys.stays = true
	f.refuse(f.setup, "did not quit", "confirm", "quit")
	f.sys.log = nil
	f.refuse(f.back, "did not quit", "confirm", "quit")
	if len(f.backups()) != 0 {
		t.Error("made a backup")
	}
}

func TestClaudeQuitsSlowly(t *testing.T) {
	f := newFixture(t)
	f.seed()
	f.quitWait, f.poll = 0, 0
	f.sys.stays = true
	synctest.Test(t, func(t *testing.T) {
		f.sys.stopsAt = time.Now().Add(10250 * time.Millisecond)
		begin := time.Now()
		if err := f.setup(); err != nil {
			t.Fatal(err)
		}
		if d := time.Since(begin); d != 10500*time.Millisecond {
			t.Error(d)
		}
	})
	synctest.Test(t, func(t *testing.T) {
		f.sys.stopsAt = time.Time{}
		begin := time.Now()
		if err := f.back(); err == nil || time.Since(begin) < 30*time.Second || time.Since(begin) > 31*time.Second {
			t.Errorf("%v after %v", err, time.Since(begin))
		}
	})
}

func TestCancelledByQuit(t *testing.T) {
	f := newFixture(t)
	f.seed()
	ctx, cancel := context.WithCancel(context.Background())
	f.sys.onConfirm = cancel
	f.refuse(func() error { return f.Setup(ctx, mapping) }, "canceled", "confirm", "quit", "open")
	f.sys.stays, f.sys.running, f.sys.log = true, true, nil
	f.refuse(func() error { return f.Setup(ctx, mapping) }, "canceled", "confirm", "quit")
}

// What Claude saves while it quits must not be lost.
func TestReadsAgainAfterQuit(t *testing.T) {
	f := newFixture(t)
	f.seed()
	f.sys.onQuit = func() { f.put(f.path(modeFile), `{"saved":"while quitting","deploymentMode":"1p"}`) }
	if err := f.setup(); err != nil {
		t.Fatal(err)
	}
	if got := f.json(modeFile); got["saved"] != "while quitting" || got["deploymentMode"] != "3p" {
		t.Errorf("%v", got)
	}

	f = newFixture(t)
	f.seed()
	f.sys.onQuit = func() { f.put(f.path(modeFile), `{"cut`) }
	if err := f.setup(); err == nil || !strings.Contains(err.Error(), "left alone") {
		t.Error(err)
	}
	if got := f.tree()[modeFile]; got != `{"cut` || !slices.Equal(f.sys.log, []string{"confirm", "quit", "open"}) {
		t.Errorf("%s %q", got, f.sys.log)
	}

	// The dialog did not mention developer mode, so it is not turned on.
	f = newFixture(t)
	f.seed()
	f.put(f.path(developer), `{"allowDevTools":true}`)
	f.sys.onQuit = func() { f.put(f.path(developer), `{"allowDevTools":false}`) }
	if err := f.setup(); err == nil || !strings.Contains(err.Error(), "meanwhile") {
		t.Error(err)
	}
	if f.tree()[developer] != `{"allowDevTools":false}` {
		t.Error("turned on without asking")
	}
}

func TestRollback(t *testing.T) {
	for fail := 1; fail <= 4; fail++ { // entry, meta, developer, mode
		for _, seeded := range []bool{true, false} {
			f := newFixture(t)
			if seeded {
				f.seed()
				os.Chmod(f.path(modeFile), 0o644)
			}
			n := 0
			f.write = func(path string, data []byte, mode fs.FileMode) error {
				if n++; n == fail {
					if fail == 2 { // the worst case: the file changed before the error
						writeAtomic(path, data, mode)
					}
					return errors.New("disk full")
				}
				return writeAtomic(path, data, mode)
			}
			f.refuse(f.setup, "disk full", "confirm", "quit", "open")
			if n != fail {
				t.Errorf("%d writes, want %d", n, fail)
			}
			if seeded {
				f.perm(f.path(modeFile), 0o644)
			}
		}
	}
}

func TestRollbackWhenClaudeDoesNotOpen(t *testing.T) {
	f := newFixture(t)
	f.seed()
	f.sys.openErr = errors.New("no app")
	f.refuse(f.setup, "old settings are back", "confirm", "quit", "open", "open")
}

func TestRollbackFailureIsReported(t *testing.T) {
	f := newFixture(t)
	f.seed()
	n := 0
	f.write = func(path string, data []byte, mode fs.FileMode) error {
		if n++; n < 4 {
			return writeAtomic(path, data, mode)
		}
		os.RemoveAll(f.path("configLibrary"))
		return errors.New("disk full")
	}
	if err := f.setup(); err == nil || !strings.Contains(err.Error(), "putting the old files back failed") {
		t.Error(err)
	}
	if len(f.backups()) != 1 {
		t.Error("no backup left")
	}
}

func TestChangedBetweenReadAndWrite(t *testing.T) {
	f := newFixture(t)
	f.seed()
	f.write = func(path string, data []byte, mode fs.FileMode) error {
		if strings.HasSuffix(path, "_meta.json") {
			f.put(f.path(modeFile), `{"someone":"else"}`)
		}
		return writeAtomic(path, data, mode)
	}
	if err := f.setup(); err == nil || !strings.Contains(err.Error(), "changed meanwhile") {
		t.Error(err)
	}
	if got := f.tree(); got[modeFile] != `{"someone":"else"}` || !strings.Contains(got[filepath.Join("configLibrary", "_meta.json")], `"appliedId":"`+idA) {
		t.Errorf("%v", got)
	}
}

func TestKeepsFiveBackups(t *testing.T) {
	f := newFixture(t)
	f.seed()
	f.put(filepath.Join(f.BackupDir, "mine", "note"), "x")
	at := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	f.now = func() time.Time { return at }
	for i := range 8 {
		f.sys.running = true
		run := f.setup
		if i%2 == 1 {
			run = f.back
		}
		if i != 4 { // two in the same millisecond
			at = at.Add(time.Second)
		}
		if err := run(); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"20261125-100004.000", "20261125-100004.000-1", "20261125-100005.000", "20261125-100006.000", "20261125-100007.000", "mine"}
	if got := f.backups(); !slices.Equal(got, want) {
		t.Errorf("%q", got)
	}
}

func TestLabelsAndPaths(t *testing.T) {
	for in, want := range map[string]string{"gpt-5.6-sol": "GPT-5.6 Sol", "gpt-6": "GPT-6", "o9": "o9", "gpt-6-a--b": "GPT-6 A B"} {
		if got := upstreamLabel(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
	if c := newConfig("u", "h", nil); c.Models[0].LabelOverride != "Opus" {
		t.Errorf("%+v", c.Models)
	}
	if b, _ := json.Marshal(newConfig("u", "h", mapping)); strings.Contains(string(b), "1m") || strings.Contains(strings.ToLower(string(b)), "apikey") {
		t.Errorf("%s", b)
	}
	for exe, warn := range map[string]bool{
		"/Volumes/Second Life/A.app/Contents/MacOS/slife-tray":    true,
		"/private/var/folders/x/AppTranslocation/y/d/A.app/slife": true,
		"/Applications/A.app/Contents/MacOS/slife-tray":           false,
		"/Users/x/A.app/Contents/MacOS/slife-tray":                false,
	} {
		if got := StartWarning("darwin", exe); (got != "") != warn {
			t.Errorf("%s: %q", exe, got)
		}
		if StartWarning("windows", exe) != "" || CheckLocation("windows", exe) != nil {
			t.Error("macOS rule on Windows")
		}
	}
	const local = `C:\Users\x\AppData\Local`
	for path, want := range map[string]bool{
		local + `\AnthropicClaude\app-6.1.0\claude.exe`:          true,
		`C:\Program Files\WindowsApps\Claude_6.1_x64\claude.exe`: true,
		`C:\Users\x\.local\bin\claude.exe`:                       false,
		``:                                                       false,
	} {
		if isDesktop(path, local) != want {
			t.Error(path)
		}
	}
}

func TestObject(t *testing.T) {
	o, err := parseObject([]byte(`{"a":1,"b":{"c":[1.0,"é"]},"a":2}`))
	if err != nil || string(o.get("a")) != "2" || o.get("z") != nil {
		t.Fatal(o, err)
	}
	if o, err = o.with("a", "x"); err != nil {
		t.Fatal(err)
	}
	if b, _ := o.encode(); strings.Join(strings.Fields(string(b)), "") != `{"a":"x","b":{"c":[1.0,"é"]}}` {
		t.Errorf("%s", b)
	}
	if _, ok := o.str("b"); ok {
		t.Error("an object is not text")
	}
}
