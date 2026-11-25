package translate

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/Lookoff-AIMLAPI/second-life/internal/dto"
)

const (
	stMessage  = "message"
	stReasonIt = "reasoning"
	stCall     = "function_call"
	stSearch   = "web_search_call"
	stIgnored  = ""

	stMalformed = "slife.malformed_event" // ReadSSE reports an event it skipped
)

// Notes: fixed texts for the log, never content.
const (
	stNoteMalformed  = "malformed upstream event skipped"
	stNoteCommentary = "commentary after the final answer dropped"
	stNoteDiffers    = "streamed text differs from the final item"
	stNoteUnfinished = "unfinished item dropped"
	stNoteClosed     = "unfinished item closed"
	stNoteUnknown    = "text for an unknown item dropped"
	stNoteRole       = "message with a foreign role dropped"
	stNoteEndTurn    = "upstream wants to continue, but made no tool call"
)

// Stream turns one Responses event stream into Anthropic events and keeps what Message needs.
// It is not safe for concurrent use.
//
// Upstream items may interleave, Anthropic blocks may not. Items wait in a queue in arrival order; only the head
// emits, and the next one starts when the head is done.
type Stream struct {
	ctx       dto.StreamContext
	id        string
	started   bool
	ended     bool // message_stop or error went out
	failed    *dto.APIError
	stop      string
	usage     dto.Usage
	refused   bool
	answered  bool   // a final_answer message was emitted
	estimated bool   // the usage is an estimate
	stopSeq   string // the stop sequence that ended the text; nothing is emitted after it
	notes     []string
	counts    map[string]int

	queue  []*stItem
	blocks []*stItem // items that got an Anthropic block; the position is the block index

	byIndex map[int]*stItem
	byID    map[string]*stItem
	byCall  map[string]*stItem
	live    []*stItem // every item, in arrival order, for events with no key
	events  bool      // an output_item event arrived
}

type stItem struct {
	kind             string
	index            int // -1 when unknown
	id, callID, name string
	phase            string

	parts    map[int]*strings.Builder // text received, per upstream part
	unkeyed  bool                     // a delta came without its part number
	lastPart int
	pending  strings.Builder // received, not emitted
	out      strings.Builder // emitted
	final    string          // text of the finished item
	hasFinal bool
	enc      string
	sig      string   // a ready signature (ChatStream)
	closed   bool     // content_block_stop went out
	closing  bool     // the block is about to close: hold nothing back
	query    string   // web search
	urls     []string // web search
	queued   bool     // false: only text events named it so far
	hidden   bool     // must not be shown
	refused  bool
	cut      bool // finished by upstream as incomplete
	done     bool
	block    int // -1 until content_block_start
}

// NewStream starts the translation of one upstream response.
func NewStream(ctx dto.StreamContext) *Stream {
	return &Stream{ctx: ctx, counts: map[string]int{},
		byIndex: map[int]*stItem{}, byID: map[string]*stItem{}, byCall: map[string]*stItem{}}
}

// Usage is what upstream reported so far.
func (s *Stream) Usage() dto.Usage { return s.usage }

// Failed is the error that ended the stream, if any.
func (s *Stream) Failed() *dto.APIError { return s.failed }

// Ended is true after a terminal event: nothing that follows matters.
func (s *Stream) Ended() bool { return s.ended }

// Notes lists what was dropped or repaired, for the log. It holds no content.
func (s *Stream) Notes() []string {
	var out []string
	for _, n := range s.notes {
		if c := s.counts[n]; c > 1 {
			n += " (x" + strconv.Itoa(c) + ")"
		}
		out = append(out, n)
	}
	return out
}

func (s *Stream) note(n string) {
	if s.counts[n]++; s.counts[n] == 1 {
		s.notes = append(s.notes, n)
	}
}

