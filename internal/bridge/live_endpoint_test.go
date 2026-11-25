//go:build live

package bridge

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Lookoff-AIMLAPI/second-life/internal/codex"
	"github.com/Lookoff-AIMLAPI/second-life/internal/route"
)

// liveEndpoint puts every Claude name on one custom endpoint.
func liveEndpoint(t *testing.T, protocol, base, model, key string) *Bridge {
	t.Helper()
	b, err := New(Options{Home: t.TempDir(), Auth: &fakeAuth{}, Keys: &memKeys{}, KeysFor: func(string) Keys { return &memKeys{key: key} }, Log: log.New(testWriter{t}, "", 0)})
	if err != nil {
		t.Fatal(err)
	}
	c := b.Config()
	c.Endpoints = []route.Endpoint{{ID: "ep-0123abcd", Name: "Live", Protocol: protocol, BaseURL: base}}
	for _, s := range route.Slots {
		c.Slots[s] = route.Target{Provider: "ep-0123abcd", Model: model}
	}
	if err := b.Save(c); err != nil {
		t.Fatal(err)
	}
	return b
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimSpace(string(p)))
	return len(p), nil
}

// through runs a check against another bridge.
func through(t *testing.T, b *Bridge, f func()) {
	s := httptest.NewServer(b.handler())
	defer s.Close()
	ob, ou := liveB, liveURL
	liveB, liveURL = b, s.URL
	defer func() { liveB, liveURL = ob, ou }()
	f()
}

func checks(t *testing.T) {
	m := ok(t, M{"model": "opus", "messages": L{u("Reply with exactly: pong")}})
	if !strings.Contains(strings.ToLower(text(m)), "pong") {
		t.Fatal(m)
	}
	TestLiveToolChoice(t)
	if us := uses(ok(t, M{"model": "opus", "tools": L{weather}, "messages": L{u("Weather in Paris via the tool."), M{"role": "assistant", "content": L{M{"type": "tool_use", "id": "toolu_1", "name": "get_weather", "input": M{"city": "Paris"}}}}, u(L{M{"type": "tool_result", "tool_use_id": "toolu_1", "content": "11C"}, txt("Answer with the number only.")})}})); len(us) != 0 {
		t.Fatal("called again")
	}
}

func TestLiveGenericResponses(t *testing.T) {
	auth := &codex.Client{Version: "test"}
	defer auth.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cred, err := auth.Credentials(r.Context(), "")
		if err != nil {
			w.WriteHeader(500)
			return
		}
		req, _ := http.NewRequestWithContext(r.Context(), "POST", defaultUpstream+"/responses", r.Body)
		req.Header.Set("Authorization", "Bearer "+cred.Token)
		req.Header.Set("ChatGPT-Account-ID", cred.AccountID)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("originator", "second_life")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			w.WriteHeader(502)
			return
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(flushing{w}, resp.Body)
	}))
	defer proxy.Close()
	through(t, liveEndpoint(t, route.Responses, proxy.URL+"/v1", "gpt-5.6-luna", ""), func() { checks(t) })
}

func TestLiveMessagesEndpoint(t *testing.T) {
	through(t, liveEndpoint(t, route.Anthropic, liveURL+"/v1", "gpt-5.6-luna", liveB.Token()), func() { checks(t) })
}
