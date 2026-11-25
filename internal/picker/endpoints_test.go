package picker

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Lookoff-AIMLAPI/second-life/internal/route"
)

var idShape = regexp.MustCompile(`^ep-[0-9a-f]{8}$`)

type answer struct {
	ID     string
	Key    *bool
	Config route.Config
	Error  string
	Slots  []string
	Extra  int
	Models []wireModel
	Hidden int
	Keys   map[string]bool
}

func parse(t *testing.T, w *httptest.ResponseRecorder) (a answer) {
	t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), &a); err != nil {
		t.Fatal(err, w.Body)
	}
	return a
}

func (c client) want(status int, method, name, body string) answer {
	c.t.Helper()
	w := c.do(method, "/picker/api/"+name, body)
	if w.Code != status {
		c.t.Fatalf("%s %s %s: %d %s", method, name, body, w.Code, w.Body)
	}
	return parse(c.t, w)
}

func obj(kv ...string) string {
	m := map[string]string{}
	for i := 0; i < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	b, _ := json.Marshal(m)
	return string(b)
}

const (
	home  = "http://127.0.0.1:11434/v1"
	away  = "https://api.example/v1"
	other = "https://evil.example/v1"
)

// add saves an endpoint with a key and puts Opus and an extra model on it.
func add(t *testing.T, f *fake, c client, key string) string {
	t.Helper()
	id := c.want(200, "POST", "endpoint", obj("name", "Mine", "protocol", "chat", "baseUrl", away, "key", key)).ID
	cfg := f.Config()
	cfg.Slots = maps.Clone(cfg.Slots)
	cfg.Slots["opus"] = route.Target{Provider: id, Model: "big", Context: 128_000}
	cfg.Extra = []route.Target{{Provider: id, Model: "small"}, {Provider: route.AIMLAPI, Model: "a/b"}}
	body, _ := json.Marshal(cfg)
	c.want(200, "PUT", "config", string(body))
	return id
}

func TestEndpointAdd(t *testing.T) {
	f, _, c := setup(t)
	a := c.want(200, "POST", "endpoint", obj("name", "  Ollama‮\n ", "protocol", "chat", "baseUrl", " "+home+"// "))
	if !idShape.MatchString(a.ID) || a.Key == nil || *a.Key || len(f.epKeys) != 0 || len(a.Config.Endpoints) != 1 {
		t.Fatalf("%+v", a)
	}
	if want := (route.Endpoint{ID: a.ID, Name: "Ollama", Protocol: "chat", BaseURL: home}); f.cfg.Endpoints[0] != want || a.Config.Endpoints[0] != want {
		t.Fatalf("%+v", f.cfg.Endpoints)
	}
	for _, p := range []string{"responses", "anthropic"} {
		b := c.want(200, "POST", "endpoint", obj("name", "X", "protocol", p, "baseUrl", away, "key", " "+testKey+"\n"))
		if b.ID == a.ID || !*b.Key || f.epKeys[b.ID] != testKey {
			t.Fatalf("%+v %v", b, f.epKeys)
		}
	}
	one := c.want(200, "POST", "endpoint", obj("name", "X", "protocol", "chat", "baseUrl", home, "key", "x"))
	if f.epKeys[one.ID] != "x" { // local servers take any word
		t.Fatal(f.epKeys)
	}

	keys, saves := len(f.epKeys), f.saves
	ok := []string{"name", "A", "protocol", "chat", "baseUrl", away}
	for what, body := range map[string]string{
		"an id from the page": obj(append(ok, "id", "ep-00000000")...),
		"no name":             obj("protocol", "chat", "baseUrl", away),
		"blank name":          obj("name", " ‮", "protocol", "chat", "baseUrl", away),
		"protocol":            obj("name", "A", "protocol", "soap", "baseUrl", away),
		"no protocol":         obj("name", "A", "baseUrl", away),
		"no address":          obj("name", "A", "protocol", "chat"),
		"key":                 obj(append(ok, "key", "a b")...),
		"key 2":               obj(append(ok, "key", "ключ")...),
		"key 3":               obj(append(ok, "key", strings.Repeat("k", 513))...),
	} {
		if w := c.do("POST", "/picker/api/endpoint", body); w.Code != 400 {
			t.Errorf("%s: %d %s", what, w.Code, w.Body)
		}
	}
	for _, u := range badAddresses {
		for _, name := range []string{"endpoint", "endpoint-models"} {
			if w := c.do("POST", "/picker/api/"+name, strings.Replace(obj("protocol", "chat", "baseUrl", u, "name", "A"), `"name":"A",`, map[string]string{"endpoint": `"name":"A",`}[name], 1)); w.Code != 400 {
				t.Errorf("%s %s: %d", name, u, w.Code)
			}
		}
	}
	if len(f.epKeys) != keys || f.saves != saves || len(f.asked) != 0 {
		t.Fatal("a refused endpoint did something")
	}

	for len(f.cfg.Endpoints) < 20 {
		c.want(200, "POST", "endpoint", obj(ok...))
	}
	c.want(400, "POST", "endpoint", obj(append(ok, "key", testKey)...))
	if len(f.epKeys) != keys || len(f.cfg.Endpoints) != 20 {
		t.Fatal("the 21st left something")
	}
}

