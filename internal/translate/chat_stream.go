package translate

import (
	"encoding/json"
	"strconv"
	"strings"
	"unicode"

	"github.com/Lookoff-AIMLAPI/second-life/internal/dto"
)

const (
	chatNoteThought  = "reasoning after a tool call dropped"
	chatNoteArgs     = "arguments after a complete tool call dropped"
	chatNoteNameless = "tool call without a name dropped"
	chatNoteExtra    = "late tool call state dropped"
	chatNoteCut      = "reasoning without an end tag"
	chatNoteReason   = "unknown finish reason"

	thinkOpen, thinkClose = "<think>", "</think>"
)

// Where the leading <think> of a text is.
const (
	thinkUnknown = iota
	thinkInside
	thinkOver
)

// ChatStream turns one Chat Completions answer into Anthropic events. It is a Stream whose items come from chunks:
// the same queue, stop sequences and checks. Read the body with ReadSSE in Lenient mode. It is not safe for concurrent
// use.
type ChatStream struct {
	*Stream

	finish   string
	stopWord string // the stop sequence the server says it stopped at
	counted  bool   // the server reported usage
	chunks   int
	whole    bool // a whole message came, not a delta
	content  bool // something of the answer came

	cur     *stItem // the text or reasoning being written
	field   string  // the field the current reasoning came in
	thought strings.Builder
	hidden  int    // bytes of reasoning that were not shown
	lead    string // white space that may open a text or a reasoning
	leadOf  string // which of the two
	think   int
	held    string // text that may be part of a think tag
	trim    bool   // skip white space after the end tag

	calls   []*chatStreamCall
	byIndex map[int]*chatStreamCall
	used    map[string]bool // ids that went out
}

type chatStreamCall struct {
	it       *stItem
	raw      string // the server's id
	name     string
	extra    json.RawMessage
	held     string // arguments that may be only a placeholder
	scan     jsonScan
	fed      bool // arguments went to the item
	complete bool // a whole object went to the item
}

// NewChatStream starts the translation of one Chat Completions answer.
func NewChatStream(ctx dto.StreamContext) *ChatStream {
	return &ChatStream{Stream: NewStream(ctx), byIndex: map[int]*chatStreamCall{}, used: map[string]bool{}}
}

// Push takes one chunk, or a whole completion, and returns the Anthropic events it produces.
func (c *ChatStream) Push(ev map[string]any) []dto.Event {
	if c.ended {
		return nil
	}
	typ := stStr(ev["type"])
	switch {
	case typ == stMalformed:
		c.note(stNoteMalformed)
		return nil
	case typ == stDoneMark:
		return c.end()
	case chatIsError(ev):
		return c.fail(nil, chatStreamError(ev))
	}
	choice := chatChoice(ev)
	usage, counted := chatUsage(ev, choice)
	if counted {
		c.usage, c.counted = usage, true
	}
	if choice == nil && !counted {
		return nil
	}
	c.chunks++
	if id := reqClean(stStr(ev["id"])); id != "" && c.id == "" {
		c.id = "msg_" + strings.TrimPrefix(id[:min(len(id), reqNameMax)], "chatcmpl-")
	}
	out := c.start(nil)
	if e := choice["error"]; chatHasError(e) {
		return c.fail(out, chatStreamError(map[string]any{"error": e}))
	}
	if delta := stMap(choice["delta"]); delta != nil {
		c.delta(delta)
	} else if m := stMap(choice["message"]); m != nil {
		// After deltas a whole message only repeats them.
		if c.whole = true; !c.content || ev["object"] != "chat.completion" {
			c.delta(m)
		}
	} else if text := stStr(choice["text"]); text != "" {
		c.text(text)
	}
	finish := chatFinish(choice)
	if finish != "" {
		c.finish = finish
		c.stopWord = stStr(choice["stop_reason"])
	}
	out = c.drain(out, false)
	switch {
	case c.ended:
		return out
	case c.stopSeq != "":
		return c.endAtStop(out)
	case c.finish != "" && counted:
		return append(out, c.end()...) // nothing comes after both, and the server may keep the body open
	}
	return out
}

