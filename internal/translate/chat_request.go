package translate

import (
	"encoding/json"
	"strings"

	"github.com/Lookoff-AIMLAPI/second-life/internal/dto"
)

const (
	chatMaxStop     = 4 // most servers take no more; ChatStream enforces all of them
	chatNoOutput    = "(no output)"
	chatSeeNext     = "(see the next message)"
	chatNoImage     = "[an image was left out: this model cannot see images]"
	chatPlaceholder = `{"google":{"thought_signature":"skip_thought_signature_validator"}}` // Google's own stand-in
	chatReminder    = "<system-reminder>"
	chatReminderEnd = "</system-reminder>"
)

// ToChat translates a Messages request for an OpenAI Chat Completions server.
func ToChat(req *dto.MessagesRequest, model string, o Options) (*dto.ChatTranslated, *dto.APIError) {
	identity := o.Identity(model)
	style := reqStyle{who: "the endpoint", callID: func(id string) string { return id },
		noFileURL: "a document by URL is not supported by this endpoint; send it as base64 or text",
		reasoning: func(signature string) map[string]any {
			field, text, ok := DecodeReasoning(signature, identity)
			if !ok || o.NoReasoning {
				return nil
			}
			return map[string]any{"type": "reasoning", "field": field, "text": text}
		}}
	if o.NoFiles {
		style.noFileData = "PDF documents are not supported by this endpoint; send the text"
	}
	b := newReqBuilder(model, style)
	r, err := b.build(req)
	if err != nil {
		return nil, err
	}
	if o.omits("response_format") {
		schemaAsText(b, r)
	}
	o.choose(b, r)

	c := &chatBuilder{o: o, b: b, identity: identity, tools: len(r.tools) > 0}
	c.out = []dto.ChatMessage{{Role: "system", Content: r.instructions}}
	c.messages()
	if c.turns == 0 {
		return nil, reqInvalid("messages: there is no content")
	}
	out := &dto.ChatTranslated{ToolNames: b.names, Compaction: r.compaction, Body: dto.ChatRequest{
		Model: model, Messages: c.out, Stream: true, ReasoningEffort: o.effort(req, r.reasoning),
	}}
	body := &out.Body
	if !o.omits("stream_options") {
		body.StreamOptions = &dto.ChatStreamOptions{IncludeUsage: true}
	}
	for _, t := range r.tools {
		if f, ok := t.(dto.FunctionTool); ok {
			body.Tools = append(body.Tools, dto.ChatTool{Type: "function",
				Function: dto.ChatFunctionDef{Name: f.Name, Description: f.Description, Parameters: f.Parameters}})
		}
	}
	if m, ok := r.choice.(map[string]any); ok {
		body.ToolChoice = map[string]any{"type": "function", "function": map[string]any{"name": m["name"]}}
	} else {
		body.ToolChoice = r.choice
	}
	if c := req.ToolChoice; c != nil && c.DisableParallelToolUse && len(body.Tools) > 0 && !o.omits("parallel_tool_calls") {
		body.ParallelToolCalls = new(bool)
	}
	if f, ok := r.text["format"].(map[string]any); ok {
		body.ResponseFormat = map[string]any{"type": f["type"],
			"json_schema": map[string]any{"name": f["name"], "schema": f["schema"], "strict": f["strict"]}}
	}
	switch n, field := o.maxTokens(b, req.MaxTokens); field {
	case "max_tokens":
		body.MaxTokens = n
	case "max_completion_tokens":
		body.MaxCompletionTokens = n
	}
	body.Temperature, body.TopP = o.sampling(b, req)
	if !o.omits("stop") {
		for _, s := range req.StopSeqs {
			if s != "" && len(body.Stop) < chatMaxStop {
				body.Stop = append(body.Stop, s)
			}
		}
	}
	out.Warnings = b.warnings
	return out, nil
}

