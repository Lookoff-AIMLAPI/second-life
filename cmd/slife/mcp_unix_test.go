//go:build !windows

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The test binary is the fake claude of these tests with SLIFE_FAKE_MCP: it logs its arguments and stores what it is given,
// as Claude Code does.
func init() {
	if log := os.Getenv("SLIFE_FAKE_MCP"); log != "" && os.Getenv("SLIFE_RUN") == "" {
		line, _ := json.Marshal(os.Args[1:])
		f, _ := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		f.Write(append(line, '\n'))
		f.Close()
		path := filepath.Join(os.Getenv("HOME"), ".claude.json")
		var top struct {
			Servers map[string]json.RawMessage `json:"mcpServers"`
		}
		b, _ := os.ReadFile(path)
		_ = json.Unmarshal(b, &top)
		name, text := os.Args[len(os.Args)-2], os.Args[len(os.Args)-1]
		if name == "refused" {
			os.Stderr.WriteString("Invalid configuration: " + text)
			os.Exit(1)
		}
		if top.Servers == nil {
			top.Servers = map[string]json.RawMessage{}
		}
		top.Servers[name] = json.RawMessage(text)
		b, _ = json.Marshal(top)
		_ = os.WriteFile(path, b, 0o600)
		os.Stdout.WriteString("Added " + text)
		os.Exit(0)
	}
}

const mcpCodexList = `[
 {"name":"context7","enabled":true,"auth_status":"unsupported","transport":{"type":"stdio","command":"npx","args":["-y","@upstash/context7-mcp","--api-key","VALUE-1"],"env":{"CONTEXT7_KEY":"VALUE-2"},"env_vars":["HOME_DIR"],"cwd":null}},
 {"name":"files","enabled":true,"auth_status":"unsupported","transport":{"type":"stdio","command":"npx","args":["-y","files-mcp","/srv"],"env":null,"env_vars":[],"cwd":null}},
 {"name":"docs","enabled":true,"auth_status":"bearer_token","transport":{"type":"streamable_http","url":"https://u:VALUE-3@docs.test/mcp?token=VALUE-4&mode=read","bearer_token_env_var":"DOCS_TOKEN","http_headers":{"X-Team":"VALUE-5"},"env_http_headers":null}},
 {"name":"linear","enabled":true,"auth_status":"o_auth","transport":{"type":"streamable_http","url":"https://mcp.linear.test/VALUE-6","bearer_token_env_var":null,"http_headers":null,"env_http_headers":null}},
 {"name":"paused","enabled":false,"auth_status":"unsupported","transport":{"type":"stdio","command":"npx","args":["VALUE-7"],"env":null,"env_vars":[],"cwd":null}},
 {"name":"node_repl","enabled":true,"auth_status":"unsupported","transport":{"type":"stdio","command":"/Applications/Codex.app/x","args":[],"env":{"A":"VALUE-8"},"env_vars":[],"cwd":null}},
 {"name":"gone","enabled":true,"auth_status":"unsupported","transport":{"type":"stdio","command":"slife-no-such-tool","args":[],"env":{"A":"VALUE-9"},"env_vars":[],"cwd":null}},
 {"name":"refused","enabled":true,"auth_status":"unsupported","transport":{"type":"stdio","command":"npx","args":["refused"],"env":{"A":"VALUE-10"},"env_vars":[],"cwd":null}},
 {"name":"e\u001b[2Jvil","enabled":true,"auth_status":"unsupported","transport":{"type":"stdio","command":"npx","args":["VALUE-11"],"env":null,"env_vars":[],"cwd":null}}
]`

const mcpUsual = `{"mcpServers":{
 "folders":{"command":"npx","args":["-y","files-mcp","/srv"]},
 "git":{"command":"uvx","args":["mcp-server-git"],"env":{"GIT_PASS":"VALUE-12"}},
 "kept":{"command":"uvx","args":["kept","VALUE-13"],"env":{"A":"VALUE-14"}}}}`

// mcpRun runs slife in a closed world. Nothing of the real home or PATH is in it.
type mcpRun struct {
	t         *testing.T
	home, dir string
}

