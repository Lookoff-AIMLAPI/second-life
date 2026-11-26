package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Lookoff-AIMLAPI/second-life/internal/aimlapi"
	"github.com/Lookoff-AIMLAPI/second-life/internal/dto"
	"github.com/Lookoff-AIMLAPI/second-life/internal/keystore"
	"github.com/Lookoff-AIMLAPI/second-life/internal/route"
	"github.com/Lookoff-AIMLAPI/second-life/internal/translate"
)

// Keys is the store of the AIMLAPI key.
type Keys interface {
	Get() (string, error)
	Set(string) error
	Delete() error
}

type systemKeys struct{}

func (systemKeys) Get() (string, error) { return keystore.Get() }
func (systemKeys) Set(k string) error   { return keystore.Set(k) }
func (systemKeys) Delete() error        { return keystore.Delete() }

// cachedKeys asks the system store rarely: on macOS every read starts a process and may ask to unlock.
type cachedKeys struct {
	Keys
	mu  sync.Mutex
	key string
	at  time.Time
}

func (c *cachedKeys) Get() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.key != "" && time.Since(c.at) < 10*time.Minute {
		return c.key, nil
	}
	k, err := c.Keys.Get()
	if err != nil {
		c.key = ""
		return "", err
	}
	c.key, c.at = k, time.Now()
	return k, nil
}

func (c *cachedKeys) forget() {
	c.mu.Lock()
	c.key = ""
	c.mu.Unlock()
}

func (c *cachedKeys) Set(k string) error { defer c.forget(); return c.Keys.Set(k) }
func (c *cachedKeys) Delete() error      { defer c.forget(); return c.Keys.Delete() }

// withoutBilling drops Claude Code's billing line: it is for Anthropic, not for another provider.
func withoutBilling(raw []byte) []byte {
	var top map[string]json.RawMessage
	if json.Unmarshal(raw, &top) != nil {
		return raw
	}
	var blocks []json.RawMessage
	if json.Unmarshal(top["system"], &blocks) != nil {
		return raw
	}
	kept := make([]json.RawMessage, 0, len(blocks))
	for _, b := range blocks {
		var t struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(b, &t) == nil && strings.HasPrefix(t.Text, "x-anthropic-billing-header:") {
			continue
		}
		kept = append(kept, b)
	}
	if len(kept) == len(blocks) {
		return raw
	}
	if len(kept) == 0 {
		delete(top, "system")
	} else {
		s, err := json.Marshal(kept)
		if err != nil {
			return raw
		}
		top["system"] = s
	}
	out, err := json.Marshal(top)
	if err != nil {
		return raw
	}
	return out
}

// outputLimit is what the catalogue says the model may write; 0 when unknown.
func (b *Bridge) outputLimit(ctx context.Context, model string) int {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	list, err := b.aiml.Models(ctx)
	if err != nil {
		return 0
	}
	for _, m := range list {
		if strings.EqualFold(m.ID, model) {
			return m.OutputMax
		}
	}
	return 0
}