// Push takes one upstream event and returns the Anthropic events it produces.
func (s *Stream) Push(ev map[string]any) []dto.Event {
	if s.ended {
		return nil
	}
	switch typ := stStr(ev["type"]); typ {
	case "response.created":
		if id := stStr(stMap(ev["response"])["id"]); id != "" && s.id == "" {
			s.id = "msg_" + strings.TrimPrefix(id, "resp_")
		}
		return s.start(nil)

	case "response.output_item.added", "response.output_item.done":
		item := stMap(ev["item"])
		if item == nil {
			return nil
		}
		s.events = true
		done := typ == "response.output_item.done"
		s.item(ev, item, done)

	case stMalformed:
		s.note(stNoteMalformed)
		return nil

	case "response.output_text.delta":
		s.text(ev, stMessage, "delta", "content_index", false)
	case "response.output_text.done":
		s.text(ev, stMessage, "text", "content_index", false)
	case "response.refusal.delta":
		s.text(ev, stMessage, "delta", "content_index", true)
	case "response.refusal.done":
		s.text(ev, stMessage, "refusal", "content_index", true)
	case "response.reasoning_summary_text.delta":
		s.text(ev, stReasonIt, "delta", "summary_index", false)
	case "response.reasoning_summary_text.done":
		s.text(ev, stReasonIt, "text", "summary_index", false)
	case "response.function_call_arguments.delta":
		s.text(ev, stCall, "delta", "", false)
	case "response.function_call_arguments.done":
		s.text(ev, stCall, "arguments", "", false)

	case "response.completed":
		return s.complete(stMap(ev["response"]), "")

	case "response.incomplete":
		resp := stMap(ev["response"])
		reason := stStr(stMap(resp["incomplete_details"])["reason"])
		switch reason {
		case "max_output_tokens":
			return s.complete(resp, "max_tokens")
		case "content_filter":
			return s.complete(resp, "refusal")
		case "":
			reason = "unknown"
		}
		s.readUsage(resp)
		return s.fail(nil, &dto.APIError{Status: 502, Type: "api_error", Message: stCap("upstream response incomplete: " + reason)})

	case "response.failed":
		resp := stMap(ev["response"])
		s.readUsage(resp)
		return s.fail(nil, stStreamError(map[string]any{"error": resp["error"]}))

	case "error":
		return s.fail(nil, stStreamError(ev))

	default:
		return nil
	}
	out := s.drain(nil, false)
	if s.stopSeq != "" && !s.ended {
		return s.endAtStop(out)
	}
	return out
}

// Close reports the end of the upstream body. A stream that ended without a terminal event is a failure.
func (s *Stream) Close() []dto.Event {
	if s.ended {
		return nil
	}
	return s.fail(nil, &dto.APIError{Status: 502, Type: "api_error", Message: "upstream stream ended early"})
}

// Message is the whole response, or nil when the stream failed or has not ended. Where the finished item differs from
// its deltas, the finished item counts. Tool arguments that are not a JSON
// object fail it, since a non-streaming client has no way to see them.
func (s *Stream) Message() *dto.MessageResponse {
	if !s.ended || s.failed != nil {
		return nil
	}
	content := make([]map[string]any, 0, len(s.blocks))
	for _, it := range s.blocks {
		if it.hidden {
			continue
		}
		out := it.out.String()
		if it.hasFinal {
			out = it.final
			if it.kind == stMessage {
				out, _ = s.cutStop(out)
			}
		}
		switch it.kind {
		case stMessage:
			if out == "" {
				continue // every word was withdrawn
			}
			content = append(content, map[string]any{"type": "text", "text": out})
		case stReasonIt:
			content = append(content, map[string]any{"type": "thinking", "thinking": out, "signature": s.signature(it)})
		case stSearch:
			content = append(content, stSearchBlocks(it)...)
		case stCall:
			if !json.Valid([]byte(out)) || !strings.HasPrefix(strings.TrimSpace(out), "{") {
				s.failed = &dto.APIError{Status: 502, Type: "api_error", Message: "upstream sent invalid JSON arguments for a tool call"}
				return nil
			}
			content = append(content, map[string]any{"type": "tool_use", "id": it.callID, "name": s.toolName(it), "input": json.RawMessage(out)})
		}
	}
	stop := s.stop
	var seq *string
	if s.stopSeq != "" {
		seq = &s.stopSeq
	}
	return &dto.MessageResponse{ID: s.id, Type: "message", Role: "assistant", Model: s.ctx.PublicModel,
		Content: content, StopReason: &stop, StopSequence: seq, Usage: s.usage}
}

