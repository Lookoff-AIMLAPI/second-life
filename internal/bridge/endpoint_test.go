package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Lookoff-AIMLAPI/second-life/internal/route"
)

const epKey = "sk-endpoint-key-0123456789"

type fakeServer struct {
	paths, auth []string
	bodies      []map[string]any
	answer      func(n int, w http.ResponseWriter, r *http.Request, body map[string]any)
}

// epSetup puts Opus on a custom endpoint.
func epSetup(t *testing.T, protocol, key string, f *fakeServer) (*Bridge, *httptest.Server, func() string) {
	t.Helper()
	b, s, logs, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { t.Error("Codex was called"); answer(w, r) })
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.paths, f.auth, f.bodies = append(f.paths, r.URL.Path), append(f.auth, r.Header.Get("Authorization")), append(f.bodies, body)
		f.answer(len(f.paths)-1, w, r, body)
	}))
	t.Cleanup(up.Close)
	b.newKeys = func(id string) Keys { return &memKeys{key: key} }
	c := b.Config()
	c.Endpoints = []route.Endpoint{{ID: "ep-0123abcd", Name: "Mine", Protocol: protocol, BaseURL: up.URL + "/v1"}}
	c.Slots["opus"] = route.Target{Provider: "ep-0123abcd", Model: "llama-4"}
	if err := b.Save(c); err != nil {
		t.Fatal(err)
	}
	return b, s, logs.String
}