// serveAIML passes a request to AIMLAPI, which speaks the same protocol.
func (b *Bridge) serveAIML(w http.ResponseWriter, r *http.Request, t *trip, raw []byte, req *dto.MessagesRequest, target route.Target) {
	t.upstream = "aimlapi/" + target.Model
	fail := func(e *dto.APIError) {
		if r.Context().Err() != nil {
			e = errGone
		}
		b.finish(t, dto.Usage{}, e)
		writeError(w, e)
	}
	body, warnings, e := aimlapi.Prepare(withoutBilling(raw), target.Model)
	if e != nil {
		fail(e)
		return
	}
	if limit := b.outputLimit(r.Context(), target.Model); limit > 0 {
		var warn string
		if body, warn = aimlapi.CapMaxTokens(body, limit); warn != "" {
			warnings = append(warnings, warn)
		}
	}
	t.items, t.tools = len(req.Messages), len(req.Tools)
	if req.OutputConfig != nil {
		t.effort = req.OutputConfig.Effort
	}
	key, estimate := talkKey(r.Header.Get("X-Claude-Code-Session-Id"), req)+"a", translate.CountTokens(req)
	if key == "a" {
		key = ""
	}
	if target.Context > 0 {
		if e := b.guard.check(key, estimate, target.Context-min(32_000, target.Context/5)); e != nil && !translate.IsCompaction(req) {
			fail(e)
			return
		}
	}
	for _, msg := range warnings {
		b.log.Printf("warn model=%q %s", t.upstream, msg)
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	slow := b.watchSlow(t)
	defer slow.Stop()
	resp, e := b.aiml.Send(ctx, body, r.Header)
	if e != nil {
		if e.Status == 401 {
			b.keys.forget()
		}
		fail(e)
		return
	}
	defer resp.Body.Close()
	t.mark(&t.headers)
	idle := time.AfterFunc(idleLimit, cancel)
	defer idle.Stop()

	w.Header().Set("X-Second-Life-Model", t.upstream)
	b.pass(w, r, t, req, resp, &activity{r: resp.Body, t: idle}, key, estimate, "AIMLAPI")
}

// pass sends an answer that already speaks the protocol.
func (b *Bridge) pass(w http.ResponseWriter, r *http.Request, t *trip, req *dto.MessagesRequest, resp *http.Response, body io.Reader, key string, estimate int, name string) {
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/json"
		if req.Stream {
			ct = "text/event-stream"
		}
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(200)
	meter := aimlapi.NewMeter()
	meter.Seen = func(event string) {
		t.mark(&t.event)
		if event == "content_block_delta" {
			t.mark(&t.text)
		}
	}
	src := io.TeeReader(aimlapi.RewriteModel(body, req.Model), meter)
	_, err := io.Copy(flushing{w}, src)
	f := meter.Failed()
	switch {
	case r.Context().Err() != nil && !meter.Done():
		f = errGone
	case f == nil && !meter.Done():
		f = &dto.APIError{Status: 502, Type: "api_error", Message: name + " ended the answer early"}
	}
	if f != nil && f != errGone && req.Stream && !meter.Reported() {
		_ = translate.WriteEvent(w, dto.Event{Name: "error", Data: map[string]any{"type": "error", "error": map[string]any{"type": f.Type, "message": f.Message}}})
	}
	if err != nil && f == nil {
		b.log.Printf("warn upstream copy: %T", err)
	}
	b.guard.learn(key, estimate, meter.Usage())
	if usd := resp.Header.Get("X-Aimlapi-Usd-Spent"); usd != "" && len(usd) < 32 {
		t.usd = usd
	}
	b.finish(t, meter.Usage(), f)
}

// flushing sends every piece at once: Claude shows text as it comes.
type flushing struct{ w http.ResponseWriter }

func (f flushing) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if fl, ok := f.w.(http.Flusher); ok {
		fl.Flush()
	}
	return n, err
}

// ---- what the picker needs ----

// Config is who answers for each Claude name.
func (b *Bridge) Config() route.Config {
	c := *b.routes.Load()
	c.Slots, c.Extra, c.Endpoints = maps.Clone(c.Slots), slices.Clone(c.Extra), slices.Clone(c.Endpoints) // the caller may change its copy
	return c
}

// Save stores and applies a new config.
func (b *Bridge) Save(c route.Config) error {
	if err := route.Save(b.home, c); err != nil {
		return err
	}
	c.Slots, c.Extra, c.Endpoints = maps.Clone(c.Slots), slices.Clone(c.Extra), slices.Clone(c.Endpoints)
	b.learned.Clear() // the owner may have changed the servers
	b.routes.Store(&c)
	b.look()
	return nil
}

// AIMLAPIModels lists what AIMLAPI serves on this protocol.
func (b *Bridge) AIMLAPIModels(ctx context.Context) ([]aimlapi.Model, error) {
	return b.aiml.Models(ctx)
}

