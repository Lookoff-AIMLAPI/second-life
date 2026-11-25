package bridge

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Upstream may keep the body open after its last event; the answer must not wait for it.
func TestAnswerDoesNotWaitForUpstreamClose(t *testing.T) {
	for _, stream := range []bool{true, false} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			b, s, _, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
				answer(w, r)
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			})
			began := time.Now()
			resp, body := post(t, b, s.URL, fmt.Sprintf(ask, stream))
			if resp.StatusCode != 200 || !strings.Contains(body, "hello") || time.Since(began) > 3*time.Second {
				t.Fatal(resp.StatusCode, time.Since(began), body)
			}
			if st := b.Status(); st.Errors != 0 {
				t.Fatalf("%+v", st)
			}
		})
	}
	b, s, _, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		sse(w, created, `{"type":"response.failed","response":{"error":{"code":"context_length_exceeded","message":"too long"}}}`)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	if resp, body := post(t, b, s.URL, fmt.Sprintf(ask, true)); resp.StatusCode != 400 || !strings.Contains(body, "prompt is too long") {
		t.Fatal(resp.StatusCode, body)
	}
}
