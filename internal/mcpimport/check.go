package mcpimport

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

var (
	envPattern    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	headerPattern = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+.^_`|~-]+$")
	reference     = regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*(?::-[^}]*)?\}`) // what Claude Code 2.1.325 replaces
)

// maxJSON keeps the command line under the limit of Windows.
const maxJSON = 24 << 10

// codexNames are servers Codex ships for itself.
var codexNames = []string{"codex_app", "node_repl", "cua_repl", "computer-use"}

// codexOwn: the server belongs to Codex or ChatGPT and is of no use elsewhere.
func (m Machine) codexOwn(s Server) bool {
	if slices.Contains(codexNames, s.Name) || inApp(s.Command) || slices.ContainsFunc(s.Args, inApp) {
		return true
	}
	if s.Command == "" {
		return false
	}
	p := m.place(s.Command)
	if inApp(p) {
		return true
	}
	if real, err := filepath.EvalSymlinks(p); err == nil && inApp(real) {
		return true
	}
	for _, dir := range m.CodexDirs {
		if rel, err := filepath.Rel(dir, p); dir != "" && err == nil && filepath.IsLocal(rel) {
			return true
		}
	}
	return false
}

func inApp(path string) bool {
	for _, part := range strings.FieldsFunc(strings.ToLower(path), func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == "codex.app" || part == "chatgpt.app" || strings.HasPrefix(part, "openai.codex_") || strings.HasPrefix(part, "openai.chatgpt") {
			return true
		}
	}
	return false
}

// Unusable says why a server cannot be copied to any Claude, or "".
func (m Machine) Unusable(s Server) string {
	switch {
	case !ValidName(s.Name):
		return "its name has characters Claude Code does not take"
	case m.codexOwn(s):
		return "works only inside Codex"
	case !showable(s.Command) || !showable(s.URL) || slices.ContainsFunc(s.Args, func(a string) bool { return !showable(a) }):
		return "it has characters that cannot be shown"
	}
	for k := range s.Env {
		if !envPattern.MatchString(k) {
			return "the name of a variable is not valid"
		}
	}
	if !s.remote() && !m.installed(s.Command) {
		return "its command was not found on this computer"
	}
	return ""
}

func (m Machine) installed(command string) bool {
	if filepath.IsAbs(command) {
		st, err := os.Stat(command)
		return err == nil && st.Mode().IsRegular() && (m.GOOS == "windows" || st.Mode().Perm()&0o111 != 0)
	}
	if strings.ContainsAny(command, `/\`) {
		return false // depends on the folder Claude is started in
	}
	_, err := m.Find(command)
	return err == nil
}

// unsafe adds what matters to Claude Code alone.
func (f found) unsafe(source bool) string {
	for k, v := range f.Headers {
		if !headerPattern.MatchString(k) || strings.ContainsAny(v, "\r\n\x00") {
			return "a header is not valid"
		}
	}
	if f.remote() && !plainURL(f.URL) {
		return "its address is not valid"
	}
	if source { // before our own references go in
		for _, v := range f.all() {
			if reference.MatchString(v) {
				return "it contains ${...}, which Claude Code would replace"
			}
		}
	}
	if j, err := f.JSON(); err != nil || len(j) > maxJSON {
		return "its settings are too large"
	}
	return ""
}

func (s Server) all() []string {
	out := append([]string{s.Command, s.URL}, s.Args...)
	for _, v := range s.Env {
		out = append(out, v)
	}
	for _, v := range s.Headers {
		out = append(out, v)
	}
	return out
}

// Claude Code 2.1.325 reads these as empty in the address and headers of a server, so that a credential of its own does
// not travel. The list is its own, shortened to families where that is safe.
var (
	neverPrefix = []string{"ANTHROPIC_", "CLAUDE_", "AWS_", "AZURE_", "ACTIONS_", "ARTIFACTS_CREDENTIALPROVIDER_", "BUNDLE_", "CARGO_REGISTR",
		"CLOUDSDK_", "CONAN_", "CONSUL_HTTP_", "FLIT_", "GIT_CONFIG_", "GOOGLE_", "HATCH_INDEX_", "INPUT_", "MATURIN_", "NOMAD_", "OTEL_",
		"POETRY_HTTP_BASIC_", "SELF_HOSTED_RUNNER_", "SONAR", "TWINE_", "UV_", "VAULT_", "VSS_NUGET_", "_CLAUDE_"}
	neverNames = []string{"AGENT_PROXY_AUTH_TOKEN", "ALL_INPUTS", "ALL_PROXY", "ANACONDA_API_TOKEN", "BINSTAR_API_TOKEN", "CCR_AGENT_PROXY_TOKEN_FILE_DESCRIPTOR",
		"CI_DEPLOY_USER", "CI_REGISTRY_USER", "CODEARTIFACT_AUTH_TOKEN", "COMPOSER_AUTH", "DEFAULT_WORKFLOW_TOKEN", "DISCORD_WEBHOOK", "DISCORD_WEBHOOK_URL",
		"ENVIRONMENT_SERVICE_KEY", "FASTLANE_SESSION", "GEM_HOST_API_KEY", "GOAUTH", "GOPROXY", "HTTPS_PROXY", "HTTP_PROXY", "IDENTITY_HEADER", "JF_USER",
		"MATCH_GIT_BASIC_AUTHORIZATION", "MCP_CLIENT_SECRET", "MCP_XAA_IDP_CLIENT_SECRET", "MSI_SECRET", "MS_TEAMS_WEBHOOK_URI", "NODE_AUTH_TOKEN", "NPM_TOKEN",
		"NUGET_AUTH_TOKEN", "OVERRIDE_GITHUB_TOKEN", "PIP_EXTRA_INDEX_URL", "PIP_INDEX_URL", "PYPI_API_TOKEN", "PYPI_TOKEN", "SLACK_WEBHOOK", "SLACK_WEBHOOK_URL",
		"SSH_SIGNING_KEY", "TEAMS_WEBHOOK_URL"}
)

func neverExpanded(name string) bool {
	n := strings.ToUpper(name)
	return slices.Contains(neverNames, n) || slices.ContainsFunc(neverPrefix, func(p string) bool { return strings.HasPrefix(n, p) })
}
