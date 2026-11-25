package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/Lookoff-AIMLAPI/second-life/internal/mcpimport"
)

const mcpUsage = `  slife mcp list                                       show the MCP servers of Codex and Claude Desktop
  slife mcp import [--from codex|claude] [name...]     add them to Claude Code, asking for each one
`

// mcpRunner is what the mcp command needs from the machine.
type mcpRunner struct {
	m   mcpimport.Machine
	in  *bufio.Reader
	out io.Writer
}

func mcpCommand(args []string) error {
	m, err := mcpimport.Here()
	if err != nil {
		return err
	}
	wait := slices.Contains(args, "--wait") // started from the menu: keep the window readable
	args = slices.DeleteFunc(slices.Clone(args), func(a string) bool { return a == "--wait" })
	r := mcpRunner{m: m, in: bufio.NewReader(os.Stdin), out: os.Stdout}
	err = r.command(context.Background(), args)
	if wait {
		if err != nil {
			fmt.Println("slife:", plain(err.Error()))
		}
		fmt.Print("Press Enter to close. ")
		_, _ = r.in.ReadString('\n')
	}
	return err
}

func (r mcpRunner) command(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage:\n" + mcpUsage)
	}
	switch args[0] {
	case "list":
		if len(args) > 1 {
			return errors.New("usage:\n" + mcpUsage)
		}
		return r.list(ctx)
	case "import":
		from, names, err := importArgs(args[1:])
		if err != nil {
			return err
		}
		return r.imports(ctx, from, names)
	}
	return errors.New("usage:\n" + mcpUsage)
}

// importArgs returns the source, or "" for both, and the names.
func importArgs(args []string) (from string, names []string, err error) {
	sources := map[string]string{"codex": mcpimport.Codex, "claude": mcpimport.Desktop}
	for i := 0; i < len(args); i++ {
		v, joined := strings.CutPrefix(args[i], "--from=")
		switch {
		case joined:
		case args[i] == "--from":
			if i++; i == len(args) {
				return "", nil, errors.New("--from needs codex or claude")
			}
			v = args[i]
		case strings.HasPrefix(args[i], "-"):
			return "", nil, errors.New("unknown option " + args[i])
		default:
			names = append(names, args[i])
			continue
		}
		if from = sources[v]; from == "" {
			return "", nil, errors.New("--from needs codex or claude")
		}
	}
	return from, names, nil
}

func (r mcpRunner) say(format string, a ...any) { fmt.Fprint(r.out, plain(fmt.Sprintf(format, a...))) }

func (r mcpRunner) load(ctx context.Context) (mcpimport.Report, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second) // Codex asks remote servers how they sign in
	defer cancel()
	return r.m.Load(ctx)
}

func (r mcpRunner) list(ctx context.Context) error {
	rep, err := r.load(ctx)
	if err != nil {
		return err
	}
	for _, src := range []string{mcpimport.Codex, mcpimport.Desktop} {
		title := src
		if src == mcpimport.Desktop {
			title += ", usual profile"
		}
		r.say("%s\n", title)
		if p := rep.Problems[src]; p != "" {
			r.say("  %s\n", p)
			continue
		}
		none := true
		for _, it := range rep.Items {
			if it.Source == src {
				none = false
				r.one(rep, it, nil)
			}
			for _, t := range it.Twins {
				if t.Source == src {
					none = false
					r.one(rep, t, &it)
				}
			}
		}
		if none {
			r.say("  none\n")
		}
	}
	r.say("Claude Code has: %s\n", nameList(rep.Code))
	r.say("Claude Desktop's gateway mode has: %s\n", nameList(rep.Gateway))
	return nil
}

