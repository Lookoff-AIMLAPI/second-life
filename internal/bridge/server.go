package bridge

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Lookoff-AIMLAPI/second-life/internal/codex"
	"github.com/Lookoff-AIMLAPI/second-life/internal/dto"
	"github.com/Lookoff-AIMLAPI/second-life/internal/route"
	"github.com/Lookoff-AIMLAPI/second-life/internal/translate"
)

const (
	maxBody    = 64 << 20
	holdFirst  = 10 * time.Second // how long an early failure can still become an HTTP status
	pingEvery  = 15 * time.Second
	idleLimit  = 15 * time.Minute
	maxErrBody = 1 << 20
)

func (b *Bridge) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/hello", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		out := map[string]string{"service": "second-life"}
		if n := r.URL.Query().Get("nonce"); n != "" && len(n) <= 128 {
			out["proof"] = Proof(b.token, b.Status().Addr, n)
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("POST /v1/messages", b.local(b.messages))
	mux.HandleFunc("POST /v1/messages/count_tokens", b.local(b.countTokens))
	mux.HandleFunc("GET /v1/models", b.local(b.models))
	mux.Handle("/picker/", b.picker.Handler())
	mux.HandleFunc("GET /admin/status", b.local(func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, b.Status()) }))
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, &dto.APIError{Status: 404, Type: "not_found_error", Message: "no such route"})
	})
	return b.sameMachine(mux)
}

