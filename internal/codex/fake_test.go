package codex

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The test binary stands in for the codex CLI when SLIFE_FAKE is set. It writes its pid to SLIFE_FAKE_PID and every
// request to SLIFE_FAKE_LOG.
func init() {
	mode := os.Getenv("SLIFE_FAKE")
	if mode == "" {
		return
	}
	fakeCodex(mode)
	os.Exit(0)
}

func fakeCodex(mode string) {
	if p := os.Getenv("SLIFE_FAKE_PID"); p != "" && !strings.HasPrefix(mode, "wrap:") && !strings.HasPrefix(mode, "dies:") {
		_ = os.WriteFile(p, []byte(strconv.Itoa(os.Getpid())), 0o600)
	}
	hang := func() { time.Sleep(time.Minute); os.Exit(0) }
	if inner, ok := strings.CutPrefix(mode, "wrap:"); ok { // like the npm launcher
		_ = fakeChild(inner).Wait()
		return
	}
	if inner, ok := strings.CutPrefix(mode, "dies:"); ok { // the launcher is killed, the real one stays
		fakeChild(inner)
		time.Sleep(time.Second)
		return
	}
	if mode == "mute" {
		hang()
	}
	in, tokens := bufio.NewScanner(os.Stdin), 0
	for in.Scan() {
		var m struct {
			ID     *int64
			Method string
			Params struct{ RefreshToken bool }
		}
		_ = json.Unmarshal(in.Bytes(), &m)
		if p := os.Getenv("SLIFE_FAKE_LOG"); p != "" {
			f, _ := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
			fmt.Fprintf(f, "%s refresh=%v\n", m.Method, m.Params.RefreshToken)
			f.Close()
		}
		if m.ID == nil {
			if mode == "hangs" { // after the handshake
				hang()
			}
			continue
		}
		result := "{}"
		switch m.Method {
		case "account/read":
			result = `{"account":{"type":"chatgpt","email":"a@example.com","planType":"plus"}}`
		case "account/login/start":
			result = `{"authUrl":"https://auth.openai.com/x"}`
		case "account/rateLimits/read":
			if mode == "slow" {
				continue
			}
		case "getAuthStatus":
			if m.Params.RefreshToken {
				tokens++
			}
			result = fmt.Sprintf(`{"authMethod":"chatgpt","authToken":%q}`, fakeToken(tokens))
		}
		fmt.Printf(`{"id":%d,"result":%s}`+"\n", *m.ID, result)
	}
}

func fakeChild(mode string) *exec.Cmd {
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "SLIFE_FAKE="+mode)
	cmd.Stdin, cmd.Stdout = os.Stdin, os.Stdout
	if err := cmd.Start(); err != nil {
		panic(err)
	}
	return cmd
}

func fakeToken(n int) string {
	body := fmt.Sprintf(`{"exp":4000000000,"n":%d,"https://api.openai.com/auth":{"chatgpt_account_id":"acct"}}`, n)
	return "e30." + base64.RawURLEncoding.EncodeToString([]byte(body)) + ".x"
}

func fake(t *testing.T, mode string) *Client {
	t.Helper()
	t.Setenv("SLIFE_FAKE", mode)
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	c := &Client{Bin: os.Args[0], Version: "test", Dir: t.TempDir()}
	t.Cleanup(c.Close)
	return c
}
