package translate

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"

	"github.com/Lookoff-AIMLAPI/second-life/internal/dto"
)

// Options is what is known about a custom endpoint and one of its models. The zero value is the cautious default.
// The endpoint's own refusals teach the rest: see Without.
type Options struct {
	Endpoint string // id of the endpoint; reasoning goes back only where it came from

	SystemFirstOnly bool   // no system message inside the conversation
	Effort          bool   // the server takes a reasoning effort
	MaxTokensField  string // Chat: max_tokens (the default) or max_completion_tokens
	MaxOutput       int    // tokens the model may write; 0 when unknown
	Encrypted       bool   // Responses: reasoning comes encrypted and goes back
	WebSearch       bool   // Responses: the server runs web_search

	HighEffort    bool // nothing above high
	EchoReasoning bool // Chat: every assistant turn needs reasoning_content, even an empty one
	NoReasoning   bool // Chat: the server refuses reasoning_content
	ShortCallIDs  bool // Chat: call ids are nine letters or digits (Mistral)
	PlaceholdSigs bool // Chat: a call needs a thought signature, even a stand-in (Gemini)
	NoImages      bool // images become a note
	NoFiles       bool // PDFs are refused

	Omit []string // request fields never sent
}

// Values of Without that are not the name of a request field.
const (
	LowerMaxTokens  = "max_tokens<=" // followed by the limit
	NoMaxTokens     = "max_tokens:none"
	LowerEffort     = "reasoning_effort<=high"
	NeedReasoning   = "reasoning_content:required"
	NeedSignatures  = "thought_signature:required"
	NeedShortIDs    = "tool_call_id:9"
	NoSystemInside  = "system"
	NoReasoningBack = "reasoning_content"
	NoImageParts    = "image_url"
	NoFileParts     = "file"
	NoWebSearch     = "web_search"
)

// optionFields may be left out of a request: the answer is still usable. messages, tools, model and stream may not.
var optionFields = []string{
	"max_tokens", "max_completion_tokens", "max_output_tokens", "temperature", "top_p", "top_k", "stop", "stream_options",
	"parallel_tool_calls", "tool_choice", "response_format", "text", "store", "include", "reasoning.summary",
	"context_management", "metadata", "output_config", "thinking", "fallbacks", "service_tier", // Messages
}

// Without returns the options for a retry without what the endpoint refused. False means there is nothing to change.
func (o Options) Without(field string) (Options, bool) {
	was := o
	o.Omit = slices.Clone(o.Omit)
	omit := func(names ...string) (changed bool) {
		for _, n := range names {
			if !o.omits(n) {
				o.Omit, changed = append(o.Omit, n), true
			}
		}
		return changed
	}
	set := func(flag *bool, v bool) bool { changed := *flag != v; *flag = v; return changed }
	ok := false
	switch field {
	case NoMaxTokens:
		ok = omit("max_tokens", "max_completion_tokens", "max_output_tokens")
	case LowerEffort:
		ok = o.Effort && set(&o.HighEffort, true)
	case "reasoning_effort", "reasoning", "reasoning.effort":
		ok = set(&o.Effort, false)
	case NeedReasoning:
		ok = !o.NoReasoning && set(&o.EchoReasoning, true)
	case NoReasoningBack:
		ok = !o.EchoReasoning && set(&o.NoReasoning, true)
	case NeedSignatures:
		ok = set(&o.PlaceholdSigs, true)
	case NeedShortIDs:
		ok = set(&o.ShortCallIDs, true)
	case NoSystemInside:
		ok = set(&o.SystemFirstOnly, true)
	case NoImageParts:
		ok = set(&o.NoImages, true)
	case NoFileParts:
		ok = set(&o.NoFiles, true)
	case NoWebSearch:
		ok = set(&o.WebSearch, false)
	case "include":
		ok = omit("include")
		ok = set(&o.Encrypted, false) || ok
	default:
		if limit, found := strings.CutPrefix(field, LowerMaxTokens); found {
			n, err := strconv.Atoi(limit)
			if ok = err == nil && n > 0 && (o.MaxOutput == 0 || n < o.MaxOutput); ok {
				o.MaxOutput = n
			}
		} else {
			ok = slices.Contains(optionFields, field) && omit(field)
		}
	}
	if !ok {
		return was, false
	}
	return o, true
}

// Identity names the model that signs reasoning. It belongs in StreamContext.UpstreamModel.
func (o Options) Identity(model string) string {
	if o.Endpoint == "" {
		return model
	}
	return o.Endpoint + "/" + model
}

func (o Options) omits(field string) bool { return slices.Contains(o.Omit, field) }

// maxTokens is the output limit to send, 0 for none, and the Chat field it goes in.
func (o Options) maxTokens(b *reqBuilder, asked int) (n int, field string) {
	n = max(0, asked)
	if o.MaxOutput > 0 && n > o.MaxOutput {
		n = o.MaxOutput
		b.warn("max_tokens was lowered to the output limit of the model")
	}
	field, other := "max_tokens", "max_completion_tokens"
	if o.MaxTokensField == other {
		field, other = other, field
	}
	if o.omits(field) {
		field = other
	}
	if o.omits(field) {
		field = ""
	}
	return n, field
}

// effort is what to ask for, "" for the server's own default.
func (o Options) effort(req *dto.MessagesRequest, r dto.Reasoning) string {
	asked := req.OutputConfig != nil && req.OutputConfig.Effort != "" || req.Thinking != nil && req.Thinking.Type == "disabled"
	if !o.Effort || !asked {
		return ""
	}
	if o.HighEffort && (r.Effort == "xhigh" || r.Effort == "max") {
		return "high"
	}
	return r.Effort
}

// sampling drops what may not be sent, with a warning.
func (o Options) sampling(b *reqBuilder, req *dto.MessagesRequest) (temperature, topP *float64) {
	if temperature = req.Temperature; temperature != nil && o.omits("temperature") {
		temperature = nil
		b.warn("temperature is not supported by the endpoint and was dropped")
	}
	if topP = req.TopP; topP != nil && o.omits("top_p") {
		topP = nil
		b.warn("top_p is not supported by the endpoint and was dropped")
	}
	if req.TopK != nil {
		b.warn("top_k is not supported by the endpoint and was dropped")
	}
	return temperature, topP
}

// schemaAsText asks in words for what the endpoint cannot enforce.
func schemaAsText(b *reqBuilder, r *reqBuilt) {
	f, _ := r.text["format"].(map[string]any)
	schema, ok := f["schema"]
	if !ok {
		return
	}
	raw, _ := json.Marshal(schema)
	r.instructions += "\n\nAnswer with one JSON value and nothing else. It must match this JSON schema:\n" + string(raw)
	r.text = nil
	b.warn("output_config.format is not supported by the endpoint and became an instruction")
}

// choose applies a tool_choice that cannot be sent.
func (o Options) choose(b *reqBuilder, r *reqBuilt) {
	if len(r.tools) == 0 || r.choice == "auto" {
		r.choice = nil
		return
	}
	if o.omits("tool_choice") {
		if r.choice == "none" {
			r.tools = nil
		}
		r.choice = nil
		b.warn("tool_choice is not supported by the endpoint and was dropped")
	}
}
