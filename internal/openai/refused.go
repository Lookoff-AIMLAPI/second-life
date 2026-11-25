package openai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/Lookoff-AIMLAPI/second-life/internal/route"
	"github.com/Lookoff-AIMLAPI/second-life/internal/translate"
)

// optional are the fields that a request can do without, by protocol.
var optional = map[string][]string{
	route.Chat: {"temperature", "top_p", "stop", "stream_options", "parallel_tool_calls", "tool_choice", "response_format",
		"reasoning_effort", "max_tokens", "max_completion_tokens"},
	route.Responses: {"temperature", "top_p", "parallel_tool_calls", "tool_choice", "text", "store", "include", "reasoning", "max_output_tokens"},
	route.Anthropic: {"temperature", "top_p", "top_k", "tool_choice", "thinking", "output_config", "context_management", "metadata", "fallbacks", "service_tier"},
}

var (
	refusal = regexp.MustCompile(`unsupported|not support|n't support|only support|unknown|unrecogni[sz]ed|unexpected|unavailable|extra inputs|extra_forbidden|extra fields` +
		`|not permitted|not allowed|additional propert|invalid|deprecated|only the default|cannot be (used|set|specified)|not available|no such|not implemented` +
		`|not compatible|incompatible|forbidden|must not|should not be|are supported|is supported`)

	systemRefused = regexp.MustCompile(`system (message|prompt)s? (must|should|can|may) (only )?(be|appear|come)|system.{0,40}(at the (beginning|start)|first message|only at)` +
		`|roles must alternate|unexpected role|after role|only user and assistant|'user' (or|and) 'assistant'|multiple system messages` +
		`|role.{0,30}(system|developer).{0,60}(support|invalid|unknown|unexpected|not)|(invalid|unknown|unsupported|unexpected).{0,40}role.{0,30}(system|developer)|unknown variant .(system|developer)`)
	imageRefused = regexp.MustCompile(`(image_url|image input|input_image|images?|vision).{0,80}(not support|unsupported|n't support|not available|not allowed|unknown|invalid type)` +
		`|(not|n't) (support|accept|handle).{0,30}(image|vision|multimodal)|not a multimodal|support(s)? image input|unknown variant .(image_url|input_image)|mmproj` +
		`|content.{0,40}(must be|should be|expected).{0,10}string|sequence, expected a string|unsupported content type.{0,10}image|only .?text.? content`)
	fileRefused = regexp.MustCompile("(['\"`]file['\"`]|input_file|file_data|file input|pdf|documents?).{0,80}(support|unknown|invalid|unexpected|not)|(unsupported|unknown|invalid|not support).{0,60}(['\"`]file['\"`]|input_file|file_data|pdf)")
	shortIDs    = regexp.MustCompile(`tool call id.{0,120}(length of 9|9 characters|nine)`)
	needThought = regexp.MustCompile(`reasoning_content.{0,80}(must be (passed|provided|included)|missing|required)|(missing|must (pass|include|provide)).{0,40}reasoning(_content)?`)
	ownThought  = regexp.MustCompile("reasoning_content|['\"`.]reasoning['\"`]")
	effortName  = regexp.MustCompile(`reasoning[_. ]effort|reasoning value|think(ing)? value|not support (thinking|reasoning)|n't support (thinking|reasoning)`)
	valueBad    = regexp.MustCompile(`supported values|one of|must be|invalid value|unsupported value|expected|input should be|unknown variant|not a valid|invalid reasoning value`)

	answerLimit = regexp.MustCompile("max_(completion_|output_|new_)?tokens|max tokens|completion tokens|output tokens")
	windowSize  = regexp.MustCompile("(?:maximum context length (?:is|of)|context length is only|context (?:window|size) (?:is|of)|`max_new_tokens` must be <=) (\\d+)")
	inputSize   = regexp.MustCompile("(\\d+) in the messages|request has (\\d+) input tokens|passed (\\d+) input tokens|\\((\\d+) of text input|prompt contains (?:at least )?(\\d+)|given: (\\d+) `inputs`")
	thousands   = regexp.MustCompile(`(\d),(\d{3})\b`)
	limits      = []*regexp.Regexp{
		regexp.MustCompile(`\[ ?[01], ?(\d+) ?\]`), // DeepSeek, DashScope
		regexp.MustCompile(`tokens: \d+ > (\d+)`),  // Anthropic
		regexp.MustCompile("(?:at most|less than or equal to|<=|cannot exceed|no more than|up to|maximum (?:allowed )?(?:value|number)[^.0-9]{0,60}) ?[`'\"]?(\\d+)"), // OpenAI, Groq
		regexp.MustCompile(`between \d+ and (\d+)`),
	}
)

