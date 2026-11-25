package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Lookoff-AIMLAPI/second-life/internal/dto"
	"github.com/Lookoff-AIMLAPI/second-life/internal/keystore"
	"github.com/Lookoff-AIMLAPI/second-life/internal/openai"
	"github.com/Lookoff-AIMLAPI/second-life/internal/picker"
	"github.com/Lookoff-AIMLAPI/second-life/internal/route"
	"github.com/Lookoff-AIMLAPI/second-life/internal/translate"
)

// translator turns one answer of a provider into Anthropic events.
type translator interface {
	Push(map[string]any) []dto.Event
	Close() []dto.Event
	Usage() dto.Usage
	Failed() *dto.APIError
	Message() *dto.MessageResponse
	Ended() bool
	Notes() []string
	Estimated() bool
}

// maxAdjust bounds how often one request is sent again without something the server refused.
const maxAdjust = 6

// learnKey remembers what an endpoint refused, per model.
type learnKey struct{ endpoint, model string }

func (b *Bridge) options(ep route.Endpoint, model string) translate.Options {
	if v, ok := b.learned.Load(learnKey{ep.ID, model}); ok {
		return v.(translate.Options)
	}
	return translate.Options{Endpoint: ep.ID, Effort: true}
}

// endpointKeys is the cached key store of one endpoint.
func (b *Bridge) endpointKeys(id string) *cachedKeys {
	b.epMu.Lock()
	defer b.epMu.Unlock()
	if k := b.epKeys[id]; k != nil {
		return k
	}
	if b.epKeys == nil {
		b.epKeys = map[string]*cachedKeys{}
	}
	k := &cachedKeys{Keys: b.newKeys(id)}
	b.epKeys[id] = k
	return k
}

// endpointKey returns "" for a server that needs no key.
func (b *Bridge) endpointKey(id string) func() (string, error) {
	return func() (string, error) {
		k, err := b.endpointKeys(id).Get()
		if errors.Is(err, keystore.ErrNone) {
			return "", nil
		}
		return k, err
	}
}

// ourselves is true for an address that leads back to this bridge.
func (b *Bridge) ourselves(base string) bool {
	u, err := url.Parse(base)
	if err != nil {
		return false
	}
	host, port := u.Hostname(), u.Port()
	ip := net.ParseIP(host)
	if !strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback()) {
		return false
	}
	_, own, err := net.SplitHostPort(b.Status().Addr)
	return err == nil && port == own
}

// built is one request ready to go out and the way back.
type built struct {
	body       []byte
	stream     translator // nil for the Anthropic protocol, which passes through
	warnings   []string
	compaction bool
	effort     string
	estimate   int
}

func (b *Bridge) build(ep route.Endpoint, model string, o translate.Options, raw []byte, req *dto.MessagesRequest) (built, *dto.APIError) {
	if ep.Protocol == route.Anthropic {
		body, warnings, e := openai.PrepareMessages(raw, model, o)
		return built{body: body, warnings: warnings, compaction: translate.IsCompaction(req)}, e
	}
	sc := dto.StreamContext{
		PublicModel: req.Model, UpstreamModel: o.Identity(model), StopSeqs: req.StopSeqs,
		HideThinking: req.Thinking == nil || req.Thinking.Type == "disabled",
		ShowThinking: req.Thinking != nil && req.Thinking.Display == "summarized",
	}
	var v any
	var out built
	if ep.Protocol == route.Responses {
		tr, e := translate.ToResponsesGeneric(req, model, o)
		if e != nil {
			return built{}, e
		}
		sc.ToolNames = tr.ToolNames
		v, out.warnings, out.compaction, out.effort = tr.Body, tr.Warnings, tr.Compaction, tr.Body.Reasoning.Effort
		out.stream = translate.NewStream(sc)
	} else {
		tr, e := translate.ToChat(req, model, o)
		if e != nil {
			return built{}, e
		}
		sc.ToolNames = tr.ToolNames
		v, out.warnings, out.compaction = tr.Body, tr.Warnings, tr.Compaction
		out.stream = translate.NewChatStream(sc)
	}
	body, err := json.Marshal(v)
	if err != nil {
		return built{}, &dto.APIError{Status: 400, Type: "invalid_request_error", Message: "could not encode the request"}
	}
	out.body = body
	return out, nil
}

