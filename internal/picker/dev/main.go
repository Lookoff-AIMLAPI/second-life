//go:build dev

// Command dev shows the picker with made-up models: go run -tags dev ./internal/picker/dev
// Open /dev/open to sign in. /dev/set?models=ok|fail|empty|slow&key=none|bad|ok&save=ok|fail&test=ok|fail|slow&desktop=on|off|fail
// changes what the fakes answer. &keystore=fail refuses endpoint keys, &list=fail|slow changes every endpoint list. An
// endpoint whose address has "fail", "nolist", "empty", "slow", "evil" or "keyed" in it answers so. /dev/asked shows which
// addresses were asked and with which key.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Lookoff-AIMLAPI/second-life/internal/picker"
	"github.com/Lookoff-AIMLAPI/second-life/internal/route"
)

const addr = "127.0.0.1:18901"

type fake struct {
	mu  sync.Mutex
	cfg route.Config
	set map[string]string

	keys  map[string]string
	asked []string
}

func (f *fake) get(k string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.set[k]
}

func (f *fake) put(k, v string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.set[k] = v
}

func (f *fake) Config() route.Config {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cfg
}

func (f *fake) Save(c route.Config) error {
	if f.get("save") == "fail" {
		return errors.New("the disk is full")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cfg = c
	log.Printf("saved %d endpoints", len(c.Endpoints))
	return nil
}

func (f *fake) AIMLAPIModels(ctx context.Context) ([]picker.Model, error) {
	switch f.get("models") {
	case "fail":
		return nil, errors.New("AIMLAPI answered 503")
	case "empty":
		return nil, nil
	case "slow":
		select {
		case <-time.After(5 * time.Second):
		case <-ctx.Done():
		}
	}
	return models(), nil
}

func (f *fake) KeyState(context.Context) (bool, bool, string) {
	switch f.get("key") {
	case "ok":
		return true, true, ""
	case "bad":
		return true, false, "AIMLAPI answered 401."
	}
	return false, false, ""
}

func (f *fake) SetKey(k string) error {
	if strings.Contains(k, "fail") {
		return errors.New("the keychain refused " + k)
	}
	f.put("key", "ok")
	if strings.Contains(k, "bad") {
		f.put("key", "bad")
	}
	return nil
}

func (f *fake) DeleteKey() error { f.put("key", "none"); return nil }

func (f *fake) Test(ctx context.Context, t route.Target) (int64, string, error) {
	switch f.get("test") {
	case "fail":
		return 0, "", errors.New("<b>401</b> invalid key")
	case "slow":
		select {
		case <-time.After(4 * time.Second):
		case <-ctx.Done():
		}
	}
	return 842, "It said “ok”.", nil
}

func (f *fake) DesktopConfigured() bool { return f.get("desktop") != "off" }

func (f *fake) ApplyDesktop() error {
	time.Sleep(time.Second)
	if f.get("desktop") == "fail" {
		return errors.New("Claude did not quit")
	}
	return nil
}

func (f *fake) EndpointModels(ctx context.Context, e route.Endpoint, key string) ([]picker.Model, error) {
	f.mu.Lock()
	from := "typed"
	if key == "" {
		key, from = f.keys[e.ID], "stored"
	}
	f.asked = append(f.asked, fmt.Sprintf("%s %s %s key=%q (%s)", e.ID, e.Protocol, e.BaseURL, key, from))
	f.mu.Unlock()
	has := func(s string) bool { return strings.Contains(e.BaseURL, s) }
	if has("slow") || f.get("list") == "slow" {
		select {
		case <-time.After(4 * time.Second):
		case <-ctx.Done():
			return nil, errors.New("the server did not answer in time")
		}
	}
	switch {
	case has("fail") || f.get("list") == "fail":
		return nil, errors.New("could not reach the server")
	case has("nolist"):
		return nil, errors.New("the server has no model list (HTTP 404)")
	case has("empty"):
		return nil, nil
	case has("keyed") && key != "good":
		return nil, fmt.Errorf("the server refused the key %s (HTTP 401)", key)
	case has("evil"):
		return []picker.Model{{ID: "evil/one", Name: `<img src=x onerror=alert(1)><script>alert(2)</script>`, Developer: "\u202egpj.exe"}, {ID: "bad id"}}, nil
	}
	return []picker.Model{
		{ID: "llama4:8b"}, {ID: "qwen4-coder:30b", Name: "Qwen4 Coder 30B", Context: 262_144}, {ID: "tiny:1b", Context: 8_192}, {ID: "gpt-oss:20b"}, {ID: "claude-opus-5-5"},
	}, nil
}

func (f *fake) EndpointKey(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.keys[id] != ""
}

func (f *fake) SetEndpointKey(id, k string) error {
	if f.get("keystore") == "fail" {
		return errors.New("the keychain refused " + k)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys[id] = k
	return nil
}

func (f *fake) DeleteEndpointKey(id string) error {
	if f.get("keystore") == "fail" {
		return errors.New("the keychain is locked")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.keys, id)
	return nil
}

func models() []picker.Model {
	out := []picker.Model{
		{ID: "zhipu/glm-5.3", Name: "GLM 5.3", Developer: "Zhipu AI", Description: "General model for text and code.", Context: 200_000, OutputMax: 128_000, ReleasedAt: "2026-09-01"},
		{ID: "moonshot/kimi-k3", Name: "Kimi K3", Developer: "Moonshot AI", Context: 1_048_576, OutputMax: 64_000, ReleasedAt: "2026-10-10"},
		{ID: "evil/html", Name: `<img src=x onerror=alert(1)><script>alert(2)</script>`, Developer: `"><svg onload=alert(3)>`, Description: `<a href="javascript:alert(4)">click</a>`, Context: 128_000, ReleasedAt: "2026-11-20"},
		{ID: "odd/right-to-left", Name: "‮gpj.exe אבג مرحبا", Developer: "Odd & Co", Context: 32_000, ReleasedAt: "2026-11-19"},
		{ID: "odd/long-name-" + strings.Repeat("x", 100), Name: strings.Repeat("Verylongnamewithoutspaces", 8), Developer: strings.Repeat("Longdeveloper", 10), Context: 16_385, OutputMax: 4096, ReleasedAt: "2026-11-18"},
		{ID: "odd/emoji", Name: "模型 👩‍💻 Zoë", Developer: "Ünïcode", Context: 99_999, ReleasedAt: "2026-11-17"},
		{ID: "odd/no-name", ReleasedAt: "2026-11-16"},
		{ID: "odd/control", Name: "line\nbreak\x00\x07", Context: -5, ReleasedAt: "2026-11-15"},
		{ID: "claude-opus-4-1", Name: "Claude Opus 4.1", Developer: "Anthropic", Context: 200_000},
		{ID: "bad id with spaces", Name: "Not usable"},
		{ID: "ZHIPU/GLM-5.3", Name: "Repeated"},
	}
	devs := []string{"Alibaba", "DeepSeek", "Google", "Meta", "Mistral", "xAI"}
	for i := 0; len(out) < 309; i++ { // 307 usable
		d := devs[i%len(devs)]
		out = append(out, picker.Model{
			ID: fmt.Sprintf("%s/model-%03d", strings.ToLower(d), i), Name: fmt.Sprintf("%s Model %03d", d, i), Developer: d,
			Context: []int{8_192, 32_768, 128_000, 256_000, 1_000_000, 0}[i%6], OutputMax: 8192,
			ReleasedAt: fmt.Sprintf("2025-%02d-%02d", i%12+1, i%28+1),
		})
	}
	return out
}

func main() {
	f := &fake{cfg: route.Default(), set: map[string]string{}, keys: map[string]string{}}
	p := picker.New(f)
	mux := http.NewServeMux()
	mux.Handle("/picker/", p.Handler())
	mux.HandleFunc("/dev/open", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, p.URL("http://"+addr), http.StatusSeeOther)
	})
	mux.HandleFunc("/dev/set", func(w http.ResponseWriter, r *http.Request) {
		for k, v := range r.URL.Query() {
			f.put(k, v[0])
		}
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("/dev/asked", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		fmt.Fprintf(w, "%s\nkeys %v\n", strings.Join(f.asked, "\n"), f.keys)
	})
	log.Print("http://" + addr + "/dev/open")
	log.Fatal(http.ListenAndServe(addr, mux))
}