// complaint is what a server said about a request.
type complaint struct {
	text   string // in lower case
	code   string
	params []string // fields it named in a field of their own
}

func (c complaint) names(field string) bool {
	return slices.ContainsFunc(c.params, func(p string) bool { return strings.Contains(p, field) })
}

func readComplaint(raw []byte) complaint {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return complaint{text: strings.ToLower(string(raw))}
	}
	if list, ok := v.([]any); ok && len(list) > 0 {
		v = list[0]
	}
	top, _ := v.(map[string]any)
	var c complaint
	var texts []string
	var take func(v any, depth int)
	take = func(v any, depth int) {
		if depth > 4 {
			return
		}
		switch v := v.(type) {
		case string:
			texts = append(texts, v)
			var inner map[string]any // a provider's answer inside the gateway's
			if strings.HasPrefix(strings.TrimSpace(v), "{") && json.Unmarshal([]byte(v), &inner) == nil {
				take(inner, depth+1)
			}
		case []any:
			for _, e := range v[:min(len(v), 8)] {
				take(e, depth+1)
			}
		case map[string]any:
			if s, _ := v["param"].(string); s != "" {
				c.params = append(c.params, s)
			}
			if s, _ := v["code"].(string); c.code == "" {
				c.code = s
			}
			if loc, _ := v["loc"].([]any); len(loc) > 0 {
				var at []string
				for _, l := range loc {
					at = append(at, fmt.Sprint(l))
				}
				c.params = append(c.params, strings.TrimPrefix(strings.Join(at, "."), "body."))
			}
			for _, k := range []string{"message", "msg", "type", "error", "detail", "details", "errors", "reason", "metadata", "raw"} {
				take(v[k], depth+1)
			}
		}
	}
	take(top, 0)
	c.text = strings.ToLower(strings.Join(texts, "\n"))
	return c
}

// refusedField names what the server refused, as Options.Without takes it, or "".
func refusedField(protocol string, status int, sent, raw []byte) string {
	if status != 400 && status != 422 && status != 404 {
		return ""
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(sent, &top) != nil {
		return ""
	}
	c := readComplaint(raw)
	if status == 404 { // OpenRouter: no provider of the model can do it
		if protocol != route.Anthropic && hasImage(sent) && imageRefused.MatchString(c.text) {
			return translate.NoImageParts
		}
		return ""
	}
	if f, known := refusedLimit(protocol, c, top); known {
		return f
	}
	if translate.Overflow(c.text) || c.code == "context_length_exceeded" || c.code == "invalid_encrypted_content" ||
		protocol == route.Anthropic && strings.Contains(c.text, "signature") { // Claude Code answers that one itself
		return ""
	}
	if f := refusedSpecial(protocol, c, top, sent); f != "" {
		return f
	}
	return refusedByName(protocol, c, top)
}

// refusedLimit handles complaints about the size of the answer. The limit and the input share the context window on
// many servers: then no limit at all fits best, unless the input alone nearly fills the window.
func refusedLimit(protocol string, c complaint, top map[string]json.RawMessage) (field string, known bool) {
	asked := 0
	for _, f := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens"} {
		if n, err := strconv.Atoi(string(top[f])); err == nil {
			asked = n
		}
	}
	text := thousands.ReplaceAllString(c.text, "$1$2")
	number := func(re *regexp.Regexp) int {
		m := re.FindStringSubmatch(text)
		for _, s := range m[min(1, len(m)):] {
			if n, err := strconv.Atoi(s); err == nil {
				return n
			}
		}
		return 0
	}
	if window, input := number(windowSize), number(inputSize); window > 0 {
		if asked > 0 && input > 0 && input < window/8*7 && input+asked > window {
			if protocol == route.Anthropic { // the field is required there
				return translate.LowerMaxTokens + strconv.Itoa(max(1, window/8)), true
			}
			return translate.NoMaxTokens, true
		}
		return "", true // the prompt is too long
	}
	if asked == 0 || !answerLimit.MatchString(text) {
		return "", false
	}
	for _, re := range limits {
		if n := number(re); n >= 16 && n < asked {
			return translate.LowerMaxTokens + strconv.Itoa(n), true
		}
	}
	return "", false
}

func hasImage(sent []byte) bool {
	return bytes.Contains(sent, []byte(`"image_url"`)) || bytes.Contains(sent, []byte(`"input_image"`))
}

