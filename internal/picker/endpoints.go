package picker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/Lookoff-AIMLAPI/second-life/internal/route"
)

const maxListed = 3000

// presets only fill in the form.
var presets = []route.Endpoint{
	{Name: "Ollama", Protocol: route.Chat, BaseURL: "http://127.0.0.1:11434/v1"},
	{Name: "LM Studio", Protocol: route.Chat, BaseURL: "http://127.0.0.1:1234/v1"},
	{Name: "OpenAI", Protocol: route.Responses, BaseURL: "https://api.openai.com/v1"},
	{Name: "OpenRouter", Protocol: route.Chat, BaseURL: "https://openrouter.ai/api/v1"},
}

var errGone = errors.New("an endpoint was changed or removed in another window; reload this page")

// A stored key goes only to the address it was typed for. So an id belongs to one address and protocol for life: a
// change of either makes a new id, which has no key, and nothing the page sends is put next to a stored key.

type endpointIn struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	BaseURL  string `json:"baseUrl"`
	Key      string `json:"key"`
}

func (in endpointIn) endpoint(id string) (route.Endpoint, error) {
	e := route.Endpoint{ID: id, Name: text(in.Name, 60), Protocol: in.Protocol, BaseURL: strings.TrimRight(strings.TrimSpace(in.BaseURL), "/")}
	if e.Name == "" {
		return e, errors.New("the endpoint needs a name")
	}
	if strings.ContainsAny(e.BaseURL, "?# \\") {
		return e, errors.New("the address must not have ?, # or spaces")
	}
	if err := e.Valid(); err != nil {
		return e, err
	}
	if u, err := url.Parse(e.BaseURL); err != nil || u.Hostname() == "" {
		return e, errors.New("the address has no host")
	}
	return e, nil
}

// typedKey returns the key as it will be stored; an empty one is fine.
func typedKey(s string) (string, error) {
	k := strings.TrimSpace(s)
	if len(k) > maxKey || strings.ContainsFunc(k, func(c rune) bool { return c <= ' ' || c > '~' }) {
		return "", errors.New("that does not look like a key")
	}
	return k, nil
}

// newID is an id that no endpoint of the config has.
func newID(c route.Config) string {
	for {
		var b [4]byte
		_, _ = rand.Read(b[:]) // never fails
		id := "ep-" + hex.EncodeToString(b[:])
		if _, taken := c.Endpoint(id); !taken {
			return id
		}
	}
}

// own returns a copy of the saved config that may be changed. p.edit is held.
func (p *Picker) own() route.Config {
	c := p.d.Config()
	c.Slots, c.Extra, c.Endpoints = maps.Clone(c.Slots), slices.Clone(c.Extra), slices.Clone(c.Endpoints)
	return c
}

// commit saves c in place of old. Keys of endpoints that are gone are deleted first: if that fails, nothing has
// changed and the owner can try again. p.edit is held.
func (p *Picker) commit(old, c route.Config) error {
	if err := c.Valid(); err != nil {
		return err
	}
	for _, e := range old.Endpoints {
		if _, kept := c.Endpoint(e.ID); !kept {
			if err := p.d.DeleteEndpointKey(e.ID); err != nil {
				return err
			}
		}
	}
	return p.d.Save(c)
}

// saved answers with what is stored now. The caller adds what it knows about the key: asking the key store may wait
// for the owner.
func (p *Picker) saved(w http.ResponseWriter, more map[string]any) {
	out := map[string]any{"config": p.d.Config(), "desktop": p.d.DesktopConfigured()}
	maps.Copy(out, more)
	reply(w, http.StatusOK, out)
}

// keys says which endpoints have a key. It is not part of state: see saved.
func (p *Picker) keys(w http.ResponseWriter, _ *http.Request) {
	out := map[string]bool{}
	for _, e := range p.d.Config().Endpoints {
		out[e.ID] = p.d.EndpointKey(e.ID)
	}
	reply(w, http.StatusOK, map[string]any{"keys": out})
}

func (p *Picker) addEndpoint(w http.ResponseWriter, r *http.Request) {
	var in struct { // no id: the page does not choose one
		Name     string `json:"name"`
		Protocol string `json:"protocol"`
		BaseURL  string `json:"baseUrl"`
		Key      string `json:"key"`
	}
	if !read(w, r, &in) {
		return
	}
	p.edit.Lock()
	defer p.edit.Unlock()
	old := p.own()
	e, err := endpointIn{Name: in.Name, Protocol: in.Protocol, BaseURL: in.BaseURL}.endpoint(newID(old))
	key, kerr := typedKey(in.Key)
	c := p.own()
	c.Endpoints = append(c.Endpoints, e)
	if err = errors.Join(err, kerr); err == nil {
		err = c.Valid()
	}
	if err != nil {
		fail(w, http.StatusBadRequest, sentence(err, key))
		return
	}
	p.put(w, old, c, e.ID, key, true)
}

func (p *Picker) editEndpoint(w http.ResponseWriter, r *http.Request) {
	var in endpointIn
	if !read(w, r, &in) {
		return
	}
	p.edit.Lock()
	defer p.edit.Unlock()
	old := p.own()
	was, ok := old.Endpoint(in.ID)
	if !ok {
		fail(w, http.StatusNotFound, errGone.Error())
		return
	}
	e, err := in.endpoint(was.ID)
	key, kerr := typedKey(in.Key)
	moved := e.BaseURL != was.BaseURL || e.Protocol != was.Protocol
	if moved {
		e.ID = newID(old)
	}
	c := p.own()
	for i := range c.Endpoints {
		if c.Endpoints[i].ID == was.ID {
			c.Endpoints[i] = e
		}
	}
	for s, t := range c.Slots {
		if t.Provider == was.ID {
			t.Provider = e.ID
			c.Slots[s] = t
		}
	}
	for i := range c.Extra {
		if c.Extra[i].Provider == was.ID {
			c.Extra[i].Provider = e.ID
		}
	}
	if err = errors.Join(err, kerr); err == nil {
		err = c.Valid()
	}
	if err != nil {
		fail(w, http.StatusBadRequest, sentence(err, key))
		return
	}
	p.put(w, old, c, e.ID, key, moved)
}