// Close reports the end of the body. Without a finish reason or [DONE] the answer was cut, unless it was one whole
// message.
func (c *ChatStream) Close() []dto.Event {
	if c.ended || c.finish == "" && !(c.whole && c.chunks == 1) {
		return c.Stream.Close()
	}
	return c.end()
}

func chatIsError(ev map[string]any) bool {
	if chatHasError(ev["error"]) || ev["object"] == "error" || ev["type"] == "error" {
		return true
	}
	if stInt(stMap(ev["base_resp"])["status_code"]) != 0 {
		return true
	}
	if ev["choices"] != nil || ev["usage"] != nil {
		return false
	}
	return stStr(ev["message"]) != "" || ev["detail"] != nil
}

// chatChoice is the first choice, or what stands for it in Ollama's own format.
func chatChoice(ev map[string]any) map[string]any {
	list, _ := ev["choices"].([]any)
	for _, v := range list {
		if m := stMap(v); m != nil && stInt(m["index"]) == 0 {
			return m
		}
	}
	if m := stMap(ev["message"]); m != nil && ev["choices"] == nil {
		choice := map[string]any{"delta": m}
		if ev["done"] == true {
			choice["finish_reason"] = "stop"
			if r := stStr(ev["done_reason"]); r != "" {
				choice["finish_reason"] = r
			}
		}
		return choice
	}
	return nil
}

func chatFinish(choice map[string]any) string {
	f := strings.ToLower(stStr(choice["finish_reason"]))
	if f == "null" || f == "none" {
		return ""
	}
	return f
}

func (c *ChatStream) delta(d map[string]any) {
	for _, f := range []string{chatFieldContent, chatFieldPlain, "thinking"} {
		if v := chatReasoningText(d[f]); v != "" { // some servers send the same text in two fields
			if f == "thinking" {
				f = chatFieldContent
			}
			c.reason(f, v)
			break
		} else if f == "thinking" {
			c.reason(chatFieldPlain, chatReasoningText(d["reasoning_details"]))
		}
	}
	switch v := d["content"].(type) {
	case string:
		c.text(v)
	case []any: // Mistral
		for _, p := range v {
			part := stMap(p)
			switch stStr(part["type"]) {
			case "thinking", "reasoning":
				c.reason(chatFieldContent, chatReasoningText(part["thinking"]))
			case "refusal":
				c.refuse(stStr(part["refusal"]))
			default:
				c.text(stStr(part["text"]))
			}
		}
	}
	c.refuse(stStr(d["refusal"]))
	if f := stMap(d["function_call"]); f != nil {
		c.call(map[string]any{"index": -1.0, "function": f}, 0)
	}
	list, _ := d["tool_calls"].([]any)
	for i, v := range list {
		if m := stMap(v); m != nil {
			c.call(m, i)
		}
	}
}

// chatReasoningText reads a string, or a list of parts.
func chatReasoningText(v any) string {
	switch v := v.(type) {
	case string:
		return v
	case map[string]any:
		for _, k := range []string{"text", "summary", "content"} {
			if s := stStr(v[k]); s != "" {
				return s
			}
		}
	case []any:
		var b strings.Builder
		for _, p := range v {
			b.WriteString(chatReasoningText(p))
		}
		return b.String()
	}
	return ""
}

func (c *ChatStream) refuse(v string) {
	if v == "" {
		return
	}
	c.settle()
	c.plain(v)
	if c.cur != nil && c.cur.kind == stMessage {
		c.cur.refused = true
	}
}

// begin ends what is being written and starts another item.
func (c *ChatStream) begin(kind string) *stItem {
	c.endCur()
	c.cur = c.create(kind)
	c.enqueue(c.cur)
	c.content = true
	return c.cur
}

func (c *ChatStream) endCur() {
	if it := c.cur; it != nil {
		if it.kind == stReasonIt {
			it.sig = EncodeReasoning(c.ctx.UpstreamModel, c.field, c.thought.String())
			if !c.shows(it) {
				c.hidden += c.thought.Len()
			}
			c.thought.Reset()
		}
		it.done = true
	}
	c.cur, c.lead = nil, ""
}

