package mcpimport

import (
	"fmt"
	"strings"
	"testing"
)

func TestMaskArgs(t *testing.T) {
	for in, want := range map[string]string{
		// what the owner must be able to read
		"-y @modelcontextprotocol/server-filesystem@2025.1.14 /Users/x/My-Project": "-y @modelcontextprotocol/server-filesystem@2025.1.14 /Users/x/My-Project",
		"/opt/MyCompanyInternalServer/dist/index.js":                               "/opt/MyCompanyInternalServer/dist/index.js",
		"-y npm-check-updates npm_config_registry":                                 "-y npm-check-updates npm_config_registry",
		"run -p 3000 --rm -i mcp/server:1.2.3":                                     "run -p 3000 --rm -i mcp/server:1.2.3",
		`C:\Users\x\server.exe --log-level=debug`:                                  `C:\Users\x\server.exe --log-level=debug`,
		"mcp-remote https://mcp.example.com:8443/v1/sse?mode=read":                 "mcp-remote https://mcp.example.com:8443/v1/sse?mode=read",
		"-e x=1;evil()": "-e x=1;evil()",

		// secrets
		"--token abc":                                "--token ***",
		"--api-key=abc":                              "--api-key=***",
		"--apiKey abc":                               "--apiKey ***",
		"--key abc --verbose":                        "--key *** --verbose",
		"--auth --token abc":                         "--auth --token ***",
		"-e GITHUB_TOKEN=abc mcp/x":                  "-e GITHUB_TOKEN=*** mcp/x",
		"-e PLAIN=abc mcp/x":                         "-e PLAIN=*** mcp/x",
		"--header X-Any:abc":                         "--header X-Any: ***",
		"-H abc":                                     "-H ***",
		"-u bob:abc":                                 "-u bob:***",
		"PASSWORD=abc":                               "PASSWORD=***",
		"sk-abcdef123":                               "***",
		"ghp_abcdefgh1":                              "***",
		"xoxb-1234567":                               "***",
		"a8f5f167f44f4964e6c998dee827":               "***",
		"Ab3dE6gH9jK2mN5p":                           "***",
		"aBcDeFgHiJkLmNoPqRsT":                       "***",
		"abcdefghijklmnopqrstuvwxyz":                 "***",
		"1234567890123456":                           "***",
		"eyJhbGciOi.eyJzdWIiOi.c2lnbmF":              "***.***.c2lnbmF",
		"https://u:p@x.test/":                        "https://***@x.test/",
		"https://x.test/s/?token=abc":                "https://x.test/s/?token=***",
		"https://x.test/?a=b&k=1&apikey=abc#abc":     "https://x.test/?a=b&k=1&apikey=***#***",
		"https://x.test/token/abc/mcp":               "https://x.test/token/***/mcp",
		"https://x.test/Ab3dE6gH9jK2mN5p/mcp":        "https://x.test/***/mcp",
		"https://x.test/%41b3dE6gH9jK2mN5p":          "https://x.test/***",
		"https://x.test/?to=https%3A%2F%2Fu:p@y":     "https://x.test/?to=***",
		"https://x.test/?abcdefghijklmnopqrstuvwxyz": "https://x.test/?***",
		"https://x.test:bad/":                        "***",
	} {
		if got := strings.Join(maskArgs(strings.Fields(in)), " "); got != want {
			t.Errorf("%s\n got %s\nwant %s", in, got, want)
		}
	}
	for in, want := range map[string]string{
		`curl -H "Authorization: Bearer abc" x`:      `curl -H "Authorization: ***" x`,
		`{"token":"abc","a":"b"}`:                    `{"token":***,"a":"b"}`,
		`x --password 'a b' y`:                       `x --password *** y`,
		`exec Bearer abc`:                            `exec Bearer ***`,
		`--x="https://u:p@h.test/a" password: "a b"`: `--x="https://***@h.test/a" password: ***`,
	} {
		if got := maskText(in); got != want {
			t.Errorf("%s\n got %s\nwant %s", in, got, want)
		}
	}
}

func TestNamesAndText(t *testing.T) {
	for n, want := range map[string]bool{"a": true, "context7": true, "my-server_2": true, strings.Repeat("a", 64): true, strings.Repeat("a", 65): false,
		"": false, "-s": false, "--scope": false, "a b": false, "a;b": false, "a\n": false, "é": false, "__proto__": false, "constructor": false, "a/../b": false} {
		if ValidName(n) != want {
			t.Errorf("%q", n)
		}
	}
	if got := Clean("a\x1b[2Jb\r\nc\u202ed\u200be"); strings.ContainsFunc(got, odd) || strings.ContainsAny(got, "\x1b\r\n") {
		t.Errorf("%q", got)
	}
}

// A server that ends up in a log or an error prints as its name.
func TestServersDoNotPrintValues(t *testing.T) {
	s := Server{Name: "x", Command: "c", Args: []string{"ARGSECRET"}, Env: map[string]string{"A": "ENVSECRET"}, Headers: map[string]string{"B": "HEADERSECRET"}, URL: "URLSECRET"}
	for _, v := range []any{s, &s, Item{Server: s, Twins: []Item{{Server: s}}}, found{Server: s}, []Server{s}, Report{Items: []Item{{Server: s}}}} {
		for _, f := range []string{"%v", "%+v", "%#v", "%s", "%q", "%d"} {
			if out := fmt.Sprintf(f, v); strings.Contains(out, "SECRET") {
				t.Errorf("%s of %T: %s", f, v, out)
			}
		}
	}
}

// A value is hidden wherever it turns up again, also when it does not look like a secret.
func TestValuesAreHiddenInTheLine(t *testing.T) {
	s := Server{Name: "x", Command: "run", Args: []string{"--mode", "plainword", "x=plainword"}, Env: map[string]string{"A": "plainword"}}
	if got := s.Line(); got != "run --mode *** x=***" {
		t.Error(got)
	}
	h := Server{Name: "x", URL: "https://x.test/plainword", Headers: map[string]string{"A": "plainword"}}
	if got := h.Line(); got != "https://x.test/***" {
		t.Error(got)
	}
}
