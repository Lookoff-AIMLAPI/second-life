// Package bridge serves the Anthropic Messages API on this machine and answers from Codex models.
package bridge

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Lookoff-AIMLAPI/second-life/internal/aimlapi"
	"github.com/Lookoff-AIMLAPI/second-life/internal/codex"
	"github.com/Lookoff-AIMLAPI/second-life/internal/keystore"
	"github.com/Lookoff-AIMLAPI/second-life/internal/picker"
	"github.com/Lookoff-AIMLAPI/second-life/internal/route"
)

// Auth is what the bridge needs from the Codex CLI.
type Auth interface {
	Account(context.Context) (*codex.Account, error)
	Limits(context.Context) (primary, secondary *codex.Window, err error)
	// rejected is the token upstream answered with 401, or "".
	Credentials(ctx context.Context, rejected string) (codex.Credentials, error)
	Login(context.Context) (string, error)
	Close()
}

// Options are all optional.
type Options struct {
	Addr     string
	Home     string
	Token    string // fixed local bearer; empty: the one in Home
	Upstream string // test override
	Auth     Auth
	Log      *log.Logger                // empty: the log file in Home
	Keys     Keys                       // empty: the system key store
	AIMLAPI  string                     // test override of its address
	KeysFor  func(endpoint string) Keys // empty: the system key store
	Echo     io.Writer                  // gets a copy of the log file; unused with Log
}

// Bridge is safe for concurrent use.
type Bridge struct {
	addr, upstream, token string
	auth                  Auth
	log                   *log.Logger
	logFile               io.Closer
	client                *http.Client

	guard contextGuard

	kick   chan struct{} // look at the login and the key again now
	home   string
	routes atomic.Pointer[route.Config]
	aiml   *aimlapi.Client
	keys   *cachedKeys
	picker *picker.Picker

	learned sync.Map // learnKey -> translate.Options
	newKeys func(id string) Keys
	epMu    sync.Mutex
	epKeys  map[string]*cachedKeys

	deskConfigured func() bool
	deskApply      func() error

	ops sync.Mutex // serialises Start and Stop

	mu     sync.Mutex
	st     Status
	srv    *http.Server
	run    context.Context // of the current Start; nil when off
	cancel context.CancelFunc
	tasks  sync.WaitGroup // what the current Start runs
}