func (c *ChatStream) reason(field, v string) {
	if v == "" {
		return
	}
	if len(c.calls) > 0 {
		c.note(chatNoteThought)
		return
	}
	if c.cur == nil || c.cur.kind != stReasonIt {
		if v = c.blank(stReasonIt, v); v == "" {
			return
		}
		c.begin(stReasonIt)
		c.field = field
	}
	c.thought.WriteString(v)
	c.receive(c.cur, 0, v)
}

// blank holds white space back until something of the same kind follows it.
func (c *ChatStream) blank(kind, v string) string {
	if c.leadOf != kind {
		c.lead, c.leadOf = "", kind
	}
	if c.lead += v; strings.TrimSpace(c.lead) == "" {
		return ""
	}
	v, c.lead = c.lead, ""
	return v
}

// text takes answer text, which may start with reasoning between think tags. A tag may be cut anywhere.
func (c *ChatStream) text(v string) {
	if v == "" {
		return
	}
	switch c.think {
	case thinkUnknown:
		c.held += v
		t := strings.TrimLeftFunc(c.held, unicode.IsSpace)
		switch {
		case strings.HasPrefix(t, thinkOpen):
			c.think, c.held = thinkInside, ""
			c.text(t[len(thinkOpen):])
		case !strings.HasPrefix(thinkOpen, t):
			c.settle()
		}
	case thinkInside:
		c.held += v
		if i := strings.Index(c.held, thinkClose); i >= 0 {
			rest := c.held[i+len(thinkClose):]
			c.reason(chatFieldContent, c.held[:i])
			c.endCur()
			c.think, c.held, c.trim = thinkOver, "", true
			c.plain(rest)
			return
		}
		keep := 0
		for n := min(len(thinkClose)-1, len(c.held)); n > 0; n-- {
			if strings.HasSuffix(c.held, thinkClose[:n]) {
				keep = n
				break
			}
		}
		out := c.held[:len(c.held)-keep]
		c.held = c.held[len(c.held)-keep:]
		c.reason(chatFieldContent, out)
	default:
		c.plain(v)
	}
}

// settle ends the wait for a think tag.
func (c *ChatStream) settle() {
	held := c.held
	c.held = ""
	switch c.think {
	case thinkUnknown:
		if held != "" || len(c.calls) > 0 {
			c.think = thinkOver
		}
		c.plain(held)
	case thinkInside:
		c.reason(chatFieldContent, held)
	}
}

func (c *ChatStream) plain(v string) {
	if c.trim {
		if v = strings.TrimLeftFunc(v, unicode.IsSpace); v == "" {
			return
		}
		c.trim = false
	}
	if v == "" {
		return
	}
	if c.cur == nil || c.cur.kind != stMessage {
		if len(c.ctx.StopSeqs) == 0 { // white space may be a stop sequence
			if v = c.blank(stMessage, v); v == "" {
				return
			}
		}
		c.begin(stMessage)
	}
	c.receive(c.cur, 0, v)
}

// call takes one entry of tool_calls; at is its place in the list.
func (c *ChatStream) call(m map[string]any, at int) {
	f := stMap(m["function"])
	if f == nil {
		f = m
	}
	id, name, args := stStr(m["id"]), stStr(f["name"]), ""
	switch v := f["arguments"].(type) {
	case string:
		args = v
	case nil:
	default:
		raw, _ := json.Marshal(v)
		args = string(raw)
	}
	var extra json.RawMessage
	if x := stMap(m["extra_content"]); len(x) > 0 {
		extra, _ = json.Marshal(x)
	}
	if c.think == thinkUnknown {
		c.settle()
		c.think = thinkOver
	}
	call := c.which(m["index"], at, id, name, args)
	if call == nil {
		c.endCur()
		call = &chatStreamCall{it: c.create(stCall)}
		c.enqueue(call.it)
		c.calls = append(c.calls, call)
		c.content = true
	}
	if i, ok := chatIndex(m["index"]); ok {
		c.byIndex[i] = call
	}
	if call.raw == "" {
		call.raw = id
	}
	if call.name == "" {
		call.name = name
	}
	if extra != nil && call.extra == nil {
		if call.extra = extra; call.it.callID != "" {
			c.note(chatNoteExtra)
		}
	}
	c.arguments(call, args)
	if call.name != "" && call.fed {
		c.identify(call)
	}
}

