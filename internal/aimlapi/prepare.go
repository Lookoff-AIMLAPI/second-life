package aimlapi

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/Lookoff-AIMLAPI/second-life/internal/dto"
)

// What Prepare reports. The texts are static: they go to the log.
const (
	warnCodexThinking = "thinking from Codex was dropped from the history: only Codex can read it"
	warnUnsigned      = "thinking without a signature was dropped from the history: the endpoint cannot verify it"
	warnEmptied       = "a turn that held only dropped thinking was replaced by a placeholder"
	warnCodexSearch   = "a Codex web search in the history was rewritten as text: the endpoint refuses its results"
	warnNull          = "null fields that the endpoint refuses were dropped"
	warnFallbacks     = "fallbacks is not supported by the endpoint and was dropped"
	warnAdvisor       = "the advisor tool is not supported by the endpoint and was dropped"
	warnToolChanges   = "tool_addition and tool_removal were applied to tools: this model does not read them in the conversation"
	warnToolChange    = "a tool_addition or tool_removal that does not name a tool was dropped"
	warnFormat        = "output_config.format is ignored by the endpoint for this model"
	warnMaxTokens     = "max_tokens was lowered to the output limit of the model"
)

const (
	codexSignature = "c2c1."
	chatSignature  = "c2r1." // thinking of a Chat Completions endpoint
	omitted        = `{"type":"text","text":"[content omitted]"}`
)

// Native reports whether the endpoint hands this model's requests to Anthropic as they are. The other models are
// translated from Chat Completions, and the -latest names go through an aggregator that reads fewer fields.
func Native(model string) bool {
	m := strings.TrimPrefix(strings.ToLower(model), "anthropic/")
	return strings.HasPrefix(m, "claude") && !strings.Contains(m, "/") && !strings.HasSuffix(m, "-latest")
}

// Prepare makes a Claude request acceptable to POST /v1/messages. Only what that endpoint refuses, or ignores at a
// cost, is changed; every other byte stays. Each kind of change has one warning.
func Prepare(raw []byte, model string) (body []byte, warnings []string, err *dto.APIError) {
	if !json.Valid(raw) {
		return nil, nil, invalid("request is not valid JSON")
	}
	top, ok := members(raw)
	if !ok {
		return nil, nil, invalid("request must be a JSON object")
	}
	p := &prep{native: Native(model)}
	if m, ok := field(top, "thread"); ok && !m.null() && !p.native {
		// Native links refuse it in words Claude Code knows how to recover from; the translated ones would
		// answer without the conversation.
		return nil, nil, invalid("thread: message threads are not supported; send the whole conversation")
	}
	named, hasTools := false, false
	for i, m := range top { // tools wait for the messages that change them
		switch m.key {
		case "model":
			top[i].val, named = quote(model), true
		case "messages":
			top[i].val = p.messages(m.val)
		case "system":
			top[i].val = p.each(m.val, func(ms []member) []member { return p.clean(ms) })
		case "output_config":
			if cfg, _ := members(m.val); !p.native {
				if f, ok := field(cfg, "format"); ok && !f.null() {
					p.warn(warnFormat)
				}
			}
		}
	}
	top = without(top, func(m member) bool {
		switch m.key {
		case "thread", "container":
			return m.null() && p.warn(warnNull)
		case "fallbacks":
			return m.null() && p.warn(warnNull) || p.warn(warnFallbacks)
		}
		return false
	})
	for i, m := range top {
		if m.key == "tools" {
			top[i].val, hasTools = p.tools(m.val), true
		}
	}
	if !hasTools && len(p.changes) > 0 {
		if t := p.tools([]byte("[]")); string(t) != "[]" {
			top = append(top, member{rawKey: []byte(`"tools"`), val: t})
		}
	}
	if !named {
		top = append(top, member{rawKey: []byte(`"model"`), val: quote(model)})
	}
	return object(top), p.warnings, nil
}

// CapMaxTokens lowers max_tokens (and a thinking budget that no longer fits) to the output limit of a model. Claude
// asks for what its own model can write, and Anthropic refuses more than the target can; translated models are capped
// by the endpoint itself. The warning is empty when nothing changed. body must come from Prepare.
func CapMaxTokens(body []byte, limit int) (out []byte, warning string) {
	top, ok := members(body)
	if !ok || limit <= 0 {
		return body, ""
	}
	for i, m := range top {
		switch m.key {
		case "max_tokens":
			if n, err := strconv.ParseFloat(string(m.val), 64); err == nil && n > float64(limit) {
				top[i].val, warning = strconv.AppendInt(nil, int64(limit), 10), warnMaxTokens
			}
		case "thinking":
			ms, _ := members(m.val)
			for j, b := range ms {
				if n, err := strconv.ParseFloat(string(b.val), 64); b.key == "budget_tokens" && err == nil && n >= float64(limit) {
					ms[j].val, warning = strconv.AppendInt(nil, int64(limit-1), 10), warnMaxTokens
					top[i].val = object(ms)
				}
			}
		}
	}
	if warning == "" {
		return body, ""
	}
	return object(top), warning
}

