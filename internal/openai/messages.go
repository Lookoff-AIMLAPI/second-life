package openai

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/Lookoff-AIMLAPI/second-life/internal/aimlapi"
	"github.com/Lookoff-AIMLAPI/second-life/internal/dto"
	"github.com/Lookoff-AIMLAPI/second-life/internal/translate"
)

// What PrepareMessages reports. The texts are static: they go to the log.
const (
	warnTools   = "deferred tools were applied to tools: this model does not read them in the conversation"
	warnThought = "thinking from another model was dropped from the history: only that model can read it"
)

const (
	billing    = "x-anthropic-billing-header:"
	chatSigned = "c2r1." // a signature of translate.ChatStream
	codexSign  = "c2c1." // one that aimlapi.Prepare drops
	unsigned   = "c2k"   // marks thinking that came without a signature, between the steps below
)

type object = map[string]json.RawMessage

// PrepareMessages makes a Claude request fit a custom Messages endpoint. aimlapi.Prepare does the history: the traces
// of other models, and for a model that is not Claude the changes of its tools. Its rules for the AIMLAPI endpoint
// alone (the fields that endpoint refuses) do not apply here.
func PrepareMessages(raw []byte, model string, o translate.Options) (body []byte, warnings []string, e *dto.APIError) {
	var top object
	if json.Unmarshal(raw, &top) != nil || top == nil {
		return nil, nil, invalid("request must be a JSON object")
	}
	if t := bytes.TrimSpace(top["thread"]); len(t) > 0 && string(t) != "null" {
		return nil, nil, invalid("thread: message threads are not supported; send the whole conversation")
	}
	claude := aimlapi.Native(model)
	top["model"] = encode(model)
	if sys, changed := withoutBilling(top["system"]); changed {
		if top["system"] = sys; sys == nil {
			delete(top, "system")
		}
	}

	h := &history{claude: claude, refs: map[string]bool{}}
	part := object{"model": top["model"]}
	if top["messages"] != nil {
		part["messages"] = h.each(top["messages"], h.before)
	}
	if !claude && top["tools"] != nil {
		part["tools"] = top["tools"]
	}
	done, warnings, e := aimlapi.Prepare(encode(part), model)
	if e != nil {
		return nil, nil, e
	}
	var out object
	if json.Unmarshal(done, &out) != nil {
		return nil, nil, invalid("request is not valid JSON")
	}
	if h.kept {
		out["messages"] = h.each(out["messages"], after)
	}
	if o.SystemFirstOnly {
		out["messages"] = foldSystem(out["messages"])
	}
	if out["messages"] != nil {
		top["messages"] = out["messages"]
	}
	if tools := out["tools"]; !claude && tools != nil {
		var c struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(top["tool_choice"], &c)
		h.refs[c.Name] = true
		var changed bool
		if top["tools"], changed = h.tools(tools); changed || h.said {
			warnings = append(warnings, warnTools)
		}
	}
	for _, f := range o.Omit {
		if f != "messages" && f != "model" && f != "tools" && f != "max_tokens" && f != "stream" {
			delete(top, f)
		}
	}
	body = encode(top)
	if o.MaxOutput > 0 {
		var warn string
		if body, warn = aimlapi.CapMaxTokens(body, o.MaxOutput); warn != "" {
			warnings = append(warnings, warn)
		}
	}
	if h.foreign {
		for i, w := range warnings {
			if strings.HasPrefix(w, "thinking from Codex") {
				warnings[i] = warnThought
			}
		}
	}
	return body, warnings, nil
}

func invalid(msg string) *dto.APIError {
	return &dto.APIError{Status: 400, Type: "invalid_request_error", Message: msg}
}

// encode keeps < > & as they are.
func encode(v any) json.RawMessage {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if enc.Encode(v) != nil {
		return nil
	}
	return bytes.TrimSpace(b.Bytes())
}

