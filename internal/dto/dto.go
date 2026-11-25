// Package dto holds the Anthropic Messages shapes (Claude side) and the OpenAI Responses shapes (Codex side).
package dto

import "encoding/json"

// ---- Anthropic request ----

type MessagesRequest struct {
	Model        string          `json:"model"`
	Messages     []Message       `json:"messages"`
	System       json.RawMessage `json:"system,omitempty"` // string or []Block
	Tools        []Tool          `json:"tools,omitempty"`
	ToolChoice   *ToolChoice     `json:"tool_choice,omitempty"`
	Thinking     *Thinking       `json:"thinking,omitempty"`
	OutputConfig *OutputConfig   `json:"output_config,omitempty"`
	MaxTokens    int             `json:"max_tokens,omitempty"`
	Temperature  *float64        `json:"temperature,omitempty"`
	TopP         *float64        `json:"top_p,omitempty"`
	TopK         *int            `json:"top_k,omitempty"`
	StopSeqs     []string        `json:"stop_sequences,omitempty"`
	Stream       bool            `json:"stream,omitempty"`
	Thread       json.RawMessage `json:"thread,omitempty"` // server-side conversation; not supported
}

type Message struct {
	Role    string          `json:"role"`    // user, assistant, system
	Content json.RawMessage `json:"content"` // string or []Block
}

