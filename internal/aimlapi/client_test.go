package aimlapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Lookoff-AIMLAPI/second-life/internal/dto"
)

const testKey = "test-key-0123456789abcdef"

func serve(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return &Client{BaseURL: s.URL, HTTP: s.Client(), Version: "1.2.3", Key: func() (string, error) { return testKey, nil }}
}

const listing = `{"object":"list","data":[
 {"id":"zhipu/glm-5.3","type":"anthropic/messages","info":{"name":"GLM\u202e 5.3\n\u200b","developer":"Zhipu AI","description":"a\tb\u0007","contextLength":1000000,"outputMax":131072,"releasedAt":"2026-08-01"},"aliases":[]},
 {"id":"zhipu/glm-5.3","type":"openai/chat-completions","info":{}},
 {"id":"zhipu/glm-5.3","type":"anthropic/messages","info":{"name":"twice"}},
 {"id":"bad id\n","type":"anthropic/messages","info":{}},
 {"id":7,"type":"anthropic/messages"}, null, "x", {"id":"a/info","type":"anthropic/messages","info":"x"},
 {"id":"a/b","type":"anthropic/messages","info":{"name":7,"contextLength":"x","outputMax":-5,"releasedAt":"<b>"}},
 {"id":"a/c","type":"anthropic/messages"}]}`

func TestModels(t *testing.T) {
	calls, status, body := 0, 200, listing
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/models" || r.URL.Query().Get("type") != "anthropic/messages" || r.Header.Get("Authorization") != "" || r.Header.Get("Anthropic-Version") != "" {
			t.Errorf("request %s %v", r.URL, r.Header)
		}
		w.WriteHeader(status)
		io.WriteString(w, body)
	})
	clock := time.Unix(1_800_000_000, 0)
	now = func() time.Time { return clock }
	defer func() { now = time.Now }()
	want := []Model{
		{"zhipu/glm-5.3", "GLM 5.3", "Zhipu AI", "a b", 1000000, 131072, "2026-08-01"},
		{ID: "a/b"}, {ID: "a/c"},
	}
	for range 2 {
		got, err := c.Models(context.Background())
		if err != nil || fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("%v\n got %+v\nwant %+v", err, got, want)
		}
		got[0].ID = "changed by the caller"
	}
	if calls != 1 {
		t.Errorf("%d calls", calls)
	}
	clock, status = clock.Add(modelsFresh), 500
	if got, err := c.Models(context.Background()); err != nil || len(got) != 3 || calls != 2 {
		t.Errorf("a failed refresh lost the list: %v %v, %d calls", got, err, calls)
	}
	status, body = 200, `{"data":[]}`
	if got, err := c.Models(context.Background()); err != nil || len(got) != 0 || calls != 3 {
		t.Errorf("%v %v, %d calls", got, err, calls)
	}
}

func TestModelsFailures(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"status":   func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) },
		"redirect": func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/elsewhere", 302) },
		"not JSON": func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "<html>") },
		"no data":  func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "{}") },
		"too big": func(w http.ResponseWriter, _ *http.Request) {
			io.WriteString(w, `{"data":[],"x":"`)
			io.WriteString(w, strings.Repeat("x", maxModels))
			io.WriteString(w, `"}`)
		},
	} {
		if got, err := serve(t, h).Models(context.Background()); err == nil {
			t.Errorf("%s: %v", name, got)
		}
	}
}

