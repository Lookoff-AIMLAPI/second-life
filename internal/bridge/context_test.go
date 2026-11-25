package bridge

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/Lookoff-AIMLAPI/second-life/internal/dto"
)

func TestContextGuard(t *testing.T) {
	g := contextGuard{cap: 1000}
	if g.check("a", 900, 1000) != nil || g.check("", 9_000_000, 1000) != nil {
		t.Fatal("too early")
	}
	e := g.check("a", 1100, 1000)
	if e == nil || e.Status != 400 || e.Message != "prompt is too long: 1100 tokens > 1000 maximum" {
		t.Fatal(e)
	}
	if g.check("a", 1150, 1000) != nil {
		t.Fatal("the compaction request must get through")
	}
	if g.check("a", 300, 1000) != nil || g.check("a", 1200, 1000) == nil {
		t.Fatal("must ask again after a compaction")
	}
	g.learn("b", 1000, dto.Usage{InputTokens: 300, CacheReadInputTokens: 1100})
	if g.check("b", 800, 1000) == nil {
		t.Fatal("learned ratio ignored")
	}
	g.learn("c", 2000, dto.Usage{InputTokens: 1})
	if g.check("c", 1900, 1000) != nil {
		t.Fatal("ratio must stay within bounds")
	}
	for i := range 3 * maxTalks {
		g.check(fmt.Sprint(i), 1, 1000)
	}
	if len(g.talks) > maxTalks || len(g.order) > maxTalks {
		t.Fatal("grows without end")
	}
}

func TestGuardOnTheWire(t *testing.T) {
	calls := 0
	b, s, _, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { calls++; answer(w, r) })
	b.guard.cap = 500
	send := func(session string, size int) (int, string) {
		r, _ := http.NewRequest("POST", s.URL+"/v1/messages", strings.NewReader(`{"model":"opus","max_tokens":9,"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"`+strings.Repeat("x", size)+`"},{"role":"user","content":"go"}]}`))
		r.Header.Set("X-Api-Key", b.Token())
		if session != "" {
			r.Header.Set("X-Claude-Code-Session-Id", session)
		}
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var sb strings.Builder
		buf := make([]byte, 4096)
		n, _ := resp.Body.Read(buf)
		sb.Write(buf[:n])
		return resp.StatusCode, sb.String()
	}
	if st, _ := send("s", 100); st != 200 {
		t.Fatal(st)
	}
	if st, body := send("s", 9000); st != 400 || !strings.Contains(body, "prompt is too long: ") || calls != 1 {
		t.Fatal(st, body, calls)
	}
	if st, _ := send("s", 9000); st != 200 {
		t.Fatal("second try must pass")
	}
	if st, _ := send("", 9000); st != 200 {
		t.Fatal("plain API clients are not guarded")
	}
}
