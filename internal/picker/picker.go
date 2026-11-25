// Package picker is the local page where the owner chooses which model answers for each Claude name.
package picker

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"mime"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Lookoff-AIMLAPI/second-life/internal/route"
)

const (
	codeLife    = time.Minute
	idleLimit   = 8 * time.Hour
	maxSessions = 5
	maxCodes    = 20
	maxBody     = 64 << 10
	maxKey      = 512
	cookieName  = "slife_picker"
	prefix      = "/picker/"

	listWait = 30 * time.Second
	keyWait  = 20 * time.Second
	testWait = 90 * time.Second

	policy = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; " +
		"base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
)

// Model is one model of AIMLAPI or of an endpoint. Its text is untrusted.
type Model struct {
	ID, Name, Developer, Description string
	Context, OutputMax               int
	ReleasedAt                       string
}

// Deps is everything the page acts on.
type Deps interface {
	Config() route.Config
	Save(route.Config) error
	AIMLAPIModels(ctx context.Context) ([]Model, error)
	KeyState(ctx context.Context) (present bool, ok bool, detail string)
	SetKey(string) error
	DeleteKey() error
	Test(ctx context.Context, t route.Target) (ms int64, detail string, err error)
	DesktopConfigured() bool
	ApplyDesktop() error

	// EndpointModels lists what an endpoint serves; with an empty key it uses the one stored under the endpoint's id,
	// if there is one. The endpoint need not be saved. Errors must not hold a key.
	EndpointModels(ctx context.Context, e route.Endpoint, key string) ([]Model, error)
	EndpointKey(id string) bool
	SetEndpointKey(id, key string) error
	DeleteEndpointKey(id string) error
}

//go:embed static
var static embed.FS

var pages = map[string]struct{ file, kind string }{
	prefix:              {"static/index.html", "text/html; charset=utf-8"},
	prefix + "app.js":   {"static/app.js", "text/javascript; charset=utf-8"},
	prefix + "app.css":  {"static/app.css", "text/css; charset=utf-8"},
	prefix + "icon.svg": {"static/icon.svg", "image/svg+xml"},
}

// Picker serves the page. Codes and sessions live in memory, as hashes.
type Picker struct {
	d   Deps
	now func() time.Time

	edit sync.Mutex // one change of the config at a time

	mu       sync.Mutex
	codes    map[[32]byte]time.Time // when each one expires
	sessions map[[32]byte]time.Time // when each one was last used

	routes map[string]map[string]http.HandlerFunc
}

// New returns a picker without codes or sessions.
func New(d Deps) *Picker {
	p := &Picker{d: d, now: time.Now, codes: map[[32]byte]time.Time{}, sessions: map[[32]byte]time.Time{}}
	p.routes = map[string]map[string]http.HandlerFunc{
		"session": {http.MethodPost: p.session},
		"state":   {http.MethodGet: p.state},
		"models":  {http.MethodGet: p.models},
		"config":  {http.MethodPut: p.save},
		"key":     {http.MethodGet: p.key, http.MethodPut: p.setKey, http.MethodDelete: p.deleteKey},
		"test":    {http.MethodPost: p.test},
		"desktop": {http.MethodPost: p.desktop},

		"endpoint":        {http.MethodPost: p.addEndpoint, http.MethodPut: p.editEndpoint, http.MethodDelete: p.deleteEndpoint},
		"endpoint-key":    {http.MethodGet: p.keys, http.MethodDelete: p.deleteEndpointKey},
		"endpoint-models": {http.MethodPost: p.endpointModels},
	}
	return p
}

// URL is a link that signs one browser in, once, within a minute.
func (p *Picker) URL(base string) string {
	code := secret()
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	for k, end := range p.codes {
		if !now.Before(end) {
			delete(p.codes, k)
		}
	}
	trim(p.codes, maxCodes-1)
	p.codes[sha256.Sum256([]byte(code))] = now.Add(codeLife)
	return base + prefix + "#" + code
}

// Handler serves everything under /picker/.
func (p *Picker) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", policy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		if name, ok := strings.CutPrefix(r.URL.Path, prefix+"api/"); ok {
			p.api(w, r, name)
			return
		}
		page, ok := pages[r.URL.Path]
		if !ok {
			fail(w, http.StatusNotFound, "no such page")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			h.Set("Allow", "GET, HEAD")
			fail(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		b, err := static.ReadFile(page.file)
		if err != nil {
			fail(w, http.StatusInternalServerError, "the page is missing")
			return
		}
		h.Set("Content-Type", page.kind)
		_, _ = w.Write(b) // net/http drops it for HEAD
	})
}

