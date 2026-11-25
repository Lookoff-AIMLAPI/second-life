package translate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Lookoff-AIMLAPI/second-life/internal/dto"
)

const (
	reqPause         = "(Paused here: the conversation is about to be summarised.)"
	reqBillingPrefix = "x-anthropic-billing-header:"
	reqErrorPrefix   = "[tool error] "
)

// ToResponses translates a Messages request. cacheKey becomes the upstream prompt cache key.
func ToResponses(req *dto.MessagesRequest, upstream, cacheKey string) (*dto.Translated, *dto.APIError) {
	b := newReqBuilder(upstream, reqStyle{who: "Codex", inline: true, search: true, callID: reqCallID,
		reasoning: func(signature string) map[string]any {
			enc, ok := DecodeSignature(signature, upstream)
			if !ok {
				return nil
			}
			return map[string]any{"type": "reasoning", "summary": []any{}, "encrypted_content": enc}
		}})
	r, err := b.build(req)
	if err != nil {
		return nil, err
	}
	return &dto.Translated{
		Body: dto.ResponsesRequest{
			Model:             upstream,
			Instructions:      r.instructions,
			Input:             b.input,
			Tools:             r.tools,
			ToolChoice:        r.choice,
			ParallelToolCalls: req.ToolChoice == nil || !req.ToolChoice.DisableParallelToolUse,
			Reasoning:         r.reasoning,
			Text:              r.text,
			Store:             false,
			Stream:            true,
			Include:           reqInclude(r.search),
			PromptCacheKey:    cacheKey,
		},
		Compaction: r.compaction,
		ToolNames:  b.names,
		Warnings:   append(reqWarnings(req), b.warnings...),
	}, nil
}

// reqStyle is how the upstreams differ.
type reqStyle struct {
	who       string                                // in warnings
	inline    bool                                  // loaded tools enter the conversation, and assistant messages have a phase
	search    bool                                  // upstream runs web_search
	callID    func(id string) string                // Claude's call id -> upstream's
	reasoning func(signature string) map[string]any // the input item to send back, or nil

	noFileURL, noFileData string // why such a document is refused; empty when it is not
}

func newReqBuilder(upstream string, style reqStyle) *reqBuilder {
	return &reqBuilder{
		reqStyle: style,
		upstream: upstream,
		names:    map[string]string{},
		tools:    reqToolSet{defs: map[string]reqTool{}, removed: map[string]bool{}},
		answered: map[string]bool{},
		called:   map[string]bool{},
		input:    []map[string]any{},
		runStart: -1,
	}
}

// reqBuilt is everything but the input.
type reqBuilt struct {
	instructions string
	tools        []any
	search       bool
	choice       any
	reasoning    dto.Reasoning
	text         map[string]any
	compaction   bool
}

