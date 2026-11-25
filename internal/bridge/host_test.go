package bridge

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestForeignHostIsRefused(t *testing.T) {
	b, s, _, _ := setup(t, answer)
	for host, want := range map[string]int{"evil.example": 403, "127.0.0.1.evil.example:8787": 403, "localhost:8787": 200, "127.0.0.1": 200, "[::1]:8787": 200} {
		r, _ := http.NewRequest("GET", s.URL+"/healthz", nil)
		r.Host = host
		resp, err := http.DefaultClient.Do(r)
		if err != nil || resp.StatusCode != want {
			t.Errorf("%s: %v %v", host, resp.StatusCode, err)
		}
	}
	b.addr = "0.0.0.0:8787"
	r, _ := http.NewRequest("GET", s.URL+"/healthz", nil)
	r.Host = "bridge:8787"
	if resp, err := http.DefaultClient.Do(r); err != nil || resp.StatusCode != 200 {
		t.Error("container name refused")
	}
}

func TestUpstreamEchoDoesNotLeakToken(t *testing.T) {
	b, s, logs, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		fmt.Fprintf(w, `{"error":{"message":"bad header %s"}}`, r.Header.Get("Authorization"))
	})
	_, body := post(t, b, s.URL, fmt.Sprintf(ask, true))
	if all := body + logs.String() + b.Status().LastError; strings.Contains(all, "stale") || !strings.Contains(body, "[token]") {
		t.Fatal(all)
	}
}