// ---- upstream side ----

func stKind(typ string) string {
	switch typ {
	case stMessage, stReasonIt, stCall, stSearch:
		return typ
	}
	return stIgnored
}

type stKeys struct {
	index          int // -1 when absent
	id, call, name string
}

func stKeysOf(ev, item map[string]any) stKeys {
	k := stKeys{index: -1, id: stStr(ev["item_id"]), call: stStr(ev["call_id"])}
	if f, ok := ev["output_index"].(float64); ok && f >= 0 {
		k.index = stInt(f)
	}
	if item != nil {
		k.call, k.name = stStr(item["call_id"]), stStr(item["name"])
		if id := stStr(item["id"]); id != "" {
			k.id = id
		}
	}
	return k
}

func (k stKeys) empty() bool { return k.index < 0 && k.id == "" && k.call == "" }

// find looks an item up by its strongest key: the id, then the call id, then the place.
func (s *Stream) find(k stKeys) *stItem {
	if it := s.byID[k.id]; it != nil {
		return it
	}
	if it := s.byCall[k.call]; it != nil && (k.name == "" || it.name == "" || it.name == k.name) {
		return it
	}
	return s.byIndex[k.index]
}

func stDiffer(a, b string) bool { return a != "" && b != "" && a != b }

func (s *Stream) create(kind string) *stItem {
	it := &stItem{kind: kind, index: -1, block: -1, lastPart: -1, parts: map[int]*strings.Builder{}}
	s.live = append(s.live, it)
	return it
}

func (s *Stream) register(it *stItem, k stKeys) {
	if k.index >= 0 {
		if it.index < 0 {
			it.index = k.index
		}
		s.byIndex[k.index] = it
	}
	if k.id != "" {
		if it.id == "" {
			it.id = k.id
		}
		s.byID[k.id] = it
	}
	if k.call != "" {
		if it.callID == "" {
			it.callID = k.call
		}
		s.byCall[k.call] = it
	}
	if it.name == "" {
		it.name = k.name
	}
}

func (s *Stream) enqueue(it *stItem) {
	if !it.queued && it.kind != stIgnored {
		it.queued = true
		s.queue = append(s.queue, it)
	}
}

// unfinished is the latest announced item of this kind that is not done and does not contradict the keys.
func (s *Stream) unfinished(kind string, k stKeys) *stItem {
	for i := len(s.live) - 1; i >= 0; i-- {
		c := s.live[i]
		if c.kind == kind && c.queued && !c.done && (k.index < 0 || c.index < 0) && (k.id == "" || c.id == "") && !stDiffer(k.call, c.callID) {
			return c
		}
	}
	return nil
}

// item handles an output_item event, or one item of the final output.
func (s *Stream) item(ev, item map[string]any, done bool) {
	kind, k := stKind(stStr(item["type"])), stKeysOf(ev, item)
	it := s.find(k)
	if it != nil && (it.kind != kind || stDiffer(it.callID, k.call) || (it.done || !done) && stDiffer(it.id, k.id)) {
		it = nil // another item under the same index
	}
	if it == nil && done {
		it = s.unfinished(kind, k)
	}
	if it == nil {
		it = s.create(kind)
	}
	s.register(it, k)
	if it.done {
		return
	}
	s.enqueue(it)
	if p := stStr(item["phase"]); p != "" {
		it.phase = p
	}
	if role := stStr(item["role"]); kind == stMessage && role != "" && role != "assistant" && !it.hidden {
		it.hidden = true
		s.note(stNoteRole)
	}
	if done {
		s.finish(it, item)
	}
}