func chatAnswer(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, d := range []string{`{"id":"x","choices":[{"index":0,"delta":{"role":"assistant","content":"hel"}}]}`, `{"choices":[{"index":0,"delta":{"content":"lo"}}]}`, `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, `{"choices":[],"usage":{"prompt_tokens":9,"completion_tokens":2}}`} {
		fmt.Fprintf(w, "data: %s\n\n", d)
	}
	io.WriteString(w, "data: [DONE]\n\n")
}

const askTools = `{"model":"claude-opus-5-5","stream":%v,"max_tokens":64,"output_config":{"effort":"medium"},"tools":[{"name":"t","input_schema":{"type":"object","properties":{}}}],"messages":[{"role":"user","content":"secret-prompt"}]}`

func TestEndpointProtocols(t *testing.T) {
	for _, stream := range []bool{true, false} {
		for protocol, path := range map[string]string{route.Chat: "/v1/chat/completions", route.Responses: "/v1/responses", route.Anthropic: "/v1/messages"} {
			t.Run(fmt.Sprint(protocol, stream), func(t *testing.T) {
				f := &fakeServer{answer: func(_ int, w http.ResponseWriter, r *http.Request, body map[string]any) {
					switch protocol {
					case route.Chat:
						chatAnswer(w)
					case route.Responses:
						answer(w, r)
					default:
						if body["stream"] == true {
							w.Header().Set("Content-Type", "text/event-stream")
							io.WriteString(w, aimlStream+aimlEnd)
							return
						}
						io.WriteString(w, `{"id":"m","type":"message","role":"assistant","model":"llama-4","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":9,"output_tokens":2}}`)
					}
				}}
				b, s, logs := epSetup(t, protocol, epKey, f)
				resp, body := post(t, b, s.URL, fmt.Sprintf(askTools, stream))
				text := strings.ReplaceAll(strings.ReplaceAll(body, `"text":"hel"`, `"text":"hello"`), `"text":"lo"`, "")
				if resp.StatusCode != 200 || !strings.Contains(text, "hello") || strings.Contains(body, "event: error") || resp.Header.Get("X-Second-Life-Model") != "ep-0123abcd/llama-4" {
					t.Fatal(resp.StatusCode, body)
				}
				if f.paths[0] != path || f.auth[0] != "Bearer "+epKey || f.bodies[0]["model"] != "llama-4" {
					t.Fatal(f.paths, f.auth, f.bodies[0]["model"])
				}
				if st := b.Status(); st.Errors != 0 || st.InputTokens == 0 || st.OutputTokens == 0 {
					t.Fatalf("%+v", st)
				}
				if l := logs(); strings.Contains(l, epKey) || strings.Contains(l, "secret-prompt") {
					t.Fatal(l)
				}
			})
		}
	}
}

func TestEndpointLearnsWhatIsRefused(t *testing.T) {
	refuse := []string{"stream_options", "reasoning_effort"}
	f := &fakeServer{answer: func(_ int, w http.ResponseWriter, _ *http.Request, body map[string]any) {
		for _, k := range refuse {
			if _, ok := body[k]; ok {
				w.WriteHeader(400)
				fmt.Fprintf(w, `{"error":{"message":"Unsupported parameter: '%s'","type":"invalid_request_error","param":"%s","code":"unsupported_parameter"}}`, k, k)
				return
			}
		}
		chatAnswer(w)
	}}
	b, s, logs := epSetup(t, route.Chat, "", f)
	if resp, body := post(t, b, s.URL, fmt.Sprintf(askTools, true)); resp.StatusCode != 200 || !strings.Contains(body, "message_stop") {
		t.Fatal(resp.StatusCode, body)
	}
	first := len(f.paths)
	if first != 3 || f.auth[0] != "" {
		t.Fatal(first, f.auth)
	}
	if resp, _ := post(t, b, s.URL, fmt.Sprintf(askTools, true)); resp.StatusCode != 200 || len(f.paths) != first+1 {
		t.Fatal("not remembered", len(f.paths))
	}
	if strings.Count(logs(), "adjust endpoint=") != 2 {
		t.Fatal(logs())
	}
}

func TestEndpointRefusalEnds(t *testing.T) {
	f := &fakeServer{answer: func(n int, w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		w.WriteHeader(400)
		fmt.Fprintf(w, `{"error":{"message":"Unsupported parameter: 'p%d'","param":"p%d","code":"unsupported_parameter"}}`, n, n)
	}}
	b, s, _ := epSetup(t, route.Chat, epKey, f)
	if resp, body := post(t, b, s.URL, fmt.Sprintf(askTools, true)); resp.StatusCode < 400 || len(f.paths) > maxAdjust+1 || strings.Contains(body, epKey) {
		t.Fatal(resp.StatusCode, len(f.paths), body)
	}
}

func TestEndpointIsNotTheBridge(t *testing.T) {
	b, s, _ := epSetup(t, route.Anthropic, epKey, &fakeServer{answer: func(int, http.ResponseWriter, *http.Request, map[string]any) { t.Error("called") }})
	b.update(func(st *Status) { st.Addr = "127.0.0.1:8787" })
	c := b.Config()
	for _, u := range []string{"http://127.0.0.1:8787/v1", "http://localhost:8787/v1", "http://[::1]:8787/v1"} {
		c.Endpoints[0].BaseURL = u
		if err := b.Save(c); err != nil {
			t.Fatal(err)
		}
		if resp, body := post(t, b, s.URL, fmt.Sprintf(askTools, true)); resp.StatusCode != 400 || !strings.Contains(body, "bridge itself") {
			t.Fatal(u, resp.StatusCode, body)
		}
		if _, err := (pickerDeps{b}).EndpointModels(context.Background(), c.Endpoints[0], ""); err == nil {
			t.Fatal("listed", u)
		}
	}
}

func TestEndpointBadKey(t *testing.T) {
	f := &fakeServer{answer: func(_ int, w http.ResponseWriter, r *http.Request, _ map[string]any) {
		w.WriteHeader(401)
		fmt.Fprintf(w, `{"error":{"message":"bad key %s"}}`, r.Header.Get("Authorization"))
	}}
	b, s, logs := epSetup(t, route.Chat, epKey, f)
	resp, body := post(t, b, s.URL, fmt.Sprintf(askTools, true))
	if resp.StatusCode != 401 || strings.Contains(body+logs()+b.Status().LastError, epKey) || len(f.paths) != 1 {
		t.Fatal(resp.StatusCode, body)
	}
}

func TestEndpointModelsAndTest(t *testing.T) {
	f := &fakeServer{answer: func(_ int, w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			fmt.Fprintf(w, `{"data":[{"id":"llama-4"},{"id":"echo-%s"}]}`, epKey)
			return
		}
		chatAnswer(w)
	}}
	b, _, _ := epSetup(t, route.Chat, epKey, f)
	list, err := (pickerDeps{b}).EndpointModels(context.Background(), b.Config().Endpoints[0], "")
	if err != nil || len(list) != 1 || list[0].ID != "llama-4" {
		t.Fatal(list, err)
	}
	if ms, _, err := b.Test(context.Background(), b.Config().Slots["opus"]); err != nil || ms < 0 {
		t.Fatal(ms, err)
	}
	if !b.aimlReady() {
		t.Error("a server of one's own is enough without Codex")
	}
}
