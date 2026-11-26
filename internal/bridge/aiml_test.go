package bridge

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/Lookoff-AIMLAPI/second-life/internal/keystore"
	"github.com/Lookoff-AIMLAPI/second-life/internal/route"
)

const testKey = "aiml-test-key-0123456789abcdef"

type memKeys struct{ key string }

func (m *memKeys) Get() (string, error) {
	if m.key == "" {
		return "", keystore.ErrNone
	}
	return m.key, nil
}
func (m *memKeys) Set(k string) error { m.key = k; return nil }
func (m *memKeys) Delete() error      { m.key = ""; return nil }

const aimlStream = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"zhipu/glm-5.3\",\"content\":[],\"usage\":{\"input_tokens\":12,\"output_tokens\":0}}}\n\n" +
	"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n" +
	"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
	"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":3}}\n\n"
const aimlEnd = "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

// aimlSetup puts Opus on an AIMLAPI model served by a fake.
func aimlSetup(t *testing.T, key string, up http.HandlerFunc) (*Bridge, *httptest.Server, func() string) {
	t.Helper()
	b, s, logs, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { t.Error("Codex was called"); answer(w, r) })
	fake := httptest.NewServer(up)
	t.Cleanup(fake.Close)
	b.aiml.BaseURL = fake.URL
	b.keys.Keys = &memKeys{key: key}
	c := b.Config()
	c.Slots["opus"] = route.Target{Provider: route.AIMLAPI, Model: "zhipu/glm-5.3", Name: "GLM 5.3", Context: 200_000}
	if err := b.Save(c); err != nil {
		t.Fatal(err)
	}
	return b, s, logs.String
}

func models(w http.ResponseWriter) {
	io.WriteString(w, `{"data":[{"id":"zhipu/glm-5.3","type":"anthropic/messages","info":{"name":"GLM 5.3","contextLength":200000,"outputMax":4000}}]}`)
}

func TestAIMLStream(t *testing.T) {
	var got map[string]any
	var auth string
	b, s, logs := aimlSetup(t, testKey, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			models(w)
			return
		}
		auth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Aimlapi-Usd-Spent", "0.0001")
		io.WriteString(w, aimlStream+aimlEnd)
	})
	resp, body := post(t, b, s.URL, `{"model":"claude-opus-5-5","stream":true,"max_tokens":128000,"system":[{"type":"text","text":"x-anthropic-billing-header: z"},{"type":"text","text":"Be terse."}],"messages":[{"role":"user","content":"secret-prompt"}]}`)
	if resp.StatusCode != 200 || !strings.Contains(body, "hello") || !strings.Contains(body, "message_stop") || !strings.Contains(body, `"model":"claude-opus-5-5"`) {
		t.Fatal(resp.StatusCode, body)
	}
	if auth != "Bearer "+testKey || got["model"] != "zhipu/glm-5.3" || got["max_tokens"] != 4000.0 {
		t.Fatal("upstream got", got["model"], got["max_tokens"])
	}
	if strings.Contains(fmt.Sprint(got["system"]), "billing") || !strings.Contains(fmt.Sprint(got["system"]), "Be terse.") {
		t.Fatal(got["system"])
	}
	if st := b.Status(); st.Errors != 0 || st.InputTokens != 12 || st.OutputTokens != 3 || st.LastModel != "aimlapi/zhipu/glm-5.3" {
		t.Fatalf("%+v", st)
	}
	if l := logs(); strings.Contains(l, testKey) || strings.Contains(l, "secret-prompt") || !strings.Contains(l, "usd=0.0001") {
		t.Fatal(l)
	}
}

func TestAIMLFailures(t *testing.T) {
	for name, c := range map[string]struct {
		key, body string
		status    int
		want      int
		text      string
	}{
		"no key":    {"", "", 0, 401, "authentication_error"},
		"wrong key": {testKey, `{"type":"error","error":{"type":"authentication_error","message":"bad ` + testKey + `"}}`, 401, 401, "authentication_error"},
		"cut":       {testKey, aimlStream, 200, 200, "event: error"},
	} {
		t.Run(name, func(t *testing.T) {
			b, s, _ := aimlSetup(t, c.key, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/models") {
					models(w)
					return
				}
				w.WriteHeader(c.status)
				io.WriteString(w, c.body)
			})
			resp, body := post(t, b, s.URL, fmt.Sprintf(ask, true))
			if resp.StatusCode != c.want || !strings.Contains(body, c.text) || strings.Contains(body, testKey) || strings.Contains(body, "message_stop") {
				t.Fatal(resp.StatusCode, body)
			}
			if b.Status().Errors != 1 {
				t.Fatalf("%+v", b.Status())
			}
		})
	}
}

func TestOtherSlotsStayOnCodex(t *testing.T) {
	b, s, _, _ := setup(t, answer)
	c := b.Config()
	c.Slots["opus"] = route.Target{Provider: route.AIMLAPI, Model: "a/b"}
	if err := b.Save(c); err != nil {
		t.Fatal(err)
	}
	if resp, body := post(t, b, s.URL, `{"model":"claude-sonnet-5-5","max_tokens":9,"messages":[{"role":"user","content":"hi"}]}`); resp.StatusCode != 200 || resp.Header.Get("X-Second-Life-Model") != "gpt-5.6-terra" {
		t.Fatal(resp.StatusCode, body)
	}
	again, err := New(Options{Home: b.home, Auth: &fakeAuth{}, Keys: &memKeys{}})
	if err != nil || again.Config().Slots["opus"].Model != "a/b" {
		t.Fatal("not kept", err)
	}
}

func TestPickerIsMountedAndLocked(t *testing.T) {
	b, s, _, _ := setup(t, answer)
	r, err := http.Get(s.URL + "/picker/")
	if err != nil || r.StatusCode != 200 || r.Header.Get("Content-Security-Policy") == "" {
		t.Fatal(r.StatusCode, err)
	}
	for _, p := range []string{"state", "models", "key"} {
		if r, _ := http.Get(s.URL + "/picker/api/" + p); r.StatusCode < 400 {
			t.Error(p, r.StatusCode)
		}
	}
	if b.PickerURL() != "" {
		t.Error("a link while off")
	}
	b.update(func(st *Status) { st.State = StateOn; st.Addr = "127.0.0.1:1" })
	if u := b.PickerURL(); !strings.HasPrefix(u, "http://127.0.0.1:1/picker/#") {
		t.Error(u)
	}
}

func TestPluginsLinkAndStore(t *testing.T) {
	b, s, _, _ := setup(t, answer)
	if b.PluginsURL() != "" {
		t.Error("a link while off")
	}
	b.update(func(st *Status) { st.State = StateOn; st.Addr = "127.0.0.1:1" })
	if u := b.PluginsURL(); !regexp.MustCompile(`^http://127\.0\.0\.1:1/picker/plugins#[0-9a-f]{64}$`).MatchString(u) {
		t.Error(u)
	}
	if (pickerDeps{b}).Plugins() == nil {
		t.Error("no plugin store")
	}
	if r, _ := http.Get(s.URL + "/picker/api/plugins"); r.StatusCode < 400 {
		t.Error("plugins without sign-in:", r.StatusCode)
	}
}