var badAddresses = []string{"", "x", "api.example/v1", "http://api.example/v1", "http://10.0.0.5/v1", "http://127.0.0.1.evil.example/v1", "https://u:p@api.example/v1",
	"https://api.example/v1?x=1", "https://api.example/v1?", "https://api.example/v1#", "https://api.example/v1#f", "https://api.example/v 1", `https://api.example\@evil.example/v1`,
	"https://:8080/v1", "https://", "file:///etc/passwd", "ftp://api.example", "javascript:alert(1)", "//api.example/v1", "https://api.example/" + strings.Repeat("v", 300)}

func TestEndpointAddFaults(t *testing.T) {
	f, _, c := setup(t)
	body := obj("name", "A", "protocol", "chat", "baseUrl", away, "key", testKey)
	f.keyFault = errors.New("the store refused " + testKey)
	c.want(500, "POST", "endpoint", body)
	if f.saves != 0 {
		t.Fatal("saved without its key")
	}
	f.keyFault, f.fault = nil, errors.New("disk full")
	if a := c.want(500, "POST", "endpoint", body); a.Error != "disk full" || len(f.epKeys) != 0 {
		t.Fatal(a.Error, f.epKeys)
	}
}

// The heart of it: whatever the page sends, a stored key goes to the address it was typed for.
func TestStoredKeyStaysWithItsAddress(t *testing.T) {
	f, p, c := setup(t)
	id := add(t, f, c, testKey)
	list := func(status int, kv ...string) { t.Helper(); c.want(status, "POST", "endpoint-models", obj(kv...)) }

	list(200, "id", id)
	if f.asked[0].e.BaseURL != away || f.asked[0].key != testKey {
		t.Fatalf("%+v", f.asked)
	}
	list(400, "id", id, "protocol", "chat", "baseUrl", other)
	list(400, "id", id, "baseUrl", other)
	list(400, "id", id, "protocol", "anthropic")
	list(400, "id", id, "name", "x")
	list(400, "name", "x")
	list(404, "id", "ep-00000000")
	list(404, "id", "aimlapi")
	list(200, "protocol", "chat", "baseUrl", other)
	list(200, "protocol", "chat", "baseUrl", away) // the same address, but not by id
	list(200, "protocol", "chat", "baseUrl", other, "key", "typed")
	list(200, "id", id, "key", "new")

	saved := f.Config()
	for what, e := range map[string]route.Endpoint{
		"address":  {ID: id, Name: "Mine", Protocol: "chat", BaseURL: other},
		"protocol": {ID: id, Name: "Mine", Protocol: "anthropic", BaseURL: away},
		"new id":   {ID: "ep-00000000", Name: "Mine", Protocol: "chat", BaseURL: away},
	} {
		x := p.own()
		x.Endpoints[0] = e
		for s, t := range x.Slots {
			if t.Provider == id {
				t.Provider = e.ID
				x.Slots[s] = t
			}
		}
		x.Extra = nil
		b, _ := json.Marshal(x)
		if a := c.want(400, "PUT", "config", string(b)); a.Error != errGone.Error() {
			t.Errorf("%s: %s", what, a.Error)
		}
	}
	if b, _ := json.Marshal(saved); len(f.cfg.Endpoints) != 1 || f.cfg.Endpoints[0] != saved.Endpoints[0] || f.epKeys[id] != testKey {
		t.Fatalf("a crafted config changed it: %s", b)
	}

	// A rename keeps id and key.
	a := c.want(200, "PUT", "endpoint", obj("id", id, "name", "Renamed", "protocol", "chat", "baseUrl", away+"/"))
	if a.ID != id || a.Key != nil || f.epKeys[id] != testKey || f.cfg.Endpoints[0].Name != "Renamed" {
		t.Fatalf("%+v", a)
	}
	// A new key for the same address.
	if a = c.want(200, "PUT", "endpoint", obj("id", id, "name", "Renamed", "protocol", "chat", "baseUrl", away, "key", "second")); a.ID != id || !*a.Key || f.epKeys[id] != "second" {
		t.Fatalf("%+v", a)
	}

	for _, change := range [][2]string{{"chat", "http://localhost:1/v1"}, {"anthropic", "http://localhost:1/v1"}, {"anthropic", away}, {"anthropic", away + "/x"}, {"anthropic", strings.ToUpper(away)}} {
		was := f.cfg.Endpoints[0].ID
		f.epKeys[was] = testKey
		a = c.want(200, "PUT", "endpoint", obj("id", was, "name", "Mine", "protocol", change[0], "baseUrl", change[1]))
		if a.ID == was || !idShape.MatchString(a.ID) || *a.Key || len(f.epKeys) != 0 {
			t.Fatalf("%v: %+v %v", change, a, f.epKeys)
		}
		if f.cfg.Slots["opus"] != (route.Target{Provider: a.ID, Model: "big", Context: 128_000}) || f.cfg.Extra[0].Provider != a.ID || f.cfg.Extra[1].Provider != route.AIMLAPI {
			t.Fatalf("what used it was lost: %+v", f.cfg)
		}
		list(404, "id", was)
		c.want(404, "PUT", "endpoint", obj("id", was, "name", "Mine", "protocol", "chat", "baseUrl", away))
		list(200, "id", a.ID)
	}
	// A new address with its key.
	was := f.cfg.Endpoints[0].ID
	f.epKeys[was] = testKey
	a = c.want(200, "PUT", "endpoint", obj("id", was, "name", "Mine", "protocol", "chat", "baseUrl", other, "key", "third"))
	if !*a.Key || len(f.epKeys) != 1 || f.epKeys[a.ID] != "third" {
		t.Fatalf("%+v %v", a, f.epKeys)
	}
	list(200, "id", a.ID)

	for _, x := range f.asked {
		if x.key == testKey && x.e.BaseURL != away || x.key == "typed" && x.e.BaseURL != other || x.key == "third" && x.e.BaseURL != other {
			t.Errorf("%+v", x)
		}
		if x.e.ID == id && x.e.BaseURL != away {
			t.Errorf("an id has two addresses: %+v", x)
		}
	}
	if last := f.asked[len(f.asked)-1]; last.key != "third" {
		t.Errorf("%+v", last)
	}
}