func refusedSpecial(protocol string, c complaint, top map[string]json.RawMessage, sent []byte) string {
	no := refusal.MatchString(c.text)
	openAI := protocol != route.Anthropic
	effort := sentEffort(top)
	switch {
	case protocol == route.Chat && strings.Contains(c.text, "thought_signature") && strings.Contains(c.text, "missing"):
		return translate.NeedSignatures
	case protocol == route.Chat && shortIDs.MatchString(c.text):
		return translate.NeedShortIDs
	case systemRefused.MatchString(c.text) && systemInside(protocol, top):
		return translate.NoSystemInside
	case protocol == route.Chat && needThought.MatchString(c.text):
		return translate.NeedReasoning
	case protocol == route.Chat && no && ownThought.MatchString(c.text) && (bytes.Contains(sent, []byte(`"reasoning_content":`)) || bytes.Contains(sent, []byte(`"reasoning":"`))):
		return translate.NoReasoningBack
	case openAI && effort != "" && (effortName.MatchString(c.text) || c.names("effort")) && (no || valueBad.MatchString(c.text)):
		if valueBad.MatchString(c.text) && (effort == "xhigh" || effort == "max") {
			return translate.LowerEffort
		}
		return "reasoning_effort"
	case protocol == route.Responses && strings.Contains(c.text, "reasoning summar") || slices.Contains(c.params, "reasoning.summary"):
		return "reasoning.summary"
	case protocol == route.Responses && top["include"] != nil && strings.Contains(c.text, "encrypted"):
		return "include"
	case protocol == route.Responses && no && strings.Contains(c.text, "web_search") && bytes.Contains(top["tools"], []byte(`"web_search"`)):
		return translate.NoWebSearch
	case openAI && hasImage(sent) && imageRefused.MatchString(c.text):
		return translate.NoImageParts
	case openAI && (bytes.Contains(sent, []byte(`"type":"file"`)) || bytes.Contains(sent, []byte(`"input_file"`))) && fileRefused.MatchString(c.text):
		return translate.NoFileParts
	case protocol == route.Chat && no && top["response_format"] != nil && (strings.Contains(c.text, "json_schema") || strings.Contains(c.text, "structured output")):
		return "response_format"
	}
	return ""
}

func sentEffort(top map[string]json.RawMessage) string {
	var s string
	if json.Unmarshal(top["reasoning_effort"], &s) == nil && s != "" {
		return s
	}
	var r struct {
		Effort string `json:"effort"`
	}
	_ = json.Unmarshal(top["reasoning"], &r)
	return r.Effort
}

// systemInside is true when the conversation has a system message that can be folded.
func systemInside(protocol string, top map[string]json.RawMessage) bool {
	raw := top["messages"]
	if protocol == route.Responses {
		raw = top["input"]
	}
	var list []struct {
		Role string `json:"role"`
	}
	_ = json.Unmarshal(raw, &list)
	for i, m := range list {
		if m.Role == "developer" || m.Role == "system" && (i > 0 || protocol != route.Chat) {
			return true
		}
	}
	return false
}

// plainWords are field names that are also words: they count only when they are quoted or lead the message.
var plainWords = []string{"stop", "store", "include", "text", "reasoning", "thinking", "metadata"}

func refusedByName(protocol string, c complaint, top map[string]json.RawMessage) string {
	fields := optional[protocol]
	can := func(f string) bool { return slices.Contains(fields, f) && top[f] != nil }
	for _, p := range c.params {
		p = strings.ToLower(p)
		if f, _, _ := strings.Cut(strings.ReplaceAll(p, "[", "."), "."); can(f) {
			return f
		}
	}
	if !refusal.MatchString(c.text) {
		return ""
	}
	best, at := "", len(c.text)
	for _, f := range fields {
		if !can(f) {
			continue
		}
		if i := mention(c.text, f); i >= 0 && i < at {
			best, at = f, i
		}
	}
	return best
}

// mention is where the text names the field, or -1.
func mention(text, field string) int {
	word := slices.Contains(plainWords, field)
	for from := 0; ; {
		i := strings.Index(text[from:], field)
		if i < 0 {
			return -1
		}
		i += from
		from = i + len(field)
		before, after := byte('\n'), byte('\n')
		if i > 0 {
			before = text[i-1]
		}
		if from < len(text) {
			after = text[from]
		}
		if isWord(before) || isWord(after) {
			continue
		}
		quoted := strings.IndexByte("'\"`.", before) >= 0 && strings.IndexByte("'\"`", after) >= 0
		leads := before == '\n' && (after == ':' || after == '.')
		if !word || quoted || leads {
			return i
		}
	}
}

func isWord(b byte) bool {
	return b == '_' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9'
}
