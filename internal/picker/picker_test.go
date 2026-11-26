package picker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Lookoff-AIMLAPI/second-life/internal/route"
)

const testKey = "0123456789abcdef0123456789abcdef"

type fake struct {
	mu      sync.Mutex
	cfg     route.Config
	models  []Model
	key     string
	fault   error
	saves   int
	applied int
	tested  route.Target

	epKeys   map[string]string
	epModels []Model
	epFault  error // of a list
	keyFault error // of an endpoint key
	asked    []ask

	store PluginStore // nil: no plugin store
}

// ask is one list call with the key that would be sent.
type ask struct {
	e   route.Endpoint
	key string
}

func (f *fake) Config() route.Config { f.mu.Lock(); defer f.mu.Unlock(); return f.cfg }
func (f *fake) Save(c route.Config) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fault != nil {
		return f.fault
	}
	f.cfg = c
	f.saves++
	return nil
}
func (f *fake) AIMLAPIModels(context.Context) ([]Model, error) { return f.models, f.fault }
func (f *fake) KeyState(context.Context) (bool, bool, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fault != nil {
		return true, false, f.fault.Error()
	}
	return f.key != "", f.key != "", ""
}
func (f *fake) SetKey(k string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.key = k
	if f.fault != nil {
		return f.fault
	}
	return nil
}
func (f *fake) DeleteKey() error { f.mu.Lock(); defer f.mu.Unlock(); f.key = ""; return f.fault }
func (f *fake) Test(_ context.Context, t route.Target) (int64, string, error) {
	f.tested = t
	return 12, "ok", f.fault
}
func (f *fake) Plugins() PluginStore    { return f.store }
func (f *fake) DesktopConfigured() bool { return true }
func (f *fake) ApplyDesktop() error     { f.applied++; return f.fault }
func (f *fake) EndpointModels(_ context.Context, e route.Endpoint, key string) ([]Model, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if key == "" {
		key = f.epKeys[e.ID]
	}
	f.asked = append(f.asked, ask{e, key})
	return f.epModels, f.epFault
}
func (f *fake) EndpointKey(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.epKeys[id] != ""
}
func (f *fake) SetEndpointKey(id, k string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.keyFault != nil {
		return f.keyFault
	}
	if f.epKeys == nil {
		f.epKeys = map[string]string{}
	}
	f.epKeys[id] = k
	return nil
}
func (f *fake) DeleteEndpointKey(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.keyFault != nil {
		return f.keyFault
	}
	delete(f.epKeys, id)
	return nil
}

const host = "127.0.0.1:8787"

type client struct {
	t      *testing.T
	p      *Picker
	cookie *http.Cookie
}

type edit func(*http.Request)

func header(k, v string) edit { return func(r *http.Request) { r.Header.Set(k, v) } }
func drop(k string) edit      { return func(r *http.Request) { r.Header.Del(k) } }

func (c client) do(method, path, body string, edits ...edit) *httptest.ResponseRecorder {
	c.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, "http://"+host+path, rd)
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("X-Second-Life-Picker", "1")
	if c.cookie != nil {
		r.AddCookie(c.cookie)
	}
	for _, e := range edits {
		e(r)
	}
	w := httptest.NewRecorder()
	c.p.Handler().ServeHTTP(w, r)
	for k := range w.Header() {
		if strings.HasPrefix(k, "Access-Control-") {
			c.t.Errorf("%s %s: CORS header %s", method, path, k)
		}
	}
	if strings.Contains(path, "/api/") && w.Header().Get("Content-Type") != "application/json" {
		c.t.Errorf("%s %s: type %q", method, path, w.Header().Get("Content-Type"))
	}
	if strings.Contains(w.Body.String(), testKey) {
		c.t.Errorf("%s %s: the key is in the answer: %s", method, path, w.Body)
	}
	return w
}

func code(t *testing.T, p *Picker) string {
	t.Helper()
	c, ok := strings.CutPrefix(p.URL("http://"+host), "http://"+host+"/picker/#")
	if !ok || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(c) {
		t.Fatalf("code %q", c)
	}
	return c
}

func (c client) trade(code string) *httptest.ResponseRecorder {
	return client{t: c.t, p: c.p}.do("POST", "/picker/api/session", `{"code":"`+code+`"}`)
}

func signIn(t *testing.T, p *Picker) client {
	t.Helper()
	c := client{t: t, p: p}
	w := c.trade(code(t, p))
	cs := w.Result().Cookies()
	if w.Code != 200 || len(cs) != 1 {
		t.Fatal(w.Code, w.Body)
	}
	c.cookie = cs[0]
	return c
}

