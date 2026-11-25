package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestCodexSetup(t *testing.T) {
	missing := errors.New("no")
	for _, c := range []struct {
		name, input, goos, wantErr string
		installed, works, in       bool
		fail                       string
		calls                      string
	}{
		{name: "all there", installed: true, in: true},
		{name: "signed out", installed: true, calls: "/x/codex login"},
		{name: "refused", input: "n\n", wantErr: "not installed"},
		{name: "only Enter", input: "\n", wantErr: "not installed"},
		{name: "end of input", input: "", wantErr: "not installed"},
		{name: "mac", input: "y\n", works: true, calls: "curl install.sh | sh install.sh | /x/codex login"},
		{name: "windows", goos: "windows", input: "yes\n", works: true, calls: "powershell irm | /x/codex login"},
		{name: "download fails", input: "y\n", fail: "curl", wantErr: "installer failed", calls: "curl install.sh"},
		{name: "installer fails", input: "y\n", fail: "sh", wantErr: "installer failed", calls: "curl install.sh | sh install.sh"},
		{name: "nothing installed", input: "y\n", wantErr: "still not found", calls: "curl install.sh | sh install.sh"},
		{name: "login fails", installed: true, fail: "/x/codex", wantErr: "codex login failed", calls: "/x/codex login"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var calls []string
			in, installed := c.in, c.installed
			var out bytes.Buffer
			var script string
			r := runner{goos: "darwin", in: bufio.NewReader(strings.NewReader(c.input)), out: &out}
			if c.goos != "" {
				r.goos = c.goos
			}
			r.find = func() (string, error) {
				if installed {
					return "/x/codex", nil
				}
				return "", missing
			}
			r.run = func(name string, args ...string) error {
				line := name
				switch name {
				case "curl":
					line += " install.sh"
					script = args[len(args)-2]
					if args[len(args)-1] != codexScriptURL || !strings.Contains(strings.Join(args, " "), "--proto =https") {
						t.Error(args)
					}
				case "sh":
					line += " install.sh"
					if len(args) != 1 || args[0] != script {
						t.Error(args)
					}
					installed = installed || c.works
				case "powershell":
					line += " irm"
					installed = installed || c.works
				default:
					line += " " + strings.Join(args, " ")
					in = true
				}
				calls = append(calls, line)
				if name == c.fail {
					return fmt.Errorf("boom")
				}
				return nil
			}
			err := codexSetup(r, func() (bool, error) { return in, nil })
			if (err == nil) != (c.wantErr == "") || err != nil && !strings.Contains(err.Error(), c.wantErr) {
				t.Fatal(err)
			}
			if got := strings.Join(calls, " | "); got != c.calls {
				t.Fatalf("%q, want %q", got, c.calls)
			}
			if script != "" {
				if _, err := os.Stat(script); err == nil {
					t.Error("the script stayed on disk")
				}
			}
		})
	}
}