// textItem finds the item of a text event. Text that names an item nobody announced is kept aside until it is.
func (s *Stream) textItem(ev map[string]any, kind string) *stItem {
	k := stKeysOf(ev, nil)
	it := s.find(k)
	if it == nil && k.empty() {
		if it = s.unfinished(kind, k); it == nil {
			it = s.create(kind)
			s.enqueue(it)
		}
	}
	if it == nil {
		it = s.create(kind)
	}
	if at := s.byIndex[k.index]; it.kind != kind || it.done || at != nil && at.kind != kind {
		return nil
	}
	s.register(it, k)
	return it
}

// text handles a text event: a delta, or a whole part whose missing tail is taken.
func (s *Stream) text(ev map[string]any, kind, field, partField string, refusal bool) {
	v := stStr(ev[field])
	if v == "" {
		return
	}
	it := s.textItem(ev, kind)
	if it == nil {
		return
	}
	it.refused = it.refused || refusal
	part, keyed := 0, partField == ""
	if f, ok := ev[partField].(float64); ok {
		part, keyed = stInt(f), true
	}
	if field == "delta" {
		it.unkeyed = it.unkeyed || !keyed
		s.receive(it, part, v)
		return
	}
	if rest, ok := strings.CutPrefix(v, it.part(part)); ok {
		s.receive(it, part, rest)
	}
}

func (it *stItem) part(i int) string {
	if b := it.parts[i]; b != nil {
		return b.String()
	}
	return ""
}

// receive adds new text to an item.
func (s *Stream) receive(it *stItem, part int, text string) {
	if text == "" || !s.shows(it) {
		return
	}
	if it.kind == stReasonIt && it.lastPart >= 0 && part != it.lastPart {
		it.pending.WriteString("\n\n")
	}
	it.lastPart = part
	if it.parts[part] == nil {
		it.parts[part] = &strings.Builder{}
	}
	it.parts[part].WriteString(text)
	it.pending.WriteString(text)
}

func (s *Stream) shows(it *stItem) bool {
	return it.kind != stReasonIt || s.ctx.ShowThinking && !s.ctx.HideThinking
}

// finish applies a complete item and marks it done.
func (s *Stream) finish(it *stItem, item map[string]any) {
	it.done = true
	it.cut = stStr(item["status"]) == "incomplete"
	var parts []string
	sep := ""
	whole := false // the item states its whole text
	switch it.kind {
	case stMessage:
		list, ok := item["content"].([]any)
		whole = ok
		for _, p := range list {
			part := stMap(p)
			text := "" // other part types are not for the reader
			switch stStr(part["type"]) {
			case "output_text":
				text = stStr(part["text"])
			case "refusal":
				text = stStr(part["refusal"])
				it.refused = true
			}
			parts = append(parts, text)
		}
	case stReasonIt:
		sep = "\n\n"
		list, _ := item["summary"].([]any)
		for _, p := range list {
			parts = append(parts, stStr(stMap(p)["text"]))
		}
		it.enc = stStr(item["encrypted_content"])
		whole = len(parts) > 0
	case stSearch:
		it.query, it.urls = stSources(item)
	case stCall:
		args := item["arguments"]
		if m := stMap(args); m != nil {
			raw, _ := json.Marshal(m)
			args = string(raw)
		}
		parts = append(parts, stStr(args))
		whole = strings.TrimSpace(parts[0]) != ""
	}
	if !whole || !s.shows(it) {
		return
	}
	full := strings.Join(parts, sep)
	if it.out.Len() == 0 {
		it.pending.Reset() // nothing went out yet: the finished text replaces the deltas
		it.pending.WriteString(full)
		return
	}
	if rest, ok := strings.CutPrefix(full, it.out.String()); ok {
		it.pending.Reset() // a tail held for a stop sequence is part of rest
		it.pending.WriteString(rest)
		return
	}
	// Text that went out cannot be recalled. If every part only grew, the tails still go out.
	grew := !it.unkeyed
	for i, p := range parts {
		grew = grew && strings.HasPrefix(p, it.part(i))
	}
	for i, p := range parts {
		if grew {
			s.receive(it, i, p[len(it.part(i)):])
		}
	}
	if it.kind != stReasonIt {
		it.final, it.hasFinal = full, true
		s.note(stNoteDiffers)
	}
}