func TestAddress(t *testing.T) {
	for in, want := range map[string]string{
		"": "https://api.aimlapi.com/v1/messages", "https://a.b/v1/": "https://a.b/v1/messages", "https://a.b": "https://a.b/v1/messages",
		"http://127.0.0.1:9": "http://127.0.0.1:9/v1/messages", "http://localhost:9/": "http://localhost:9/v1/messages",
		"http://a.b": "", "ftp://a.b": "", "a.b": "", "https://u:p@a.b": "", "https://a.b?x=1": "", "http://localhost.a.b": "",
	} {
		if got, err := (&Client{BaseURL: in}).address("/v1/messages"); got != want || (err == nil) != (want != "") {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
}

func TestSend(t *testing.T) {
	var got http.Header
	var body string
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got, body = r.Header, r.Method+" "+r.URL.Path+" "+string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: ping\n\n")
	})
	in := http.Header{}
	in.Add("Anthropic-Beta", "effort-2025-11-24, oauth-2025-04-20,advisor-tool-2026-03-01, bad value,effort-2025-11-24")
	in.Add("Anthropic-Beta", "context-management-2025-06-27,\"x\",")
	for k, v := range map[string]string{"Anthropic-Version": "2024-01-02", "Authorization": "Bearer local", "X-Api-Key": "local", "Cookie": "a=b", "X-Stainless-Os": "x", "X-Claude-Code-Session-Id": "s", "User-Agent": "claude"} {
		in.Set(k, v)
	}
	resp, e := c.Send(context.Background(), []byte(`{"n":1.0}`), in)
	if e != nil {
		t.Fatal(e)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "event: ping\n\n" || body != `POST /v1/messages {"n":1.0}` {
		t.Errorf("%q %q", b, body)
	}
	want := map[string]string{
		"Authorization": "Bearer " + testKey, "Content-Type": "application/json", "Accept": "application/json, text/event-stream",
		"Accept-Encoding": "identity", "User-Agent": "second-life/1.2.3", "Anthropic-Version": "2024-01-02",
		"Anthropic-Beta": "effort-2025-11-24,context-management-2025-06-27", "Content-Length": "9",
	}
	for k := range got {
		if got.Get(k) != want[k] {
			t.Errorf("%s: %q, want %q", k, got.Get(k), want[k])
		}
	}
	if len(got) != len(want) {
		t.Errorf("headers %v", got)
	}
	c.Version = "bad\nversion"
	if resp, e = c.Send(context.Background(), nil, http.Header{"Anthropic-Version": {"x"}}); e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if got.Get("Anthropic-Version") != "2023-06-01" || got.Get("User-Agent") != "second-life/dev" || got.Values("Anthropic-Beta") != nil {
		t.Errorf("defaults: %v", got)
	}
}

func TestSendErrors(t *testing.T) {
	funds := "You've run out of funds. Please top up your balance or update your payment method to continue: https://aimlapi.com/app/billing/"
	problem := func(status int, msg, extra string) string {
		return fmt.Sprintf(`{"title":"x","status":%d,"message":%q,"instance":"/v1/messages","requestId":"r","error":{"name":"E","message":%q%s}}`, status, msg, msg, extra)
	}
	for _, c := range []struct {
		name   string
		status int
		retry  string
		body   string
		want   dto.APIError
	}{
		{"anthropic", 429, "7", `{"type":"error","error":{"type":"rate_limit_error","message":"slow"}}`, dto.APIError{Status: 429, Type: "rate_limit_error", Message: "slow", RetryAfter: "7"}},
		{"overloaded", 529, "", `{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`, dto.APIError{Status: 529, Type: "overloaded_error", Message: "busy"}},
		{"openai", 400, "", `{"error":{"message":"m","type":"invalid_request_error","code":"x","request_id":"r"}}`, dto.APIError{Status: 400, Type: "invalid_request_error", Message: "m"}},
		{"context", 400, "", `{"error":{"message":"too many tokens","type":"invalid_request_error","code":"context_length_exceeded"}}`, dto.APIError{Status: 400, Type: "invalid_request_error", Message: "prompt is too long: too many tokens"}},
		{"context from Anthropic", 400, "", problem(400, "prompt is too long: 2 tokens > 1 maximum context length", ""), dto.APIError{Status: 400, Type: "invalid_request_error", Message: "prompt is too long: 2 tokens > 1 maximum context length"}},
		{"validation", 400, "", problem(400, "Validation failed", `,"type":"invalid_request_error","details":[{"path":"tools.0.type","reason":"Tool type 'x' is hosted.","code":"c"},7,{"path":"a","reason":"a: no"}]`), dto.APIError{Status: 400, Type: "invalid_request_error", Message: "Validation failed tools.0.type: Tool type 'x' is hosted. a: no"}},
		{"wrong key", 401, "", problem(401, "This request requires a valid API key.", ""), dto.APIError{Status: 401, Type: "authentication_error", Message: "the AIMLAPI key is wrong: This request requires a valid API key."}},
		{"no money", 403, "", problem(403, funds, ""), dto.APIError{Status: 402, Type: "billing_error", Message: noBalance}},
		{"payment required", 402, "", "", dto.APIError{Status: 402, Type: "billing_error", Message: noBalance}},
		{"key quota", 403, "", problem(403, "API key quota exceeded (DAILY_LIMIT_EXCEEDED).", ""), dto.APIError{Status: 403, Type: "permission_error", Message: "API key quota exceeded (DAILY_LIMIT_EXCEEDED)."}},
		{"unknown model", 404, "", problem(404, "no such model", ""), dto.APIError{Status: 404, Type: "not_found_error", Message: "no such model"}},
		{"gateway page", 503, "junk", "<html>" + testKey, dto.APIError{Status: 503, Type: "overloaded_error", Message: "AIMLAPI returned HTTP 503"}},
		{"redirect", 302, "", "", dto.APIError{Status: 502, Type: "api_error", Message: "AIMLAPI returned HTTP 302"}},
		{"no content", 204, "", "", dto.APIError{Status: 502, Type: "api_error", Message: "AIMLAPI returned HTTP 204"}},
		{"echoed key", 400, "", `{"error":{"message":"bad\u0007 \u0074est-key-0123456789abcdef here"}}`, dto.APIError{Status: 400, Type: "invalid_request_error", Message: "bad [key] here"}},
		{"key in the type", 400, "", `{"error":{"type":"` + testKey + `","message":"m"}}`, dto.APIError{Status: 400, Type: "invalid_request_error", Message: "m"}},
		{"funds in a prompt", 400, "", problem(400, "out of funds", ""), dto.APIError{Status: 400, Type: "invalid_request_error", Message: "out of funds"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			hits := 0
			cl := serve(t, func(w http.ResponseWriter, r *http.Request) {
				hits++
				w.Header().Set("Location", "/v1/elsewhere")
				if c.retry != "" {
					w.Header().Set("Retry-After", c.retry)
				}
				w.WriteHeader(c.status)
				io.WriteString(w, c.body)
			})
			resp, e := cl.Send(context.Background(), []byte("{}"), nil)
			if resp != nil || e == nil || *e != c.want || hits != 1 {
				t.Errorf("%d hits\n got %+v\nwant %+v", hits, e, c.want)
			}
		})
	}
	t.Run("key cut by the cap", func(t *testing.T) {
		cl := serve(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(400)
			fmt.Fprintf(w, `{"error":{"message":"%s%s"}}`, strings.Repeat("x", maxMessage-8), testKey)
		})
		if _, e := cl.Send(context.Background(), nil, nil); strings.Contains(e.Message, testKey[:5]) || len(e.Message) > maxMessage+10 {
			t.Error("part of the key is left")
		}
	})
	t.Run("endless body", func(t *testing.T) {
		cl := serve(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(500)
			for range 64 {
				if _, err := io.WriteString(w, strings.Repeat("x", 1<<20)); err != nil {
					return
				}
			}
			t.Error("the whole body was read")
		})
		if _, e := cl.Send(context.Background(), nil, nil); e.Status != 500 || e.Type != "api_error" {
			t.Errorf("%+v", e)
		}
	})
	t.Run("page instead of an answer", func(t *testing.T) {
		cl := serve(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			io.WriteString(w, "<html>")
		})
		if _, e := cl.Send(context.Background(), nil, nil); e == nil || e.Status != 502 {
			t.Errorf("%+v", e)
		}
	})
	t.Run("unreachable", func(t *testing.T) {
		cl := serve(t, func(http.ResponseWriter, *http.Request) {})
		cl.BaseURL = "http://127.0.0.1:1"
		if _, e := cl.Send(context.Background(), nil, nil); e == nil || e.Status != 502 || e.Message != "could not reach AIMLAPI" {
			t.Errorf("%+v", e)
		}
	})
}

