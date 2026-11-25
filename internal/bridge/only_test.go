package bridge

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Lookoff-AIMLAPI/second-life/internal/codex"
	"github.com/Lookoff-AIMLAPI/second-life/internal/route"
)

// noCodex is a machine without the Codex CLI, or with it signed out.
type noCodex struct {
	fakeAuth
	err error
}

func (n *noCodex) Account(context.Context) (*codex.Account, error) { return nil, n.err }
func (n *noCodex) Credentials(context.Context, string) (codex.Credentials, error) {
	if n.err != nil {
		return codex.Credentials{}, n.err
	}
	return codex.Credentials{}, codex.ErrSignedOut
}

func waitState(t *testing.T, b *Bridge, want State) Status {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for b.Status().State != want && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if st := b.Status(); st.State != want {
		t.Fatalf("state %s, want %s", st.State, want)
	}
	return b.Status()
}

func TestAIMLAPIAloneIsEnough(t *testing.T) {
	for name, c := range map[string]struct {
		err  error
		idle State
	}{"no CLI": {codex.ErrMissing, StateCodexMissing}, "signed out": {nil, StateNeedsLogin}} {
		t.Run(name, func(t *testing.T) {
			b, err := New(Options{Home: t.TempDir(), Addr: "127.0.0.1:0", Auth: &noCodex{err: c.err}, Keys: &memKeys{}, Log: log.New(io.Discard, "", 0)})
			if err != nil {
				t.Fatal(err)
			}
			if err := b.Start(); err != nil {
				t.Fatal(err)
			}
			defer b.Stop()
			waitState(t, b, c.idle)
			cfg := b.Config()
			cfg.Slots["opus"] = route.Target{Provider: route.AIMLAPI, Model: "zhipu/glm-5.3"}
			if err := b.Save(cfg); err != nil {
				t.Fatal(err)
			}
			if err := b.SetKey(testKey); err != nil {
				t.Fatal(err)
			}
			if st := waitState(t, b, StateOn); !st.AIMLOnly || st.SignedIn || st.CodexMissing != (c.err != nil) {
				t.Fatalf("%+v", st)
			}
			r, _ := http.NewRequest("POST", "http://"+b.Status().Addr+"/v1/messages", strings.NewReader(`{"model":"haiku","max_tokens":9,"messages":[{"role":"user","content":"hi"}]}`))
			r.Header.Set("X-Api-Key", b.Token())
			resp, err := http.DefaultClient.Do(r)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 401 || !strings.Contains(string(body), "choose an AIMLAPI model") {
				t.Fatal(resp.StatusCode, fmt.Sprintf("%s", body))
			}
			if err := b.DeleteKey(); err != nil {
				t.Fatal(err)
			}
			waitState(t, b, c.idle)
		})
	}
}
