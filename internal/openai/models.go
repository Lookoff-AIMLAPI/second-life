package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Lookoff-AIMLAPI/second-life/internal/route"
)

const (
	maxModelBody  = 16 << 20
	maxModels     = 2000
	modelsTimeout = 30 * time.Second // the list only, never an answer
)

// Model is one model of an endpoint. Zero means unknown.
type Model struct {
	ID, Name           string
	Context, OutputMax int
}

var idShape = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/\[\]-]{0,127}$`) // as in package route

// Models lists what the endpoint serves. Not every server has a list.
func (c *Client) Models(ctx context.Context) ([]Model, error) {
	ctx, cancel := context.WithTimeout(ctx, modelsTimeout)
	defer cancel()
	key, e := c.key()
	if e != nil {
		return nil, e
	}
	path := "/models"
	if c.Endpoint.Protocol == route.Anthropic {
		path += "?limit=1000"
	}
	req, e := c.request(ctx, "GET", path, key, nil)
	if e != nil {
		return nil, e
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach %s", c.name())
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s did not list its models: HTTP %d", c.name(), resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxModelBody+1))
	if err != nil {
		return nil, fmt.Errorf("could not read the model list")
	}
	if len(raw) > maxModelBody {
		return nil, fmt.Errorf("the model list is over 16 MB")
	}
	return parseModels(raw)
}

// parseModels reads the lists of OpenAI, Anthropic, Ollama, Gemini, LM Studio, vLLM, llama.cpp, OpenRouter, Groq and
// Together. One bad entry does not hide the rest.
func parseModels(raw []byte) ([]Model, error) {
	var items []any
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return nil, errModels
	}
	switch v := v.(type) {
	case []any:
		items = v
	case map[string]any:
		found := false
		for _, k := range []string{"data", "models", "result"} {
			list, ok := v[k].([]any)
			_, there := v[k]
			found = found || ok || there && v[k] == nil // an empty list may be null
			if items = list; len(items) > 0 {
				break
			}
		}
		if !found {
			return nil, errModels
		}
	default:
		return nil, errModels
	}
	out, seen := []Model{}, map[string]bool{}
	for _, item := range items {
		m := map[string]any{}
		switch item := item.(type) {
		case string:
			m["id"] = item
		case map[string]any:
			m = item
		}
		id := first(m, "id", "model", "name", "key", "slug")
		if !idShape.MatchString(id) || seen[strings.ToLower(id)] || len(out) == maxModels {
			continue
		}
		seen[strings.ToLower(id)] = true
		meta, _ := m["meta"].(map[string]any)        // llama.cpp
		top, _ := m["top_provider"].(map[string]any) // OpenRouter
		name := label(first(m, "display_name", "displayName", "name"), 100)
		if name == id {
			name = ""
		}
		out = append(out, Model{ID: id, Name: name,
			Context:   size([]map[string]any{m, meta}, "loaded_context_length", "max_model_len", "context_length", "context_window", "max_context_length", "max_input_tokens", "inputTokenLimit", "n_ctx", "n_ctx_train"),
			OutputMax: size([]map[string]any{m, top}, "max_completion_tokens", "max_output_tokens", "outputTokenLimit"),
		})
	}
	return out, nil
}

func first(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, _ := m[k].(string); s != "" {
			return s
		}
	}
	return ""
}

func size(from []map[string]any, keys ...string) int {
	for _, m := range from {
		for _, k := range keys {
			if f, ok := m[k].(float64); ok && f >= 1 && f <= 10_000_000 && f == math.Trunc(f) {
				return int(f)
			}
		}
	}
	return 0
}

// label is one line of printable text, at most limit bytes. Invisible characters could make one model look like another.
func label(s string, limit int) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsSpace(r):
			return ' '
		case !unicode.IsPrint(r):
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, ""))
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > limit {
		s = s[:limit]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return strings.TrimSpace(s)
}