func (b *reqBuilder) build(req *dto.MessagesRequest) (*reqBuilt, *dto.APIError) {
	if req == nil || len(req.Messages) == 0 {
		return nil, reqInvalid("messages: at least one message is required")
	}
	if !reqNull(req.Thread) {
		return nil, reqInvalid("thread: message threads are not supported; send the whole conversation")
	}
	r := &reqBuilt{}
	var err *dto.APIError
	if r.instructions, err = reqInstructions(req.System, b.upstream); err != nil {
		return nil, err
	}
	for i, t := range req.Tools {
		if err := b.declare(t, fmt.Sprintf("tools.%d", i)); err != nil {
			return nil, err
		}
	}
	// Every message is parsed before tools are filtered: a reference may come at any point.
	turns, err := b.parse(req.Messages)
	if err != nil {
		return nil, err
	}
	b.tools.turn, b.tools.early = len(turns), true
	if c := req.ToolChoice; c != nil && c.Type == "tool" {
		b.tools.use(c.Name)
	}
	final := b.tools.final()
	for i, t := range turns {
		if err := b.showTools(place{i, true}); err != nil {
			return nil, err
		}
		if err := b.turn(t); err != nil {
			return nil, err
		}
		if err := b.showTools(place{i, false}); err != nil {
			return nil, err
		}
	}
	b.endRun()
	if err := b.showTools(place{len(turns), true}); err != nil {
		return nil, err
	}
	if r.choice, err = b.toolChoice(req.ToolChoice, final); err != nil {
		return nil, err
	}
	shown := final
	if b.inline {
		shown = b.tools.always()
	}
	if r.tools, r.search, err = b.upstreamTools(shown); err != nil {
		return nil, err
	}
	if r.reasoning, err = reqReasoning(req.Thinking, req.OutputConfig); err != nil {
		return nil, err
	}
	if r.text, err = reqTextFormat(req.OutputConfig); err != nil {
		return nil, err
	}
	r.compaction = reqCompaction(turns)
	if n := len(b.input); r.compaction && n > 1 && b.input[n-2]["type"] == "function_call_output" {
		// A tool result just before the request reads as work still to do: smaller models then answer the task instead
		// of summarising, and Claude Code loses the session ("summarization produced empty response").
		pause := reqMessage("assistant", reqText("output_text", reqPause))
		if b.inline {
			pause["phase"] = "final_answer"
		}
		b.input = append(b.input[:n-1:n-1], pause, b.input[n-1])
	}
	return r, nil
}

func reqInvalid(format string, args ...any) *dto.APIError {
	return &dto.APIError{Status: 400, Type: "invalid_request_error", Message: fmt.Sprintf(format, args...)}
}

func reqUnsupported(at, kind string) *dto.APIError {
	return reqInvalid("%s: content block type %q is not supported here", at, kind)
}

func reqNull(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || string(trimmed) == "null"
}

// reqBlocks decodes a string or a block array; allowed lists the block types valid at this place.
// Error messages never quote the content itself.
func reqBlocks(raw json.RawMessage, at string, allowed ...string) ([]dto.Block, *dto.APIError) {
	if text := bytes.TrimSpace(raw); len(text) > 0 && text[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return nil, reqInvalid("%s: malformed content", at)
		}
		return []dto.Block{{Type: "text", Text: s}}, nil
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return nil, reqInvalid("%s: content must be a string or an array of blocks", at)
	}
	blocks := make([]dto.Block, 0, len(items))
	for i, item := range items {
		// The type comes first: an unknown block may not fit the Block shape at all.
		var head struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(item, &head) != nil {
			return nil, reqInvalid("%s.%d: malformed content block", at, i)
		}
		here := fmt.Sprintf("%s.%d", at, i)
		server := strings.HasSuffix(head.Type, reqServerResult)
		if !reqIn(head.Type, allowed) && !(server && reqIn(reqServerResult, allowed)) {
			return nil, reqUnsupported(here, head.Type)
		}
		var block dto.Block
		switch {
		case head.Type == "search_result":
			text, err := reqSearchResult(item, here)
			if err != nil {
				return nil, err
			}
			block = dto.Block{Type: "text", Text: text}
		case server || reqIn(head.Type, reqServerBlocks):
			// These come in many shapes; only what is used is read.
			var s struct {
				ID        string          `json:"id"`
				ToolUseID string          `json:"tool_use_id"`
				Content   json.RawMessage `json:"content"`
			}
			if json.Unmarshal(item, &s) != nil {
				return nil, reqInvalid("%s: malformed content block", here)
			}
			block = dto.Block{Type: head.Type, ID: s.ID, ToolUseID: s.ToolUseID, Content: s.Content}
		case json.Unmarshal(item, &block) != nil:
			return nil, reqInvalid("%s: malformed content block", here)
		}
		blocks = append(blocks, block)
	}
	return blocks, nil
}