// adopt takes the final response.output of a stream that had no item events and showed nothing.
func (s *Stream) adopt(resp map[string]any) {
	if s.events {
		return
	}
	for _, it := range s.live {
		if it.queued {
			return
		}
	}
	list, _ := resp["output"].([]any)
	for i, v := range list {
		if item := stMap(v); item != nil {
			s.item(map[string]any{"output_index": float64(i)}, item, true)
		}
	}
}

func (s *Stream) readUsage(resp map[string]any) {
	u := stMap(resp["usage"])
	if u == nil {
		return
	}
	cached := max(0, stInt(stMap(u["input_tokens_details"])["cached_tokens"]))
	s.usage = dto.Usage{
		InputTokens:          max(0, stInt(u["input_tokens"])-cached),
		CacheReadInputTokens: cached,
		OutputTokens:         max(0, stInt(u["output_tokens"])),
	}
}

// ---- Anthropic side ----

func stUsageMap(u dto.Usage) map[string]any {
	return map[string]any{
		"input_tokens": u.InputTokens, "output_tokens": u.OutputTokens,
		"cache_creation_input_tokens": u.CacheCreationInputTokens, "cache_read_input_tokens": u.CacheReadInputTokens,
	}
}

func stEvent(name string, data map[string]any) dto.Event {
	data["type"] = name
	return dto.Event{Name: name, Data: data}
}

func (s *Stream) toolName(it *stItem) string {
	if n, ok := s.ctx.ToolNames[it.name]; ok {
		return n
	}
	return it.name
}

func (s *Stream) signature(it *stItem) string {
	if it.enc == "" {
		return it.sig
	}
	return EncodeSignature(s.ctx.UpstreamModel, it.enc)
}

func (s *Stream) start(out []dto.Event) []dto.Event {
	if s.started {
		return out
	}
	s.started = true
	if s.id == "" {
		var b [12]byte
		rand.Read(b[:])
		s.id = "msg_" + hex.EncodeToString(b[:])
	}
	return append(out, stEvent("message_start", map[string]any{"message": map[string]any{
		"id": s.id, "type": "message", "role": "assistant", "model": s.ctx.PublicModel, "content": []any{},
		"stop_reason": nil, "stop_sequence": nil, "usage": stUsageMap(dto.Usage{}),
	}}))
}

// drain emits whatever the head of the queue allows. final ends every item: one that never got done is dropped, unless
// part of it is out already.
func (s *Stream) drain(out []dto.Event, final bool) []dto.Event {
	for len(s.queue) > 0 {
		it := s.queue[0]
		if s.stopSeq != "" && it.block < 0 {
			s.queue = s.queue[1:] // the answer is over
			continue
		}
		if it.block < 0 {
			switch {
			case it.hidden:
				s.pop()
				continue
			case s.answered && it.kind == stMessage && it.phase == "commentary":
				it.hidden = true
				s.note(stNoteCommentary)
				s.pop()
				continue
			case final && !it.done:
				it.hidden = true
				s.note(stNoteUnfinished)
				s.pop()
				continue
			case s.answered && it.kind == stMessage && it.phase == "" && !it.done:
				return out // it may turn out to be commentary
			case it.kind == stSearch:
				if !it.done {
					return out
				}
				out = s.emitSearch(out, it)
				s.pop()
				continue
			case it.kind == stCall && it.done && (it.callID == "" || it.name == ""):
				return s.fail(out, &dto.APIError{Status: 502, Type: "api_error", Message: "upstream sent a function call without a name or call_id"})
			}
			out = s.open(out, it)
		}
		closing := it.done || final
		if it.block >= 0 {
			out = s.flush(out, it)
			if closing {
				if !it.done {
					s.note(stNoteClosed)
				}
				out = s.close(out, it)
			}
		}
		if !closing {
			return out
		}
		s.pop()
	}
	return out
}

func (s *Stream) pop() {
	it := s.queue[0]
	s.queue = s.queue[1:]
	if it.hidden {
		return
	}
	s.refused = s.refused || it.refused
	s.answered = s.answered || it.block >= 0 && it.phase == "final_answer"
}