// ToResponsesGeneric translates a Messages request for a Responses server that is not Codex.
func ToResponsesGeneric(req *dto.MessagesRequest, model string, o Options) (*dto.Translated, *dto.APIError) {
	identity := o.Identity(model)
	style := reqStyle{who: "the endpoint", search: o.WebSearch, callID: reqCallID,
		reasoning: func(signature string) map[string]any {
			enc, ok := DecodeSignature(signature, identity)
			if !ok || !o.Encrypted {
				return nil
			}
			return map[string]any{"type": "reasoning", "summary": []any{}, "encrypted_content": enc}
		}}
	if o.NoFiles {
		style.noFileData = "PDF documents are not supported by this endpoint; send the text"
		style.noFileURL = style.noFileData
	}
	b := newReqBuilder(model, style)
	r, err := b.build(req)
	if err != nil {
		return nil, err
	}
	if o.omits("text") {
		schemaAsText(b, r)
	}
	o.choose(b, r)
	for _, item := range b.input {
		for _, key := range []string{"content", "output"} {
			if parts, ok := item[key].([]map[string]any); ok {
				o.foldImages(b, parts)
			}
		}
		if item["role"] == "developer" && o.SystemFirstOnly {
			item["role"] = "user"
			for _, p := range item["content"].([]map[string]any) {
				p["text"] = Remind(stStr(p["text"]))
			}
		}
	}
	if len(b.input) == 0 {
		return nil, reqInvalid("messages: there is no content")
	}
	reasoning := dto.Reasoning{Effort: o.effort(req, r.reasoning)}
	if o.Effort && !o.omits("reasoning.summary") {
		reasoning.Summary = r.reasoning.Summary
	}
	body := dto.ResponsesRequest{
		Model: model, Instructions: r.instructions, Input: b.input, Tools: r.tools, ToolChoice: r.choice,
		ParallelToolCalls: true, Reasoning: reasoning, Text: r.text, Stream: true, Omit: o.Omit,
	}
	if !o.omits("include") {
		for _, v := range reqInclude(r.search) {
			if o.Encrypted || v != reqEncrypted {
				body.Include = append(body.Include, v)
			}
		}
	}
	if c := req.ToolChoice; c != nil && c.DisableParallelToolUse && len(r.tools) > 0 {
		body.ParallelToolCalls = false
	} else {
		body.Omit = append([]string{"parallel_tool_calls"}, body.Omit...)
	}
	body.MaxOutputTokens, _ = o.maxTokens(b, req.MaxTokens)
	body.Temperature, body.TopP = o.sampling(b, req)
	return &dto.Translated{Body: body, ToolNames: b.names, Warnings: b.warnings, Compaction: r.compaction}, nil
}

// foldImages replaces images in place.
func (o Options) foldImages(b *reqBuilder, parts []map[string]any) {
	for i, p := range parts {
		if p["type"] == "input_image" && o.NoImages {
			b.warn("an image was left out: the model cannot see images")
			parts[i] = reqText("input_text", chatNoImage)
		}
	}
}

// Remind wraps a system text that must go into a user message.
func Remind(text string) string {
	if t := strings.TrimSpace(text); strings.HasPrefix(t, chatReminder) && strings.HasSuffix(t, chatReminderEnd) {
		return text
	}
	return chatReminder + "\n" + text + "\n" + chatReminderEnd
}

// chatBuilder turns Responses input items into Chat messages.
type chatBuilder struct {
	o        Options
	b        *reqBuilder
	identity string
	tools    bool
	out      []dto.ChatMessage
	turns    int

	outputOf map[int]int // function_call item -> its function_call_output item
	answers  map[int]int // and back

	user  []map[string]any // parts of the user message being collected
	texts []string         // the assistant message being collected
	calls []int            // positions of its calls
	think map[string][]string
}

func (c *chatBuilder) messages() {
	items := c.b.input
	c.pair(items)
	for i, item := range items {
		switch {
		case item["type"] == "reasoning":
			if c.think == nil {
				c.think = map[string][]string{}
			}
			f := stStr(item["field"])
			c.think[f] = append(c.think[f], stStr(item["text"]))
		case item["type"] == "function_call":
			c.flushUser()
			c.calls = append(c.calls, i)
		case item["type"] == "function_call_output":
			c.flushAssistant()
			c.attachments(i, item)
		case item["role"] == "assistant":
			c.flushUser()
			c.texts = append(c.texts, chatTexts(item))
		case item["role"] == "developer" && !c.o.SystemFirstOnly:
			c.flushAssistant()
			c.flushUser()
			c.out = append(c.out, dto.ChatMessage{Role: "system", Content: chatTexts(item)})
		default:
			c.flushAssistant()
			parts, _ := item["content"].([]map[string]any)
			for _, p := range parts {
				if item["role"] == "developer" {
					p = reqText("input_text", Remind(stStr(p["text"])))
				}
				c.user = append(c.user, p)
			}
		}
	}
	c.flushAssistant()
	c.flushUser()
}