func newMCPRun(t *testing.T) mcpRun {
	t.Helper()
	r := mcpRun{t, t.TempDir(), t.TempDir()}
	write := func(path, text string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(r.dir, "list.json"), mcpCodexList)
	write(filepath.Join(r.dir, "codex"), "#!/bin/sh\n[ \"$*\" = 'mcp list --json' ] || exit 3\nexec cat '"+filepath.Join(r.dir, "list.json")+"'\n")
	write(filepath.Join(r.dir, "claude"), "#!/bin/sh\nexec '"+os.Args[0]+"' \"$@\"\n")
	write(filepath.Join(r.dir, "npx"), "#!/bin/sh\nexit 1\n")
	write(filepath.Join(r.dir, "uvx"), "#!/bin/sh\nexit 1\n")
	write(filepath.Join(r.home, ".claude.json"), `{"mcpServers":{"mine":{"type":"stdio","command":"x","args":[],"env":{"A":"VALUE-15"}},"kept":{"type":"stdio","command":"other","args":["VALUE-16"]}}}`)
	base := filepath.Join(r.home, "Library", "Application Support")
	write(filepath.Join(base, "Claude", "claude_desktop_config.json"), mcpUsual)
	write(filepath.Join(base, "Claude-3p", "claude_desktop_config.json"), `{"mcpServers":{"KEPT":{"command":"x","env":{"A":"VALUE-17"}}}}`)
	return r
}

// run returns everything that was printed, without any VALUE in it.
func (r mcpRun) run(input string, args ...string) string {
	r.t.Helper()
	cmd := exec.Command(os.Args[0], append([]string{"mcp"}, args...)...)
	cmd.Env = []string{"SLIFE_RUN=1", "SLIFE_FAKE_MCP=" + filepath.Join(r.dir, "log"), "PATH=" + r.dir + ":/usr/bin:/bin", "HOME=" + r.home, "SLIFE_HOME=" + filepath.Join(r.dir, "slife")}
	var out bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = strings.NewReader(input), &out, &out
	err := cmd.Run()
	if err != nil {
		out.WriteString("[exit]\n")
	}
	if strings.Contains(out.String(), "VALUE") || strings.Contains(out.String(), "\x1b") {
		r.t.Errorf("a value or a control character was printed:\n%s", out.String())
	}
	return out.String()
}

func (r mcpRun) log() string {
	b, _ := os.ReadFile(filepath.Join(r.dir, "log"))
	return string(b)
}

const mcpGoldenList = `Codex
  context7
    command: npx -y @upstash/context7-mcp --api-key ***
    variables, values not shown: CONTEXT7_KEY
    *** hides what looks like a secret
    Codex passes on HOME_DIR; Claude Code passes on everything it is started with, so set them there
    Claude Code: can be added
  files
    command: npx -y files-mcp /srv
    also in Claude Desktop as folders
    Codex starts it with few variables; Claude Code passes on everything it is started with
    Claude Code: can be added
  docs
    address: https://***@docs.test/mcp?token=***&mode=read
    headers, values not shown: Authorization, X-Team
    *** hides what looks like a secret
    takes DOCS_TOKEN from the environment Claude Code is started in
    Claude Code: can be added
  linear
    Claude Code: no, it signs in with OAuth, and a sign-in cannot be copied; add it in Claude and sign in there
  paused
    Claude Code: no, switched off in Codex
  node_repl
    Claude Code: no, works only inside Codex
  gone
    Claude Code: no, its command was not found on this computer
  refused
    command: npx refused
    variables, values not shown: A
    Codex starts it with few variables; Claude Code passes on everything it is started with
    Claude Code: can be added
  e [2Jvil
    Claude Code: no, its name has characters Claude Code does not take
Claude Desktop, usual profile
  folders
    the same as files in Codex
    gateway mode: Set up Claude Desktop in the app copies it
  git
    command: uvx mcp-server-git
    variables, values not shown: GIT_PASS
    Claude Code: can be added
    gateway mode: Set up Claude Desktop in the app copies it
  kept
    Claude Code: no, Claude Code has a different server with this name, which stays as it is
    gateway mode: there
Claude Code has: kept, mine
Claude Desktop's gateway mode has: KEPT
`

func TestMCPList(t *testing.T) {
	r := newMCPRun(t)
	if runtime.GOOS == "darwin" { // Linux has no Claude Desktop
		if got := r.run("", "list"); got != mcpGoldenList {
			t.Errorf("%s", got)
		}
	} else {
		r.run("", "list")
	}
	if r.log() != "" {
		t.Error("the list changed something: " + r.log())
	}
}