func TestEndpointEditFaults(t *testing.T) {
	f, _, c := setup(t)
	id := add(t, f, c, "first")
	saves := f.saves
	for what, body := range map[string]string{
		"address": obj("id", id, "name", "A", "protocol", "chat", "baseUrl", "http://api.example/v1", "key", "k"),
		"key":     obj("id", id, "name", "A", "protocol", "chat", "baseUrl", away, "key", "a b"),
		"name":    obj("id", id, "protocol", "chat", "baseUrl", away),
	} {
		if w := c.do("PUT", "/picker/api/endpoint", body); w.Code != 400 {
			t.Errorf("%s: %d", what, w.Code)
		}
	}
	c.want(404, "PUT", "endpoint", obj("id", "", "name", "A", "protocol", "chat", "baseUrl", away))
	move := obj("id", id, "name", "A", "protocol", "chat", "baseUrl", other)
	f.keyFault = errors.New("locked")
	if a := c.want(500, "PUT", "endpoint", move); a.Error != "locked" || f.saves != saves || f.cfg.Endpoints[0].BaseURL != away {
		t.Fatal("moved with its key left behind")
	}
	f.keyFault, f.fault = nil, errors.New("disk full")
	c.want(500, "PUT", "endpoint", obj("id", id, "name", "A", "protocol", "chat", "baseUrl", other, "key", testKey))
	if f.cfg.Endpoints[0].BaseURL != away || len(f.epKeys) != 0 {
		t.Fatal(f.cfg.Endpoints, f.epKeys)
	}
}

