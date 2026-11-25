package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Lookoff-AIMLAPI/second-life/internal/codex"
)

type fakeAuth struct{ refreshes int }

func (f *fakeAuth) Account(context.Context) (*codex.Account, error) {
	return &codex.Account{Plan: "plus"}, nil
}
func (f *fakeAuth) Limits(context.Context) (*codex.Window, *codex.Window, error) {
	return nil, nil, nil
}
func (f *fakeAuth) Credentials(_ context.Context, rejected string) (codex.Credentials, error) {
	if rejected != "" {
		f.refreshes++
		return codex.Credentials{Token: "fresh", AccountID: "acct"}, nil
	}
	return codex.Credentials{Token: "stale", AccountID: "acct"}, nil
}
func (f *fakeAuth) Login(context.Context) (string, error) { return "https://x", nil }
func (f *fakeAuth) Close()                                {}

func setup(t *testing.T, up http.HandlerFunc) (*Bridge, *httptest.Server, *bytes.Buffer, *fakeAuth) {
	t.Helper()
	u := httptest.NewServer(up)
	t.Cleanup(u.Close)
	var logs bytes.Buffer
	a := &fakeAuth{}
	b, err := New(Options{Home: t.TempDir(), Upstream: u.URL, Auth: a, Keys: &memKeys{}, Log: log.New(&logs, "", 0)})
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewServer(b.handler())
	t.Cleanup(s.Close)
	return b, s, &logs, a
}

func post(t *testing.T, b *Bridge, url, body string) (*http.Response, string) {
	t.Helper()
	r, _ := http.NewRequest("POST", url+"/v1/messages?beta=true", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+b.Token())
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp, string(raw)
}

func sse(w io.Writer, evs ...string) {
	for _, e := range evs {
		fmt.Fprintf(w, "data: %s\n\n", e)
	}
}

const created = `{"type":"response.created","response":{"id":"resp_1"}}`
const completed = `{"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":10,"input_tokens_details":{"cached_tokens":4},"output_tokens":3}}}`
const ask = `{"model":"claude-opus-5-5","stream":%v,"max_tokens":10,"messages":[{"role":"user","content":"secret-prompt"}]}`

func answer(w http.ResponseWriter, _ *http.Request) {
	sse(w, created, `{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"m1"}}`,
		`{"type":"response.output_text.delta","output_index":0,"item_id":"m1","delta":"hello"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"m1","content":[{"type":"output_text","text":"hello"}]}}`, completed)
}

func TestStreamAndNonStream(t *testing.T) {
	var got map[string]any
	b, s, logs, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		answer(w, r)
	})
	resp, body := post(t, b, s.URL, fmt.Sprintf(ask, true))
	if resp.StatusCode != 200 || !strings.Contains(body, "event: message_stop") || !strings.Contains(body, "hello") {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if got["model"] != "gpt-5.6-sol" || got["store"] != false {
		t.Fatalf("upstream got %v", got)
	}
	resp, body = post(t, b, s.URL, fmt.Sprintf(ask, false))
	if resp.StatusCode != 200 || !strings.Contains(body, `"stop_reason":"end_turn"`) || !strings.Contains(body, `"cache_read_input_tokens":4`) {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if strings.Contains(logs.String(), "secret-prompt") || strings.Contains(logs.String(), b.Token()) {
		t.Fatal("log leaks content")
	}
}

func TestLocalTokenRequired(t *testing.T) {
	_, s, _, _ := setup(t, answer)
	for _, p := range []string{"/v1/messages", "/v1/messages/count_tokens"} {
		r, err := http.Post(s.URL+p, "application/json", strings.NewReader(`{}`))
		if err != nil || r.StatusCode != 401 {
			t.Fatalf("%s: %v %v", p, r.StatusCode, err)
		}
	}
	r, _ := http.Get(s.URL + "/admin/status")
	if r.StatusCode != 401 {
		t.Fatal("status open")
	}
	r, _ = http.Head(s.URL + "/api/hello")
	if r.StatusCode != 200 {
		t.Fatal("hello closed")
	}
}

func TestRefreshOnceOn401(t *testing.T) {
	b, s, _, a := setup(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fresh" {
			w.WriteHeader(401)
			return
		}
		answer(w, r)
	})
	if resp, body := post(t, b, s.URL, fmt.Sprintf(ask, true)); resp.StatusCode != 200 || a.refreshes != 1 {
		t.Fatal(resp.StatusCode, a.refreshes, body)
	}
}

func TestEarlyFailureKeepsStatus(t *testing.T) {
	b, s, _, _ := setup(t, func(w http.ResponseWriter, _ *http.Request) {
		sse(w, created, `{"type":"response.failed","response":{"error":{"code":"context_length_exceeded","message":"too long"}}}`)
	})
	resp, body := post(t, b, s.URL, fmt.Sprintf(ask, true))
	if resp.StatusCode != 400 || !strings.Contains(body, "prompt is too long: ") {
		t.Fatal(resp.StatusCode, body)
	}
}

func TestCutStreamIsAnError(t *testing.T) {
	b, s, _, _ := setup(t, func(w http.ResponseWriter, _ *http.Request) {
		sse(w, created, `{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"m1"}}`,
			`{"type":"response.output_text.delta","output_index":0,"item_id":"m1","delta":"par"}`)
	})
	_, body := post(t, b, s.URL, fmt.Sprintf(ask, true))
	if !strings.Contains(body, "event: error") || strings.Contains(body, "message_stop") {
		t.Fatal(body)
	}
	if st := b.Status(); st.Errors != 1 {
		t.Fatalf("%+v", st)
	}
}

func TestClientGoneStopsUpstream(t *testing.T) {
	gone := make(chan struct{})
	b, s, _, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		sse(w, created)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(gone)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	r, _ := http.NewRequestWithContext(ctx, "POST", s.URL+"/v1/messages", strings.NewReader(fmt.Sprintf(ask, true)))
	r.Header.Set("X-Api-Key", b.Token())
	_, _ = http.DefaultClient.Do(r)
	select {
	case <-gone:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream kept running")
	}
}

func TestLocalTokenFile(t *testing.T) {
	h := filepath.Join(t.TempDir(), "x")
	a, err := LocalToken(h)
	if err != nil {
		t.Fatal(err)
	}
	b, err := LocalToken(h)
	if err != nil || a != b || len(a) != 64 {
		t.Fatal(a, b, err)
	}
	if st, _ := os.Stat(filepath.Join(h, "token")); st.Mode().Perm() != 0o600 && os.PathSeparator == '/' {
		t.Fatal(st.Mode())
	}
}

func TestStartStop(t *testing.T) {
	b, err := New(Options{Home: t.TempDir(), Addr: "127.0.0.1:0", Auth: &fakeAuth{}, Log: log.New(io.Discard, "", 0)})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := b.Start(); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(2 * time.Second)
		for b.Status().State != StateOn && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		if b.Status().State != StateOn {
			t.Fatalf("%+v", b.Status())
		}
		b.Stop()
		if b.Status().State != StateOff {
			t.Fatal("not off")
		}
	}
}
