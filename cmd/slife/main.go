// Command slife runs the bridge without the tray and starts Claude Code through it.
package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/Lookoff-AIMLAPI/second-life/internal/bridge"
	"github.com/Lookoff-AIMLAPI/second-life/internal/codex"
)

const usage = `slife: Claude Code and Claude Desktop on the models you pick

  slife claude [args]     start Claude Code through its own private bridge
  slife serve             run the bridge in this terminal
  slife status            show the running bridge
  slife logs [-f]         show the end of the bridge log, -f keeps following it
  slife login [--device]  sign in to Codex with ChatGPT
  slife codex             install the Codex CLI and sign in
  slife mcp list          show the MCP servers of Codex and Claude Desktop
  slife mcp import        add them to Claude Code, asking for each one
  slife doctor            check what is installed and what is missing
  slife token             print the local API key and nothing else
  slife version
`

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = serve()
	case "claude":
		err = claude(os.Args[2:])
	case "status":
		err = status()
	case "logs":
		err = logs(len(os.Args) > 2 && os.Args[2] == "-f")
	case "doctor":
		err = doctor(os.Stdout)
	case "codex":
		err = codexCommand()
	case "mcp":
		err = mcpCommand(os.Args[2:])
	case "login":
		err = login(len(os.Args) > 2 && os.Args[2] == "--device")
	case "token":
		var tok string
		if tok, err = token(); err == nil {
			fmt.Print(tok)
		}
	case "version", "--version":
		fmt.Println(bridge.Version)
	default:
		fmt.Print(usage)
		os.Exit(2)
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if code := ee.ExitCode(); code >= 0 {
			os.Exit(code)
		}
		os.Exit(1) // ended by a signal
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "slife:", plain(err.Error()))
		os.Exit(1)
	}
}

// plain keeps text from elsewhere from driving the terminal.
func plain(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return ' '
		}
		return r
	}, s)
}

// endSignals is everything that should end a run cleanly. Under nohup a closed terminal must not.
func endSignals() []os.Signal {
	s := []os.Signal{os.Interrupt, syscall.SIGTERM}
	if !signal.Ignored(syscall.SIGHUP) {
		s = append(s, syscall.SIGHUP)
	}
	return s
}

func serve() error {
	b, err := bridge.New(bridge.Options{Echo: os.Stderr})
	if err != nil {
		return err
	}
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, endSignals()...)
	if err := b.Start(); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "listening on", b.Status().Addr)
	<-ch
	signal.Stop(ch) // a second one ends it at once
	b.Stop()
	return nil
}

func base() string { return "http://" + bridge.DialAddr(bridge.Addr()) }

// local talks to this machine only: no proxy, no redirects.
func local(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// alive proves that the program on the port knows the local token before we send it anything.
func alive(token string) (running bool, err error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return false, err
	}
	nonce := hex.EncodeToString(raw)
	r, err := local(3 * time.Second).Get(base() + "/healthz?nonce=" + nonce)
	if err != nil {
		return false, nil
	}
	defer r.Body.Close()
	var out struct {
		Proof string `json:"proof"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&out)
	if subtle.ConstantTimeCompare([]byte(out.Proof), []byte(bridge.Proof(token, bridge.Addr(), nonce))) != 1 {
		return false, fmt.Errorf("another program is using %s", bridge.Addr())
	}
	return true, nil
}

func token() (string, error) {
	h, err := bridge.Home()
	if err != nil {
		return "", err
	}
	return bridge.LocalToken(h)
}

// claude gives the session its own listener with its own key, so nothing else can take the port over while it runs.
func claude(args []string) error {
	bin, err := codex.FindTool("claude")
	if err != nil {
		return errors.New("Claude Code is not installed")
	}
	tok := bridge.NewToken()
	b, err := bridge.New(bridge.Options{Addr: "127.0.0.1:0", Token: tok})
	if err != nil {
		return err
	}
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, endSignals()...)
	defer signal.Stop(ch)
	if err := b.Start(); err != nil {
		return err
	}
	defer b.Stop()
	url := "http://" + b.Status().Addr
	env := claudeEnv(codex.ToolEnv(bin), url, tok)
	args, err = claudeArgs(args, settingsEnv(url, env))
	if err != nil {
		return err
	}
	cmd, err := codex.Command(bin, args...)
	if err != nil {
		return err
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = env
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	for {
		select {
		case err := <-done:
			return err
		case s := <-ch:
			// The terminal sends Ctrl-C to Claude itself; it decides what that means.
			if s != os.Interrupt {
				_ = cmd.Process.Signal(s)
			}
		}
	}
}

func status() error {
	tok, err := token()
	if err != nil {
		return err
	}
	on, err := alive(tok)
	if err != nil {
		return err
	}
	if !on {
		fmt.Println("off")
		return nil
	}
	req, err := http.NewRequest("GET", base()+"/admin/status", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	r, err := local(5 * time.Second).Do(req)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	var s bridge.Status
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&s); err != nil {
		return err
	}
	fmt.Println(plain(fmt.Sprintf("%s on %s, plan %s, requests %d, errors %d, tokens in %d out %d, last model %s", s.State, s.Addr, s.Plan, s.Requests, s.Errors, s.InputTokens, s.OutputTokens, s.LastModel)))
	if s.LastError != "" {
		fmt.Println("last error:", plain(s.LastError))
	}
	return nil
}

func login(device bool) error {
	home, err := bridge.Home()
	if err != nil {
		return err
	}
	c := &codex.Client{Version: bridge.Version, Dir: home}
	defer c.Close()
	ctx, stop := signal.NotifyContext(context.Background(), endSignals()...)
	defer stop()
	if a, err := c.Account(ctx); err != nil {
		return err
	} else if a != nil {
		fmt.Println("already signed in, plan", plain(a.Plan))
		return nil
	}
	if device {
		u, code, err := c.LoginDevice(ctx)
		if err != nil {
			return err
		}
		fmt.Println(plain(fmt.Sprintf("Open %s and enter %s", u, code)))
	} else {
		u, err := c.Login(ctx)
		if err != nil {
			return err
		}
		fmt.Println("Open this address to sign in:\n" + plain(u))
	}
	for {
		select {
		case <-ctx.Done():
			return errors.New("sign-in cancelled")
		case <-time.After(2 * time.Second):
		}
		if a, err := c.Account(ctx); err == nil && a != nil {
			fmt.Println("signed in, plan", plain(a.Plan))
			return nil
		}
	}
}