func TestEndpointRemove(t *testing.T) {
	f, _, c := setup(t)
	id := add(t, f, c, testKey)
	saves := f.saves
	a := c.want(409, "DELETE", "endpoint", obj("id", id))
	if !slices.Equal(a.Slots, []string{"opus"}) || a.Extra != 1 || f.saves != saves || f.epKeys[id] != testKey {
		t.Fatalf("%+v", a)
	}
	f.keyFault = errors.New("locked")
	c.want(500, "DELETE", "endpoint", `{"id":"`+id+`","move":true}`)
	if f.saves != saves {
		t.Fatal("removed, with its key left behind")
	}
	f.keyFault = nil
	a = c.want(200, "DELETE", "endpoint", `{"id":"`+id+`","move":true}`)
	if len(f.cfg.Endpoints) != 0 || len(f.epKeys) != 0 || f.cfg.Slots["opus"] != route.Default().Slots["opus"] || len(f.cfg.Extra) != 1 || f.cfg.Extra[0].Provider != route.AIMLAPI || len(a.Config.Endpoints) != 0 {
		t.Fatalf("%+v %v", f.cfg, f.epKeys)
	}
	c.want(404, "DELETE", "endpoint", `{"id":"`+id+`","move":true}`)
	free := c.want(200, "POST", "endpoint", obj("name", "A", "protocol", "chat", "baseUrl", away, "key", "k")).ID
	c.want(200, "DELETE", "endpoint", obj("id", free))
	if len(f.cfg.Endpoints)+len(f.epKeys) != 0 {
		t.Fatal(f.cfg, f.epKeys)
	}
}

func TestSaveAndEndpoints(t *testing.T) {
	f, p, c := setup(t)
	one := add(t, f, c, "one")
	two := c.want(200, "POST", "endpoint", obj("name", "Two", "protocol", "anthropic", "baseUrl", home, "key", "two")).ID

	x := p.own()
	x.Endpoints = nil // left out of the JSON: a page that does not know of them drops none
	b, _ := json.Marshal(x)
	if a := c.want(200, "PUT", "config", string(b)); len(a.Config.Endpoints) != 2 || len(f.epKeys) != 2 {
		t.Fatalf("%+v", a)
	}
	x = p.own()
	x.Endpoints = x.Endpoints[:1]
	b, _ = json.Marshal(x)
	c.want(200, "PUT", "config", string(b))
	if len(f.cfg.Endpoints) != 1 || f.epKeys[one] != "one" || f.epKeys[two] != "" {
		t.Fatal(f.cfg.Endpoints, f.epKeys)
	}
	bare := p.own()
	bare.Endpoints = nil
	b, _ = json.Marshal(bare)
	c.want(400, "PUT", "config", strings.Replace(string(b), `{`, `{"endpoints":[],`, 1)) // Opus is on it
	bare.Slots, bare.Extra = route.Default().Slots, nil
	b, _ = json.Marshal(bare)
	f.keyFault = errors.New("locked")
	c.want(500, "PUT", "config", strings.Replace(string(b), `{`, `{"endpoints":[],`, 1))
	f.keyFault = nil
	if len(f.cfg.Endpoints) != 1 || f.epKeys[one] != "one" {
		t.Fatal("dropped before the save")
	}
	c.want(200, "PUT", "config", strings.Replace(string(b), `{`, `{"endpoints":[],`, 1))
	if len(f.cfg.Endpoints)+len(f.epKeys) != 0 {
		t.Fatal(f.cfg.Endpoints, f.epKeys)
	}
	old := route.Default() // a window that still shows the removed endpoint
	old.Slots["opus"] = route.Target{Provider: one, Model: "big"}
	b, _ = json.Marshal(old)
	if a := c.want(400, "PUT", "config", string(b)); a.Error != errGone.Error() {
		t.Error(a.Error)
	}
}

func TestEndpointKeys(t *testing.T) {
	f, _, c := setup(t)
	id := add(t, f, c, testKey)
	none := c.want(200, "POST", "endpoint", obj("name", "B", "protocol", "chat", "baseUrl", home)).ID
	if a := c.want(200, "GET", "endpoint-key", ""); len(a.Keys) != 2 || !a.Keys[id] || a.Keys[none] {
		t.Fatalf("%+v", a)
	}
	f.keyFault = errors.New("the store refused")
	c.want(500, "DELETE", "endpoint-key", obj("id", id))
	f.keyFault = nil
	c.want(200, "DELETE", "endpoint-key", obj("id", id))
	c.want(404, "DELETE", "endpoint-key", obj("id", "aimlapi"))
	c.want(404, "DELETE", "endpoint-key", obj("id", "ep-00000000"))
	if len(f.epKeys) != 0 {
		t.Fatal(f.epKeys)
	}
}