// sameMachine refuses names that point at loopback through DNS, which is how a web page would reach the bridge.
// A bridge told to listen wider (the container) is reached by other names.
func (b *Bridge) sameMachine(next http.Handler) http.Handler {
	loop := func(h string) bool {
		if host, _, err := net.SplitHostPort(h); err == nil {
			h = host
		}
		h = strings.Trim(h, "[]")
		ip := net.ParseIP(h)
		return strings.EqualFold(h, "localhost") || ip != nil && ip.IsLoopback()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if loop(b.addr) && !loop(r.Host) {
			writeError(w, &dto.APIError{Status: 403, Type: "permission_error", Message: "use 127.0.0.1"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (b *Bridge) local(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if got == "" {
			got = r.Header.Get("X-Api-Key")
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(b.token)) != 1 {
			writeError(w, &dto.APIError{Status: 401, Type: "authentication_error", Message: "wrong local token; start Claude with `slife claude`"})
			return
		}
		next(w, r)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, e *dto.APIError) {
	if e.RetryAfter != "" {
		w.Header().Set("Retry-After", e.RetryAfter)
	}
	writeJSON(w, e.Status, map[string]any{"type": "error", "error": map[string]string{"type": e.Type, "message": e.Message}})
}

func readBody(w http.ResponseWriter, r *http.Request, v any) ([]byte, *dto.APIError) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		var big *http.MaxBytesError
		if errors.As(err, &big) {
			return nil, &dto.APIError{Status: 413, Type: "request_too_large", Message: "request is over 64 MB"}
		}
		return nil, &dto.APIError{Status: 400, Type: "invalid_request_error", Message: "could not read the request"}
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return nil, &dto.APIError{Status: 400, Type: "invalid_request_error", Message: "request is not valid: " + err.Error()}
	}
	return raw, nil
}

func (b *Bridge) models(w http.ResponseWriter, _ *http.Request) {
	data := []map[string]any{}
	c := b.Config()
	add := func(id, label string) {
		data = append(data, map[string]any{"type": "model", "id": id, "display_name": label, "created_at": "2026-01-01T00:00:00Z"})
	}
	for _, slot := range route.Slots {
		add(slot, slot+" on "+c.Slots[slot].Label())
	}
	for _, t := range c.Extra {
		add(t.Model, t.Label())
	}
	writeJSON(w, 200, map[string]any{"data": data, "has_more": false})
}

// countTokens is an estimate: Codex has no counting call.
func (b *Bridge) countTokens(w http.ResponseWriter, r *http.Request) {
	var req dto.MessagesRequest
	if _, e := readBody(w, r, &req); e != nil {
		writeError(w, e)
		return
	}
	writeJSON(w, 200, map[string]int{"input_tokens": translate.CountTokens(&req)})
}

// trip is what one request leaves in the log: sizes and times, never content.
type trip struct {
	began                time.Time
	model, upstream      string
	effort               string
	usd                  string // what AIMLAPI charged
	bytes, items, tools  int
	stream               bool
	headers, event, text atomic.Int64 // nanoseconds since began; 0 when it never came
}

func (t *trip) mark(d *atomic.Int64) { d.CompareAndSwap(0, int64(time.Since(t.began))) }

// slowAfter is when a request without text is worth a line of its own.
const slowAfter = 20 * time.Second

func (t *trip) phase() string {
	switch {
	case t.headers.Load() == 0:
		return "waiting for the provider to accept the request"
	case t.event.Load() == 0:
		return "waiting for the first event"
	case t.text.Load() == 0:
		return "the model is thinking"
	}
	return ""
}

// errStop ends reading once the answer is complete; upstream may keep the body open.
var errStop = errors.New("answer complete")

func (b *Bridge) watchSlow(t *trip) *time.Timer {
	return time.AfterFunc(slowAfter, func() {
		if p := t.phase(); p != "" {
			b.log.Printf("slow model=%q after=%s kb=%d tools=%d: %s", t.upstream, slowAfter, t.bytes>>10, t.tools, p)
		}
	})
}

func (b *Bridge) messages(w http.ResponseWriter, r *http.Request) {
	t := &trip{began: time.Now()}
	var req dto.MessagesRequest
	raw, e := readBody(w, r, &req)
	var target route.Target
	var tr *dto.Translated
	if e == nil {
		target, e = b.Config().Resolve(req.Model)
	}
	t.model, t.bytes, t.stream = req.Model, len(raw), req.Stream
	if e == nil && target.Provider == route.AIMLAPI {
		b.serveAIML(w, r, t, raw, &req, target)
		return
	}
	if ep, ok := b.Config().Endpoint(target.Provider); e == nil && ok {
		b.serveEndpoint(w, r, t, raw, &req, target, ep)
		return
	}
	upstream := target.Model
	if e == nil {
		tr, e = translate.ToResponses(&req, upstream, r.Header.Get("X-Claude-Code-Session-Id"))
	}
	t.upstream = upstream
	if e != nil {
		b.finish(t, dto.Usage{}, e)
		writeError(w, e)
		return
	}
	t.effort, t.items, t.tools = tr.Body.Reasoning.Effort, len(tr.Body.Input), len(tr.Body.Tools)
	key, estimate := talkKey(r.Header.Get("X-Claude-Code-Session-Id"), &req), estimateTokens(&tr.Body)
	if e := b.guard.check(key, estimate, b.guard.cap); e != nil && !tr.Compaction {
		b.finish(t, dto.Usage{}, e)
		writeError(w, e)
		return
	}
	slow := b.watchSlow(t)
	defer slow.Stop()
	for _, msg := range tr.Warnings {
		b.log.Printf("warn model=%q %s", upstream, msg)
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	resp, e := b.send(ctx, &tr.Body)
	if e != nil {
		if r.Context().Err() != nil {
			e = errGone
		}
		b.finish(t, dto.Usage{}, e)
		writeError(w, e)
		return
	}
	t.mark(&t.headers)
	defer resp.Body.Close()
	w.Header().Set("X-Second-Life-Model", upstream)
	s := translate.NewStream(dto.StreamContext{
		PublicModel: req.Model, UpstreamModel: upstream, ToolNames: tr.ToolNames,
		StopSeqs: req.StopSeqs, InputEstimate: b.guard.scale(key, estimate),
		HideThinking: req.Thinking == nil || req.Thinking.Type == "disabled",
		ShowThinking: req.Thinking != nil && req.Thinking.Display == "summarized",
	})
	idle := time.AfterFunc(idleLimit, cancel)
	defer idle.Stop()
	body := &activity{r: resp.Body, t: idle}
	b.answer(w, r, t, &req, s, body, key, estimate)
}

// answer sends a translated answer to the client and books it.
func (b *Bridge) answer(w http.ResponseWriter, r *http.Request, t *trip, req *dto.MessagesRequest, s translator, body io.Reader, key string, estimate int, mode ...translate.ReadMode) {
	if req.Stream {
		b.stream(w, s, body, t, mode...)
	} else {
		_ = translate.ReadSSE(body, func(ev map[string]any) error {
			t.see(s.Push(ev))
			if s.Ended() {
				return errStop
			}
			return nil
		}, mode...)
		s.Close()
		if m := s.Message(); m != nil && s.Failed() == nil {
			writeJSON(w, 200, m)
		} else if f := s.Failed(); f != nil {
			writeError(w, f)
		} else {
			writeError(w, &dto.APIError{Status: 502, Type: "api_error", Message: "upstream sent no message"})
		}
	}
	if !s.Estimated() {
		b.guard.learn(key, estimate, s.Usage())
	}
	f := s.Failed()
	if f != nil && r.Context().Err() != nil {
		f = errGone // the owner pressed Esc or closed the client
	}
	for _, n := range s.Notes() {
		b.log.Printf("note model=%q %s", t.upstream, n)
	}
	b.finish(t, s.Usage(), f)
}

// errGone marks a client that left; it is not a failure of the bridge.
var errGone = &dto.APIError{Status: 499, Type: "client_gone", Message: "the client closed the request"}

func (t *trip) see(evs []dto.Event) {
	for _, e := range evs {
		t.mark(&t.event)
		if e.Name == "content_block_delta" {
			t.mark(&t.text)
		}
	}
}

type activity struct {
	r io.Reader
	t *time.Timer
}

func (a *activity) Read(p []byte) (int, error) {
	n, err := a.r.Read(p)
	if n > 0 {
		a.t.Reset(idleLimit)
	}
	return n, err
}

// stream holds the opening event until real content arrives, so early failures keep their HTTP status.
func (b *Bridge) stream(w http.ResponseWriter, s translator, body io.Reader, t *trip, mode ...translate.ReadMode) {
	var mu sync.Mutex
	var held []dto.Event
	open, ended := false, false
	began := time.Now()
	flush := func() { // mu held
		if !open {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(200)
			open = true
		}
		for _, e := range held {
			_ = translate.WriteEvent(w, e)
		}
		held = nil
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
	emit := func(evs []dto.Event) { // mu held
		held = append(held, evs...)
		for _, e := range held {
			if e.Name != "message_start" && e.Name != "error" || open {
				flush()
				return
			}
		}
	}
	stop := make(chan struct{})
	gone := make(chan struct{})
	go func() {
		defer close(gone)
		t := time.NewTicker(pingEvery / 3)
		defer t.Stop()
		last := began
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				mu.Lock()
				if !ended && time.Since(last) >= pingEvery || !ended && !open && time.Since(began) >= holdFirst {
					held = append(held, dto.Event{Name: "ping", Data: map[string]any{"type": "ping"}})
					flush()
					last = time.Now()
				}
				mu.Unlock()
			}
		}
	}()
	err := translate.ReadSSE(body, func(ev map[string]any) error {
		mu.Lock()
		defer mu.Unlock()
		evs := s.Push(ev)
		t.see(evs)
		emit(evs)
		if s.Ended() {
			return errStop
		}
		return nil
	}, mode...)
	if err != nil && !errors.Is(err, errStop) {
		b.log.Printf("warn upstream read: %T", err)
	}
	mu.Lock()
	ended = true
	evs := s.Close()
	if f := s.Failed(); f != nil && !open {
		writeError(w, f)
	} else {
		held = append(held, evs...)
		flush()
	}
	mu.Unlock()
	close(stop)
	<-gone
}

// send posts to Codex, refreshing the token once after a 401.
func (b *Bridge) send(ctx context.Context, body *dto.ResponsesRequest) (*http.Response, *dto.APIError) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, &dto.APIError{Status: 400, Type: "invalid_request_error", Message: "could not encode the request"}
	}
	rejected := ""
	for attempt := 0; ; attempt++ {
		cred, err := b.auth.Credentials(ctx, rejected)
		if err != nil {
			msg := "Codex login: " + err.Error()
			if errors.Is(err, codex.ErrSignedOut) || errors.Is(err, codex.ErrMissing) {
				msg += "; sign in to Codex, or choose an AIMLAPI model for this name under Choose models"
				return nil, &dto.APIError{Status: 401, Type: "authentication_error", Message: msg}
			}
			return nil, &dto.APIError{Status: 502, Type: "api_error", Message: msg}
		}
		req, err := http.NewRequestWithContext(ctx, "POST", b.upstream+"/responses", bytes.NewReader(raw))
		if err != nil {
			return nil, &dto.APIError{Status: 500, Type: "api_error", Message: "bad upstream address"}
		}
		req.Header.Set("Authorization", "Bearer "+cred.Token)
		req.Header.Set("ChatGPT-Account-ID", cred.AccountID)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("originator", "second_life")
		req.Header.Set("User-Agent", "second-life/"+Version)
		if body.PromptCacheKey != "" {
			req.Header.Set("session-id", body.PromptCacheKey)
		}
		resp, err := b.client.Do(req)
		if err != nil {
			return nil, &dto.APIError{Status: 502, Type: "api_error", Message: "could not reach Codex"}
		}
		if resp.StatusCode == 200 {
			return resp, nil
		}
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBody))
		resp.Body.Close()
		if resp.StatusCode == 401 && attempt == 0 {
			rejected = cred.Token
			continue
		}
		errBody = bytes.ReplaceAll(errBody, []byte(cred.Token), []byte("[token]"))
		return nil, translate.UpstreamError(resp.StatusCode, resp.Header, errBody)
	}
}

func (b *Bridge) finish(t *trip, u dto.Usage, e *dto.APIError) {
	kind := "ok"
	b.update(func(s *Status) {
		s.Requests++
		s.InputTokens += int64(u.InputTokens + u.CacheReadInputTokens)
		s.OutputTokens += int64(u.OutputTokens)
		if t.upstream != "" {
			s.LastModel = t.upstream
		}
		if e == errGone {
			kind = e.Type
		} else if e != nil {
			s.Errors++
			s.LastError = e.Type + ": " + e.Message
			kind = e.Type
		} else {
			s.LastError = ""
		}
	})
	ms := func(ns int64) int64 { return ns / int64(time.Millisecond) }
	b.log.Printf("messages model=%q upstream=%q result=%s effort=%s stream=%t kb=%d items=%d tools=%d headers_ms=%d event_ms=%d text_ms=%d total_ms=%d in=%d cached=%d out=%d usd=%s",
		t.model, t.upstream, kind, t.effort, t.stream, t.bytes>>10, t.items, t.tools, ms(t.headers.Load()), ms(t.event.Load()), ms(t.text.Load()), ms(int64(time.Since(t.began))), u.InputTokens, u.CacheReadInputTokens, u.OutputTokens, t.usd)
}
