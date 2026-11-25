package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func values(env []string) map[string]string {
	m := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	return m
}

func TestClaudeEnv(t *testing.T) {
	in := []string{"KEEP=1", "HTTPS_PROXY=http://proxy:3128", "NO_PROXY=corp.example", "CLAUDECODE=1", "ANTHROPIC_BASE_URL=https://elsewhere",
		"ANTHROPIC_AUTH_TOKEN=other", "CLAUDE_CODE_USE_POWERSHELL_TOOL=1", "CLAUDE_CODE_USE_SOMETHING_NEW=1"}
	for _, k := range routing {
		in = append(in, k+"=x")
	}
	got := values(claudeEnvOn("linux", in, "http://127.0.0.1:1", "tok"))
	for k, want := range map[string]string{
		"KEEP": "1", "HTTPS_PROXY": "http://proxy:3128", "CLAUDE_CODE_USE_POWERSHELL_TOOL": "1",
		"ANTHROPIC_BASE_URL": "http://127.0.0.1:1", "ANTHROPIC_AUTH_TOKEN": "tok", "CLAUDE_CODE_AUTO_COMPACT_WINDOW": "240000",
		"NO_PROXY": "corp.example,127.0.0.1,localhost,::1", "no_proxy": "corp.example,127.0.0.1,localhost,::1",
	} {
		if got[k] != want {
			t.Errorf("%s=%q want %q", k, got[k], want)
		}
	}
	for _, k := range append(routing, "CLAUDECODE", "CLAUDE_CODE_USE_SOMETHING_NEW") {
		if _, ok := got[k]; ok {
			t.Error(k, "stays")
		}
	}
	win := claudeEnvOn("windows", []string{"Anthropic_Api_Key=x", "claude_code_use_bedrock=1", "no_proxy=a"}, "u", "t")
	if all := strings.Join(win, " "); strings.Contains(all, "=x") || strings.Contains(strings.ToLower(all), "bedrock") || !strings.Contains(all, "NO_PROXY=a,127.0.0.1") {
		t.Error(win)
	}
}

func TestClaudeArgs(t *testing.T) {
	const tok = "the-local-key"
	env := settingsEnv("http://127.0.0.1:1", claudeEnvOn("linux", []string{"NO_PROXY=corp"}, "http://127.0.0.1:1", tok))
	file := filepath.Join(t.TempDir(), "s.json")
	if err := os.WriteFile(file, []byte(`{"model":"opus","env":{"MINE":"1","ANTHROPIC_BASE_URL":"https://elsewhere","ANTHROPIC_AUTH_TOKEN":"z"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, in := range [][]string{{"-p", "hi"}, {"-p", "--settings", file, "hi"}, {"--settings=" + file, "-p", "hi"}, {"-p", "hi", "--", "--settings", "text"}} {
		got, err := claudeArgs(in, env)
		if err != nil || got[0] != "--settings" {
			t.Fatal(got, err)
		}
		if strings.Contains(strings.Join(got, " "), tok) {
			t.Error("the key is in the arguments")
		}
		var s struct {
			Model string
			Env   map[string]string
		}
		if err := json.Unmarshal([]byte(got[1]), &s); err != nil {
			t.Fatal(err)
		}
		if v, ok := s.Env["CLAUDE_CODE_USE_BEDROCK"]; !ok || v != "" || s.Env["ANTHROPIC_BASE_URL"] != "http://127.0.0.1:1" || s.Env["NO_PROXY"] != "corp,127.0.0.1,localhost,::1" {
			t.Error(s.Env)
		}
		if _, ok := s.Env["ANTHROPIC_AUTH_TOKEN"]; ok {
			t.Error("a key in the settings would replace ours")
		}
		own := strings.Contains(strings.Join(in, " "), file)
		if own && (s.Model != "opus" || s.Env["MINE"] != "1") {
			t.Error("the user's settings are lost")
		}
		want := "-p hi"
		if len(in) == 5 {
			want = "-p hi -- --settings text"
		}
		if rest := strings.Join(got[2:], " "); rest != want {
			t.Errorf("%q want %q", rest, want)
		}
	}
	for _, bad := range [][]string{{"--settings"}, {"--settings", "{"}, {"--settings", filepath.Join(t.TempDir(), "no")}, {"--settings", `{"env":1}`}} {
		if _, err := claudeArgs(bad, env); err == nil {
			t.Error(bad)
		}
	}
}

func TestPlain(t *testing.T) {
	if got := plain("a\x1b]0;x\a\x1b[2J\tb\nc\u009b"); strings.ContainsAny(got, "\x1b\a\u009b") || !strings.Contains(got, "\tb\nc") {
		t.Errorf("%q", got)
	}
}