func chatIndex(v any) (int, bool) {
	if s, ok := v.(string); ok {
		n, err := strconv.Atoi(s)
		return n, err == nil
	}
	f, ok := v.(float64)
	return stInt(f), ok
}

// which finds the call that a piece belongs to, nil for a new one. Servers number every call 0, leave out ids, repeat
// ids and names, or send them late.
func (c *ChatStream) which(index any, at int, id, name, args string) *chatStreamCall {
	var last *chatStreamCall
	if len(c.calls) > 0 {
		last = c.calls[len(c.calls)-1]
	}
	if i, ok := chatIndex(index); ok {
		call := c.byIndex[i]
		if call == nil || call.another(id, name, args) {
			return nil
		}
		return call
	}
	if id != "" {
		for i := len(c.calls) - 1; i >= 0; i-- {
			if call := c.calls[i]; call.raw == id && !call.another(id, name, args) {
				return call
			}
		}
		if last != nil && last.raw == "" && name == "" && !last.complete {
			return last
		}
		return nil
	}
	if name == "" {
		return last
	}
	if last != nil && at == 0 && !last.complete && (last.name == "" || last.name == name && (last.fed || last.held != "")) {
		return last
	}
	return nil
}

// another is true when a piece under the same index starts a new call.
func (s *chatStreamCall) another(id, name, args string) bool {
	renamed := name != "" && s.name != "" && name != s.name
	again := s.complete && name != "" && strings.TrimSpace(args) != ""
	switch {
	case id != "" && s.raw != "" && id != s.raw:
		return true
	case id != "" && s.raw == id:
		return renamed && s.complete
	case id != "" && s.complete && name != "":
		return true
	}
	return renamed || again
}

// arguments gives the item what it can have. An empty object may be a placeholder for what follows.
func (c *ChatStream) arguments(call *chatStreamCall, args string) {
	if args == "" {
		return
	}
	if call.complete {
		if strings.TrimSpace(args) != "" {
			c.note(chatNoteArgs)
		}
		return
	}
	if !call.fed {
		if t := strings.TrimSpace(call.held); (t == "{}" || t == "null") && strings.TrimSpace(args) != "" {
			call.held = ""
		}
		call.held += args
		t := strings.TrimSpace(call.held)
		if t == "" || t[0] != '{' || t == "{}" {
			return
		}
		args, call.held, call.fed = strings.TrimLeftFunc(call.held, unicode.IsSpace), "", true
	}
	n, complete := call.scan.feed([]byte(args))
	c.receive(call.it, 0, args[:n])
	if call.complete = complete; complete && strings.TrimSpace(args[n:]) != "" {
		c.note(chatNoteArgs)
	}
}

// identify gives the item its name and an id of its own.
func (c *ChatStream) identify(call *chatStreamCall) {
	it := call.it
	if it.callID != "" {
		return
	}
	id := call.raw
	if id == "" || c.used[id] {
		id = newCallID()
	}
	c.used[id] = true
	it.name, it.callID = call.name, sealCall(id, c.ctx.UpstreamModel, call.extra)
}

// settleCall ends a call. The arguments may be a string that holds the object.
func (c *ChatStream) settleCall(call *chatStreamCall, cut bool) {
	it := call.it
	it.done = true
	it.cut = cut && !call.complete && strings.TrimSpace(call.held) != "{}"
	blank := !call.fed && strings.TrimSpace(call.held) == ""
	if call.name == "" {
		if cut || blank {
			it.hidden = true
			c.note(chatNoteNameless)
		}
		return
	}
	if t := strings.TrimSpace(call.held); !call.fed && t != "" && t != "null" && t != "{}" {
		var inner string
		if json.Unmarshal([]byte(t), &inner) == nil {
			t = strings.TrimSpace(inner)
		}
		if t != "" && t != "null" {
			c.receive(it, 0, t)
		}
	}
	c.identify(call)
}