func str(raw json.RawMessage) string {
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

// list decodes an array of objects; false for anything else.
func list(raw json.RawMessage) ([]object, bool) {
	var out []object
	if json.Unmarshal(raw, &out) != nil || out == nil {
		return nil, false
	}
	for _, o := range out {
		if o == nil {
			return nil, false
		}
	}
	return out, true
}

func withoutBilling(raw json.RawMessage) (json.RawMessage, bool) {
	blocks, ok := list(raw)
	if !ok {
		return raw, false
	}
	var kept []object
	for _, b := range blocks {
		if !strings.HasPrefix(str(b["text"]), billing) {
			kept = append(kept, b)
		}
	}
	switch {
	case len(kept) == len(blocks):
		return raw, false
	case len(kept) == 0:
		return nil, true
	}
	return encode(kept), true
}

type history struct {
	claude  bool
	kept    bool // some thinking without a signature was marked
	foreign bool // some thinking of a Chat model was there
	said    bool // a tool reference became text
	refs    map[string]bool
}

// each runs fn over the blocks of every message and keeps what it leaves alone.
func (h *history) each(raw json.RawMessage, fn func(object) bool) json.RawMessage {
	msgs, ok := list(raw)
	if !ok {
		return raw
	}
	any := false
	for _, m := range msgs {
		blocks, ok := list(m["content"])
		if !ok {
			continue
		}
		changed := false
		for _, b := range blocks {
			changed = fn(b) || changed
		}
		if changed {
			m["content"], any = encode(blocks), true
		}
	}
	if !any {
		return raw
	}
	return encode(msgs)
}

// before prepares a block for aimlapi.Prepare.
func (h *history) before(b object) bool {
	switch str(b["type"]) {
	case "thinking":
		sig := str(b["signature"])
		switch {
		case strings.HasPrefix(sig, chatSigned):
			b["signature"], h.foreign = encode(codexSign+sig[len(chatSigned):]), true
			return true
		case sig == "" && !h.claude: // only Claude's own servers need one
			b["signature"], h.kept = encode(unsigned), true
			return true
		}
	case "tool_use":
		h.refs[str(b["name"])] = true
	case "tool_result":
		if h.claude {
			return false
		}
		inner, ok := list(b["content"])
		changed := false
		for i, in := range inner {
			if name := str(in["tool_name"]); ok && str(in["type"]) == "tool_reference" && name != "" {
				h.refs[name], h.said, changed = true, true, true
				inner[i] = object{"type": encode("text"), "text": encode("Tool " + name + " is now available.")}
			}
		}
		if changed {
			b["content"] = encode(inner)
		}
		return changed
	}
	return false
}

func after(b object) bool {
	if str(b["type"]) == "thinking" && str(b["signature"]) == unsigned {
		delete(b, "signature")
		return true
	}
	return false
}

// tools leaves out the deferred tools nothing has loaded and loads the rest.
func (h *history) tools(raw json.RawMessage) (json.RawMessage, bool) {
	all, ok := list(raw)
	if !ok {
		return raw, false
	}
	kept, changed := []object{}, false
	for _, t := range all {
		if string(t["defer_loading"]) == "true" {
			changed = true
			if !h.refs[str(t["name"])] {
				continue
			}
			delete(t, "defer_loading")
		}
		kept = append(kept, t)
	}
	if !changed {
		return raw, false
	}
	return encode(kept), true
}

// foldSystem turns the system messages of a conversation into text of the user, in place.
func foldSystem(raw json.RawMessage) json.RawMessage {
	msgs, ok := list(raw)
	if !ok {
		return raw
	}
	type turn struct {
		object
		folded bool
	}
	var out []turn
	any := false
	for _, m := range msgs {
		t := turn{object: m}
		if str(m["role"]) == "system" {
			any = true
			blocks := blocksOf(m["content"])
			kept := blocks[:0]
			for _, b := range blocks {
				if str(b["type"]) == "text" && str(b["text"]) != "" {
					b["text"] = encode(translate.Remind(str(b["text"])))
					kept = append(kept, b)
				}
			}
			if len(kept) == 0 {
				continue
			}
			t = turn{object{"role": encode("user"), "content": encode(kept)}, true}
		}
		if n := len(out); n > 0 && str(t.object["role"]) == "user" && str(out[n-1].object["role"]) == "user" && (t.folded || out[n-1].folded) {
			both := append(blocksOf(out[n-1].object["content"]), blocksOf(t.object["content"])...)
			var results, rest []object // results answer the assistant, so they come first
			for _, b := range both {
				if str(b["type"]) == "tool_result" {
					results = append(results, b)
				} else {
					rest = append(rest, b)
				}
			}
			out[n-1] = turn{object{"role": encode("user"), "content": encode(append(results, rest...))}, true}
			continue
		}
		out = append(out, t)
	}
	if !any {
		return raw
	}
	final := []object{}
	for _, t := range out {
		final = append(final, t.object)
	}
	return encode(final)
}

func blocksOf(raw json.RawMessage) []object {
	if blocks, ok := list(raw); ok {
		return blocks
	}
	if s := str(raw); s != "" {
		return []object{{"type": encode("text"), "text": encode(s)}}
	}
	return nil
}
