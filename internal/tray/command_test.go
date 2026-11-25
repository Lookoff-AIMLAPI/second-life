package tray

import (
	"encoding/xml"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSafeURL(t *testing.T) {
	for _, ok := range []string{
		"https://developers.openai.com/codex/cli",
		"https://auth.openai.com/oauth/authorize?response_type=code&redirect_uri=http%3A%2F%2Flocalhost%3A1455%2Fcb",
		"HTTPS://example.com/",
	} {
		if _, err := safeURL(ok); err != nil {
			t.Errorf("%s refused: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"", "http://example.com", "file:///etc/passwd", "javascript:alert(1)", "https://", "https:///path", "https://:443/",
		"https:example.com", "//example.com", "example.com", "--args", `C:\Windows\System32\calc.exe`,
		"https://user:secret@example.com/", "https://example.com/a b", "https://example.com/\n", " https://example.com", "https://exa\x00mple.com",
	} {
		got, err := safeURL(bad)
		if err == nil {
			t.Errorf("%q accepted as %q", bad, got)
		} else if strings.Contains(err.Error(), "secret") {
			t.Errorf("error repeats the link: %v", err)
		}
	}
}

func TestShQuote(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil || runtime.GOOS == "windows" {
		t.Skip("no sh")
	}
	for _, path := range []string{"/plain/slife", "/My Apps/slife", "/Stan's/slife", `/a"b$(touch x)` + "`id`" + `\slife`, "/a;b&c|d\n/slife", "''"} {
		out, err := exec.Command(sh, "-c", "printf %s "+shQuote(path)).Output()
		if err != nil || string(out) != path {
			t.Errorf("%q came back as %q (%v)", path, out, err)
		}
	}
}

// The script is run for real with a stand-in for slife.
func TestClaudeScript(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs sh")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "Stan's Apps $HOME")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	slife := filepath.Join(dir, "slife")
	if err := os.WriteFile(slife, []byte("#!/bin/sh\npwd -P\necho \"$#:$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(root, "cache")
	var script string
	for range 2 { // the second write replaces the first
		var err error
		if script, err = writeClaudeScript(cache, slife, verbClaude); err != nil {
			t.Fatal(err)
		}
	}
	for path, mode := range map[string]os.FileMode{script: 0o700, filepath.Dir(script): 0o700} {
		if st, err := os.Stat(path); err != nil || st.Mode().Perm() != mode {
			t.Errorf("%s: %v %v", path, st.Mode(), err)
		}
	}
	if left, _ := os.ReadDir(filepath.Dir(script)); len(left) != 1 {
		t.Errorf("leftovers: %v", left)
	}
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(script)
	cmd.Env = append(os.Environ(), "HOME="+home)
	out, err := cmd.CombinedOutput()
	if err != nil || string(out) != home+"\n1:claude\n" {
		t.Errorf("%q %v", out, err)
	}
}

func TestPrivateDirRefusesSymlink(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(root, appDir)); err != nil {
		t.Skip(err)
	}
	if _, err := writeClaudeScript(root, "/slife", verbClaude); err == nil {
		t.Error("wrote through a symlink")
	}
}

func TestClaudeCmdLine(t *testing.T) {
	const cmdExe = `C:\Windows\system32\cmd.exe`
	got, err := claudeCmdLine(cmdExe, `C:\Users\Stan's & Co (x86)\slife.exe`, verbClaude)
	want := `"C:\Windows\system32\cmd.exe" /d /v:off /s /c "start "" "C:\Users\Stan's & Co (x86)\slife.exe" claude"`
	if err != nil || got != want {
		t.Errorf("%s %v", got, err)
	}
	for _, bad := range []string{`C:\a" & calc & "\slife.exe`, `C:\%PATH%\slife.exe`, "C:\\a\n\\slife.exe"} {
		if got, err := claudeCmdLine(cmdExe, bad, verbClaude); err == nil {
			t.Errorf("%q accepted: %s", bad, got)
		}
	}
	if got := runValue(`C:\My Apps\slife-tray.exe`); got != `"C:\My Apps\slife-tray.exe"` {
		t.Error(got)
	}
}

func TestMenuTitle(t *testing.T) {
	if got := menuTitle("a & b", "windows"); got != "a && b" {
		t.Error(got)
	}
	if got := menuTitle("a & b", "darwin"); got != "a & b" {
		t.Error(got)
	}
}

func TestLaunchAgentPlist(t *testing.T) {
	exe := `/Applications/A & B <"C's">/slife-tray`
	plist, err := launchAgentPlist(exe)
	if err != nil {
		t.Fatal(err)
	}
	var strs []string
	dec := xml.NewDecoder(strings.NewReader(string(plist)))
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if el, ok := tok.(xml.StartElement); ok && el.Name.Local == "string" {
			var s string
			if err := dec.DecodeElement(&s, &el); err != nil {
				t.Fatal(err)
			}
			strs = append(strs, s)
		}
	}
	if len(strs) < 2 || strs[0] != appID || strs[1] != exe {
		t.Errorf("%q", strs)
	}
	if s := string(plist); !strings.Contains(s, "<key>RunAtLoad</key>\n\t<true/>") || strings.Contains(s, "KeepAlive") {
		t.Error(s)
	}
	if lint, err := exec.LookPath("plutil"); err == nil {
		path := filepath.Join(t.TempDir(), "a.plist")
		if err := os.WriteFile(path, plist, 0o600); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(lint, "-lint", path).CombinedOutput(); err != nil {
			t.Errorf("plutil: %s", out)
		}
	}
}

func TestSetLaunchAgent(t *testing.T) {
	home := t.TempDir()
	const exe = "/Applications/Second Life.app/Contents/MacOS/slife-tray"
	if launchAgentEnabled(home, exe) {
		t.Error("enabled before it was set")
	}
	if err := setLaunchAgent(home, exe, false); err != nil {
		t.Errorf("turning off twice: %v", err)
	}
	if err := setLaunchAgent(home, exe, true); err != nil {
		t.Fatal(err)
	}
	if !launchAgentEnabled(home, exe) {
		t.Error("not enabled")
	}
	if launchAgentEnabled(home, "/moved/slife-tray") {
		t.Error("enabled for another path")
	}
	if err := setLaunchAgent(home, exe, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(launchAgentPath(home)); !os.IsNotExist(err) {
		t.Errorf("file left: %v", err)
	}
	if err := setLaunchAgent(home, "/private/var/folders/x/AppTranslocation/y/slife-tray", true); err == nil {
		t.Error("accepted a translocated path")
	}
}

func TestSiblingC2C(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "slife-tray")
	if _, err := siblingCLI(exe, "slife"); err != errNoCLI {
		t.Errorf("missing: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "slife"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := siblingCLI(exe, "slife"); err != nil || got != filepath.Join(dir, "slife") {
		t.Errorf("%q %v", got, err)
	}
}
