package translate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"maps"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/Lookoff-AIMLAPI/second-life/internal/dto"
)

// overflowWords are the ways servers say that the prompt does not fit. They are matched in lower case.
var overflowWords = regexp.MustCompile(strings.Join([]string{
	`maximum context length`,                                             // OpenAI, vLLM, DeepSeek, OpenRouter
	`exceeds? (the |this )?(model'?s )?context (window|length)`,          // OpenAI Responses, Ollama
	`exceeds? the (available|maximum) context (size|length)`,             // llama.cpp, Ollama
	`context (window|length|size) (exceeded|exceeds limit)`,              // MiniMax, LiteLLM
	`exceeded model token limit`,                                         // Moonshot
	`prompt exceeds max length`,                                          // Z.ai
	`prompt is too long`,                                                 // Fireworks, Anthropic
	`maximum prompt length is \d+`,                                       // xAI
	`longer than the (maximum model length|model'?s context length)`,     // vLLM, SGLang
	`model'?s context length is only`,                                    // vLLM
	`exceeds the max_model_len`,                                          // vLLM
	`input length .*exceeds (the )?maximum`,                              // SGLang, Ollama
	`range of input length should be`,                                    // DashScope
	`exceeds the maximum number of tokens allowed`,                       // Gemini
	`input is too long for (the )?requested model`,                       // Bedrock
	`too large for model with \d+ maximum context`,                       // Mistral
	"`inputs` (tokens \\+ `max_new_tokens` must be|must have less than)", // TGI, Together
	`loaded with context length of only`,                                 // LM Studio
	`tokens to keep from the initial prompt is greater than the context`, // LM Studio
	`reduce the length of the (messages|input|prompt)`,                   // Groq
	`too many tokens: .*cannot exceed`,                                   // Cohere
}, "|"))

// answerToo are messages of the same kind in which the limit on the answer is part of the problem. Claude Code reads
// Anthropic's one itself.
var answerToo = regexp.MustCompile("input length and `max_tokens` exceed context limit")

// Overflow tells whether a server's message says that the prompt does not fit into the model.
func Overflow(msg string) bool {
	low := strings.ToLower(msg)
	return overflowWords.MatchString(low) && !answerToo.MatchString(low)
}

var dashCode = regexp.MustCompile(`^<\d+> *`) // DashScope starts its messages with the status

var htmlTitle = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)

// ServerError turns a failed HTTP response of a custom endpoint into an Anthropic error.
func ServerError(status int, header http.Header, body []byte) *dto.APIError {
	text := bytes.TrimSpace(body)
	if strings.Contains(strings.ToLower(header.Get("Content-Type")), "html") || bytes.HasPrefix(text, []byte("<")) {
		msg := fmt.Sprintf("the server answered with a web page (HTTP %d); check the address", status)
		if m := htmlTitle.FindSubmatch(text); m != nil {
			if t := strings.Join(strings.Fields(html.UnescapeString(string(m[1]))), " "); t != "" && len(t) <= 100 {
				msg = fmt.Sprintf("the server answered with a web page (HTTP %d): %s", status, t)
			}
		}
		return stClassify(status, header, map[string]any{"message": msg}, msg)
	}
	var v any
	if json.Unmarshal(text, &v) != nil {
		return UpstreamError(status, header, body)
	}
	if list, ok := v.([]any); ok && len(list) > 0 { // Gemini
		v = list[0]
	}
	top := stMap(v)
	if top == nil {
		return UpstreamError(status, header, body)
	}
	return stClassify(status, header, chatErrorShape(top), fmt.Sprintf("the server returned HTTP %d", status))
}

// chatStreamError classifies an error inside a stream.
func chatStreamError(ev map[string]any) *dto.APIError { return stStreamError(chatErrorShape(ev)) }

// chatErrorShape brings the variants of an error object to the one stClassify reads.
func chatErrorShape(ev map[string]any) map[string]any {
	top := maps.Clone(ev)
	obj := top
	if e := stMap(top["error"]); e != nil {
		obj = maps.Clone(e)
		top["error"] = obj
		if raw := stStr(stMap(obj["metadata"])["raw"]); raw != "" { // OpenRouter: the provider's own words
			obj["message"] = stStr(obj["message"]) + ": " + raw
		}
	} else if base := stMap(top["base_resp"]); base != nil { // MiniMax
		top["message"] = base["status_msg"]
	} else if list, _ := top["detail"].([]any); len(list) > 0 { // FastAPI
		var msgs []string
		for _, v := range list[:min(len(list), 3)] {
			d := stMap(v)
			var at []string
			loc, _ := d["loc"].([]any)
			for _, l := range loc {
				at = append(at, fmt.Sprint(l))
			}
			msgs = append(msgs, strings.Join(at, ".")+": "+stStr(d["msg"]))
		}
		top["detail"] = strings.Join(msgs, "; ")
	}
	if m := stStr(obj["message"]); dashCode.MatchString(m) { // it would be taken for HTML
		obj["message"] = dashCode.ReplaceAllString(m, "")
	}
	if stInt(top["status"]) == 0 && stInt(top["status_code"]) == 0 {
		for _, v := range []any{obj["code"], obj["status"], obj["status_code"], obj["http_status_code"], top["code"]} {
			n := stInt(v)
			if s, ok := v.(string); ok {
				n, _ = strconv.Atoi(s)
			}
			if n >= 400 && n < 600 {
				top["status_code"] = float64(n)
				break
			}
		}
	}
	return top
}

// chatHasError is false for the empty values some servers put into every chunk.
func chatHasError(v any) bool {
	switch v := v.(type) {
	case string:
		return v != ""
	case map[string]any:
		return len(v) > 0
	}
	return false
}