func reqIn(s string, list []string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func reqInstructions(system json.RawMessage, upstream string) (string, *dto.APIError) {
	var parts []string
	if !reqNull(system) {
		blocks, err := reqBlocks(system, "system", "text")
		if err != nil {
			return "", err
		}
		for _, b := range blocks {
			if b.Text != "" && !strings.HasPrefix(b.Text, reqBillingPrefix) {
				parts = append(parts, b.Text)
			}
		}
	}
	parts = append(parts, "You are served by "+upstream+" through second-life.")
	return strings.Join(parts, "\n\n"), nil
}

type reqBlock struct {
	dto.Block
	at    string
	inner []dto.Block // tool_result content
}

type reqTurn struct {
	role   string
	blocks []reqBlock
}

type reqBuilder struct {
	reqStyle
	upstream string
	names    map[string]string // upstream -> Claude, changed names only
	tools    reqToolSet
	answered map[string]bool // positions of tool_use blocks that get a result later
	called   map[string]bool // tool_use ids sent and still waiting for a result
	warnings []string
	input    []map[string]any
	runStart int              // where the current run of assistant messages starts in input, or -1
	parts    []map[string]any // message content waiting for a flush
}

func (b *reqBuilder) warn(text string) {
	if !reqIn(text, b.warnings) {
		b.warnings = append(b.warnings, text)
	}
}

var reqAllowed = map[string][]string{
	"user":      {"text", "image", "document", "search_result", "tool_result"},
	"assistant": append([]string{"text", "thinking", "redacted_thinking", "tool_use", reqServerResult}, reqServerBlocks...),
	"system":    {"text", "tool_addition", "tool_removal"},
}

// parse decodes all messages and records which deferred tools they load.
func (b *reqBuilder) parse(messages []dto.Message) ([]reqTurn, *dto.APIError) {
	turns := make([]reqTurn, 0, len(messages))
	waiting := map[string]string{} // tool_use id -> position, until its result
	served := map[string]bool{}    // ids of server tool calls that have a result
	var server []string            // ids of server tool calls
	for i, m := range messages {
		b.tools.turn, b.tools.early = i, m.Role == "assistant"
		allowed, ok := reqAllowed[m.Role]
		if !ok {
			return nil, reqInvalid("messages.%d: role %q is not supported", i, m.Role)
		}
		at := fmt.Sprintf("messages.%d.content", i)
		blocks, err := reqBlocks(m.Content, at, allowed...)
		if err != nil {
			return nil, err
		}
		turn := reqTurn{role: m.Role}
		for j, block := range blocks {
			rb := reqBlock{Block: block, at: fmt.Sprintf("%s.%d", at, j)}
			switch block.Type {
			case "tool_use":
				b.tools.use(block.Name)
				waiting[block.ID] = rb.at
			case "tool_result":
				if at, ok := waiting[block.ToolUseID]; ok {
					b.answered[at] = true
					delete(waiting, block.ToolUseID)
				}
				if reqNull(block.Content) {
					break
				}
				rb.inner, err = reqBlocks(block.Content, rb.at+".content", "text", "image", "document", "search_result", "tool_reference")
				if err != nil {
					return nil, err
				}
				for _, in := range rb.inner {
					if in.Type == "tool_reference" {
						b.tools.load(in.ToolName)
					}
				}
			case "tool_addition", "tool_removal":
				if err := b.toolChange(block, rb.at); err != nil {
					return nil, err
				}
			case "server_tool_use", "mcp_tool_use":
				server = append(server, block.ID)
			default:
				if strings.HasSuffix(block.Type, reqServerResult) {
					served[block.ToolUseID] = true
					for _, name := range reqToolRefs(block) {
						b.tools.load(name)
					}
				}
			}
			turn.blocks = append(turn.blocks, rb)
		}
		turns = append(turns, turn)
	}
	for _, id := range server {
		if !served[id] {
			b.warn("a server tool call without a result was dropped from the history")
		}
	}
	return turns, nil
}

func (b *reqBuilder) turn(t reqTurn) *dto.APIError {
	role, textType := t.role, "input_text"
	switch role {
	case "assistant":
		textType = "output_text"
	case "system":
		role = "developer"
	}
	if role != "assistant" {
		b.endRun()
	} else if b.runStart < 0 {
		b.runStart = len(b.input)
	}
	var open []string // calls of this turn that never get a result: Codex refuses them, so they are closed here
	for _, block := range t.blocks {
		switch block.Type {
		case "server_tool_use", "mcp_tool_use": // the result carries what matters
		case "fallback", "tool_addition", "tool_removal":
		case "mcp_tool_listing", "container_upload":
			b.warn("a server block that " + b.who + " cannot use was dropped from the history")
		case "text", "image", "document":
			parts, err := b.part(block.Block, block.at, textType)
			if err != nil {
				return err
			}
			b.parts = append(b.parts, parts...)
		case "thinking", "redacted_thinking":
			b.flush(role)
			if item := b.reasoning(block.Signature); item != nil && block.Type == "thinking" {
				b.input = append(b.input, item)
			}
		case "tool_use":
			b.flush(role)
			item, err := b.toolUse(block)
			if err != nil {
				return err
			}
			b.input = append(b.input, item)
			b.called[block.ID] = true
			if !b.answered[block.at] {
				open = append(open, block.ID)
			}
		case "tool_result":
			b.flush(role)
			item, err := b.toolResult(block)
			if err != nil {
				return err
			}
			if !b.called[block.ToolUseID] {
				// No call to attach to (history was cut, or a second result): keep the content as a message.
				b.input = append(b.input, reqLooseResult(item["output"])...)
				break
			}
			delete(b.called, block.ToolUseID)
			b.input = append(b.input, item)
		default:
			if !strings.HasSuffix(block.Type, reqServerResult) {
				return reqUnsupported(block.at, block.Type)
			}
			text := b.serverText(block.Block)
			if text == "" {
				b.warn("a server tool result that " + b.who + " cannot read was dropped from the history")
				break
			}
			b.flush(role)                                                              // not the assistant's own words
			b.input = append(b.input, reqMessage("user", reqText("input_text", text))) // not developer: web and tool text must not outrank the owner
		}
	}
	b.flush(role)
	for _, id := range open {
		delete(b.called, id)
		b.input = append(b.input, map[string]any{"type": "function_call_output", "call_id": b.callID(id), "output": reqNoResult})
	}
	return nil
}

// endRun labels the assistant messages of a run of assistant turns: Codex answers worse when the label is lost. Only
// the last text is the answer, and only when no call follows it.
func (b *reqBuilder) endRun() {
	if b.runStart < 0 || !b.inline {
		b.runStart = -1
		return
	}
	more := false
	for i := len(b.input) - 1; i >= b.runStart; i-- {
		item := b.input[i]
		switch {
		case item["type"] == "function_call":
			more = true
		case item["type"] == "message" && item["role"] == "assistant":
			item["phase"] = "final_answer"
			if more {
				item["phase"] = "commentary"
			}
			more = true
		}
	}
	b.runStart = -1
}

func reqMessage(role string, parts ...map[string]any) map[string]any {
	return map[string]any{"type": "message", "role": role, "content": parts}
}

func (b *reqBuilder) flush(role string) {
	if len(b.parts) == 0 {
		return
	}
	b.input = append(b.input, reqMessage(role, b.parts...))
	b.parts = nil
}

// part translates one content block; it returns nothing for an empty text.
func (b *reqBuilder) part(block dto.Block, at, textType string) ([]map[string]any, *dto.APIError) {
	switch block.Type {
	case "text":
		return reqTexts(textType, block.Text), nil
	case "tool_reference":
		if block.ToolName == "" {
			return nil, reqInvalid("%s: tool_reference needs tool_name", at)
		}
		// The text uses the upstream name, the only one the model can call.
		return reqTexts(textType, b.available(block.ToolName)), nil
	case "image":
		image, err := reqImage(block.Source, at)
		if err != nil {
			return nil, err
		}
		return []map[string]any{image}, nil
	case "document":
		return b.document(block, at, textType)
	}
	return nil, reqUnsupported(at, block.Type)
}

func (b *reqBuilder) available(tool string) string {
	return "Tool " + b.name(tool) + " is now available."
}

func reqText(textType, text string) map[string]any {
	return map[string]any{"type": textType, "text": text}
}

func reqTexts(textType, text string) []map[string]any {
	if text == "" {
		return nil
	}
	return []map[string]any{reqText(textType, text)}
}

func reqImage(src *dto.Source, at string) (map[string]any, *dto.APIError) {
	if src == nil {
		return nil, reqInvalid("%s: image needs a source", at)
	}
	url := src.URL
	switch src.Type {
	case "base64":
		if src.MediaType == "" || src.Data == "" {
			return nil, reqInvalid("%s: base64 image needs media_type and data", at)
		}
		url = reqDataURL(src)
	case "url":
		if url == "" {
			return nil, reqInvalid("%s: url image needs a url", at)
		}
	default:
		return nil, reqInvalid("%s: image source type %q is not supported", at, src.Type)
	}
	return map[string]any{"type": "input_image", "image_url": url}, nil
}

func (b *reqBuilder) document(block dto.Block, at, textType string) ([]map[string]any, *dto.APIError) {
	src := block.Source
	if src == nil {
		return nil, reqInvalid("%s: document needs a source", at)
	}
	head := ""
	if block.Title != "" {
		head = "Document: " + block.Title
	}
	if block.Context != "" {
		head = strings.TrimPrefix(head+"\nContext: "+block.Context, "\n")
	}
	switch src.Type {
	case "text":
		if head != "" && src.Data != "" {
			head += "\n\n"
		}
		return reqTexts(textType, head+src.Data), nil
	case "content":
		blocks, err := reqBlocks(src.Content, at+".source.content", "text", "image")
		if err != nil {
			return nil, err
		}
		parts := reqTexts(textType, head)
		for i, in := range blocks {
			more, err := b.part(in, fmt.Sprintf("%s.source.content.%d", at, i), textType)
			if err != nil {
				return nil, err
			}
			parts = append(parts, more...)
		}
		return parts, nil
	case "url":
		if src.URL == "" {
			return nil, reqInvalid("%s: url document needs a url", at)
		}
		if b.noFileURL != "" {
			return nil, reqInvalid("%s: %s", at, b.noFileURL)
		}
		return append(reqTexts(textType, head), map[string]any{"type": "input_file", "file_url": src.URL}), nil
	case "base64":
		if src.MediaType != "application/pdf" {
			return nil, reqInvalid("%s: document media type %q is not supported", at, src.MediaType)
		}
		if src.Data == "" {
			return nil, reqInvalid("%s: base64 document needs data", at)
		}
		if b.noFileData != "" {
			return nil, reqInvalid("%s: %s", at, b.noFileData)
		}
		name := block.Title // carries the title, so only the context is prepended
		if name == "" {
			name = "document.pdf"
		}
		var parts []map[string]any
		if block.Context != "" {
			parts = reqTexts(textType, "Context: "+block.Context)
		}
		return append(parts, map[string]any{"type": "input_file", "filename": name, "file_data": reqDataURL(src)}), nil
	}
	return nil, reqInvalid("%s: document source type %q is not supported", at, src.Type)
}

func reqDataURL(src *dto.Source) string {
	return "data:" + src.MediaType + ";base64," + src.Data
}

func (b *reqBuilder) toolUse(block reqBlock) (map[string]any, *dto.APIError) {
	if block.ID == "" || block.Name == "" {
		return nil, reqInvalid("%s: tool_use needs id and name", block.at)
	}
	arguments := "{}"
	if !reqNull(block.Input) {
		arguments = string(block.Input) // raw: re-encoding would lose big numbers and key order
	}
	return map[string]any{"type": "function_call", "call_id": b.callID(block.ID), "name": b.name(block.Name), "arguments": arguments}, nil
}

func (b *reqBuilder) toolResult(block reqBlock) (map[string]any, *dto.APIError) {
	if block.ToolUseID == "" {
		return nil, reqInvalid("%s: tool_result needs tool_use_id", block.at)
	}
	var parts []map[string]any
	var texts []string
	for i, in := range block.inner {
		more, err := b.part(in, fmt.Sprintf("%s.content.%d", block.at, i), "input_text")
		if err != nil {
			return nil, err
		}
		parts = append(parts, more...)
		for _, part := range more {
			if text, ok := part["text"].(string); ok {
				texts = append(texts, text)
			}
		}
	}
	prefix := ""
	if block.IsError {
		prefix = reqErrorPrefix
	}
	var output any = prefix + strings.Join(texts, "\n")
	if len(texts) < len(parts) {
		if prefix != "" {
			if text, ok := parts[0]["text"].(string); ok {
				parts[0]["text"] = prefix + text
			} else {
				parts = append([]map[string]any{reqText("input_text", prefix)}, parts...)
			}
		}
		output = parts
	}
	return map[string]any{"type": "function_call_output", "call_id": b.callID(block.ToolUseID), "output": output}, nil
}

// name maps a Claude tool name to its upstream form and remembers the change for the way back.
func (b *reqBuilder) name(original string) string {
	short := reqToolName(original)
	if short != original {
		b.names[short] = original
	}
	return short
}

func (b *reqBuilder) toolChoice(c *dto.ToolChoice, final []reqTool) (any, *dto.APIError) {
	if c == nil {
		return "auto", nil
	}
	switch c.Type {
	case "auto", "none":
		return c.Type, nil
	case "any":
		if len(final) > 0 {
			return "required", nil
		}
	case "tool":
		if c.Name == "" {
			return nil, reqInvalid("tool_choice: type tool needs a name")
		}
		for _, t := range final {
			if t.Name != c.Name {
				continue
			}
			if strings.HasPrefix(t.Type, reqSearchPrefix) {
				return map[string]any{"type": "web_search"}, nil
			}
			return map[string]any{"type": "function", "name": b.name(c.Name)}, nil
		}
	default:
		return nil, reqInvalid("tool_choice: type %q is not supported", c.Type)
	}
	b.warn("tool_choice asked for a tool that is not available and became auto")
	return "auto", nil
}

type reqTool struct {
	dto.Tool
	at   string
	base bool // always on: declared in tools without defer_loading
}

// reqToolSet follows which tools the model can see as the conversation goes.
type reqToolSet struct {
	defs    map[string]reqTool
	base    []string // always-on tools, as declared
	loaded  []string // the rest, in load order
	removed map[string]bool
	where   map[string]place // where a loaded tool enters the conversation
	turn    int              // the message being parsed
	early   bool             // it is an assistant message: tools go before it, so no call is split from its result
}

// place is a spot before or after a message.
type place struct {
	turn   int
	before bool
}

// load shows a known tool.
func (s *reqToolSet) load(name string) {
	t, ok := s.defs[name]
	if !ok {
		return
	}
	delete(s.removed, name)
	if !t.base && !reqIn(name, s.loaded) {
		s.loaded = append(s.loaded, name)
		if s.where == nil {
			s.where = map[string]place{}
		}
		s.where[name] = place{s.turn, s.early}
	}
}

// use shows a tool the history calls or tool_choice names, unless it was withdrawn.
func (s *reqToolSet) use(name string) {
	if !s.removed[name] {
		s.load(name)
	}
}

func (s *reqToolSet) remove(name string) {
	s.removed[name] = true
	for i, n := range s.loaded {
		if n == name {
			s.loaded = append(s.loaded[:i], s.loaded[i+1:]...)
			return
		}
	}
}

// always lists what goes into the request's tools. Anything added there later would reset the upstream prompt cache, so
// only the always-on tools and the search Codex runs itself are here; the rest enters through showTools.
func (s *reqToolSet) always() []reqTool {
	var out []reqTool
	for _, t := range s.final() {
		if t.base || strings.HasPrefix(t.Type, reqSearchPrefix) {
			out = append(out, t)
		}
	}
	return out
}

// showTools adds the tools loaded at this spot to the conversation itself.
func (b *reqBuilder) showTools(at place) *dto.APIError {
	if !b.inline {
		return nil
	}
	var tools []reqTool
	for _, name := range b.tools.loaded {
		t := b.tools.defs[name]
		if b.tools.where[name] == at && !t.base && !strings.HasPrefix(t.Type, reqSearchPrefix) {
			tools = append(tools, t)
		}
	}
	if len(tools) == 0 {
		return nil
	}
	list, _, err := b.upstreamTools(tools)
	if err != nil {
		return err
	}
	b.endRun()
	b.input = append(b.input, map[string]any{"type": "additional_tools", "role": "developer", "tools": list})
	return nil
}

func (s *reqToolSet) final() []reqTool {
	var out []reqTool
	for _, name := range append(append([]string{}, s.base...), s.loaded...) {
		if !s.removed[name] {
			out = append(out, s.defs[name])
		}
	}
	return out
}

// check validates one definition; ok is false for a server tool that is left out.
func (b *reqBuilder) check(t *dto.Tool, at string) (ok bool, err *dto.APIError) {
	switch {
	case strings.HasPrefix(t.Type, reqSearchPrefix) && b.search:
		if t.Name == "" {
			t.Name = "web_search"
		}
		return true, nil
	case strings.HasPrefix(t.Type, "advisor_"), strings.HasPrefix(t.Type, "tool_search_"):
		b.warn("a server tool that " + b.who + " cannot run was left out")
		return false, nil
	case t.Type != "" && t.Type != "custom":
		return false, reqInvalid("%s: tool type %q is not supported", at, t.Type)
	case t.Name == "":
		return false, reqInvalid("%s: name is required", at)
	}
	return true, nil
}

// declare takes an entry of the request's tools.
func (b *reqBuilder) declare(t dto.Tool, at string) *dto.APIError {
	ok, err := b.check(&t, at)
	if err != nil || !ok {
		return err
	}
	if _, twice := b.tools.defs[t.Name]; twice {
		return reqInvalid("%s: tool names must be unique", at)
	}
	b.tools.defs[t.Name] = reqTool{Tool: t, at: at, base: !t.DeferLoading}
	if !t.DeferLoading {
		b.tools.base = append(b.tools.base, t.Name)
	}
	return nil
}

// toolChange applies a tool_addition or tool_removal block of a system message.
func (b *reqBuilder) toolChange(block dto.Block, at string) *dto.APIError {
	c := block.Tool
	if c == nil {
		return reqInvalid("%s: %s needs a tool", at, block.Type)
	}
	switch {
	case c.Type == "tool_reference" && c.Name == "":
		return reqInvalid("%s: tool_reference needs a name", at)
	case c.Type == "tool_reference" && block.Type == "tool_removal":
		b.tools.remove(c.Name)
	case c.Type == "tool_reference":
		b.tools.load(c.Name)
	case c.Type == "tool_definition" && block.Type == "tool_addition" && c.Definition != nil:
		t := *c.Definition
		ok, err := b.check(&t, at)
		if err != nil || !ok {
			return err
		}
		b.tools.defs[t.Name] = reqTool{Tool: t, at: at, base: b.tools.defs[t.Name].base}
		if !t.DeferLoading {
			b.tools.load(t.Name)
		}
	default:
		return reqInvalid("%s: %s of type %q is not supported", at, block.Type, c.Type)
	}
	return nil
}

func (b *reqBuilder) upstreamTools(final []reqTool) (out []any, search bool, err *dto.APIError) {
	for _, t := range final {
		if strings.HasPrefix(t.Type, reqSearchPrefix) {
			if t.MaxUses > 0 {
				b.warn("web_search max_uses is not supported upstream and was dropped")
			}
			out, search = append(out, reqWebSearch(t.Tool)), true
			continue
		}
		schema, err := reqSchema(t.InputSchema, t.at)
		if err != nil {
			return nil, false, err
		}
		out = append(out, dto.FunctionTool{Type: "function", Name: b.name(t.Name), Description: t.Description, Parameters: schema})
	}
	return out, search, nil
}

func reqSchema(raw json.RawMessage, at string) (json.RawMessage, *dto.APIError) {
	empty := json.RawMessage(`{"type":"object","properties":{}}`)
	if reqNull(raw) {
		return empty, nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil, reqInvalid("%s: input_schema must be a JSON object", at)
	}
	if len(fields) == 0 {
		return empty, nil
	}
	var kind string
	if json.Unmarshal(fields["type"], &kind) != nil || kind != "object" || !reqNull(fields["properties"]) {
		return raw, nil
	}
	fields["properties"] = json.RawMessage(`{}`)
	fixed, err := json.Marshal(fields)
	if err != nil {
		return nil, reqInvalid("%s: input_schema must be a JSON object", at)
	}
	return fixed, nil
}

func reqReasoning(t *dto.Thinking, c *dto.OutputConfig) (dto.Reasoning, *dto.APIError) {
	r := dto.Reasoning{Effort: "medium"}
	if c != nil && c.Effort != "" {
		if !reqIn(c.Effort, []string{"low", "medium", "high", "xhigh", "max"}) {
			return r, reqInvalid("output_config: effort %q is not supported", c.Effort)
		}
		r.Effort = c.Effort
	}
	if t != nil {
		if t.Type == "disabled" {
			r.Effort = "low"
		}
		if t.Display == "summarized" {
			r.Summary = "auto"
		}
	}
	return r, nil
}

// IsCompaction is true for Claude Code's request for a summary of the conversation.
func IsCompaction(req *dto.MessagesRequest) bool {
	if req == nil || len(req.Messages) == 0 {
		return false
	}
	m := req.Messages[len(req.Messages)-1]
	blocks, err := reqBlocks(m.Content, "", reqAllowed[m.Role]...)
	if err != nil {
		return false
	}
	t := reqTurn{role: m.Role}
	for _, b := range blocks {
		t.blocks = append(t.blocks, reqBlock{Block: b})
	}
	return reqCompaction([]reqTurn{t})
}

// reqCompaction recognises Claude Code's request for a summary of the conversation.
func reqCompaction(turns []reqTurn) bool {
	if len(turns) == 0 || turns[len(turns)-1].role != "user" {
		return false
	}
	blocks := turns[len(turns)-1].blocks
	if len(blocks) == 0 || blocks[len(blocks)-1].Type != "text" {
		return false
	}
	t := blocks[len(blocks)-1].Text
	return strings.Contains(t, "Respond with TEXT ONLY") && strings.Contains(t, "<summary>")
}

func reqTextFormat(c *dto.OutputConfig) (map[string]any, *dto.APIError) {
	if c == nil || c.Format == nil {
		return nil, nil
	}
	if c.Format.Type != "json_schema" {
		return nil, reqInvalid("output_config: format type %q is not supported", c.Format.Type)
	}
	if reqNull(c.Format.Schema) || !json.Valid(c.Format.Schema) {
		return nil, reqInvalid("output_config: format needs a valid schema")
	}
	return map[string]any{"format": map[string]any{
		"type":   "json_schema",
		"name":   "output",
		"schema": c.Format.Schema,
		"strict": false,
	}}, nil
}

func reqWarnings(req *dto.MessagesRequest) []string {
	var w []string
	if req.Temperature != nil {
		w = append(w, "temperature is not supported upstream and was dropped")
	}
	if req.TopP != nil {
		w = append(w, "top_p is not supported upstream and was dropped")
	}
	if req.TopK != nil {
		w = append(w, "top_k is not supported upstream and was dropped")
	}
	return w
}