// one shows a server; its details once, where it is listed first.
func (r mcpRunner) one(rep mcpimport.Report, it mcpimport.Item, as *mcpimport.Item) {
	gateway := ""
	if it.Source == mcpimport.Desktop {
		if slices.ContainsFunc(rep.Gateway, func(n string) bool { return strings.EqualFold(n, it.Name) }) {
			gateway = "there"
		} else if r.m.Unusable(it.Server) == "" {
			gateway = "Set up Claude Desktop in the app copies it"
		}
	}
	r.say("  %s\n", mcpimport.Clean(it.Name))
	switch {
	case as != nil && it.Skip == "":
		r.say("    the same as %s in %s\n", mcpimport.Clean(as.Name), as.Source)
	case it.Skip == "":
		r.details(it)
		r.say("    Claude Code: can be added\n")
	default:
		if strings.HasPrefix(gateway, "Set") { // shown before it is copied
			r.details(it)
		}
		r.say("    Claude Code: no, %s\n", it.Skip)
	}
	if gateway != "" {
		r.say("    gateway mode: %s\n", gateway)
	}
}

func nameList(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return mcpimport.Clean(strings.Join(names, ", "))
}

// details shows everything about a server but values.
func (r mcpRunner) details(it mcpimport.Item) {
	line := it.Line()
	if it.URL != "" {
		r.say("    address: %s\n", line)
	} else {
		r.say("    command: %s\n", line)
	}
	if n := it.EnvNames(); len(n) > 0 {
		r.say("    variables, values not shown: %s\n", strings.Join(n, ", "))
	}
	if n := it.HeaderNames(); len(n) > 0 {
		r.say("    headers, values not shown: %s\n", strings.Join(n, ", "))
	}
	if strings.Contains(line, "***") {
		r.say("    *** hides what looks like a secret\n")
	}
	for _, t := range it.Twins {
		r.say("    also in %s as %s\n", t.Source, mcpimport.Clean(t.Name))
	}
	if len(it.Refs) > 0 {
		r.say("    takes %s from the environment Claude Code is started in\n", mcpimport.Clean(strings.Join(it.Refs, ", ")))
	}
	if len(it.Inherits) > 0 {
		r.say("    Codex passes on %s; Claude Code passes on everything it is started with, so set them there\n", mcpimport.Clean(strings.Join(it.Inherits, ", ")))
	} else if it.Source == mcpimport.Codex && it.URL == "" {
		r.say("    Codex starts it with few variables; Claude Code passes on everything it is started with\n")
	}
	if strings.HasPrefix(strings.ToLower(it.URL), "http://") {
		r.say("    this address is not encrypted\n")
	}
}

func (r mcpRunner) imports(ctx context.Context, from string, names []string) error {
	if _, err := r.m.Find("claude"); err != nil {
		return errors.New("Claude Code is not installed")
	}
	rep, err := r.load(ctx)
	if err != nil {
		return err
	}
	for _, src := range []string{mcpimport.Codex, mcpimport.Desktop} {
		if p := rep.Problems[src]; p != "" && (from == "" || from == src) {
			r.say("%s: %s\n", src, p)
		}
	}
	items, missing := rep.Select(from, names)
	if len(missing) > 0 {
		return errors.New("no server named " + mcpimport.Clean(strings.Join(missing, ", ")) + "; see: slife mcp list")
	}
	added, failed := 0, 0
	for _, it := range items {
		if it.Skip == "" && added > 0 {
			if it.Skip, err = r.m.There(it.Server); err != nil {
				return err
			}
		}
		if it.Skip != "" {
			r.say("%s (%s): skipped, %s\n", mcpimport.Clean(it.Name), it.Source, it.Skip)
			continue
		}
		r.say("\n%s (%s)\n", mcpimport.Clean(it.Name), it.Source)
		r.details(it)
		if it.Exposes() {
			r.say("    Claude Code takes the settings only as a command argument: while it adds the server, for about a second, other users of this computer can read the hidden values in the list of processes.\n")
		}
		r.say("Add it to Claude Code for all projects? [y/N] ")
		if !yes(r.in) {
			r.say("not added\n")
			continue
		}
		addCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		err := r.m.Add(addCtx, it.Server)
		cancel()
		if err != nil {
			failed++
			r.say("not added: %s\n", err)
			continue
		}
		added++
		r.say("added\n")
	}
	r.say("\n%d added. ", added)
	if added > 0 {
		r.say("A new Claude Code session starts them. Codex's own limits (allowed tools, approvals, timeouts) do not move.")
	}
	r.say("\n")
	if failed > 0 {
		return fmt.Errorf("%d could not be added", failed)
	}
	return nil
}
