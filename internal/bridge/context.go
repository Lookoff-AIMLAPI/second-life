package bridge

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"sync"

	"github.com/Lookoff-AIMLAPI/second-life/internal/dto"
)

// Codex models hold 272k tokens, while Claude Code believes in a larger window for the ids it knows. The margin leaves
// room for the answer and for the compaction request itself.
const defaultMaxContext = 240_000

func maxContext() int {
	if n, err := strconv.Atoi(os.Getenv("SLIFE_MAX_CONTEXT")); err == nil && n > 0 {
		return n
	}
	return defaultMaxContext
}

// contextGuard asks for compaction once each time a conversation grows over the cap. Only once: the compaction request
// carries the same history and must get through.
type contextGuard struct {
	mu    sync.Mutex
	cap   int
	talks map[string]*talk
	order []string
}

type talk struct {
	ratio float64 // real input tokens per estimated token
	asked bool
}

const maxTalks = 256

// talkKey tells conversations apart; a session and its subagents share the session id but not the first message.
func talkKey(session string, req *dto.MessagesRequest) string {
	if session == "" || len(req.Messages) == 0 {
		return ""
	}
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00", session, req.Messages[0].Role)
	h.Write(req.Messages[0].Content)
	return string(h.Sum(nil)[:16])
}

func (g *contextGuard) get(key string) *talk {
	if t := g.talks[key]; t != nil {
		return t
	}
	if g.talks == nil {
		g.talks = map[string]*talk{}
	}
	if len(g.order) >= maxTalks {
		delete(g.talks, g.order[0])
		g.order = g.order[1:]
	}
	t := &talk{ratio: 1}
	g.talks[key] = t
	g.order = append(g.order, key)
	return t
}

// check returns the error Claude Code answers with compaction, or nil.
func (g *contextGuard) check(key string, estimate, limit int) *dto.APIError {
	if key == "" {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	t := g.get(key)
	n := int(float64(estimate) * t.ratio)
	if n <= limit {
		t.asked = false
		return nil
	}
	if t.asked {
		return nil
	}
	t.asked = true
	return &dto.APIError{Status: 400, Type: "invalid_request_error", Message: fmt.Sprintf("prompt is too long: %d tokens > %d maximum", n, limit)}
}

// scale turns an estimate into what Codex would likely count.
func (g *contextGuard) scale(key string, estimate int) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	if t := g.talks[key]; t != nil {
		return int(float64(estimate) * t.ratio)
	}
	return estimate
}

// learn corrects the estimate with what Codex counted.
func (g *contextGuard) learn(key string, estimate int, u dto.Usage) {
	real := u.InputTokens + u.CacheReadInputTokens
	if key == "" || estimate < 1000 || real == 0 {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.get(key).ratio = min(2, max(0.5, float64(real)/float64(estimate)))
}

// estimateTokens is cheap on purpose: it only has to grow with the request, learn does the rest.
func estimateTokens(body *dto.ResponsesRequest) int {
	n := len(body.Instructions)
	for _, item := range body.Input {
		n += itemSize(item)
	}
	for _, t := range body.Tools {
		if b, err := json.Marshal(t); err == nil {
			n += len(b)
		}
	}
	return n / 4
}

func itemSize(item map[string]any) int {
	n := 0
	for k, v := range item {
		switch v := v.(type) {
		case string:
			if k != "encrypted_content" {
				n += len(v)
			}
		case []map[string]any:
			for _, p := range v {
				if s, ok := p["text"].(string); ok {
					n += len(s)
				} else {
					n += 6400 // an image or a file, about 1600 tokens
				}
			}
		case []any:
			if b, err := json.Marshal(v); err == nil {
				n += len(b)
			}
		}
	}
	return n
}