// emitSearch writes the two blocks of one search. They hold two indexes, so a placeholder keeps the count right.
func (s *Stream) emitSearch(out []dto.Event, it *stItem) []dto.Event {
	if it.query == "" {
		return out
	}
	it.block = len(s.blocks)
	s.blocks = append(s.blocks, it, &stItem{kind: stIgnored})
	out = s.start(out)
	for i, b := range stSearchBlocks(it) {
		index := it.block + i
		input, isUse := b["input"]
		if isUse {
			b["input"] = map[string]any{}
		}
		out = append(out, stEvent("content_block_start", map[string]any{"index": index, "content_block": b}))
		if isUse {
			raw, _ := json.Marshal(input)
			out = append(out, stEvent("content_block_delta", map[string]any{"index": index, "delta": map[string]any{"type": "input_json_delta", "partial_json": string(raw)}}))
		}
		out = append(out, stEvent("content_block_stop", map[string]any{"index": index}))
	}
	return out
}

func stSearchBlocks(it *stItem) []map[string]any {
	id := "srvtoolu_" + strings.TrimPrefix(it.id, "ws_")
	if it.id == "" {
		id += strconv.Itoa(it.block) // stable: Message builds the blocks again
	}
	results := []any{}
	for _, u := range it.urls {
		results = append(results, map[string]any{"type": "web_search_result", "url": u, "title": stTitle(u), "encrypted_content": "", "page_age": nil})
	}
	return []map[string]any{
		{"type": "server_tool_use", "id": id, "name": "web_search", "input": map[string]any{"query": it.query}},
		{"type": "web_search_tool_result", "tool_use_id": id, "content": results},
	}
}

// stTitle names a page by its address: Codex sends no titles with its sources.
func stTitle(u string) string {
	u, _, _ = strings.Cut(u, "?")
	_, rest, ok := strings.Cut(u, "://")
	if !ok {
		return u
	}
	return strings.TrimSuffix(rest, "/")
}

func (s *Stream) open(out []dto.Event, it *stItem) []dto.Event {
	var block map[string]any
	switch it.kind {
	case stMessage:
		if it.pending.Len() == 0 {
			return out
		}
		block = map[string]any{"type": "text", "text": ""}
	case stReasonIt:
		if s.ctx.HideThinking || it.pending.Len() == 0 && s.signature(it) == "" {
			return out
		}
		block = map[string]any{"type": "thinking", "thinking": "", "signature": ""}
	case stCall:
		if it.callID == "" || it.name == "" || it.pending.Len() == 0 && !it.done {
			return out
		}
		if it.cut && it.pending.Len() == 0 {
			s.note(stNoteUnfinished)
			return out
		}
		block = map[string]any{"type": "tool_use", "id": it.callID, "name": s.toolName(it), "input": map[string]any{}}
	}
	it.block = len(s.blocks)
	s.blocks = append(s.blocks, it)
	return append(s.start(out), stEvent("content_block_start", map[string]any{"index": it.block, "content_block": block}))
}

func (s *Stream) flush(out []dto.Event, it *stItem) []dto.Event {
	text := it.pending.String()
	if text == "" {
		return out
	}
	it.pending.Reset()
	if it.kind == stMessage && len(s.ctx.StopSeqs) > 0 {
		if s.stopSeq != "" {
			return out
		}
		cut, seq := s.cutStop(text)
		if seq != "" {
			text, s.stopSeq = cut, seq
		} else if hold := s.stopPrefix(text); hold > 0 && !it.closing {
			it.pending.WriteString(text[len(text)-hold:]) // may be the start of a stop sequence
			text = text[:len(text)-hold]
		}
		if text == "" {
			return out
		}
	}
	it.out.WriteString(text)
	var delta map[string]any
	switch it.kind {
	case stMessage:
		delta = map[string]any{"type": "text_delta", "text": text}
	case stReasonIt:
		delta = map[string]any{"type": "thinking_delta", "thinking": text}
	default:
		delta = map[string]any{"type": "input_json_delta", "partial_json": text}
	}
	return append(out, stEvent("content_block_delta", map[string]any{"index": it.block, "delta": delta}))
}

