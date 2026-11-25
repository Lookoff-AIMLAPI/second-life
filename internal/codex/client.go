package codex

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Account is the signed-in ChatGPT account.
type Account struct{ Email, Plan string }

// Window is one usage window.
type Window struct {
	UsedPercent        int   `json:"usedPercent"`
	WindowDurationMins int64 `json:"windowDurationMins"`
	ResetsAt           int64 `json:"resetsAt"`
}

// Credentials authorise one upstream call. Never log them.
type Credentials struct{ Token, AccountID string }

// ErrSignedOut means the CLI has no ChatGPT login.
var ErrSignedOut = errors.New("not signed in to Codex with ChatGPT")

const (
	freshFor  = 5 * time.Minute  // a token closer to its end is replaced
	cacheFor  = 15 * time.Minute // short, so a login changed outside the bridge is noticed
	forceGap  = 30 * time.Second // at most one forced refresh in this time
	stopGrace = time.Second      // for a clean exit; the tray gives a whole stop 3 s
	stopWait  = time.Second      // for the kill to be noticed
)

// timeouts can be shortened by tests; zero means the default.
type timeouts struct{ start, call, refresh, probe time.Duration }

func (t timeouts) or() timeouts {
	set := func(d *time.Duration, def time.Duration) {
		if *d == 0 {
			*d = def
		}
	}
	set(&t.start, 30*time.Second)
	set(&t.call, 30*time.Second)
	set(&t.refresh, time.Minute)
	set(&t.probe, 10*time.Second)
	return t
}

// Client runs one app-server child, started on demand and replaced if it dies or hangs.
// Once a caller's context is cancelled it can start nothing, so cancelling every caller and then calling Close leaves
// no child behind.
type Client struct {
	Bin     string // empty: look it up
	Version string
	Dir     string // private working directory of the child; empty: the CLI's own directory

	limits timeouts

	mu       sync.Mutex
	proc     *proc
	starting *startup

	credMu  sync.Mutex
	cred    Credentials
	credExp time.Time
	credGen int       // a login started meanwhile makes a running fetch stale
	forced  time.Time // the last forced refresh
	flight  *fetching
}

type fetching struct {
	done   chan struct{}
	cancel context.CancelFunc
	cred   Credentials
	err    error
}

// call runs one method. A child that no longer answers anything is replaced.
func (c *Client) call(ctx context.Context, limit time.Duration, method string, params, out any) error {
	p, err := c.get(ctx)
	if err != nil {
		return err
	}
	lctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	err = p.call(lctx, method, params, out)
	if lctx.Err() != nil && ctx.Err() == nil {
		go c.check(p)
	}
	return err
}

// check tells a slow call from a hung child. A slow token refresh must not be killed half way.
func (c *Client) check(p *proc) {
	if !p.checking.CompareAndSwap(false, true) {
		return
	}
	defer p.checking.Store(false)
	ctx, cancel := context.WithTimeout(context.Background(), c.limits.or().probe)
	defer cancel()
	err := p.call(ctx, "account/read", map[string]any{"refreshToken": false}, nil)
	if err == nil || errors.As(err, new(*rpcError)) {
		return
	}
	c.mu.Lock()
	if c.proc == p {
		c.proc = nil
	}
	c.mu.Unlock()
	p.stop()
}

// Account returns nil when signed out or signed in without ChatGPT.
func (c *Client) Account(ctx context.Context) (*Account, error) {
	var r struct {
		Account *struct {
			Type     string `json:"type"`
			Email    string `json:"email"`
			PlanType string `json:"planType"`
		} `json:"account"`
	}
	if err := c.call(ctx, c.limits.or().call, "account/read", map[string]any{"refreshToken": false}, &r); err != nil {
		return nil, err
	}
	if r.Account == nil || r.Account.Type != "chatgpt" {
		return nil, nil
	}
	return &Account{Email: r.Account.Email, Plan: r.Account.PlanType}, nil
}

// Limits returns the primary and secondary windows; either may be nil.
func (c *Client) Limits(ctx context.Context) (primary, secondary *Window, err error) {
	var r struct {
		RateLimits struct {
			Primary   *Window `json:"primary"`
			Secondary *Window `json:"secondary"`
		} `json:"rateLimits"`
	}
	if err := c.call(ctx, c.limits.or().call, "account/rateLimits/read", nil, &r); err != nil {
		return nil, nil, err
	}
	return r.RateLimits.Primary, r.RateLimits.Secondary, nil
}

func httpsURL(s string) (string, error) {
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return "", errors.New("codex returned an unexpected sign-in address")
	}
	return s, nil
}