// chatStops maps finish reasons that are not decided by the content.
var chatStops = map[string]string{
	"stop": "", "tool_calls": "", "function_call": "", "tool_use": "", "end_turn": "", "eos": "", "eos_token": "", "stop_sequence": "", "": "",
	"length": "max_tokens", "max_tokens": "max_tokens", "max_output_tokens": "max_tokens", "model_length": "max_tokens",
	"model_context_window_exceeded": "max_tokens",
	"content_filter":                "refusal", "refusal": "refusal", "sensitive": "refusal", "safety": "refusal",
	"recitation": "refusal", "prohibited_content": "refusal",
}

// chatBroken are finish reasons of answers that did not finish.
var chatBroken = map[string]bool{"error": true, "network_error": true, "abort": true, "insufficient_system_resource": true, "malformed_function_call": true}

// end finishes the message.
func (c *ChatStream) end() []dto.Event {
	c.settle()
	if c.think == thinkInside {
		c.note(chatNoteCut)
	}
	c.endCur()
	if chatBroken[c.finish] {
		e := &dto.APIError{Status: 502, Type: "api_error", Message: "upstream ended the answer: " + c.finish}
		if c.finish == "insufficient_system_resource" {
			e.Status, e.Type = 529, "overloaded_error"
		}
		return c.fail(c.drain(nil, false), e)
	}
	stop, known := chatStops[c.finish]
	if !known {
		c.note(chatNoteReason)
	}
	for _, call := range c.calls {
		c.settleCall(call, stop == "max_tokens")
	}
	out := c.drain(c.start(nil), true)
	if c.ended {
		return out
	}
	if !c.counted {
		n := c.hidden
		for _, it := range c.blocks {
			n += it.out.Len()
		}
		c.estimated = true
		c.usage = dto.Usage{InputTokens: c.ctx.InputEstimate, OutputTokens: n/4 + 1}
	}
	if c.stopSeq == "" && c.stopWord != "" && stop == "" && reqIn(c.stopWord, c.ctx.StopSeqs) && !c.called() {
		c.stopSeq = c.stopWord // vLLM names the sequence it stopped at
	}
	return append(out, c.complete(nil, stop)...)
}

func (c *ChatStream) called() bool {
	for _, it := range c.blocks {
		if it.kind == stCall {
			return true
		}
	}
	return false
}

// chatUsage reads the counts in any of the places and shapes servers use. Zeros are no counts.
func chatUsage(ev, choice map[string]any) (dto.Usage, bool) {
	var u map[string]any
	for _, v := range []any{ev["usage"], choice["usage"], stMap(ev["x_groq"])["usage"]} {
		if m := stMap(v); len(m) > 0 {
			u = m
			break
		}
	}
	if u == nil {
		u = ev // Ollama's own format
	}
	num := func(m map[string]any, keys ...string) int {
		for _, k := range keys {
			if n := stInt(m[k]); n > 0 {
				return n
			}
		}
		return 0
	}
	in := num(u, "prompt_tokens", "input_tokens", "prompt_eval_count")
	out := num(u, "completion_tokens", "output_tokens", "eval_count")
	if in == 0 && out == 0 {
		return dto.Usage{}, false
	}
	details := stMap(u["prompt_tokens_details"])
	if details == nil {
		details = stMap(u["input_tokens_details"])
	}
	read := num(details, "cached_tokens")
	if read == 0 {
		read = num(u, "prompt_cache_hit_tokens", "cached_tokens", "cache_read_input_tokens") // DeepSeek, Moonshot, LiteLLM
	}
	written := num(details, "cache_write_tokens", "cache_creation_tokens")
	if written == 0 {
		written = num(u, "cache_creation_input_tokens")
	}
	// Grok and Gemini count reasoning outside the completion.
	if r := num(stMap(u["completion_tokens_details"]), "reasoning_tokens"); r > 0 && num(u, "total_tokens") == in+out+r {
		out += r
	}
	return dto.Usage{InputTokens: max(0, in-read-written), OutputTokens: out, CacheReadInputTokens: read, CacheCreationInputTokens: written}, true
}