func (p *Picker) api(w http.ResponseWriter, r *http.Request, name string) {
	if o, sent := r.Header["Origin"]; sent && (len(o) != 1 || o[0] != "http://"+r.Host) {
		fail(w, http.StatusForbidden, "another page may not use the picker")
		return
	}
	if name != "session" {
		if r.Header.Get("X-Second-Life-Picker") != "1" {
			fail(w, http.StatusForbidden, "missing X-Second-Life-Picker header")
			return
		}
		if !p.signedIn(r) {
			fail(w, http.StatusUnauthorized, "open the picker from the app again")
			return
		}
	}
	methods, ok := p.routes[name]
	if !ok {
		fail(w, http.StatusNotFound, "no such call")
		return
	}
	run, ok := methods[r.Method]
	if !ok {
		for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete} {
			if methods[m] != nil {
				w.Header().Add("Allow", m)
			}
		}
		fail(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	run(w, r)
}

func (p *Picker) signedIn(r *http.Request) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	p.expire(now)
	for _, c := range r.CookiesNamed(cookieName) {
		k := sha256.Sum256([]byte(c.Value))
		if _, ok := p.sessions[k]; ok {
			p.sessions[k] = now
			return true
		}
	}
	return false
}

func (p *Picker) expire(now time.Time) { // mu held
	for k, used := range p.sessions {
		if now.Sub(used) >= idleLimit {
			delete(p.sessions, k)
		}
	}
}

// trim drops the oldest entries until at most n are left.
func trim(m map[[32]byte]time.Time, n int) {
	for len(m) > n {
		var oldest [32]byte
		var at time.Time
		first := true
		for k, t := range m {
			if first || t.Before(at) {
				oldest, at, first = k, t, false
			}
		}
		delete(m, oldest)
	}
}

func (p *Picker) session(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Code string `json:"code"`
	}
	if !read(w, r, &in) {
		return
	}
	k := sha256.Sum256([]byte(in.Code))
	p.mu.Lock()
	now := p.now()
	end, ok := p.codes[k]
	delete(p.codes, k)
	ok = ok && now.Before(end)
	var token string
	if ok {
		p.expire(now)
		trim(p.sessions, maxSessions-1) // the one idle for longest makes room
		token = secret()
		p.sessions[sha256.Sum256([]byte(token))] = now
	}
	p.mu.Unlock()
	if !ok {
		fail(w, http.StatusUnauthorized, "this link was used or is too old")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: token, Path: "/picker", HttpOnly: true, SameSite: http.SameSiteStrictMode})
	reply(w, http.StatusOK, map[string]bool{"ok": true})
}

func (p *Picker) state(w http.ResponseWriter, _ *http.Request) {
	reply(w, http.StatusOK, map[string]any{
		"slots": route.Slots, "config": p.d.Config(), "defaults": route.Default(), "codex": route.CodexModels,
		"desktop": p.d.DesktopConfigured(), "presets": presets,
	})
}

type wireModel struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Developer   string `json:"developer,omitempty"`
	Description string `json:"description,omitempty"`
	Context     int    `json:"context,omitempty"`
	OutputMax   int    `json:"outputMax,omitempty"`
	ReleasedAt  string `json:"releasedAt,omitempty"`
}

func (p *Picker) models(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), listWait)
	defer cancel()
	list, err := p.d.AIMLAPIModels(ctx)
	if err != nil {
		fail(w, http.StatusBadGateway, clean(err.Error(), 300))
		return
	}
	out, hidden := offer(list, route.AIMLAPI, nil)
	reply(w, http.StatusOK, map[string]any{"models": out, "hidden": hidden})
}

// offer leaves out every model that Save would refuse.
func offer(list []Model, provider string, endpoints []route.Endpoint) (out []wireModel, hidden int) {
	out = []wireModel{}
	seen := map[string]bool{}
	for _, m := range list {
		if m.Context < 0 || m.Context > 10_000_000 {
			m.Context = 0
		}
		v := wireModel{m.ID, text(m.Name, 100), text(m.Developer, 100), text(m.Description, 300), m.Context, max(m.OutputMax, 0), text(m.ReleasedAt, 40)}
		if v.Name == "" {
			v.Name = text(m.ID, 100)
		}
		id := strings.ToLower(m.ID)
		if seen[id] || valid(route.Target{Provider: provider, Model: m.ID, Name: v.Name, Context: v.Context}, endpoints) != nil {
			hidden++
			continue
		}
		seen[id] = true
		out = append(out, v)
	}
	return out, hidden
}