func setup(t *testing.T) (*fake, *Picker, client) {
	f := &fake{cfg: route.Default()}
	p := New(f)
	return f, p, signIn(t, p)
}

func TestCookie(t *testing.T) {
	_, _, c := setup(t)
	k := c.cookie
	if k.Name != "slife_picker" || len(k.Value) != 64 || !k.HttpOnly || k.SameSite != http.SameSiteStrictMode || k.Path != "/picker" || k.Domain != "" {
		t.Fatalf("%+v", k)
	}
}

func TestEveryCallIsGuarded(t *testing.T) {
	f, p, c := setup(t)
	for name, methods := range p.routes {
		if name == "session" {
			continue
		}
		for m := range methods {
			path := "/picker/api/" + name
			for what, x := range map[string]struct {
				c    client
				e    edit
				want int
			}{
				"no cookie":      {client{t: t, p: p}, header("X", "1"), 401},
				"wrong cookie":   {client{t: t, p: p, cookie: &http.Cookie{Name: cookieName, Value: strings.Repeat("0", 64)}}, header("X", "1"), 401},
				"no header":      {c, drop("X-Second-Life-Picker"), 403},
				"wrong header":   {c, header("X-Second-Life-Picker", "0"), 403},
				"foreign origin": {c, header("Origin", "http://evil.example"), 403},
				"other port":     {c, header("Origin", "http://127.0.0.1:9999"), 403},
				"https":          {c, header("Origin", "https://"+host), 403},
				"null origin":    {c, header("Origin", "null"), 403},
				"empty origin":   {c, header("Origin", ""), 403},
			} {
				if w := x.c.do(m, path, `{}`, x.e); w.Code != x.want {
					t.Errorf("%s %s, %s: %d, want %d", m, name, what, w.Code, x.want)
				}
			}
		}
	}
	if f.saves+f.applied+len(f.asked)+len(f.epKeys) != 0 || f.key != "" || f.tested.Model != "" {
		t.Fatal("a refused call did something")
	}
	if w := c.do("GET", "/picker/api/state", "", header("Origin", "http://"+host)); w.Code != 200 {
		t.Fatal("own origin:", w.Code)
	}
	if w := client.do(client{t: t, p: p}, "POST", "/picker/api/session", `{"code":"`+code(t, p)+`"}`, header("Origin", "http://evil.example")); w.Code != 403 {
		t.Fatal("session from a foreign origin:", w.Code)
	}
}

func TestCodes(t *testing.T) {
	f := &fake{cfg: route.Default()}
	p := New(f)
	now := time.Now()
	p.now = func() time.Time { return now }
	c := client{t: t, p: p}

	one := code(t, p)
	if w := c.trade(one); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := c.trade(one); w.Code != 401 || len(w.Result().Cookies()) != 0 {
		t.Fatal("reused code:", w.Code)
	}
	old := code(t, p)
	now = now.Add(time.Minute)
	if w := c.trade(old); w.Code != 401 || len(w.Result().Cookies()) != 0 {
		t.Fatal("expired code:", w.Code)
	}
	fresh := code(t, p)
	now = now.Add(59 * time.Second)
	if w := c.trade(fresh); w.Code != 200 {
		t.Fatal("code after 59 s:", w.Code)
	}
	for _, bad := range []string{"", strings.Repeat("0", 64)} {
		if w := c.trade(bad); w.Code != 401 {
			t.Fatal("made-up code:", w.Code)
		}
	}
	for range 100 {
		code(t, p)
	}
	if len(p.codes) > maxCodes {
		t.Fatal(len(p.codes), "codes kept")
	}
}