// call sends a request, again without each thing the server refuses, and remembers that.
func (b *Bridge) call(ctx context.Context, ep route.Endpoint, model string, raw []byte, req *dto.MessagesRequest, in http.Header) (*http.Response, built, *dto.APIError) {
	if b.ourselves(ep.BaseURL) {
		return nil, built{}, &dto.APIError{Status: 400, Type: "invalid_request_error", Message: "this endpoint is the bridge itself"}
	}
	c := &openai.Client{Endpoint: ep, Key: b.endpointKey(ep.ID), Version: Version}
	o := b.options(ep, model)
	for n := 0; ; n++ {
		out, e := b.build(ep, model, o, raw, req)
		if e != nil {
			return nil, out, e
		}
		resp, err := c.Send(ctx, out.body, in)
		if err == nil {
			if n > 0 {
				b.learned.Store(learnKey{ep.ID, model}, o)
			}
			return resp, out, nil
		}
		var refused *openai.Unsupported
		if errors.As(err, &refused) {
			next, ok := o.Without(refused.Field)
			if ok && n < maxAdjust {
				b.log.Printf("adjust endpoint=%s model=%q without=%q", ep.ID, model, refused.Field)
				o = next
				continue
			}
			return nil, out, refused.API
		}
		var api *dto.APIError
		if !errors.As(err, &api) {
			api = &dto.APIError{Status: 502, Type: "api_error", Message: "could not reach the endpoint"}
		}
		if api.Status == 401 {
			b.endpointKeys(ep.ID).forget()
		}
		return nil, out, api
	}
}

// serveEndpoint answers from a server the owner added.
func (b *Bridge) serveEndpoint(w http.ResponseWriter, r *http.Request, t *trip, raw []byte, req *dto.MessagesRequest, target route.Target, ep route.Endpoint) {
	t.upstream = ep.ID + "/" + target.Model
	t.items, t.tools = len(req.Messages), len(req.Tools)
	fail := func(e *dto.APIError) {
		if r.Context().Err() != nil {
			e = errGone
		}
		b.finish(t, dto.Usage{}, e)
		writeError(w, e)
	}
	key, estimate := talkKey(r.Header.Get("X-Claude-Code-Session-Id"), req), translate.CountTokens(req)
	if key != "" {
		key += ep.ID
	}
	if target.Context > 0 {
		if e := b.guard.check(key, estimate, target.Context-min(32_000, target.Context/5)); e != nil && !translate.IsCompaction(req) {
			fail(e)
			return
		}
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	slow := b.watchSlow(t)
	defer slow.Stop()
	resp, out, e := b.call(ctx, ep, target.Model, raw, req, r.Header)
	if e != nil {
		fail(e)
		return
	}
	defer resp.Body.Close()
	t.mark(&t.headers)
	t.effort = out.effort
	for _, msg := range out.warnings {
		b.log.Printf("warn model=%q %s", t.upstream, msg)
	}
	idle := time.AfterFunc(idleLimit, cancel)
	defer idle.Stop()
	body := &activity{r: resp.Body, t: idle}
	w.Header().Set("X-Second-Life-Model", t.upstream)
	if out.stream == nil {
		b.pass(w, r, t, req, resp, body, key, estimate, ep.Name)
		return
	}
	b.answer(w, r, t, req, out.stream, body, key, estimate, translate.Lenient)
}

// ---- what the picker needs ----

// EndpointModels lists a server's models, with the given key or the stored one.
func (d pickerDeps) EndpointModels(ctx context.Context, ep route.Endpoint, key string) ([]picker.Model, error) {
	if d.ourselves(ep.BaseURL) {
		return nil, errors.New("that address is this bridge itself")
	}
	get := d.endpointKey(ep.ID)
	if key != "" {
		get = func() (string, error) { return key, nil }
	}
	c := &openai.Client{Endpoint: ep, Key: get, Version: Version}
	list, err := c.Models(ctx)
	if err != nil {
		return nil, err
	}
	stored, _ := get()
	out := make([]picker.Model, 0, len(list))
	for _, m := range list {
		if len(stored) >= 16 && strings.Contains(m.ID+m.Name, stored) {
			continue // a server that echoes the key
		}
		out = append(out, picker.Model{ID: m.ID, Name: m.Name, Context: m.Context, OutputMax: m.OutputMax})
	}
	return out, nil
}

// EndpointKey is true when a key is stored.
func (d pickerDeps) EndpointKey(id string) bool {
	_, err := d.endpointKeys(id).Get()
	return err == nil
}

func (d pickerDeps) SetEndpointKey(id, key string) error {
	defer d.look()
	return d.endpointKeys(id).Set(key)
}

func (d pickerDeps) DeleteEndpointKey(id string) error {
	defer d.look()
	return d.endpointKeys(id).Delete()
}

// testEndpoint sends one tiny request.
func (b *Bridge) testEndpoint(ctx context.Context, ep route.Endpoint, model string, raw []byte) error {
	var req dto.MessagesRequest
	_ = json.Unmarshal(raw, &req)
	resp, out, e := b.call(ctx, ep, model, raw, &req, http.Header{})
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	if out.stream == nil {
		return drainMessages(resp.Body)
	}
	err := translate.ReadSSE(io.LimitReader(resp.Body, 1<<20), func(ev map[string]any) error {
		out.stream.Push(ev)
		if out.stream.Ended() {
			return errStop
		}
		return nil
	}, translate.Lenient)
	out.stream.Close()
	if f := out.stream.Failed(); f != nil {
		return f
	}
	if errors.Is(err, errStop) {
		err = nil
	}
	return err
}
