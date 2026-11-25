package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Lookoff-AIMLAPI/second-life/internal/bridge"
	"github.com/Lookoff-AIMLAPI/second-life/internal/codex"
	"github.com/Lookoff-AIMLAPI/second-life/internal/keystore"
	"github.com/Lookoff-AIMLAPI/second-life/internal/route"
)

// check is one line of the report.
type check struct {
	name, state, hint string
	ok                bool
}

// doctor says what is there and what is missing. It changes nothing and prints no secrets.
func doctor(w io.Writer) error {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	var list []check
	add := func(name string, ok bool, state, hint string) { list = append(list, check{name, state, hint, ok}) }

	home, err := bridge.Home()
	if err != nil {
		return err
	}
	if err := bridge.Adopt(home); err != nil {
		add("settings of the former name", false, err.Error(), "")
	}
	cfg, err := route.Load(home)
	if err != nil {
		add("models", false, err.Error(), "")
		cfg = route.Default()
	}
	usesCodex, usesAIML := false, false
	for _, t := range cfg.Slots {
		usesCodex = usesCodex || t.Provider == route.Codex
		usesAIML = usesAIML || t.Provider == route.AIMLAPI
	}
	bin, ferr := codex.FindTool("codex")
	if ferr != nil {
		add("Codex CLI", !usesCodex, "not found", "run: slife codex")
	} else {
		add("Codex CLI", true, bin, "")
		c := &codex.Client{Version: bridge.Version, Dir: home}
		a, err := c.Account(ctx)
		c.Close()
		switch {
		case err != nil:
			add("ChatGPT sign-in", !usesCodex, "could not be checked: "+err.Error(), "run: codex login")
		case a == nil:
			add("ChatGPT sign-in", !usesCodex, "signed out", "run: codex login")
		default:
			add("ChatGPT sign-in", true, "plan "+a.Plan, "")
		}
	}
	_, kerr := keystore.Get()
	switch {
	case kerr == nil:
		add("AIMLAPI key", true, "stored", "")
	case errors.Is(kerr, keystore.ErrNone):
		add("AIMLAPI key", !usesAIML, "none", "open Choose models in the app")
	default:
		add("AIMLAPI key", !usesAIML, "could not be read", "")
	}
	if p, err := codex.FindTool("claude"); err != nil {
		add("Claude Code", false, "not found", "needed for Open Claude; Claude Desktop works without it")
	} else {
		add("Claude Code", true, p, "")
	}
	if tok, err := token(); err != nil {
		add("bridge", false, err.Error(), "")
	} else if on, err := alive(tok); err != nil {
		add("bridge", false, err.Error(), "another program holds the port")
	} else if !on {
		add("bridge", false, "off", "start the app")
	} else {
		add("bridge", true, "on "+bridge.Addr(), "")
	}
	fmt.Fprintf(w, "slife %s on %s/%s\n", bridge.Version, runtime.GOOS, runtime.GOARCH)
	for _, c := range list {
		mark := "ok "
		if !c.ok {
			mark = "-- "
		}
		line := mark + c.name + ": " + c.state
		if c.hint != "" && !c.ok {
			line += " (" + c.hint + ")"
		}
		fmt.Fprintln(w, plain(line))
	}
	return nil
}

const (
	codexScriptURL = "https://chatgpt.com/codex/install.sh"
	codexPSURL     = "https://chatgpt.com/codex/install.ps1"
)

// runner is what codexSetup needs from the machine.
type runner struct {
	find func() (string, error)
	run  func(name string, args ...string) error
	in   *bufio.Reader
	out  io.Writer
	goos string
}

// codexSetup installs the Codex CLI with OpenAI's own installer, after a yes, and signs in.
func codexSetup(r runner, signedIn func() (bool, error)) error {
	bin, err := r.find()
	if err != nil {
		fmt.Fprintln(r.out, "The Codex CLI is not installed.")
		src := codexScriptURL
		if r.goos == "windows" {
			src = codexPSURL
		}
		fmt.Fprintf(r.out, "OpenAI's installer, %s, puts it in your home folder and adds it to PATH.\nInstall it now? [y/N] ", src)
		if !yes(r.in) {
			return errors.New("not installed")
		}
		if r.goos == "windows" {
			err = r.run("powershell", "-NoProfile", "-ExecutionPolicy", "ByPass", "-Command", "irm "+codexPSURL+" | iex")
		} else {
			err = installUnix(r)
		}
		if err != nil {
			return fmt.Errorf("the Codex installer failed: %w", err)
		}
		if bin, err = r.find(); err != nil {
			return errors.New("the installer ended, but the Codex CLI is still not found; open a new terminal and run: slife doctor")
		}
	}
	fmt.Fprintln(r.out, "Codex CLI:", bin)
	in, err := signedIn()
	if err != nil {
		return err
	}
	if !in {
		fmt.Fprintln(r.out, "Signing in with ChatGPT; your browser opens.")
		if err := r.run(bin, "login"); err != nil {
			return fmt.Errorf("codex login failed: %w", err)
		}
		if in, err = signedIn(); err != nil || !in {
			return errors.New("still signed out of ChatGPT; run: codex login")
		}
	}
	fmt.Fprintln(r.out, "Codex is ready. The app notices it by itself.")
	return nil
}

// installUnix saves the script first: a download that breaks half way must not run.
func installUnix(r runner) error {
	dir, err := os.MkdirTemp("", "slife-codex-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	script := filepath.Join(dir, "install.sh")
	if err := r.run("curl", "-fsSL", "--proto", "=https", "--proto-redir", "=https", "-o", script, codexScriptURL); err != nil {
		return err
	}
	return r.run("sh", script)
}

func yes(in *bufio.Reader) bool {
	line, _ := in.ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

func codexCommand() error {
	home, err := bridge.Home()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	r := runner{find: func() (string, error) { return codex.FindTool("codex") }, in: bufio.NewReader(os.Stdin), out: os.Stdout, goos: runtime.GOOS}
	r.run = func(name string, args ...string) error {
		cmd := exec.Command(name, args...)
		if filepath.IsAbs(name) { // the Codex CLI itself; the rest are system tools
			c, err := codex.Command(name, args...)
			if err != nil {
				return err
			}
			cmd = c
			cmd.Env = codex.ToolEnv(name)
		}
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		return cmd.Run()
	}
	err = codexSetup(r, func() (bool, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		c := &codex.Client{Version: bridge.Version, Dir: home}
		defer c.Close()
		a, err := c.Account(ctx)
		return a != nil, err
	})
	if len(os.Args) > 2 && os.Args[2] == "--wait" { // started from the menu: keep the window readable
		if err != nil {
			fmt.Println("slife:", plain(err.Error()))
		}
		fmt.Print("Press Enter to close. ")
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	}
	return err
}