func TestSendKey(t *testing.T) {
	c := serve(t, func(http.ResponseWriter, *http.Request) { t.Error("a request left without a key") })
	for name, f := range map[string]func() (string, error){
		"none": nil, "empty": func() (string, error) { return "", nil },
		"error":  func() (string, error) { return "", errors.New("locked") },
		"header": func() (string, error) { return "abc\r\nX: y", nil },
	} {
		c.Key = f
		if _, e := c.Send(context.Background(), nil, nil); e == nil || e.Status != 401 || e.Type != "authentication_error" {
			t.Errorf("%s: %+v", name, e)
		}
	}
	c.BaseURL, c.Key = "http://example.com", func() (string, error) { return testKey, nil }
	if _, e := c.Send(context.Background(), nil, nil); e == nil || e.Status != 500 {
		t.Errorf("plain http: %+v", e)
	}
}

// The inference path has no timer of its own below the 15 minutes a provider may stay silent.
func TestNoTimers(t *testing.T) {
	c := &Client{HTTP: &http.Client{Timeout: time.Second}}
	if c.client().Timeout != 0 || base().Timeout != 0 {
		t.Error("overall timeout")
	}
	tr := base().Transport.(*http.Transport)
	if tr.ResponseHeaderTimeout != 0 || tr.TLSHandshakeTimeout != 0 {
		t.Error("transport timers")
	}
}

func TestRetryAfter(t *testing.T) {
	now = func() time.Time { return time.Unix(1_800_000_000, 0) }
	defer func() { now = time.Now }()
	for in, want := range map[string]string{
		"": "", "0": "1", "7": "7", "1.2": "2", "-1": "", "NaN": "", "1e99": "", "x": "",
		time.Unix(1_800_000_030, 0).UTC().Format(http.TimeFormat): "30", time.Unix(1, 0).UTC().Format(http.TimeFormat): "1",
	} {
		if got := retryAfter(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}