// cutStop returns the text before the earliest stop sequence, and that sequence.
func (s *Stream) cutStop(text string) (string, string) {
	at, seq := len(text), ""
	for _, q := range s.ctx.StopSeqs {
		if i := strings.Index(text, q); q != "" && i >= 0 && (i < at || seq == "" && i == at) {
			at, seq = i, q
		}
	}
	return text[:at], seq
}

// stopPrefix is the length of the longest end of the text that a stop sequence could start with.
func (s *Stream) stopPrefix(text string) int {
	best := 0
	for _, q := range s.ctx.StopSeqs {
		for n := min(len(q)-1, len(text)); n > best; n-- {
			if strings.HasSuffix(text, q[:n]) {
				best = n
			}
		}
	}
	return best
}

// Estimated is true when the usage is ours, not what Codex counted.
func (s *Stream) Estimated() bool { return s.estimated }

// endAtStop ends the answer at once: the rest would only cost time.
func (s *Stream) endAtStop(out []dto.Event) []dto.Event {
	n := 0
	for _, it := range s.blocks {
		n += it.out.Len()
		if it.kind == stMessage && !it.closed {
			out = append(out, stEvent("content_block_stop", map[string]any{"index": it.block}))
			it.closed = true
		}
	}
	s.estimated = true
	s.usage = dto.Usage{InputTokens: s.ctx.InputEstimate, OutputTokens: n/4 + 1}
	s.stop, s.ended, s.queue = "stop_sequence", true, nil
	out = s.start(out)
	return append(out,
		stEvent("message_delta", map[string]any{"delta": map[string]any{"stop_reason": s.stop, "stop_sequence": s.stopSeq}, "usage": stUsageMap(s.usage)}),
		stEvent("message_stop", map[string]any{}))
}

func (s *Stream) close(out []dto.Event, it *stItem) []dto.Event {
	it.closed = true
	if it.kind == stMessage {
		it.closing = true
		out = s.flush(out, it)
	}
	if it.kind == stCall && it.done && !it.cut && strings.TrimSpace(it.out.String()) == "" {
		it.pending.WriteString("{}")
		out = s.flush(out, it)
	}
	if sig := s.signature(it); sig != "" {
		out = append(out, stEvent("content_block_delta", map[string]any{"index": it.block, "delta": map[string]any{"type": "signature_delta", "signature": sig}}))
	}
	return append(out, stEvent("content_block_stop", map[string]any{"index": it.block}))
}

// complete ends the message. stop "" means the reason follows from the content.
func (s *Stream) complete(resp map[string]any, stop string) []dto.Event {
	s.readUsage(resp)
	s.adopt(resp)
	out := s.drain(nil, true)
	if s.ended {
		return out
	}
	for _, it := range s.live {
		if !it.queued && it.kind != stIgnored {
			s.note(stNoteUnknown)
		}
	}
	if stop == "" {
		stop = "end_turn"
		if s.refused {
			stop = "refusal"
		}
		for _, it := range s.blocks {
			if it.kind == stCall {
				stop = "tool_use"
			}
		}
		if stop == "end_turn" && resp["end_turn"] == false {
			s.note(stNoteEndTurn)
		}
	}
	var seq any
	if s.stopSeq != "" {
		stop, seq = "stop_sequence", s.stopSeq
	}
	s.stop, s.ended = stop, true
	out = s.start(out)
	return append(out,
		stEvent("message_delta", map[string]any{"delta": map[string]any{"stop_reason": stop, "stop_sequence": seq}, "usage": stUsageMap(s.usage)}),
		stEvent("message_stop", map[string]any{}))
}

func (s *Stream) fail(out []dto.Event, e *dto.APIError) []dto.Event {
	s.failed, s.ended = e, true
	return append(out, stEvent("error", map[string]any{"error": map[string]any{"type": e.Type, "message": e.Message}}))
}