func invalid(msg string) *dto.APIError {
	return &dto.APIError{Status: 400, Type: "invalid_request_error", Message: msg}
}

type prep struct {
	native   bool
	warnings []string
	changes  []toolChange
}

// toolChange is a tool_addition or tool_removal, in the order of the conversation.
type toolChange struct {
	name       string
	definition []byte // tool_addition with a whole tool
	remove     bool
}

// warn always returns true, so it can end a condition.
func (p *prep) warn(text string) bool {
	for _, w := range p.warnings {
		if w == text {
			return true
		}
	}
	p.warnings = append(p.warnings, text)
	return true
}

// refusesNull are the optional fields that the endpoint takes when absent but not as null, by block type.
var refusesNull = map[string][]string{
	"":                  {"cache_control", "caller"}, // any block
	"text":              {"citations"},
	"document":          {"title", "context"},
	"tool_result":       {"content", "is_error"},
	"web_search_result": {"page_age"},
	"web_fetch_result":  {"retrieved_at"},
	"custom":            {"type", "description", "defer_loading", "eager_input_streaming", "input_examples", "strict", "allowed_callers"},
}

func (p *prep) clean(ms []member) []member { return p.cleanAs(ms, fieldText(ms, "type")) }

func (p *prep) cleanAs(ms []member, kind string) []member {
	return without(ms, func(m member) bool {
		if !m.null() {
			return false
		}
		for _, list := range [][]string{refusesNull[""], refusesNull[kind]} {
			for _, k := range list {
				if k == m.key {
					return p.warn(warnNull)
				}
			}
		}
		return false
	})
}

// each runs fn over the objects of an array and keeps the bytes of the ones it leaves alone. A nil result drops one.
func (p *prep) each(raw []byte, fn func([]member) []member) []byte {
	els, ok := elements(raw)
	if !ok {
		return raw
	}
	out, changed := make([][]byte, 0, len(els)), false
	for _, e := range els {
		ms, ok := members(e)
		if !ok {
			out = append(out, e)
			continue
		}
		now := fn(ms)
		switch {
		case now == nil && len(ms) > 0:
			changed = true
		case same(ms, now):
			out = append(out, e)
		default:
			out, changed = append(out, object(now)), true
		}
	}
	if !changed {
		return raw
	}
	return array(out)
}

// same compares identity, not content: untouched pieces point into the request. Callers copy before they change one.
func same(a, b []member) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if len(a[i].val) != len(b[i].val) || len(a[i].val) > 0 && &a[i].val[0] != &b[i].val[0] {
			return false
		}
	}
	return true
}

func set(ms []member, key string, val []byte) []member {
	out := append([]member(nil), ms...)
	for i := range out {
		if out[i].key == key {
			out[i].val = val
		}
	}
	return out
}

func (p *prep) messages(raw []byte) []byte {
	return p.each(raw, func(msg []member) []member {
		role := fieldText(msg, "role")
		content, _ := field(msg, "content")
		els, ok := elements(content.val)
		if !ok {
			return msg
		}
		searches := codexSearches(els)
		next := p.each(content.val, func(ms []member) []member { return p.block(ms, role, searches) })
		if role != "system" && string(next) == "[]" && len(els) > 0 {
			p.warn(warnEmptied)
			next = []byte("[" + omitted + "]")
		}
		if bytes.Equal(next, content.val) {
			return msg
		}
		return set(msg, "content", next)
	})
}

