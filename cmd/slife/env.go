package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
)

// routing are the settings that would send Claude somewhere else than the bridge.
var routing = []string{
	"ANTHROPIC_API_KEY", "ANTHROPIC_CUSTOM_HEADERS", "ANTHROPIC_UNIX_SOCKET",
	"CLAUDE_CODE_API_BASE_URL", "CLAUDE_CODE_OAUTH_TOKEN",
	"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY", "CLAUDE_CODE_USE_MANTLE",
	"CLAUDE_CODE_USE_ANTHROPIC_AWS", "CLAUDE_CODE_USE_ANTHROPIC_GOOGLE_CLOUD", "CLAUDE_CODE_USE_GATEWAY",
}

// tools share the prefix of the provider switches but choose no provider.
var tools = map[string]bool{
	"CLAUDE_CODE_USE_POWERSHELL_TOOL": true, "CLAUDE_CODE_USE_NATIVE_FILE_SEARCH": true, "CLAUDE_CODE_USE_COWORK_PLUGINS": true,
}

var dropped = func() map[string]bool {
	m := map[string]bool{
		"ANTHROPIC_BASE_URL": true, "ANTHROPIC_AUTH_TOKEN": true, "CLAUDECODE": true, "NO_PROXY": true,
		"CLAUDE_CODE_API_KEY_FILE_DESCRIPTOR": true, "CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR": true,
	}
	for _, k := range routing {
		m[k] = true
	}
	return m
}()

// envKey is how the system compares names: Windows ignores case.
func envKey(kv, goos string) string {
	k, _, _ := strings.Cut(kv, "=")
	if goos == "windows" {
		k = strings.ToUpper(k)
	}
	return k
}

// claudeEnv points only the child at the bridge and drops what would route it elsewhere.
func claudeEnv(env []string, url, tok string) []string {
	return claudeEnvOn(runtime.GOOS, env, url, tok)
}

func claudeEnvOn(goos string, env []string, url, tok string) []string {
	var out, noProxy []string
	window := false
	for _, kv := range env {
		k := envKey(kv, goos)
		if strings.EqualFold(k, "NO_PROXY") {
			if _, v, _ := strings.Cut(kv, "="); v != "" {
				noProxy = append(noProxy, v)
			}
			continue
		}
		if dropped[k] || strings.HasPrefix(k, "CLAUDE_CODE_USE_") && !tools[k] {
			continue
		}
		window = window || k == "CLAUDE_CODE_AUTO_COMPACT_WINDOW"
		out = append(out, kv)
	}
	// Claude would hand even a loopback request to a proxy, prompt and key included.
	direct := strings.Join(append(noProxy, "127.0.0.1", "localhost", "::1"), ",")
	out = append(out, "ANTHROPIC_BASE_URL="+url, "ANTHROPIC_AUTH_TOKEN="+tok, "NO_PROXY="+direct)
	if goos != "windows" {
		out = append(out, "no_proxy="+direct)
	}
	if !window {
		out = append(out, "CLAUDE_CODE_AUTO_COMPACT_WINDOW=240000") // Codex models hold 272k
	}
	return out
}

// settingsEnv repeats the routing as Claude settings, because an env block in a settings file beats the environment.
// It never holds the key: arguments are visible to other users.
func settingsEnv(url string, env []string) map[string]string {
	s := map[string]string{"ANTHROPIC_BASE_URL": url}
	for _, kv := range env {
		if k, v, _ := strings.Cut(kv, "="); k == "NO_PROXY" || k == "no_proxy" {
			s[k] = v
		}
	}
	for _, k := range routing {
		s[k] = ""
	}
	return s
}

// claudeArgs adds the settings. Claude takes one --settings, so the user's own is merged in.
func claudeArgs(args []string, env map[string]string) ([]string, error) {
	settings := map[string]json.RawMessage{}
	var rest []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			rest = append(rest, args[i:]...)
			break
		}
		v, joined := strings.CutPrefix(args[i], "--settings=")
		if !joined {
			if args[i] != "--settings" {
				rest = append(rest, args[i])
				continue
			}
			if i++; i == len(args) {
				return nil, errors.New("--settings needs a value")
			}
			v = args[i]
		}
		raw := []byte(v)
		if !strings.HasPrefix(strings.TrimSpace(v), "{") {
			b, err := os.ReadFile(v)
			if err != nil {
				return nil, err
			}
			raw = b
		}
		settings = map[string]json.RawMessage{} // the last one counts, as in Claude
		if err := json.Unmarshal(raw, &settings); err != nil || settings == nil {
			return nil, errors.New("--settings is not a JSON object")
		}
	}
	merged := map[string]json.RawMessage{}
	if old, ok := settings["env"]; ok {
		if err := json.Unmarshal(old, &merged); err != nil || merged == nil {
			return nil, errors.New("--settings: env is not a JSON object")
		}
		delete(merged, "ANTHROPIC_AUTH_TOKEN")
	}
	for k, v := range env {
		merged[k], _ = json.Marshal(v)
	}
	raw, err := json.Marshal(merged)
	if err != nil {
		return nil, err
	}
	settings["env"] = raw
	if raw, err = json.Marshal(settings); err != nil {
		return nil, fmt.Errorf("--settings: %w", err)
	}
	return append([]string{"--settings", string(raw)}, rest...), nil
}