const mcpGoldenImport = `
context7 (Codex)
    command: npx -y @upstash/context7-mcp --api-key ***
    variables, values not shown: CONTEXT7_KEY
    *** hides what looks like a secret
    Codex passes on HOME_DIR; Claude Code passes on everything it is started with, so set them there
    Claude Code takes the settings only as a command argument: while it adds the server, for about a second, other users of this computer can read the hidden values in the list of processes.
Add it to Claude Code for all projects? [y/N] added

docs (Codex)
    address: https://***@docs.test/mcp?token=***&mode=read
    headers, values not shown: Authorization, X-Team
    *** hides what looks like a secret
    takes DOCS_TOKEN from the environment Claude Code is started in
    Claude Code takes the settings only as a command argument: while it adds the server, for about a second, other users of this computer can read the hidden values in the list of processes.
Add it to Claude Code for all projects? [y/N] not added
linear (Codex): skipped, it signs in with OAuth, and a sign-in cannot be copied; add it in Claude and sign in there

refused (Codex)
    command: npx refused
    variables, values not shown: A
    Codex starts it with few variables; Claude Code passes on everything it is started with
    Claude Code takes the settings only as a command argument: while it adds the server, for about a second, other users of this computer can read the hidden values in the list of processes.
Add it to Claude Code for all projects? [y/N] not added: Claude Code does not accept its settings

1 added. A new Claude Code session starts them. Codex's own limits (allowed tools, approvals, timeouts) do not move.
slife: 1 could not be added
[exit]
`

func TestMCPImport(t *testing.T) {
	r := newMCPRun(t)
	if got := r.run("y\n\nyes\n", "import", "--from", "codex", "context7", "docs", "linear", "refused"); got != mcpGoldenImport {
		t.Errorf("%s", got)
	}
	lines := strings.Split(strings.TrimSpace(r.log()), "\n")
	if len(lines) != 2 || lines[0] != `["mcp","add-json","--scope","user","context7","{\"type\":\"stdio\",\"command\":\"npx\",\"args\":[\"-y\",\"@upstash/context7-mcp\",\"--api-key\",\"VALUE-1\"],\"env\":{\"CONTEXT7_KEY\":\"VALUE-2\"}}"]` {
		t.Error(lines)
	}
	b, _ := os.ReadFile(filepath.Join(r.home, ".claude.json"))
	if !strings.Contains(string(b), "VALUE-2") || !strings.Contains(string(b), "VALUE-15") {
		t.Errorf("%s", b)
	}
	// Nothing but the target holds a value now, next to what was there before.
	filepath.WalkDir(r.home, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() && !strings.HasSuffix(p, ".json") {
			t.Error("a new file: " + p)
		}
		return nil
	})
	if _, err := os.Stat(filepath.Join(r.dir, "slife")); err == nil {
		t.Error("slife wrote something of its own")
	}
	if got := r.run("", "import", "context7"); !strings.Contains(got, "skipped, already in Claude Code") {
		t.Error(got)
	}
}

func TestMCPImportDefaultsToNo(t *testing.T) {
	for _, input := range []string{"", "\n\n\n\n\n\n", "n\nno\nall\nY!\n\n", "maybe\n"} {
		r := newMCPRun(t)
		got := r.run(input, "import")
		if strings.Contains(got, "[exit]") || !strings.Contains(got, "0 added.") || r.log() != "" {
			t.Errorf("%q: %s %s", input, got, r.log())
		}
	}
	r := newMCPRun(t)
	if got := r.run("", "import", "--wait", "files"); !strings.HasSuffix(got, "Press Enter to close. ") {
		t.Error(got)
	}
}

func TestMCPArguments(t *testing.T) {
	r := newMCPRun(t)
	for _, args := range [][]string{{}, {"remove"}, {"list", "x"}, {"import", "--yes"}, {"import", "-y"}, {"import", "--all"}, {"import", "--from"}, {"import", "--from", "other"}, {"import", "--from="}, {"import", "unknown\x1b[2J"}} {
		if got := r.run("y\ny\ny\ny\ny\ny\ny\n", args...); !strings.Contains(got, "[exit]") || r.log() != "" {
			t.Errorf("%q: %s", args, got)
		}
	}
	os.Remove(filepath.Join(r.dir, "codex"))
	os.WriteFile(filepath.Join(r.dir, "codex"), []byte("#!/bin/sh\necho VALUE; echo VALUE >&2; exit 5\n"), 0o700)
	if got := r.run("", "import", "--from=codex"); !strings.Contains(got, "could not list its servers") || strings.Contains(got, "[exit]") {
		t.Error(got)
	}
}