func TestEndpointModels(t *testing.T) {
	f, _, c := setup(t)
	id := add(t, f, c, "")
	f.epModels = []Model{
		{ID: "llama4:8b", Name: "<img src=x onerror=alert(1)>‮", Context: 8192}, {ID: "LLAMA4:8B"}, {ID: "bad id"}, {ID: "plain"}, {ID: "x/" + testKey}, {ID: "y", Name: "key " + testKey},
	}
	a := c.want(200, "POST", "endpoint-models", obj("id", id, "key", testKey)) // do() checks that the key is not in it
	if len(a.Models) != 2 || a.Hidden != 4 || a.Models[0].Name != "<img src=x onerror=alert(1)>" || a.Models[0].Context != 8192 || a.Models[1].Name != "plain" {
		t.Fatalf("%+v", a)
	}
	f.epModels = nil
	if w := c.do("POST", "/picker/api/endpoint-models", obj("id", id)); strings.TrimSpace(w.Body.String()) != `{"hidden":0,"models":[]}` {
		t.Fatal(w.Body)
	}
	for i := range maxListed + 5 {
		f.epModels = append(f.epModels, Model{ID: fmt.Sprint("m", i)})
	}
	if a = c.want(200, "POST", "endpoint-models", obj("id", id)); len(a.Models) != maxListed || a.Hidden != 5 {
		t.Fatal(len(a.Models), a.Hidden)
	}
	f.epModels = nil
	for _, key := range []string{testKey, "pa55", "k1"} {
		f.epFault = errors.New("HTTP 401 for " + key + ". ‮Bye")
		if a = c.want(502, "POST", "endpoint-models", obj("id", id, "key", key)); a.Error != "HTTP 401 for [hidden]. Bye" {
			t.Errorf("%q", a.Error)
		}
	}
	// A config from the disk that route would refuse is not asked either.
	f.epFault, f.asked = nil, nil
	f.cfg.Endpoints[0].BaseURL = "http://169.254.169.254/latest"
	c.want(400, "POST", "endpoint-models", obj("id", id))
	if len(f.asked) != 0 {
		t.Fatal(f.asked)
	}
}

func TestEndpointTest(t *testing.T) {
	f, _, c := setup(t)
	body := `{"provider":"ep-0123abcd","model":"m"}`
	c.want(400, "POST", "test", body)
	id := add(t, f, c, "")
	c.want(200, "POST", "test", strings.Replace(body, "ep-0123abcd", id, 1))
	if f.tested.Provider != id {
		t.Fatal(f.tested)
	}
}

func TestStateHasPresetsAndNoKeys(t *testing.T) {
	f, _, c := setup(t)
	add(t, f, c, testKey)
	w := c.do("GET", "/picker/api/state", "")
	for _, p := range []string{"http://127.0.0.1:11434/v1", "http://127.0.0.1:1234/v1", "https://api.openai.com/v1", "https://openrouter.ai/api/v1"} {
		if !strings.Contains(w.Body.String(), p) {
			t.Error(p)
		}
	}
	for _, e := range presets {
		if _, err := (endpointIn{Name: e.Name, Protocol: e.Protocol, BaseURL: e.BaseURL}).endpoint("ep-00000000"); err != nil {
			t.Error(e, err)
		}
	}
}

// Two windows change things at once: none of the changes is lost.
func TestChangesDoNotRace(t *testing.T) {
	f, _, c := setup(t)
	body, _ := json.Marshal(route.Default())
	var wg sync.WaitGroup
	for i := range 10 {
		wg.Go(func() {
			c.do("POST", "/picker/api/endpoint", obj("name", fmt.Sprint("E", i), "protocol", "chat", "baseUrl", home, "key", "k"))
		})
		wg.Go(func() { c.do("PUT", "/picker/api/config", string(body)) })
	}
	wg.Wait()
	if len(f.cfg.Endpoints) != 10 || len(f.epKeys) != 10 {
		t.Fatal(len(f.cfg.Endpoints), len(f.epKeys))
	}
}
