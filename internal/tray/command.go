package tray

import (
	"errors"
	"net"
	"net/url"
	"strings"
	"unicode"
)

const (
	appID  = "io.github.lookoff-aimlapi.second-life"
	appDir = "second-life"
)

// safeURL returns raw normalised, or an error unless it is a plain https URL.
func safeURL(raw string) (string, error) {
	if strings.ContainsFunc(raw, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) {
		return "", errors.New("refused to open a malformed link")
	}
	u, err := url.Parse(raw)
	// The parse error repeats the URL, and a login URL is not for the menu.
	if err != nil {
		return "", errors.New("refused to open a malformed link")
	}
	if u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return "", errors.New("refused to open a link that is not https")
	}
	return u.String(), nil
}

// shQuote quotes s as one word for a POSIX shell.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// claudeScript is the .command file that Terminal runs.
// The verbs are fixed strings of ours, never input.
const (
	verbClaude = "claude"
	verbCodex  = "codex --wait"
	verbMCP    = "mcp import --wait"
)

func claudeScript(slife, verb string) string {
	return "#!/bin/sh\ncd \"$HOME\" || exit 1\nexec " + shQuote(slife) + " " + verb + "\n"
}

// claudeCmdLine is the whole Windows command line. Go's own argument quoting is
// for programs that parse like the C runtime; cmd.exe does not. With /s cmd
// drops the outer quotes and keeps the rest as is.
func claudeCmdLine(cmdExe, slife, verb string) (string, error) {
	// Inside quotes cmd still expands %NAME%, and there is no escape for it here.
	if strings.ContainsAny(slife, `"%`) || strings.ContainsFunc(slife, unicode.IsControl) {
		return "", errors.New(`move the app to a folder without % or " in its path`)
	}
	return `"` + cmdExe + `" /d /v:off /s /c "start "" "` + slife + `" ` + verb + `"`, nil
}

// runValue is the data of the Windows Run registry value.
func runValue(exe string) string {
	return `"` + exe + `"`
}

// menuTitle doubles & on Windows, where a single one marks the shortcut letter.
func menuTitle(s, goos string) string {
	if goos == "windows" {
		return strings.ReplaceAll(s, "&", "&&")
	}
	return s
}

// localURL accepts only the bridge's own page on this machine.
func localURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.User != nil || (u.Path != "/picker/" && u.Path != "/picker/plugins") || u.RawQuery != "" {
		return false
	}
	ip := net.ParseIP(u.Hostname())
	return ip != nil && ip.IsLoopback()
}
