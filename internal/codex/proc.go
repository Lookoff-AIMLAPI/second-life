package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

type rpcError struct {
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return e.Message }

type reply struct {
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

// proc is one app-server with everything it started.
type proc struct {
	cmd      *exec.Cmd
	in       io.WriteCloser
	tree     tree
	out      chan []byte
	checking atomic.Bool
	stopOnce sync.Once
	done     chan struct{} // the child is gone and reaped

	mu   sync.Mutex
	next int64
	wait map[int64]chan reply
}

type startup struct {
	done    chan struct{}
	cancel  context.CancelFunc
	dropped bool // Close came meanwhile; guarded by Client.mu
	proc    *proc
	err     error
}

// get returns the running child or starts one. Callers share the start, and one caller giving up does not end it.
func (c *Client) get(ctx context.Context) (*proc, error) {
	c.mu.Lock()
	if err := ctx.Err(); err != nil {
		c.mu.Unlock()
		return nil, err
	}
	if c.proc != nil {
		select {
		case <-c.proc.done:
			c.proc = nil
		default:
			p := c.proc
			c.mu.Unlock()
			return p, nil
		}
	}
	s := c.starting
	if s == nil {
		sctx, cancel := context.WithTimeout(context.Background(), c.limits.or().start)
		s = &startup{done: make(chan struct{}), cancel: cancel}
		c.starting = s
		go c.run(sctx, s)
	}
	c.mu.Unlock()
	select {
	case <-s.done:
		return s.proc, s.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *Client) run(ctx context.Context, s *startup) {
	defer s.cancel()
	p, err := c.spawn(ctx)
	c.mu.Lock()
	dropped := s.dropped
	if c.starting == s {
		c.starting = nil
	}
	if err == nil && !dropped {
		c.proc = p
	}
	c.mu.Unlock()
	if dropped && err == nil {
		p.stop()
		p, err = nil, context.Canceled
	}
	s.proc, s.err = p, err
	close(s.done)
}

func (c *Client) spawn(ctx context.Context) (*proc, error) {
	bin := c.Bin
	if bin == "" {
		b, err := FindTool("codex")
		if err != nil {
			return nil, ErrMissing
		}
		bin = b
	}
	cmd, err := Command(bin, "app-server")
	if err != nil {
		return nil, err
	}
	cmd.Env = ToolEnv(bin)
	cmd.Dir = filepath.Dir(bin)
	if c.Dir != "" {
		// A shim run by cmd.exe searches the working directory first, so it must not be a shared or temporary one.
		if err := os.MkdirAll(c.Dir, 0o700); err != nil {
			return nil, err
		}
		cmd.Dir = c.Dir
	}
	p := &proc{cmd: cmd, out: make(chan []byte), wait: map[int64]chan reply{}, done: make(chan struct{})}
	p.tree.prepare(cmd)
	if p.in, err = cmd.StdinPipe(); err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start codex app-server: %w", err)
	}
	if err := p.tree.adopt(cmd); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("start codex app-server: %w", err)
	}
	go p.read(out)
	go p.send()
	go func() {
		// Wait closes both pipes, so it does not matter whether a grandchild still holds them.
		_ = cmd.Wait()
		p.tree.kill() // whatever the child left behind
		close(p.done)
	}()
	err = p.call(ctx, "initialize", map[string]any{"clientInfo": map[string]any{"name": "second_life", "title": "Second Life", "version": c.Version}, "capabilities": nil}, nil)
	if err == nil {
		err = p.write(ctx, map[string]any{"method": "initialized"})
	}
	if err != nil {
		p.stop()
		return nil, err
	}
	return p, nil
}

// Close ends the child, including one that is still starting.
func (c *Client) Close() {
	c.credMu.Lock()
	if c.flight != nil {
		c.flight.cancel()
	}
	c.credMu.Unlock()
	c.mu.Lock()
	p, s := c.proc, c.starting
	c.proc, c.starting = nil, nil
	if s != nil {
		s.dropped = true
		s.cancel()
	}
	c.mu.Unlock()
	if s != nil {
		<-s.done
	}
	if p != nil {
		p.stop()
	}
}

// stop closes stdin, which ends app-server cleanly, then kills whatever stays.
func (p *proc) stop() {
	p.stopOnce.Do(func() {
		_ = p.in.Close()
		select {
		case <-p.done:
			return
		case <-time.After(stopGrace):
		}
		p.tree.kill()
		_ = p.cmd.Process.Kill()
	})
	select {
	case <-p.done:
	case <-time.After(stopWait):
	}
}

// send is the only writer; a child that stops reading blocks it and nobody else.
func (p *proc) send() {
	for {
		select {
		case b := <-p.out:
			if _, err := p.in.Write(b); err != nil {
				return
			}
		case <-p.done:
			return
		}
	}
}

func (p *proc) write(ctx context.Context, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	select {
	case p.out <- append(b, '\n'):
		return nil
	case <-p.done:
		return errors.New("it exited")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *proc) call(ctx context.Context, method string, params, out any) error {
	ch := make(chan reply, 1)
	p.mu.Lock()
	p.next++
	id := p.next
	p.wait[id] = ch
	p.mu.Unlock()
	defer func() { p.mu.Lock(); delete(p.wait, id); p.mu.Unlock() }()
	if err := p.write(ctx, map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return fmt.Errorf("codex app-server %s: %w", method, err)
	}
	select {
	case r := <-ch:
		if r.Error != nil {
			return fmt.Errorf("codex app-server %s: %w", method, r.Error)
		}
		if out == nil {
			return nil
		}
		return json.Unmarshal(r.Result, out)
	case <-p.done:
		return fmt.Errorf("codex app-server exited during %s", method)
	case <-ctx.Done():
		return fmt.Errorf("codex app-server %s: %w", method, ctx.Err())
	}
}

func (p *proc) read(r io.Reader) {
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadBytes('\n')
		var m struct {
			ID     *json.RawMessage `json:"id"`
			Method string           `json:"method"`
			reply
		}
		if json.Unmarshal(line, &m) == nil && m.ID != nil {
			p.handle(m.ID, m.Method, m.reply)
		}
		if err != nil {
			// Nothing can be answered any more, even if the child itself is still there.
			p.tree.kill()
			_ = p.cmd.Process.Kill()
			return
		}
	}
}

func (p *proc) handle(rawID *json.RawMessage, method string, r reply) {
	if method != "" { // a request from the server: this client offers nothing
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), stopWait)
			defer cancel()
			_ = p.write(ctx, map[string]any{"id": rawID, "error": map[string]any{"code": -32601, "message": "not supported"}})
		}()
		return
	}
	var id int64
	if json.Unmarshal(*rawID, &id) != nil {
		return
	}
	p.mu.Lock()
	ch := p.wait[id]
	p.mu.Unlock()
	if ch != nil {
		select {
		case ch <- r:
		default:
		}
	}
}