func TestSingleUseUnderRace(t *testing.T) {
	p := New(&fake{cfg: route.Default()})
	k := code(t, p)
	var wg sync.WaitGroup
	var mu sync.Mutex
	won := 0
	for range 20 {
		wg.Go(func() {
			if (client{t: t, p: p}).trade(k).Code == 200 {
				mu.Lock()
				won++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if won != 1 {
		t.Fatal(won, "sessions from one code")
	}
}

func TestSessionsIdleAndCap(t *testing.T) {
	p := New(&fake{cfg: route.Default()})
	now := time.Now()
	p.now = func() time.Time { return now }
	get := func(c client) int { return c.do("GET", "/picker/api/state", "").Code }

	c := signIn(t, p)
	now = now.Add(8*time.Hour - time.Second)
	if get(c) != 200 {
		t.Fatal("ended before 8 h")
	}
	now = now.Add(8*time.Hour - time.Second)
	if get(c) != 200 {
		t.Fatal("use does not keep it")
	}
	now = now.Add(8 * time.Hour)
	if get(c) != 401 {
		t.Fatal("alive after 8 h idle")
	}

	var all []client
	for range maxSessions {
		now = now.Add(time.Second)
		all = append(all, signIn(t, p))
	}
	now = now.Add(time.Second)
	get(all[0]) // the first is now used last
	now = now.Add(time.Second)
	last := signIn(t, p)
	if len(p.sessions) != maxSessions {
		t.Fatal(len(p.sessions), "sessions")
	}
	if get(all[1]) != 401 {
		t.Fatal("the longest idle session stayed")
	}
	for _, c := range []client{all[0], all[2], all[3], all[4], last} {
		if get(c) != 200 {
			t.Fatal("a live session was dropped")
		}
	}
}

func TestMethodsAndPaths(t *testing.T) {
	_, p, c := setup(t)
	for name, methods := range p.routes {
		for _, m := range []string{"GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS"} {
			if methods[m] != nil {
				continue
			}
			w := c.do(m, "/picker/api/"+name, "")
			if w.Code != 405 || w.Header().Get("Allow") == "" {
				t.Errorf("%s %s: %d", m, name, w.Code)
			}
		}
	}
	for _, path := range []string{"/picker/api/", "/picker/api/nope", "/picker/api/state/"} {
		if w := c.do("GET", path, ""); w.Code != 404 {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
	for _, path := range []string{"/picker/static/index.html", "/picker/../go.mod", "/picker/picker.go"} {
		if w := c.do("GET", path, ""); w.Code != 404 {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
	if w := c.do("POST", "/picker/", `{}`); w.Code != 405 {
		t.Error("POST page:", w.Code)
	}
}

func TestBodies(t *testing.T) {
	f, p, c := setup(t)
	big := `{"key":"` + strings.Repeat("a", 64<<10) + `"}`
	if w := c.do("PUT", "/picker/api/key", big); w.Code != 413 {
		t.Error("over 64 KB:", w.Code)
	}
	if w := (client{t: t, p: p}).do("POST", "/picker/api/session", `{"code":"`+strings.Repeat("a", 64<<10)+`"}`); w.Code != 413 {
		t.Error("session over 64 KB:", w.Code)
	}
	if w := c.do("PUT", "/picker/api/key", `{"key":"`+testKey+`"}`+strings.Repeat(" ", 64<<10)); w.Code != 413 {
		t.Error("padding over 64 KB:", w.Code)
	}
	for _, kind := range []string{"", "text/plain", "application/x-www-form-urlencoded", "multipart/form-data"} {
		if w := c.do("PUT", "/picker/api/key", `{"key":"`+testKey+`"}`, header("Content-Type", kind)); w.Code != 415 {
			t.Errorf("%q: %d", kind, w.Code)
		}
	}
	for _, body := range []string{`{`, `[]`, `{"key":"` + testKey + `"}{}`, `{"key":"` + testKey + `","x":1}`, `{"key":` + testKey + `}`, `{"key":""}`,
		`{"key":"has space"}`, `{"key":"ключ"}`, `{"key":"` + strings.Repeat("a", 513) + `"}`} {
		if w := c.do("PUT", "/picker/api/key", body); w.Code != 400 {
			t.Errorf("%.40s: %d", body, w.Code)
		}
	}
	if f.key != "" {
		t.Fatal("a refused key was saved")
	}
}

func TestPageHeaders(t *testing.T) {
	_, p, c := setup(t)
	anon := client{t: t, p: p}
	for path, kind := range map[string]string{
		"/picker/": "text/html; charset=utf-8", "/picker/app.js": "text/javascript; charset=utf-8",
		"/picker/app.css": "text/css; charset=utf-8", "/picker/icon.svg": "image/svg+xml", "/picker/api/state": "application/json", "/picker/nope": "application/json",
	} {
		cl := anon
		if path == "/picker/api/state" {
			cl = c
		}
		h := cl.do("GET", path, "").Header()
		for k, want := range map[string]string{
			"Content-Type":            kind,
			"Content-Security-Policy": "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'",
			"X-Content-Type-Options":  "nosniff", "Referrer-Policy": "no-referrer", "Cache-Control": "no-store",
		} {
			if got := h.Get(k); got != want {
				t.Errorf("%s %s: %q", path, k, got)
			}
		}
	}
}

func TestAssetsStayLocal(t *testing.T) {
	outside := regexp.MustCompile(`(?i)(https?:|src=["']?//|url\(|@import|data:)`)
	unsafe := regexp.MustCompile(`innerHTML|outerHTML|insertAdjacent|document\.write|\beval\(|new Function|localStorage|sessionStorage|indexedDB|\.cssText`)
	inline := regexp.MustCompile(`(?i)<style|\sstyle=|\son[a-z]+=|<script[^>]*>[^<]`)
	for _, pg := range pages {
		b, err := static.ReadFile(pg.file)
		if err != nil {
			t.Fatal(err)
		}
		s := strings.ReplaceAll(string(b), `xmlns="http://www.w3.org/2000/svg"`, "")
		if m := outside.FindString(s); m != "" {
			t.Errorf("%s reaches outside: %q", pg.file, m)
		}
		if m := unsafe.FindString(s); m != "" {
			t.Errorf("%s: %q", pg.file, m)
		}
		if strings.HasSuffix(pg.file, ".html") && inline.MatchString(s) {
			t.Errorf("%s: inline code or style", pg.file)
		}
	}
}

func TestSave(t *testing.T) {
	f, _, c := setup(t)
	cfg := route.Default()
	cfg.Slots["opus"] = route.Target{Provider: route.AIMLAPI, Model: "zhipu/glm-5.3", Name: "GLM 5.3", Context: 200_000}
	cfg.Extra = []route.Target{{Provider: route.AIMLAPI, Model: "a/b"}}
	body, _ := json.Marshal(cfg)
	w := c.do("PUT", "/picker/api/config", string(body))
	if w.Code != 200 || f.cfg.Slots["opus"].Model != "zhipu/glm-5.3" || len(f.cfg.Extra) != 1 || !strings.Contains(w.Body.String(), `"desktop":true`) {
		t.Fatal(w.Code, w.Body)
	}
	for name, change := range map[string]func(*route.Config){
		"provider": func(c *route.Config) { c.Slots["opus"] = route.Target{Provider: "x", Model: "m"} },
		"missing":  func(c *route.Config) { delete(c.Slots, "opus") },
		"strange":  func(c *route.Config) { c.Slots["mythos"] = c.Slots["opus"] },
		"id":       func(c *route.Config) { c.Extra = []route.Target{{Provider: route.AIMLAPI, Model: "<img>"}} },
		"twice": func(c *route.Config) {
			c.Extra = []route.Target{{Provider: route.AIMLAPI, Model: "a"}, {Provider: route.AIMLAPI, Model: "A"}}
		},
		"context": func(c *route.Config) {
			c.Slots["opus"] = route.Target{Provider: route.AIMLAPI, Model: "a", Context: 99_000_000}
		},
	} {
		bad := route.Default()
		change(&bad)
		if bad.Valid() == nil {
			t.Fatal(name, "is valid")
		}
		body, _ := json.Marshal(bad)
		if w := c.do("PUT", "/picker/api/config", string(body)); w.Code != 400 {
			t.Errorf("%s: %d", name, w.Code)
		}
	}
	if f.saves != 1 {
		t.Fatal(f.saves, "saves")
	}
	f.fault = errors.New("disk full")
	if w := c.do("PUT", "/picker/api/config", string(body)); w.Code != 500 || !strings.Contains(w.Body.String(), "disk full") {
		t.Fatal(w.Code, w.Body)
	}
}

func TestModels(t *testing.T) {
	f, _, c := setup(t)
	f.models = []Model{
		{ID: "a/b", Name: "<b>B</b>\n‮", Context: -1, OutputMax: -1},
		{ID: "A/B", Name: "again"},
		{ID: "bad id"},
		{ID: ""},
		{ID: "a/long", Name: strings.Repeat("é", 80), Context: 99_000_000},
		{ID: "a/none"},
	}
	var out struct {
		Models []wireModel
		Hidden int
	}
	w := c.do("GET", "/picker/api/models", "")
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Hidden != 3 || len(out.Models) != 3 || out.Models[0].Name != "<b>B</b>" || out.Models[0].Context != 0 || out.Models[1].Name != strings.Repeat("é", 50) || out.Models[2].Name != "a/none" {
		t.Fatalf("%+v", out)
	}
	for _, m := range out.Models { // everything offered can be saved
		if err := valid(route.Target{Provider: route.AIMLAPI, Model: m.ID, Name: m.Name, Context: m.Context}, nil); err != nil {
			t.Error(m.ID, err)
		}
	}
	f.models = nil
	if w := c.do("GET", "/picker/api/models", ""); strings.TrimSpace(w.Body.String()) != `{"hidden":0,"models":[]}` {
		t.Fatal(w.Body)
	}
	f.fault = errors.New("AIMLAPI answered 401 for " + testKey)
	if w := c.do("GET", "/picker/api/models", ""); w.Code != 502 || !strings.Contains(w.Body.String(), "answered 401") {
		t.Fatal(w.Code, w.Body)
	}
}

func TestKey(t *testing.T) { // client.do fails the test when an answer holds the key
	f, _, c := setup(t)
	if w := c.do("GET", "/picker/api/key", ""); strings.TrimSpace(w.Body.String()) != `{"detail":"","ok":false,"present":false}` {
		t.Fatal(w.Body)
	}
	if w := c.do("PUT", "/picker/api/key", `{"key":" `+testKey+`\n"}`); w.Code != 200 || f.key != testKey || !strings.Contains(w.Body.String(), `"present":true`) {
		t.Fatal(w.Code, w.Body)
	}
	calls := func() {
		body, _ := json.Marshal(route.Default())
		c.do("GET", "/picker/api/state", "")
		c.do("GET", "/picker/api/models", "")
		c.do("GET", "/picker/api/key", "")
		c.do("PUT", "/picker/api/config", string(body))
		c.do("POST", "/picker/api/test", `{"provider":"codex","model":"gpt-6-astra"}`)
		c.do("POST", "/picker/api/desktop", `{"confirm":true}`)
		c.do("PUT", "/picker/api/key", `{"key":"`+testKey+`"}`)
		c.do("PUT", "/picker/api/key", `{"key":"`+testKey+`"`)
		c.do("DELETE", "/picker/api/key", "")
	}
	calls()
	for _, msg := range []string{"Bearer " + testKey, "key=" + testKey + ".", testKey, "x" + testKey + "y"} {
		f.fault = errors.New(msg)
		calls()
	}
	if s := clean(strings.Repeat("a b ", 70)+testKey, 300); strings.Contains(s, testKey[:8]) {
		t.Fatal("half a key is left:", s)
	}
	f.fault = errors.New("no pa55 here")
	if w := c.do("PUT", "/picker/api/key", `{"key":"pa55"}`); w.Code != 500 || strings.Contains(w.Body.String(), "pa55") {
		t.Fatal("a short key is in the answer:", w.Body)
	}
	f.fault = nil
	if w := c.do("DELETE", "/picker/api/key", ""); w.Code != 200 || f.key != "" {
		t.Fatal(w.Code, f.key)
	}
}

func TestTestAndDesktop(t *testing.T) {
	f, _, c := setup(t)
	w := c.do("POST", "/picker/api/test", `{"provider":"aimlapi","model":"a/b","name":"B","context":1}`)
	if strings.TrimSpace(w.Body.String()) != `{"detail":"ok","ms":12,"ok":true}` || f.tested.Model != "a/b" {
		t.Fatal(w.Body)
	}
	f.tested = route.Target{}
	for _, body := range []string{`{"provider":"x","model":"a"}`, `{"provider":"aimlapi","model":"a b"}`, `{}`} {
		if w := c.do("POST", "/picker/api/test", body); w.Code != 400 {
			t.Errorf("%s: %d", body, w.Code)
		}
	}
	if f.tested.Model != "" {
		t.Fatal("a refused target was tested")
	}
	for _, body := range []string{`{}`, `{"confirm":false}`} {
		if w := c.do("POST", "/picker/api/desktop", body); w.Code != 400 || f.applied != 0 {
			t.Error(body, w.Code)
		}
	}
	if w := c.do("POST", "/picker/api/desktop", `{"confirm":true}`); w.Code != 200 || f.applied != 1 {
		t.Fatal(w.Code)
	}
	f.fault = errors.New("no answer in time")
	if w := c.do("POST", "/picker/api/test", `{"provider":"codex","model":"gpt-6-astra"}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":false`) {
		t.Fatal(w.Code, w.Body)
	}
	if w := c.do("POST", "/picker/api/desktop", `{"confirm":true}`); w.Code != 500 {
		t.Fatal(w.Code)
	}
}

func TestPageURL(t *testing.T) {
	p := New(&fake{cfg: route.Default()})
	u := p.PageURL("http://"+host, "plugins")
	c, ok := strings.CutPrefix(u, "http://"+host+"/picker/plugins#")
	if !ok || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(c) {
		t.Fatal(u)
	}
	cl := client{t: t, p: p}
	if w := cl.trade(c); w.Code != 200 {
		t.Fatal("first use:", w.Code)
	}
	if w := cl.trade(c); w.Code != 401 {
		t.Fatal("second use:", w.Code)
	}
}
