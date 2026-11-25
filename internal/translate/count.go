package translate

import (
	"encoding/json"
	"unicode/utf8"

	"github.com/Lookoff-AIMLAPI/second-life/internal/dto"
)

const (
	countImage = 1600
	countFile  = 3000
)

// CountTokens estimates the input of a request: Codex has no counting call. It counts what would be sent upstream, so
// hidden deferred tools, signatures and base64 cost nothing.
func CountTokens(req *dto.MessagesRequest) int {
	if req == nil {
		return 0
	}
	var c reqCounter
	if tr, err := ToResponses(req, "", ""); err == nil {
		c.text(tr.Body.Instructions)
		for _, item := range tr.Body.Input {
			c.item(item)
		}
		for _, t := range tr.Body.Tools {
			c.json(t)
		}
		if tr.Body.Text != nil {
			c.json(tr.Body.Text)
		}
		return c.total()
	}
	// The request would be refused; a count still helps the client.
	c.raw(req.System)
	for _, m := range req.Messages {
		c.raw(m.Content)
	}
	for _, t := range req.Tools {
		c.json(t)
	}
	return c.total()
}

type reqCounter struct{ chars, tokens int }

func (c *reqCounter) total() int { return c.tokens + (c.chars+3)/4 }

func (c *reqCounter) text(s string) { c.chars += utf8.RuneCountInString(s) }

func (c *reqCounter) json(v any) {
	if b, err := json.Marshal(v); err == nil {
		c.text(string(b))
	}
}

// item counts one upstream input item.
func (c *reqCounter) item(item map[string]any) {
	if item["type"] == "additional_tools" {
		c.json(item["tools"])
		return
	}
	for _, k := range []string{"name", "arguments", "output", "content"} {
		switch v := item[k].(type) {
		case string:
			c.text(v)
		case []map[string]any:
			for _, p := range v {
				switch p["type"] {
				case "input_image":
					c.tokens += countImage
				case "input_file":
					c.tokens += countFile
				default:
					s, _ := p["text"].(string)
					c.text(s)
				}
			}
		}
	}
}

func (c *reqCounter) raw(raw json.RawMessage) {
	var v any
	if json.Unmarshal(raw, &v) == nil {
		c.walk(v)
	}
}

// walk counts Claude content that could not be translated.
func (c *reqCounter) walk(v any) {
	switch v := v.(type) {
	case string:
		c.text(v)
	case []any:
		for _, e := range v {
			c.walk(e)
		}
	case map[string]any:
		switch v["type"] {
		case "image":
			c.tokens += countImage
			return
		case "document":
			src, _ := v["source"].(map[string]any)
			switch src["type"] {
			case "text":
				c.walk(src["data"])
			case "content":
				c.walk(src["content"])
			default:
				c.tokens += countFile
			}
			c.walk(v["title"])
			c.walk(v["context"])
			return
		}
		for _, k := range []string{"text", "title", "url", "name", "content", "stdout", "stderr"} {
			c.walk(v[k])
		}
		if in, ok := v["input"]; ok {
			c.json(in)
		}
	}
}