// block returns nil to drop the block.
func (p *prep) block(ms []member, role string, searches map[string]string) []member {
	switch kind := fieldText(ms, "type"); kind {
	case "thinking":
		sig := fieldText(ms, "signature")
		if strings.HasPrefix(sig, codexSignature) || strings.HasPrefix(sig, chatSignature) {
			p.warn(warnCodexThinking)
			return nil
		}
		if sig == "" {
			p.warn(warnUnsigned)
			return nil
		}
	case "server_tool_use":
		if _, ok := searches[fieldText(ms, "id")]; ok {
			return nil
		}
	case "web_search_tool_result":
		if s, ok := searches[fieldText(ms, "tool_use_id")]; ok {
			p.warn(warnCodexSearch)
			out := []member{
				{key: "type", rawKey: []byte(`"type"`), val: []byte(`"text"`)},
				{key: "text", rawKey: []byte(`"text"`), val: quote(s)},
			}
			if c, ok := field(ms, "cache_control"); ok && !c.null() {
				out = append(out, c)
			}
			return out
		}
		ms = p.inner(ms)
	case "tool_result":
		ms = p.inner(ms)
	case "web_fetch_tool_result":
		if c, ok := field(ms, "content"); ok {
			if res, ok := members(c.val); ok {
				if now := p.clean(res); !same(res, now) {
					ms = set(ms, "content", object(now))
				}
			}
		}
	case "tool_addition", "tool_removal":
		if role == "system" && !p.native {
			p.change(ms, kind == "tool_removal")
			return nil
		}
	}
	return p.clean(ms)
}

func (p *prep) inner(ms []member) []member {
	c, _ := field(ms, "content")
	if next := p.each(c.val, p.clean); !bytes.Equal(next, c.val) {
		return set(ms, "content", next)
	}
	return ms
}

// codexSearches finds the searches this bridge wrote, by call id, as the text that replaces them. Their results have
// an empty encrypted_content, which a real one never has.
func codexSearches(blocks [][]byte) map[string]string {
	var out map[string]string
	queries := map[string]string{}
	for _, b := range blocks {
		ms, _ := members(b)
		switch fieldText(ms, "type") {
		case "server_tool_use":
			input, _ := field(ms, "input")
			in, _ := members(input.val)
			queries[fieldText(ms, "id")] = fieldText(in, "query")
		case "web_search_tool_result":
			content, _ := field(ms, "content")
			pages, _ := elements(content.val)
			lines, ours := "", false
			for _, page := range pages {
				f, _ := members(page)
				enc, ok := field(f, "encrypted_content")
				ours = ours || ok && string(enc.val) == `""`
				lines += "\n" + strings.TrimSpace(fieldText(f, "title")+" "+fieldText(f, "url"))
			}
			if ours {
				if out == nil {
					out = map[string]string{}
				}
				id := fieldText(ms, "tool_use_id")
				out[id] = strings.TrimSpace("[web search: " + queries[id] + "]" + lines)
			}
		}
	}
	return out
}

func (p *prep) change(ms []member, remove bool) {
	t, _ := field(ms, "tool")
	tool, _ := members(t.val)
	c := toolChange{name: fieldText(tool, "name"), remove: remove}
	if fieldText(tool, "type") == "tool_definition" && !remove {
		d, _ := field(tool, "definition")
		def, _ := members(d.val)
		c.name, c.definition = fieldText(def, "name"), d.val
		if strings.HasPrefix(fieldText(def, "type"), "advisor_") {
			p.warn(warnAdvisor)
			return
		}
	}
	if c.name == "" {
		p.warn(warnToolChange)
		return
	}
	p.warn(warnToolChanges)
	p.changes = append(p.changes, c)
}

// tools drops what the endpoint refuses and, for models that do not read them, applies the tool changes.
func (p *prep) tools(raw []byte) []byte {
	raw = p.each(raw, func(ms []member) []member {
		kind := fieldText(ms, "type")
		if strings.HasPrefix(kind, "advisor_") {
			p.warn(warnAdvisor)
			return nil
		}
		if kind == "" {
			kind = "custom"
		}
		return p.cleanAs(ms, kind)
	})
	els, ok := elements(raw)
	if !ok || len(p.changes) == 0 {
		return raw
	}
	at := map[string]int{}
	for i, e := range els {
		ms, _ := members(e)
		at[fieldText(ms, "name")] = i
	}
	load, gone := map[string]bool{}, map[string]bool{}
	for _, c := range p.changes {
		gone[c.name] = c.remove
		if c.definition == nil {
			load[c.name] = !c.remove
			continue
		}
		load[c.name] = false
		if i, ok := at[c.name]; ok {
			els[i] = c.definition
		} else {
			at[c.name] = len(els)
			els = append(els, c.definition)
		}
	}
	return p.each(array(els), func(ms []member) []member {
		name := fieldText(ms, "name")
		if gone[name] {
			return nil
		}
		if load[name] {
			return without(ms, func(m member) bool { return m.key == "defer_loading" })
		}
		return ms
	})
}