// pair finds the result of every call: the next one with its id that is still free.
func (c *chatBuilder) pair(items []map[string]any) {
	c.outputOf, c.answers = map[int]int{}, map[int]int{}
	waiting := map[string][]int{}
	for i, item := range items {
		id := stStr(item["call_id"])
		switch item["type"] {
		case "function_call":
			waiting[id] = append(waiting[id], i)
		case "function_call_output":
			if w := waiting[id]; len(w) > 0 {
				c.outputOf[w[0]], c.answers[i] = i, w[0]
				waiting[id] = w[1:]
			}
		}
	}
}

func chatTexts(item map[string]any) string {
	parts, _ := item["content"].([]map[string]any)
	return chatJoin(parts, "\n\n")
}

func chatJoin(parts []map[string]any, sep string) string {
	var texts []string
	for _, p := range parts {
		if t := stStr(p["text"]); t != "" {
			texts = append(texts, t)
		}
	}
	return strings.Join(texts, sep)
}

// attachments puts the images and files of a tool result into the user message that follows: a tool message is text.
func (c *chatBuilder) attachments(i int, item map[string]any) {
	parts, ok := item["output"].([]map[string]any)
	if !ok {
		return
	}
	var files []map[string]any
	for _, p := range parts {
		if p["type"] != "input_text" {
			files = append(files, p)
		}
	}
	if len(files) == 0 {
		return
	}
	name := ""
	if call, ok := c.answers[i]; ok {
		name = " of " + stStr(c.b.input[call]["name"])
	}
	c.user = append(append(c.user, reqText("input_text", "[attached to the result"+name+"]")), files...)
}

func (c *chatBuilder) flushUser() {
	if len(c.user) == 0 {
		return
	}
	c.o.foldImages(c.b, c.user)
	var content any
	var parts []map[string]any
	plain := true
	for _, p := range c.user {
		switch p["type"] {
		case "input_image":
			plain = false
			parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": p["image_url"]}})
		case "input_file":
			plain = false
			parts = append(parts, map[string]any{"type": "file", "file": map[string]any{"filename": p["filename"], "file_data": p["file_data"]}})
		default:
			parts = append(parts, map[string]any{"type": "text", "text": p["text"]})
		}
	}
	content = parts
	if plain { // not every server takes a list
		content = chatJoin(parts, "\n\n")
	}
	c.out = append(c.out, dto.ChatMessage{Role: "user", Content: content})
	c.turns++
	c.user = nil
}

// flushAssistant writes the collected assistant message and, right after it, the results of its calls in call order.
func (c *chatBuilder) flushAssistant() {
	texts, calls, think := c.texts, c.calls, c.think
	c.texts, c.calls, c.think = nil, nil, nil
	text := strings.Join(texts, "\n\n")
	if text == "" && len(calls) == 0 {
		return // reasoning alone is no turn
	}
	m := dto.ChatMessage{Role: "assistant"}
	if text != "" {
		m.Content = text
	}
	for f, list := range think {
		joined := strings.Join(list, "\n\n")
		if f == chatFieldPlain {
			m.Reasoning = &joined
		} else {
			m.ReasoningContent = &joined
		}
	}
	if c.o.EchoReasoning && c.tools && m.ReasoningContent == nil && m.Reasoning == nil {
		m.ReasoningContent = new(string)
	}
	var results []dto.ChatMessage
	signed := false
	for _, i := range calls {
		item := c.b.input[i]
		id, extra := openCall(stStr(item["call_id"]), c.identity)
		if c.o.ShortCallIDs {
			id = shortCallID(id)
		}
		signed = signed || extra != nil
		m.ToolCalls = append(m.ToolCalls, dto.ChatToolCall{ID: id, Type: "function", ExtraContent: extra,
			Function: dto.ChatFunction{Name: stStr(item["name"]), Arguments: stStr(item["arguments"])}})
		result := dto.ChatMessage{Role: "tool", ToolCallID: id, Content: reqNoResult}
		if j, ok := c.outputOf[i]; ok {
			result.Content = chatResult(c.b.input[j]["output"])
		}
		results = append(results, result)
	}
	if c.o.PlaceholdSigs && len(m.ToolCalls) > 0 && !signed {
		m.ToolCalls[0].ExtraContent = json.RawMessage(chatPlaceholder)
	}
	c.out = append(append(c.out, m), results...)
	c.turns++
}

// chatResult is the text of a tool result.
func chatResult(output any) string {
	text, _ := output.(string)
	parts, files := output.([]map[string]any)
	if files {
		text = chatJoin(parts, "\n")
	}
	switch {
	case files && strings.TrimSpace(text) == strings.TrimSpace(reqErrorPrefix):
		return reqErrorPrefix + chatSeeNext
	case text == "" && files:
		return chatSeeNext
	case text == "":
		return chatNoOutput
	}
	return text
}