// Block is any content block; Type says which fields are set.
type Block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Source    *Source         `json:"source,omitempty"`
	Title     string          `json:"title,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	Signature string          `json:"signature,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"` // tool_result: string or []Block
	IsError   bool            `json:"is_error,omitempty"`
	ToolName  string          `json:"tool_name,omitempty"` // tool_reference
	Context   string          `json:"context,omitempty"`   // document
	Tool      *ToolChange     `json:"tool,omitempty"`      // tool_addition, tool_removal
}

// ToolChange names a declared tool, or carries a whole definition.
type ToolChange struct {
	Type       string `json:"type"` // tool_reference, tool_definition
	Name       string `json:"name,omitempty"`
	Definition *Tool  `json:"definition,omitempty"`
}

type Source struct {
	Type      string          `json:"type"` // base64, url, text, content
	MediaType string          `json:"media_type,omitempty"`
	Data      string          `json:"data,omitempty"`
	URL       string          `json:"url,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"` // document: string or []Block
}

type Tool struct {
	Type         string          `json:"type,omitempty"` // empty or "custom"; anything else is a server tool
	Name         string          `json:"name"`
	Description  string          `json:"description,omitempty"`
	InputSchema  json.RawMessage `json:"input_schema,omitempty"`
	DeferLoading bool            `json:"defer_loading,omitempty"`

	// web_search server tool
	AllowedDomains []string        `json:"allowed_domains,omitempty"`
	BlockedDomains []string        `json:"blocked_domains,omitempty"`
	MaxUses        int             `json:"max_uses,omitempty"`
	UserLocation   json.RawMessage `json:"user_location,omitempty"`
}

type ToolChoice struct {
	Type                   string `json:"type"` // auto, any, none, tool
	Name                   string `json:"name,omitempty"`
	DisableParallelToolUse bool   `json:"disable_parallel_tool_use,omitempty"`
}

type Thinking struct {
	Type    string `json:"type"`              // adaptive, enabled, disabled
	Display string `json:"display,omitempty"` // omitted, summarized
}

type OutputConfig struct {
	Effort string        `json:"effort,omitempty"`
	Format *OutputFormat `json:"format,omitempty"`
}

type OutputFormat struct {
	Type   string          `json:"type"`
	Schema json.RawMessage `json:"schema"`
}

// ---- Anthropic response ----

type Usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

// MessageResponse.Content holds map-shaped blocks because empty text and thinking fields must still be serialised.
type MessageResponse struct {
	ID           string           `json:"id"`
	Type         string           `json:"type"`
	Role         string           `json:"role"`
	Model        string           `json:"model"`
	Content      []map[string]any `json:"content"`
	StopReason   *string          `json:"stop_reason"`
	StopSequence *string          `json:"stop_sequence"`
	Usage        Usage            `json:"usage"`
}

// Event is one Anthropic SSE event: Name goes on the event line, Data is the JSON body and includes "type".
type Event struct {
	Name string
	Data map[string]any
}

// APIError is an Anthropic-shaped failure.
type APIError struct {
	Status     int
	Type       string
	Message    string
	RetryAfter string // seconds; empty when unknown
}

func (e *APIError) Error() string { return e.Type + ": " + e.Message }

// ---- Responses request ----

type ResponsesRequest struct {
	Model             string           `json:"model"`
	Instructions      string           `json:"instructions"`
	Input             []map[string]any `json:"input"`
	Tools             []any            `json:"tools,omitempty"`       // FunctionTool or WebSearchTool
	ToolChoice        any              `json:"tool_choice,omitempty"` // string or {"type":"function","name":...}
	ParallelToolCalls bool             `json:"parallel_tool_calls"`
	Reasoning         Reasoning        `json:"reasoning,omitzero"`
	Text              map[string]any   `json:"text,omitempty"`
	Store             bool             `json:"store"`
	Stream            bool             `json:"stream"`
	Include           []string         `json:"include,omitempty"`
	PromptCacheKey    string           `json:"prompt_cache_key,omitempty"`

	// Not for Codex.
	MaxOutputTokens int      `json:"max_output_tokens,omitempty"`
	Temperature     *float64 `json:"temperature,omitempty"`
	TopP            *float64 `json:"top_p,omitempty"`
	Omit            []string `json:"-"` // fields that must not be sent
}

// MarshalJSON leaves out the fields named in Omit.
func (r ResponsesRequest) MarshalJSON() ([]byte, error) {
	type plain ResponsesRequest
	raw, err := json.Marshal(plain(r))
	if err != nil || len(r.Omit) == 0 {
		return raw, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	for _, name := range r.Omit {
		delete(fields, name)
	}
	return json.Marshal(fields)
}

type FunctionTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
	Strict      bool            `json:"strict"`
}

// WebSearchTool is the search Codex runs on its side.
type WebSearchTool struct {
	Type         string          `json:"type"`
	Filters      map[string]any  `json:"filters,omitempty"`
	UserLocation json.RawMessage `json:"user_location,omitempty"`
}

type Reasoning struct {
	Effort  string `json:"effort,omitempty"`
	Summary string `json:"summary,omitempty"`
}

// Translated is a ready request plus what the way back needs.
type Translated struct {
	Body       ResponsesRequest
	ToolNames  map[string]string // upstream (shortened) name -> Claude name; only names that changed
	Warnings   []string
	Compaction bool // Claude Code asks for a summary of the conversation
}

// ---- Chat Completions request ----

type ChatRequest struct {
	Model               string             `json:"model"`
	Messages            []ChatMessage      `json:"messages"`
	Tools               []ChatTool         `json:"tools,omitempty"`
	ToolChoice          any                `json:"tool_choice,omitempty"` // string or {"type":"function","function":{"name":...}}
	ParallelToolCalls   *bool              `json:"parallel_tool_calls,omitempty"`
	ResponseFormat      map[string]any     `json:"response_format,omitempty"`
	ReasoningEffort     string             `json:"reasoning_effort,omitempty"`
	MaxTokens           int                `json:"max_tokens,omitempty"`
	MaxCompletionTokens int                `json:"max_completion_tokens,omitempty"`
	Temperature         *float64           `json:"temperature,omitempty"`
	TopP                *float64           `json:"top_p,omitempty"`
	Stop                []string           `json:"stop,omitempty"`
	Stream              bool               `json:"stream"`
	StreamOptions       *ChatStreamOptions `json:"stream_options,omitempty"`
}

type ChatStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type ChatMessage struct {
	Role       string         `json:"role"`    // system, user, assistant, tool
	Content    any            `json:"content"` // string, []map[string]any, or null when an assistant only calls tools
	ToolCalls  []ChatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`

	// The model's own reasoning, back under the name it came with. An empty one may be required, so nil means absent.
	ReasoningContent *string `json:"reasoning_content,omitempty"`
	Reasoning        *string `json:"reasoning,omitempty"`
}

type ChatToolCall struct {
	ID           string          `json:"id"`
	Type         string          `json:"type"` // function
	Function     ChatFunction    `json:"function"`
	ExtraContent json.RawMessage `json:"extra_content,omitempty"` // opaque, such as Gemini's thought signature
}

type ChatFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ChatTool struct {
	Type     string          `json:"type"` // function
	Function ChatFunctionDef `json:"function"`
}

type ChatFunctionDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

// ChatTranslated is a ready Chat Completions request plus what the way back needs.
type ChatTranslated struct {
	Body       ChatRequest
	ToolNames  map[string]string // as in Translated
	Warnings   []string
	Compaction bool
}

// StreamContext drives translation of one response.
type StreamContext struct {
	PublicModel   string // what Claude asked for; echoed back
	UpstreamModel string
	ToolNames     map[string]string
	ShowThinking  bool     // thinking.display == "summarized"
	InputEstimate int      // reported when a stop sequence ends the answer before Codex counts it
	StopSeqs      []string // Codex has no stop sequences, so the translator cuts the text itself
	HideThinking  bool     // the client did not ask for thinking, so no thinking block may appear
}