// KeyState says whether an AIMLAPI key is stored and whether it works. Reading the balance costs nothing.
func (b *Bridge) KeyState(ctx context.Context) (present, ok bool, detail string) {
	key, err := b.keys.Get()
	if errors.Is(err, keystore.ErrNone) {
		return false, false, ""
	}
	if err != nil {
		return false, false, "the key store could not be read"
	}
	req, err := http.NewRequestWithContext(ctx, "GET", b.aiml.BaseURL+"/v1/billing/balance", nil)
	if err != nil {
		return true, false, "could not check the key"
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("User-Agent", "second-life/"+Version)
	resp, err := b.client.Do(req)
	if err != nil {
		return true, false, "could not reach AIMLAPI to check the key"
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	switch {
	case resp.StatusCode == 200:
		return true, true, ""
	case resp.StatusCode == 401:
		return true, false, ""
	}
	return true, false, "AIMLAPI answered HTTP " + http.StatusText(resp.StatusCode)
}

// SetKey and DeleteKey change the stored AIMLAPI key.
func (b *Bridge) SetKey(k string) error { defer b.look(); return b.keys.Set(k) }
func (b *Bridge) DeleteKey() error      { defer b.look(); return b.keys.Delete() }

func (b *Bridge) look() {
	select {
	case b.kick <- struct{}{}:
	default:
	}
}

// aimlReady is true when at least one Claude name has another provider that can answer: the bridge is useful without
// Codex.
func (b *Bridge) aimlReady() bool {
	aiml := false
	for _, t := range b.routes.Load().Slots {
		if t.Provider != route.Codex && t.Provider != route.AIMLAPI {
			return true // a server of the owner's own, with a key or without
		}
		aiml = aiml || t.Provider == route.AIMLAPI
	}
	if !aiml {
		return false
	}
	_, err := b.keys.Get()
	return err == nil
}

// Test sends one tiny request to a model.
func (b *Bridge) Test(ctx context.Context, target route.Target) (ms int64, detail string, err error) {
	began := time.Now()
	raw := []byte(`{"model":"x","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"Reply with the word ok."}]}`)
	if ep, ok := b.Config().Endpoint(target.Provider); ok {
		if err := b.testEndpoint(ctx, ep, target.Model, raw); err != nil {
			return 0, "", err
		}
		return time.Since(began).Milliseconds(), "", nil
	}
	var resp *http.Response
	var e *dto.APIError
	if target.Provider == route.AIMLAPI {
		var body []byte
		if body, _, e = aimlapi.Prepare(raw, target.Model); e == nil {
			resp, e = b.aiml.Send(ctx, body, http.Header{})
		}
	} else {
		var req dto.MessagesRequest
		_ = json.Unmarshal(raw, &req)
		req.OutputConfig = &dto.OutputConfig{Effort: "low"}
		var tr *dto.Translated
		if tr, e = translate.ToResponses(&req, target.Model, ""); e == nil {
			resp, e = b.send(ctx, &tr.Body)
		}
	}
	if e != nil {
		return 0, "", e
	}
	defer resp.Body.Close()
	if target.Provider == route.AIMLAPI {
		m := aimlapi.NewMeter()
		_, err = io.Copy(m, io.LimitReader(resp.Body, 1<<20))
		if f := m.Failed(); f != nil {
			return 0, "", f
		}
		if err == nil && !m.Done() {
			err = errors.New("the answer ended early")
		}
	} else {
		s := translate.NewStream(dto.StreamContext{PublicModel: "x", UpstreamModel: target.Model, HideThinking: true})
		err = translate.ReadSSE(resp.Body, func(ev map[string]any) error {
			s.Push(ev)
			if s.Ended() {
				return errStop
			}
			return nil
		})
		s.Close()
		if errors.Is(err, errStop) {
			err = nil
		}
		if f := s.Failed(); f != nil {
			return 0, "", f
		}
	}
	return time.Since(began).Milliseconds(), "", err
}

// SetDesktop gives the picker a way to update Claude Desktop; the tray provides it.
func (b *Bridge) SetDesktop(configured func() bool, apply func() error) {
	b.mu.Lock()
	b.deskConfigured, b.deskApply = configured, apply
	b.mu.Unlock()
}

// DesktopConfigured is true when Claude Desktop runs through this bridge.
func (b *Bridge) DesktopConfigured() bool {
	b.mu.Lock()
	f := b.deskConfigured
	b.mu.Unlock()
	return f != nil && f()
}

// ApplyDesktop starts the update of Claude Desktop.
func (b *Bridge) ApplyDesktop() error {
	b.mu.Lock()
	f := b.deskApply
	b.mu.Unlock()
	if f == nil {
		return errors.New("only the menu bar app can update Claude Desktop")
	}
	return f()
}

// PickerURL opens the model picker once; empty while the bridge is off.
func (b *Bridge) PickerURL() string {
	if b.Status().State == StateOff {
		return ""
	}
	return b.picker.URL("http://" + b.Status().Addr)
}

// PluginsURL opens the plugins page once; empty while the bridge is off.
func (b *Bridge) PluginsURL() string {
	if b.Status().State == StateOff {
		return ""
	}
	return b.picker.PageURL("http://"+b.Status().Addr, "plugins")
}

// drainMessages reads a whole Anthropic answer and says whether it ended well.
func drainMessages(r io.Reader) error {
	m := aimlapi.NewMeter()
	_, err := io.Copy(m, io.LimitReader(r, 1<<20))
	if f := m.Failed(); f != nil {
		return f
	}
	if err == nil && !m.Done() {
		err = errors.New("the answer ended early")
	}
	return err
}