// New prepares a bridge; nothing listens until Start.
func New(o Options) (*Bridge, error) {
	home := o.Home
	if home == "" {
		h, err := Home()
		if err != nil {
			return nil, err
		}
		home = h
	}
	if err := Adopt(home); err != nil {
		return nil, err
	}
	tok := o.Token
	if tok == "" {
		t, err := LocalToken(home)
		if err != nil {
			return nil, err
		}
		tok = t
	} else if len(tok) < minToken {
		return nil, errors.New("the local token is too short")
	}
	b := &Bridge{addr: o.Addr, upstream: o.Upstream, token: tok, auth: o.Auth, log: o.Log}
	if b.addr == "" {
		b.addr = Addr()
	}
	if b.upstream == "" {
		b.upstream = defaultUpstream
	}
	if b.auth == nil {
		b.auth = &codex.Client{Version: Version, Dir: home}
	}
	if b.log == nil {
		b.log = openLog(home)
		b.logFile, _ = b.log.Writer().(io.Closer)
		if o.Echo != nil {
			b.log.SetOutput(io.MultiWriter(o.Echo, b.log.Writer()))
		}
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = 15 * time.Minute
	b.guard.cap = maxContext()
	b.client = &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	b.st = Status{State: StateOff, Version: Version, Addr: b.addr}
	b.home, b.kick = home, make(chan struct{}, 1)
	routes, err := route.Load(home)
	if err != nil {
		return nil, err
	}
	b.routes.Store(&routes)
	keys := o.Keys
	if keys == nil {
		keys = systemKeys{}
	}
	b.keys = &cachedKeys{Keys: keys}
	b.newKeys = o.KeysFor
	if b.newKeys == nil {
		b.newKeys = func(id string) Keys { return keystore.For(id) }
	}
	b.aiml = &aimlapi.Client{BaseURL: o.AIMLAPI, Key: b.keys.Get, Version: Version}
	if b.aiml.BaseURL == "" {
		b.aiml.BaseURL = aimlapi.DefaultBaseURL
	}
	b.picker = picker.New(pickerDeps{b})
	return b, nil
}

// Token is the local bearer.
func (b *Bridge) Token() string { return b.token }

// Mapping lists Claude family and Codex model.
func (b *Bridge) Mapping() [][2]string {
	c := b.Config()
	var out [][2]string
	for _, s := range route.Slots {
		out = append(out, [2]string{s, c.Slots[s].Label()})
	}
	return out
}

// Status returns a snapshot.
func (b *Bridge) Status() Status {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.st
}

func (b *Bridge) update(f func(*Status)) {
	b.mu.Lock()
	f(&b.st)
	b.mu.Unlock()
}

// Start listens and begins watching the login. Calling it twice is harmless.
func (b *Bridge) Start() error {
	b.ops.Lock()
	defer b.ops.Unlock()
	if b.srv != nil {
		return nil
	}
	b.update(func(s *Status) { s.State, s.LastError = StateStarting, "" })
	ln, err := net.Listen("tcp", b.addr)
	if err != nil {
		b.update(func(s *Status) { s.State, s.LastError = StateError, err.Error() })
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	srv := &http.Server{
		Handler: b.handler(), ErrorLog: b.log, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute,
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	b.mu.Lock()
	b.srv, b.run, b.cancel = srv, ctx, cancel
	b.st.Addr = ln.Addr().String()
	b.mu.Unlock()
	var watching sync.WaitGroup
	watching.Go(func() { b.watch(ctx) })
	b.tasks.Go(func() {
		err := srv.Serve(ln)
		cancel()
		watching.Wait() // or it would report the bridge as on again
		if !errors.Is(err, http.ErrServerClosed) {
			b.update(func(s *Status) { s.State, s.LastError = StateError, err.Error() })
		}
	})
	return nil
}

// Stop ends in-flight requests and closes the port.
func (b *Bridge) Stop() {
	b.ops.Lock()
	defer b.ops.Unlock()
	if b.logFile != nil {
		defer b.logFile.Close()
	}
	b.mu.Lock()
	srv, cancel := b.srv, b.cancel
	b.srv, b.run, b.cancel = nil, nil, nil
	b.mu.Unlock()
	if srv == nil {
		b.auth.Close()
		return
	}
	cancel() // first: from here on nothing can start the app-server again
	_ = srv.Close()
	b.auth.Close()
	b.tasks.Wait()
	b.update(func(s *Status) { s.State, s.Primary, s.Secondary = StateOff, nil, nil })
}

// Login returns the address to open in a browser. The bridge must be on: the sign-in ends in the app-server.
func (b *Bridge) Login() (string, error) {
	b.mu.Lock()
	run := b.run
	b.mu.Unlock()
	if run == nil {
		return "", errors.New("turn the bridge on first")
	}
	ctx, cancel := context.WithTimeout(run, 45*time.Second)
	defer cancel()
	return b.auth.Login(ctx)
}

// watch polls the login quickly while signed out and slowly once signed in.
func (b *Bridge) watch(ctx context.Context) {
	for {
		wait := b.refresh(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		case <-b.kick:
		}
	}
}

func (b *Bridge) refresh(ctx context.Context) time.Duration {
	acct, err := b.auth.Account(ctx)
	var p, s *codex.Window
	if err == nil && acct != nil {
		p, s, _ = b.auth.Limits(ctx)
	}
	if ctx.Err() != nil {
		return 0
	}
	wait := 3 * time.Second
	missing := errors.Is(err, codex.ErrMissing)
	aiml := (missing || err == nil && acct == nil) && b.aimlReady()
	b.update(func(st *Status) {
		st.CodexMissing, st.AIMLOnly = missing, aiml
		if missing {
			wait = 10 * time.Second
		}
		switch {
		case aiml:
			st.State, st.SignedIn, st.Email, st.Plan = StateOn, false, "", ""
			st.Primary, st.Secondary = nil, nil
		case missing:
			st.State, st.SignedIn = StateCodexMissing, false
		case err != nil:
			st.State, st.LastError = StateError, err.Error()
			wait = 10 * time.Second
		case acct == nil:
			st.State, st.SignedIn, st.Email, st.Plan = StateNeedsLogin, false, "", ""
		default:
			if st.State == StateError {
				st.LastError = ""
			}
			st.State, st.SignedIn, st.Email, st.Plan = StateOn, true, acct.Email, acct.Plan
			st.Primary, st.Secondary = limit(p, st.Primary), limit(s, st.Secondary)
			wait = time.Minute
		}
	})
	return wait
}

func limit(w *codex.Window, old *Limit) *Limit {
	if w == nil {
		return old
	}
	return &Limit{UsedPercent: w.UsedPercent, WindowMins: w.WindowDurationMins, ResetsAt: w.ResetsAt}
}