// Login starts the browser sign-in; app-server finishes it in the background.
func (c *Client) Login(ctx context.Context) (string, error) {
	var r struct {
		AuthURL string `json:"authUrl"`
	}
	if err := c.call(ctx, c.limits.or().call, "account/login/start", map[string]any{"type": "chatgpt"}, &r); err != nil {
		return "", err
	}
	c.forget()
	return httpsURL(r.AuthURL)
}

// LoginDevice starts the device-code sign-in for machines with no browser.
func (c *Client) LoginDevice(ctx context.Context) (verifyURL, code string, err error) {
	var r struct {
		VerificationURL string `json:"verificationUrl"`
		UserCode        string `json:"userCode"`
	}
	if err := c.call(ctx, c.limits.or().call, "account/login/start", map[string]any{"type": "chatgptDeviceCode"}, &r); err != nil {
		return "", "", err
	}
	c.forget()
	verifyURL, err = httpsURL(r.VerificationURL)
	return verifyURL, r.UserCode, err
}

func (c *Client) forget() {
	c.credMu.Lock()
	c.cred, c.credExp, c.forced = Credentials{}, time.Time{}, time.Time{}
	c.credGen++
	c.credMu.Unlock()
}

// Credentials returns a usable token. rejected is the token upstream answered with 401, or "". Callers share one
// fetch, and a token that has already been replaced is not refreshed again.
func (c *Client) Credentials(ctx context.Context, rejected string) (Credentials, error) {
	for attempt := 0; ; attempt++ {
		c.credMu.Lock()
		if err := ctx.Err(); err != nil {
			c.credMu.Unlock()
			return Credentials{}, err
		}
		usable := c.cred.Token != "" && time.Until(c.credExp) > freshFor
		// The same token right after a forced refresh: the 401 is not about its age, so upstream may say so again.
		if usable && (c.cred.Token != rejected || time.Since(c.forced) < forceGap) {
			cred := c.cred
			c.credMu.Unlock()
			return cred, nil
		}
		f := c.flight
		if f == nil {
			fctx, cancel := context.WithCancel(context.Background())
			f = &fetching{done: make(chan struct{}), cancel: cancel}
			c.flight = f
			go c.fly(fctx, f, rejected, c.credGen)
		}
		c.credMu.Unlock()
		select {
		case <-f.done:
		case <-ctx.Done():
			return Credentials{}, ctx.Err()
		}
		// Someone else's fetch may not have known that this token was rejected.
		if f.err == nil && rejected != "" && f.cred.Token == rejected && attempt == 0 {
			continue
		}
		return f.cred, f.err
	}
}

func (c *Client) fly(ctx context.Context, f *fetching, rejected string, gen int) {
	defer f.cancel()
	cred, exp, err := c.fetch(ctx, false)
	forced := err == nil && (cred.Token == rejected || time.Until(exp) < freshFor)
	if forced {
		cred, exp, err = c.fetch(ctx, true)
	}
	if limit := time.Now().Add(cacheFor); exp.After(limit) {
		exp = limit
	}
	c.credMu.Lock()
	f.cred, f.err = cred, err
	if c.flight == f {
		c.flight = nil
	}
	if gen == c.credGen {
		if forced {
			c.forced = time.Now()
		}
		if err == nil {
			c.cred, c.credExp = cred, exp
		}
	}
	c.credMu.Unlock()
	close(f.done)
}

func (c *Client) fetch(ctx context.Context, refresh bool) (Credentials, time.Time, error) {
	var r struct {
		AuthMethod string `json:"authMethod"`
		AuthToken  string `json:"authToken"`
	}
	limit := c.limits.or().call
	if refresh {
		limit = c.limits.or().refresh
	}
	if err := c.call(ctx, limit, "getAuthStatus", map[string]any{"includeToken": true, "refreshToken": refresh}, &r); err != nil {
		return Credentials{}, time.Time{}, err
	}
	if r.AuthToken == "" || !strings.HasPrefix(r.AuthMethod, "chatgpt") {
		return Credentials{}, time.Time{}, ErrSignedOut
	}
	acct, exp, err := claims(r.AuthToken)
	if err != nil {
		return Credentials{}, time.Time{}, err
	}
	return Credentials{Token: r.AuthToken, AccountID: acct}, exp, nil
}

// claims reads the account id and expiry. The signature is the upstream's to check.
func claims(tok string) (account string, exp time.Time, err error) {
	bad := errors.New("codex returned a token in an unexpected format")
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return "", exp, bad
	}
	raw, e := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if e != nil {
		return "", exp, bad
	}
	var cl struct {
		Exp  int64 `json:"exp"`
		Auth struct {
			AccountID string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
	}
	if json.Unmarshal(raw, &cl) != nil || cl.Auth.AccountID == "" || cl.Exp == 0 {
		return "", exp, bad
	}
	return cl.Auth.AccountID, time.Unix(cl.Exp, 0), nil
}