func (p *Picker) save(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Slots     map[string]route.Target `json:"slots"`
		Extra     []route.Target          `json:"extra"`
		Endpoints *[]route.Endpoint       `json:"endpoints"` // left out: they stay as they are
	}
	if !read(w, r, &in) {
		return
	}
	p.edit.Lock()
	defer p.edit.Unlock()
	old := p.own()
	c := route.Config{Slots: in.Slots, Extra: in.Extra, Endpoints: old.Endpoints}
	if in.Endpoints != nil { // it may drop some; they are added and changed on their own
		c.Endpoints = *in.Endpoints
		for _, e := range c.Endpoints {
			if was, ok := old.Endpoint(e.ID); !ok || was != e {
				fail(w, http.StatusBadRequest, errGone.Error())
				return
			}
		}
	}
	if err := c.Valid(); err != nil {
		for _, t := range append(slices.Collect(maps.Values(c.Slots)), c.Extra...) {
			if _, ok := c.Endpoint(t.Provider); !ok && strings.HasPrefix(t.Provider, "ep-") {
				err = errGone
			}
		}
		fail(w, http.StatusBadRequest, clean(err.Error(), 300))
		return
	}
	if err := p.commit(old, c); err != nil {
		fail(w, http.StatusInternalServerError, clean(err.Error(), 300))
		return
	}
	p.saved(w, nil)
}

func (p *Picker) key(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), keyWait)
	defer cancel()
	present, ok, detail := p.d.KeyState(ctx)
	reply(w, http.StatusOK, map[string]any{"present": present, "ok": present && ok, "detail": clean(detail, 300)})
}

func (p *Picker) setKey(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Key string `json:"key"`
	}
	if !read(w, r, &in) {
		return
	}
	k := strings.TrimSpace(in.Key)
	if k == "" || len(k) > maxKey || strings.ContainsFunc(k, func(c rune) bool { return c <= ' ' || c > '~' }) {
		fail(w, http.StatusBadRequest, "that does not look like a key")
		return
	}
	if err := p.d.SetKey(k); err != nil {
		fail(w, http.StatusInternalServerError, clean(strings.ReplaceAll(err.Error(), k, "[hidden]"), 300))
		return
	}
	p.key(w, r)
}

func (p *Picker) deleteKey(w http.ResponseWriter, r *http.Request) {
	if err := p.d.DeleteKey(); err != nil {
		fail(w, http.StatusInternalServerError, clean(err.Error(), 300))
		return
	}
	p.key(w, r)
}

func (p *Picker) test(w http.ResponseWriter, r *http.Request) {
	var t route.Target
	if !read(w, r, &t) {
		return
	}
	if err := valid(t, p.d.Config().Endpoints); err != nil {
		fail(w, http.StatusBadRequest, clean(err.Error(), 300))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), testWait)
	defer cancel()
	ms, detail, err := p.d.Test(ctx, t)
	if err != nil {
		reply(w, http.StatusOK, map[string]any{"ok": false, "detail": clean(err.Error(), 300)})
		return
	}
	reply(w, http.StatusOK, map[string]any{"ok": true, "ms": ms, "detail": clean(detail, 300)})
}

func (p *Picker) desktop(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Confirm bool `json:"confirm"`
	}
	if !read(w, r, &in) {
		return
	}
	if !in.Confirm {
		fail(w, http.StatusBadRequest, "not confirmed")
		return
	}
	if err := p.d.ApplyDesktop(); err != nil {
		fail(w, http.StatusInternalServerError, clean(err.Error(), 300))
		return
	}
	reply(w, http.StatusOK, map[string]bool{"ok": true})
}

// valid asks route whether a config with this target would be saved.
func valid(t route.Target, endpoints []route.Endpoint) error {
	c := route.Default()
	c.Endpoints = endpoints
	c.Extra = []route.Target{t}
	return c.Valid()
}

// read never repeats the body in its errors: it may hold a key.
func read(w http.ResponseWriter, r *http.Request, v any) bool {
	if kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || kind != "application/json" {
		fail(w, http.StatusUnsupportedMediaType, "send JSON")
		return false
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	d.DisallowUnknownFields()
	err := d.Decode(v)
	if err == nil {
		if _, err = d.Token(); err == io.EOF {
			return true
		}
	}
	var big *http.MaxBytesError
	if errors.As(err, &big) {
		fail(w, http.StatusRequestEntityTooLarge, "the request is over 64 KB")
	} else {
		fail(w, http.StatusBadRequest, "the request is not valid JSON")
	}
	return false
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, status int, msg string) {
	reply(w, status, map[string]string{"error": msg})
}

func secret() string {
	var b [32]byte
	_, _ = rand.Read(b[:]) // never fails
	return hex.EncodeToString(b[:])
}

// keyLike catches a key that an upstream error repeats.
var keyLike = regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{8,}|[A-Za-z0-9]{24,}`)

// clean hides before it cuts, so that no part of a key is left at the end.
func clean(s string, n int) string {
	return text(keyLike.ReplaceAllString(text(s, maxBody), "[hidden]"), n)
}

// text drops control and direction characters and keeps at most n bytes.
func text(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return ' '
		case r == utf8.RuneError || unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r):
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	for len(s) > n {
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
	}
	return s
}