// put stores the typed key under id, then the config. A fresh id keeps no key when the config is not saved.
func (p *Picker) put(w http.ResponseWriter, old, c route.Config, id, key string, fresh bool) {
	if key != "" {
		if err := p.d.SetEndpointKey(id, key); err != nil {
			fail(w, http.StatusInternalServerError, sentence(err, key))
			return
		}
	}
	if err := p.commit(old, c); err != nil {
		if fresh && key != "" {
			err = errors.Join(err, p.d.DeleteEndpointKey(id))
		}
		fail(w, http.StatusInternalServerError, sentence(err, key))
		return
	}
	more := map[string]any{"id": id}
	if fresh || key != "" {
		more["key"] = key != ""
	}
	p.saved(w, more)
}

// deleteEndpoint refuses while a saved choice uses the endpoint, unless the owner said yes to the defaults.
func (p *Picker) deleteEndpoint(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID   string `json:"id"`
		Move bool   `json:"move"`
	}
	if !read(w, r, &in) {
		return
	}
	p.edit.Lock()
	defer p.edit.Unlock()
	old := p.own()
	if _, ok := old.Endpoint(in.ID); !ok {
		fail(w, http.StatusNotFound, errGone.Error())
		return
	}
	c := p.own()
	used := []string{}
	for _, s := range route.Slots {
		if c.Slots[s].Provider == in.ID {
			used = append(used, s)
			c.Slots[s] = route.Default().Slots[s]
		}
	}
	on := func(t route.Target) bool { return t.Provider == in.ID }
	c.Extra = slices.DeleteFunc(c.Extra, on)
	c.Endpoints = slices.DeleteFunc(c.Endpoints, func(e route.Endpoint) bool { return e.ID == in.ID })
	if extra := len(old.Extra) - len(c.Extra); (len(used) > 0 || extra > 0) && !in.Move {
		reply(w, http.StatusConflict, map[string]any{"error": "the endpoint is in use", "slots": used, "extra": extra})
		return
	}
	if err := p.commit(old, c); err != nil {
		fail(w, http.StatusInternalServerError, sentence(err, ""))
		return
	}
	p.saved(w, nil)
}

func (p *Picker) deleteEndpointKey(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID string `json:"id"`
	}
	if !read(w, r, &in) {
		return
	}
	p.edit.Lock()
	defer p.edit.Unlock()
	if _, ok := p.d.Config().Endpoint(in.ID); !ok {
		fail(w, http.StatusNotFound, errGone.Error())
		return
	}
	if err := p.d.DeleteEndpointKey(in.ID); err != nil {
		fail(w, http.StatusInternalServerError, sentence(err, ""))
		return
	}
	p.saved(w, map[string]any{"id": in.ID, "key": false})
}

// endpointModels asks a saved endpoint, by id alone, or an address that is not saved, which gets only the key typed
// with it.
func (p *Picker) endpointModels(w http.ResponseWriter, r *http.Request) {
	var in endpointIn
	if !read(w, r, &in) {
		return
	}
	key, err := typedKey(in.Key)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	c := p.d.Config()
	var e route.Endpoint
	if in.ID != "" {
		if in.Protocol != "" || in.BaseURL != "" || in.Name != "" {
			fail(w, http.StatusBadRequest, "send an id or an address, not both")
			return
		}
		var ok bool
		if e, ok = c.Endpoint(in.ID); !ok {
			fail(w, http.StatusNotFound, errGone.Error())
			return
		}
		err = e.Valid()
	} else {
		in.Name = "not saved"
		e, err = in.endpoint(newID(c))
	}
	if err != nil {
		fail(w, http.StatusBadRequest, sentence(err, key))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), listWait)
	defer cancel()
	list, err := p.d.EndpointModels(ctx, e, key)
	if err != nil {
		fail(w, http.StatusBadGateway, sentence(err, key))
		return
	}
	hidden := 0
	if len(list) > maxListed {
		list, hidden = list[:maxListed], len(list)-maxListed
	}
	if len(key) >= 16 { // a server that repeats the key must not get it into the config
		n := len(list)
		list = slices.DeleteFunc(slices.Clone(list), func(m Model) bool {
			return strings.Contains(m.ID+"\n"+m.Name+"\n"+m.Developer+"\n"+m.Description+"\n"+m.ReleasedAt, key)
		})
		hidden += n - len(list)
	}
	out, more := offer(list, e.ID, []route.Endpoint{e})
	reply(w, http.StatusOK, map[string]any{"models": out, "hidden": hidden + more})
}

// sentence is an error for the page, without the key.
func sentence(err error, key string) string {
	s := strings.ReplaceAll(err.Error(), "\n", "; ")
	switch {
	case key == "":
	case len(key) >= 4:
		s = strings.ReplaceAll(s, key, "[hidden]")
	default: // a key of a letter or two is part of many words
		s = regexp.MustCompile(`(^|[^A-Za-z0-9])`+regexp.QuoteMeta(key)+`($|[^A-Za-z0-9])`).ReplaceAllString(s, "${1}[hidden]${2}")
	}
	return clean(s, 300)
}
