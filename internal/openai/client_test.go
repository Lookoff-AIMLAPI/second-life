package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Lookoff-AIMLAPI/second-life/internal/dto"
	"github.com/Lookoff-AIMLAPI/second-life/internal/route"
	"github.com/Lookoff-AIMLAPI/second-life/internal/translate"
)

const testKey = "test-key<&>0123456789"

func serve(t *testing.T, protocol string, h http.HandlerFunc) *Client {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return &Client{Endpoint: route.Endpoint{ID: "ep-0123abcd", Name: "Fake", Protocol: protocol, BaseURL: s.URL + "/v1"},
		HTTP: s.Client(), Version: "1.2.3", Key: func() (string, error) { return testKey, nil }}
}

func apiErr(t *testing.T, err error) *dto.APIError {
	t.Helper()
	var e *dto.APIError
	if !errors.As(err, &e) {
		t.Fatalf("%T %v", err, err)
	}
	return e
}

func TestSendRequest(t *testing.T) {
	for protocol, path := range map[string]string{route.Chat: "/v1/chat/completions", route.Responses: "/v1/responses", route.Anthropic: "/v1/messages"} {
		for _, key := range []string{testKey, ""} {
			c := serve(t, protocol, func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				h := r.Header
				auth, xkey := "", ""
				if key != "" {
					auth = "Bearer " + key
				}
				if protocol == route.Anthropic {
					xkey = key
					if h.Get("Anthropic-Version") != "2024-01-02" || h.Get("Anthropic-Beta") != "a-1,b-2" {
						t.Error(h)
					}
				} else if h.Get("Anthropic-Version") != "" || h.Get("Anthropic-Beta") != "" {
					t.Error(h)
				}
				if r.Method != "POST" || r.URL.String() != path || string(raw) != `{"a":1}` || h.Get("Authorization") != auth || h.Get("X-Api-Key") != xkey ||
					h.Get("Content-Type") != "application/json" || h.Get("User-Agent") != "second-life/1.2.3" || h.Get("Cookie") != "" {
					t.Errorf("%s %s %s %v", r.Method, r.URL, raw, h)
				}
				io.WriteString(w, "ok")
			})
			c.Key = func() (string, error) { return key, nil }
			if key == "" {
				c.Key = nil
			}
			in := http.Header{"Anthropic-Version": {"2024-01-02"}, "Anthropic-Beta": {"a-1, oauth-2025", "b-2,a-1,bad value"}, "Cookie": {"x"}, "Authorization": {"Bearer local"}}
			resp, err := c.Send(context.Background(), []byte(`{"a":1}`), in)
			if err != nil {
				t.Fatal(err)
			}
			if b, _ := io.ReadAll(resp.Body); string(b) != "ok" {
				t.Error(string(b))
			}
			resp.Body.Close()
		}
	}
}

func TestSendFailures(t *testing.T) {
	esc, _ := json.Marshal(testKey) // < > & escaped
	for name, c := range map[string]struct {
		status      int
		ctype, body string
		want        dto.APIError
	}{
		"401":             {401, "", `{"error":{"message":"Incorrect key ` + testKey + `"}}`, dto.APIError{Status: 401, Type: "authentication_error", Message: "Incorrect key [key]"}},
		"escaped key":     {401, "", `{"error":{"message":` + string(esc) + `}}`, dto.APIError{Status: 401, Type: "authentication_error", Message: "[key]"}},
		"429":             {429, "", `{"error":{"message":"slow","code":"rate_limit_exceeded"}}`, dto.APIError{Status: 429, Type: "rate_limit_error", Message: "slow", RetryAfter: "7"}},
		"404":             {404, "", `{"error":"model 'x' not found"}`, dto.APIError{Status: 404, Type: "not_found_error", Message: "model 'x' not found"}},
		"plain text":      {500, "text/plain", "boom", dto.APIError{Status: 502, Type: "api_error", Message: "boom"}},
		"empty":           {500, "", "", dto.APIError{Status: 502, Type: "api_error", Message: "upstream returned HTTP 500"}},
		"no message":      {400, "", "{}", dto.APIError{Status: 400, Type: "invalid_request_error", Message: "the server returned HTTP 400"}},
		"503":             {503, "", `{"message":"busy"}`, dto.APIError{Status: 529, Type: "overloaded_error", Message: "busy", RetryAfter: "7"}},
		"array":           {400, "", `[{"error":{"code":400,"message":"bad","status":"INVALID_ARGUMENT"}}]`, dto.APIError{Status: 400, Type: "invalid_request_error", Message: "bad"}},
		"FastAPI":         {422, "", `{"detail":[{"loc":["body","messages",0],"msg":"field required"}]}`, dto.APIError{Status: 422, Type: "invalid_request_error", Message: "body.messages.0: field required"}},
		"provider":        {400, "", `{"error":{"message":"Provider returned error","code":400,"metadata":{"raw":"why"}}}`, dto.APIError{Status: 400, Type: "invalid_request_error", Message: "Provider returned error: why"}},
		"HTML":            {502, "text/html", "<html><title>502 Bad\n Gateway</title><body>secret " + strings.Repeat("x", 5000), dto.APIError{Status: 502, Type: "api_error", Message: "the server answered with a web page (HTTP 502): 502 Bad Gateway"}},
		"unmarked HTML":   {404, "text/plain", "<!doctype html><p>x", dto.APIError{Status: 404, Type: "not_found_error", Message: "the server answered with a web page (HTTP 404); check the address"}},
		"a web page, 200": {200, "text/html; charset=utf-8", "<html>hi", dto.APIError{Status: 502, Type: "api_error", Message: "the server answered with a web page (HTTP 200); check the address"}},
		"context":         {400, "", `{"error":{"message":"the request exceeds the available context size. try increasing the context size"}}`, dto.APIError{Status: 400, Type: "invalid_request_error", Message: "prompt is too long: the request exceeds the available context size. try increasing the context size"}},
	} {
		cl := serve(t, route.Chat, func(w http.ResponseWriter, _ *http.Request) {
			if c.ctype != "" {
				w.Header().Set("Content-Type", c.ctype)
			}
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(c.status)
			io.WriteString(w, c.body)
		})
		_, err := cl.Send(context.Background(), []byte(`{}`), nil)
		if got := apiErr(t, err); !reflect.DeepEqual(*got, c.want) {
			t.Errorf("%s: %+v", name, *got)
		}
		if strings.Contains(fmt.Sprintf("%+v", err), testKey) {
			t.Error(name, "leaks the key")
		}
	}
}

func TestSendGuards(t *testing.T) {
	hits := 0
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits++ }))
	defer other.Close()
	c := serve(t, route.Chat, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, 307) })
	if _, err := c.Send(context.Background(), []byte(`{}`), nil); apiErr(t, err).Status != 502 || hits != 0 {
		t.Error(err, hits)
	}
	for _, u := range []string{"http://host.example/v1", "ftp://127.0.0.1", ""} {
		c.Endpoint.BaseURL = u
		if _, err := c.Send(context.Background(), []byte(`{}`), nil); apiErr(t, err).Status != 500 {
			t.Error(u, err)
		}
		if _, err := c.Models(context.Background()); err == nil {
			t.Error(u)
		}
	}
	c = serve(t, route.Chat, func(http.ResponseWriter, *http.Request) { t.Error("sent") })
	for _, k := range []func() (string, error){func() (string, error) { return "a\nb", nil }, func() (string, error) { return "", errors.New(testKey) }} {
		c.Key = k
		_, err := c.Send(context.Background(), []byte(`{}`), nil)
		if e := apiErr(t, err); e.Status != 401 || strings.Contains(e.Message, testKey) {
			t.Error(e)
		}
	}
	c = serve(t, route.Chat, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(401)
		io.WriteString(w, `{"error":{"message":"no"}}`)
	})
	c.Key = nil
	if _, err := c.Send(context.Background(), []byte(`{}`), nil); apiErr(t, err).Message != "Fake wants a key: no" {
		t.Error(err)
	}
	c.Endpoint.BaseURL = "http://127.0.0.1:1/v1"
	if _, err := c.Send(context.Background(), []byte(`{}`), nil); apiErr(t, err).Message != "could not reach Fake" {
		t.Error(err)
	}
}

func TestSendHeaderLimit(t *testing.T) {
	defer func(d time.Duration) { headerLimit = d }(headerLimit)
	if headerLimit != 15*time.Minute {
		t.Error(headerLimit)
	}
	headerLimit = 100 * time.Millisecond
	release := make(chan bool)
	c := serve(t, route.Chat, func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	defer close(release)
	if _, err := c.Send(context.Background(), []byte(`{}`), nil); apiErr(t, err).Status != 504 {
		t.Error(err)
	}
	// The limit is for the headers: a slow body is not cut.
	c = serve(t, route.Chat, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		time.Sleep(3 * headerLimit)
		io.WriteString(w, "late")
	})
	resp, err := c.Send(context.Background(), []byte(`{}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if b, err := io.ReadAll(resp.Body); err != nil || string(b) != "late" {
		t.Error(string(b), err)
	}
	resp.Body.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Send(ctx, []byte(`{}`), nil); apiErr(t, err).Status != 502 {
		t.Error(err)
	}
}

func TestModels(t *testing.T) {
	for name, c := range map[string]struct {
		body string
		want []Model
	}{
		"OpenAI":     {`{"object":"list","data":[{"id":"gpt-5.6","object":"model"},{"id":"GPT-5.6"},{"id":"bad id"},{"id":7},null,"x",{"id":"a‮"}]}`, []Model{{ID: "gpt-5.6"}, {ID: "x"}}},
		"Anthropic":  {`{"data":[{"type":"model","id":"claude-x","display_name":"Claude\n X","max_input_tokens":200000}],"has_more":false}`, []Model{{ID: "claude-x", Name: "Claude X", Context: 200000}}},
		"Ollama":     {`{"models":[{"name":"llama4:8b","model":"llama4:8b","size":1}]}`, []Model{{ID: "llama4:8b"}}},
		"Gemini":     {`{"models":[{"name":"models/gemini-3","displayName":"Gemini 3","inputTokenLimit":1048576,"outputTokenLimit":65536}]}`, []Model{{ID: "models/gemini-3", Name: "Gemini 3", Context: 1048576, OutputMax: 65536}}},
		"vLLM":       {`{"data":[{"id":"Qwen/Qwen4","max_model_len":32768}]}`, []Model{{ID: "Qwen/Qwen4", Context: 32768}}},
		"llama.cpp":  {`{"models":[{"name":"x"}],"data":[{"id":"x.gguf","meta":{"n_ctx_train":8192}}]}`, []Model{{ID: "x.gguf", Context: 8192}}},
		"LM Studio":  {`{"data":[{"id":"m","max_context_length":131072,"loaded_context_length":4096}]}`, []Model{{ID: "m", Context: 4096}}},
		"OpenRouter": {`{"data":[{"id":"z/glm","name":"GLM","context_length":200000,"top_provider":{"max_completion_tokens":131072}}]}`, []Model{{ID: "z/glm", Name: "GLM", Context: 200000, OutputMax: 131072}}},
		"Groq":       {`{"data":[{"id":"g","context_window":131072,"max_completion_tokens":8192}]}`, []Model{{ID: "g", Context: 131072, OutputMax: 8192}}},
		"Together":   {`[{"id":"a/b","display_name":"B","context_length":-5}]`, []Model{{ID: "a/b", Name: "B"}}},
		"strings":    {`{"data":["a","b"]}`, []Model{{ID: "a"}, {ID: "b"}}},
		"null":       {`{"object":"list","data":null}`, []Model{}},
		"empty":      {`{"data":[]}`, []Model{}},
	} {
		cl := serve(t, route.Chat, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "GET" || r.URL.String() != "/v1/models" || r.Header.Get("Authorization") != "Bearer "+testKey {
				t.Error(r.Method, r.URL)
			}
			io.WriteString(w, c.body)
		})
		if got, err := cl.Models(context.Background()); err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %+v %v", name, got, err)
		}
	}
	cl := serve(t, route.Anthropic, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.String() != "/v1/models?limit=1000" || r.Header.Get("X-Api-Key") != testKey || r.Header.Get("Anthropic-Version") == "" {
			t.Error(r.URL, r.Header)
		}
		var ids []string
		for i := range 3000 {
			ids = append(ids, fmt.Sprintf(`"m%d"`, i))
		}
		io.WriteString(w, "["+strings.Join(ids, ",")+"]")
	})
	if got, err := cl.Models(context.Background()); err != nil || len(got) != maxModels {
		t.Error(len(got), err)
	}
	for name, h := range map[string]http.HandlerFunc{
		"status":   func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(404) },
		"redirect": func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/x", 302) },
		"HTML":     func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "<html>") },
		"shape":    func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, `{"data":{"a":1}}`) },
		"number":   func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, `7`) },
		"too big": func(w http.ResponseWriter, _ *http.Request) {
			io.WriteString(w, `["`+strings.Repeat("x", maxModelBody)+`"]`)
		},
	} {
		if got, err := serve(t, route.Chat, h).Models(context.Background()); err == nil {
			t.Error(name, got)
		}
	}
}

func TestOverflow(t *testing.T) {
	for _, msg := range []string{
		"This model's maximum context length is 8192 tokens. However, your messages resulted in 9000 tokens. Please reduce the length of the messages.",
		"Your input exceeds the context window of this model. Please adjust your input and try again.",
		"The decoder prompt (length 40000) is longer than the maximum model length of 32768.",
		"You passed 33000 input tokens and requested 100 output tokens. However, the model's context length is only 32768 tokens",
		"request (5000 tokens) exceeds the available context size (4096 tokens), try increasing it",
		"Trying to keep the first 5000 tokens when context the overflows. However, the model is loaded with context length of only 4096 tokens, which is not enough.",
		"The number of tokens to keep from the initial prompt is greater than the context length.",
		"This endpoint's maximum context length is 128000 tokens. However, you requested about 200000 tokens (190000 of text input, 10000 in the output).",
		"`inputs` tokens + `max_new_tokens` must be <= 8193. Given: 9000 `inputs` tokens and 512 `max_new_tokens`",
		"Input validation error: `inputs` must have less than 4095 tokens. Given: 5000",
		"Prompt contains 40000 tokens and 0 draft tokens, too large for model with 32768 maximum context length",
		"The input token count (1200000) exceeds the maximum number of tokens allowed (1048576).",
		"This model's maximum prompt length is 131072 but the request contains 537812 tokens.",
		"The prompt is too long: 12345, model maximum context length: 8191",
		"Invalid request: Your request exceeded model token limit: 131072", "Prompt exceeds max length", "invalid params, context window exceeds limit",
		"<400> InternalError.Algo.InvalidParameter: Range of input length should be [1, 129024]", "Input is too long for requested model.",
		"The input (40000 tokens) is longer than the model's context length (32768 tokens).", "Input length (40000 tokens) exceeds the maximum allowed length (32762 tokens).",
		"input length exceeds maximum context length", "too many tokens: total number of tokens in the prompt cannot exceed 4081",
	} {
		raw, _ := json.Marshal(map[string]any{"error": map[string]any{"message": msg, "param": "messages"}})
		cl := serve(t, route.Chat, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(400); w.Write(raw) })
		_, err := cl.Send(context.Background(), []byte(`{"max_tokens":8000,"messages":[{"role":"user","content":"x"}]}`), nil)
		if e, ok := err.(*dto.APIError); !ok || e.Status != 400 || !strings.HasPrefix(e.Message, "prompt is too long: ") {
			t.Errorf("%s: %v", msg, err)
		}
	}
	for _, msg := range []string{
		"Request too large for model on tokens per minute (TPM): Limit 6000, Requested 9000, please reduce your message size and try again.",
		"input length and `max_tokens` exceed context limit: 190000 + 20000 > 200000, decrease input length or `max_tokens` and try again",
		"string too long. Expected a string with maximum length 40", "temperature must be at most 2", "maximum number of images exceeded",
	} {
		if translate.Overflow(msg) {
			t.Error(msg)
		}
	}
}

func TestRefusedField(t *testing.T) {
	const chat, resp, anth = route.Chat, route.Responses, route.Anthropic
	openai := func(msg, param string) string {
		raw, _ := json.Marshal(map[string]any{"error": map[string]any{"message": msg, "type": "invalid_request_error", "param": param, "code": "unsupported_parameter"}})
		return string(raw)
	}
	fast := func(msg string, loc ...any) string {
		raw, _ := json.Marshal(map[string]any{"detail": []any{map[string]any{"loc": loc, "msg": msg, "type": "extra_forbidden"}}})
		return string(raw)
	}
	mid := `"messages":[{"role":"system","content":"s"},{"role":"user","content":"u"},{"role":"system","content":"t"},{"role":"user","content":"v"}]`
	for _, c := range []struct {
		protocol, sent, body, want string
	}{
		{chat, `{"max_tokens":9}`, openai("Unsupported parameter: 'max_tokens' is not supported with this model. Use 'max_completion_tokens' instead.", "max_tokens"), "max_tokens"},
		{chat, `{"max_completion_tokens":9}`, fast("Extra inputs are not permitted", "body", "max_completion_tokens"), "max_completion_tokens"},
		{chat, `{"temperature":0.2}`, openai("Unsupported value: 'temperature' does not support 0.2 with this model. Only the default (1) value is supported.", "temperature"), "temperature"},
		{chat, `{"temperature":0.2}`, openai("Unsupported value: 'temperature' does not support 0.2 with this model.", ""), "temperature"},
		{chat, `{"temperature":0.2}`, `{"error":"temperature is not supported by this model"}`, "temperature"},
		{chat, `{"top_p":1}`, openai("Unsupported parameter: 'temperature'", "temperature"), ""}, // it was not sent
		{chat, `{"temperature":1,"top_p":1}`, "`temperature` and `top_p` cannot both be specified for this model. Invalid request.", "temperature"},
		{chat, `{"stop":["a"]}`, openai("Unsupported parameter: 'stop' is not supported with this model.", "stop"), "stop"},
		{chat, `{"stop":["a"]}`, `{"error":{"message":"Please stop sending invalid requests"}}`, ""},
		{chat, `{"stream_options":{}}`, `{"object":"error","message":"stream_options: Extra inputs are not permitted"}`, "stream_options"},
		{chat, `{"parallel_tool_calls":false}`, openai("Unknown parameter: 'parallel_tool_calls'.", "parallel_tool_calls"), "parallel_tool_calls"},
		{chat, `{"tool_choice":"required"}`, `{"error":{"message":"Only tool_choice auto and none are supported"}}`, "tool_choice"},
		{chat, `{"tool_choice":"required"}`, `{"error":{"message":"tool_choice 'required' is incompatible with thinking enabled"}}`, "tool_choice"},
		{chat, `{"response_format":{"type":"json_schema"}}`, `{"error":{"message":"This response_format type is unavailable now","type":"invalid_request_error","param":null}}`, "response_format"},
		{chat, `{"response_format":{}}`, `{"error":{"message":"json_schema is not supported by this model"}}`, "response_format"},
		{chat, `{"reasoning_effort":"max"}`, openai("Unsupported value: 'max' is not supported with the 'gpt-5' model. Supported values are: 'low', 'medium', and 'high'.", "reasoning_effort"), translate.LowerEffort},
		{chat, `{"reasoning_effort":"high"}`, openai("Unrecognized request argument supplied: reasoning_effort", ""), "reasoning_effort"},
		{chat, `{"reasoning_effort":"max"}`, openai("Unrecognized request argument supplied: reasoning_effort", ""), "reasoning_effort"},
		{chat, `{"reasoning_effort":"low"}`, `{"error":{"message":"\"gemma3\" does not support thinking"}}`, "reasoning_effort"},
		{chat, `{"max_tokens":128000}`, openai("max_tokens is too large: 128000. This model supports at most 16384 completion tokens, whereas you provided 128000.", "max_tokens"), "max_tokens<=16384"},
		{chat, `{"max_tokens":128000}`, `{"error":{"message":"Invalid max_tokens value, the valid range of max_tokens is [1, 8192]"}}`, "max_tokens<=8192"},
		{chat, `{"max_tokens":128000}`, `{"error":{"message":"Range of max_tokens should be [1,8192]"}}`, "max_tokens<=8192"},
		{chat, `{"max_tokens":128000}`, "`max_tokens` must be less than or equal to `8192`, the maximum value for `max_tokens` is less than the `context_window` for this model", "max_tokens<=8192"},
		{chat, `{"max_tokens":128000}`, `{"message":"'max_tokens' or 'max_completion_tokens' is too large: 128000. This model's maximum context length is 32768 tokens and your request has 500 input tokens (128000 > 32768 - 500)."}`, translate.NoMaxTokens},
		{chat, `{"max_tokens":8000}`, `{"message":"This model's maximum context length is 32768 tokens. However, you requested 33000 tokens (25000 in the messages, 8000 in the completion). Please reduce the length of the messages or completion."}`, translate.NoMaxTokens},
		{chat, `{"max_tokens":8000}`, `{"message":"This model's maximum context length is 32768 tokens. However, you requested 40000 tokens (32000 in the messages, 8000 in the completion)."}`, ""},
		{chat, `{"max_tokens":8000}`, "`inputs` tokens + `max_new_tokens` must be <= 8193. Given: 500 `inputs` tokens and 8000 `max_new_tokens`", translate.NoMaxTokens},
		{chat, `{` + mid + `}`, `{"error":{"message":"System message must be at the beginning."}}`, "system"},
		{chat, `{"messages":[{"role":"system","content":"s"},{"role":"user","content":"u"}]}`, `{"error":{"message":"System message must be at the beginning."}}`, ""},
		{chat, `{` + mid + `}`, `{"message":"Conversation roles must alternate user/assistant/user/assistant/..."}`, "system"},
		{chat, `{` + mid + `}`, `{"object":"error","message":"Unexpected role 'system' after role 'user'"}`, "system"},
		{resp, `{"input":[{"role":"developer"}]}`, openai("Invalid value: 'developer'. Unknown role: 'developer'", "input[0].role"), "system"},
		{anth, `{` + mid + `}`, fast("Input should be 'user' or 'assistant'", "body", "messages", 2, "role"), "system"},
		{chat, `{"messages":[{"content":[{"type":"image_url"}]}]}`, `{"error":{"message":"Failed to deserialize the JSON body: unknown variant ` + "`image_url`, expected `text`" + `"}}`, "image_url"},
		{chat, `{"messages":[{"content":[{"type":"image_url"}]}]}`, `{"error":{"message":"this model does not support image input"}}`, "image_url"},
		{chat, `{"messages":[{"content":[{"type":"file"}]}]}`, openai("Invalid content type. 'file' is not supported by this model", "messages[0].content[0]"), "file"},
		{chat, `{"messages":[]}`, `{"error":{"message":"this model does not support image input"}}`, ""},
		{chat, `{"messages":[]}`, `{"error":{"message":"The reasoning_content in the thinking mode must be passed back to the API."}}`, translate.NeedReasoning},
		{chat, `{"messages":[{"role":"assistant","reasoning_content":"x"}]}`, `{"error":{"message":"'messages.2' : for 'role:assistant' the following must be satisfied[('messages.2' : property 'reasoning_content' is unsupported)]"}}`, "reasoning_content"},
		{chat, `{"messages":[{"role":"assistant","reasoning":"x"}]}`, `{"error":{"message":"property 'reasoning' is unsupported"}}`, "reasoning_content"},
		{chat, `{"messages":[]}`, `[{"error":{"code":400,"message":"Function call is missing a thought_signature in functionCall parts."}}]`, translate.NeedSignatures},
		{chat, `{"messages":[]}`, `{"message":"Tool call id was toolu_01 but must be a-z, A-Z, 0-9, with a length of 9."}`, translate.NeedShortIDs},
		{chat, `{"tools":[{}]}`, `{"error":{"message":"registry.ollama.ai/library/gemma:2b does not support tools"}}`, ""},
		{chat, `{"temperature":1}`, `{"error":{"message":"Provider returned error","metadata":{"raw":"{\"error\":{\"message\":\"Unsupported parameter: 'temperature'\",\"param\":\"temperature\"}}"}}}`, "temperature"},
		{resp, `{"max_output_tokens":9}`, openai("Unsupported parameter: max_output_tokens", ""), "max_output_tokens"},
		{resp, `{"reasoning":{"effort":"xhigh"}}`, openai("Unsupported value: 'xhigh'. Supported values are: 'low', 'high'.", "reasoning.effort"), translate.LowerEffort},
		{resp, `{"reasoning":{"effort":"low"}}`, openai("Unsupported parameter: 'reasoning.effort' is not supported with this model.", "reasoning.effort"), "reasoning_effort"},
		{resp, `{"reasoning":{"summary":"auto"}}`, openai("Your organization must be verified to generate reasoning summaries.", "reasoning.summary"), "reasoning.summary"},
		{resp, `{"include":["reasoning.encrypted_content"]}`, openai("Encrypted content is not supported with this model.", "include"), "include"},
		{resp, `{"tools":[{"type":"web_search"}]}`, openai("Hosted tool 'web_search' is not supported with this model.", "tools"), "web_search"},
		{resp, `{"store":false}`, fast("Extra inputs are not permitted", "body", "store"), "store"},
		{resp, `{"text":{}}`, openai("Invalid 'text'", "text.format.type"), "text"},
		{resp, `{"include":[]}`, `{"error":{"code":"invalid_encrypted_content","message":"The encrypted content could not be verified."}}`, ""},
		{anth, `{"context_management":{}}`, `{"type":"error","error":{"type":"invalid_request_error","message":"context_management: Extra inputs are not permitted"}}`, "context_management"},
		{anth, `{"thinking":{}}`, `{"type":"error","error":{"type":"invalid_request_error","message":"thinking: Extra inputs are not permitted"}}`, "thinking"},
		{anth, `{"max_tokens":128000}`, `{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens: 128000 > 64000, which is the maximum allowed number of output tokens for claude-x"}}`, "max_tokens<=64000"},
		{anth, `{"max_tokens":20000}`, "{\"error\":{\"message\":\"input length and `max_tokens` exceed context limit: 190000 + 20000 > 200000, decrease input length or `max_tokens` and try again\"}}", ""},
		{anth, `{"thinking":{},"messages":[]}`, "{\"error\":{\"message\":\"messages.1.content.0: Invalid `signature` in `thinking` block\"}}", ""},
		{chat, `{}`, `not json`, ""}, {chat, `junk`, openai("Unsupported 'temperature'", "temperature"), ""}, {chat, `{"messages":[],"model":"x"}`, openai("Invalid model", "model"), ""},
	} {
		if got := refusedField(c.protocol, 400, []byte(c.sent), []byte(c.body)); got != c.want {
			t.Errorf("%s %s %s: %q, want %q", c.protocol, c.sent, c.body, got, c.want)
		}
		for _, status := range []int{401, 429, 500} {
			if got := refusedField(c.protocol, status, []byte(c.sent), []byte(c.body)); got != "" {
				t.Errorf("HTTP %d: %q", status, got)
			}
		}
		if c.want != "" { // whatever is found can be acted on
			if _, ok := (translate.Options{Effort: true, WebSearch: true, Encrypted: true}).Without(c.want); !ok {
				t.Errorf("Without(%q)", c.want)
			}
		}
	}
	if got := refusedField(route.Chat, 404, []byte(`{"messages":[{"type":"image_url"}]}`), []byte(`{"error":{"message":"No endpoints found that support image input","code":404}}`)); got != "image_url" {
		t.Error(got)
	}
	cl := serve(t, route.Chat, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(400)
		io.WriteString(w, openai("Unsupported parameter: 'temperature'", "temperature"))
	})
	_, err := cl.Send(context.Background(), []byte(`{"temperature":1}`), nil)
	var u *Unsupported
	if !errors.As(err, &u) || u.Field != "temperature" || apiErr(t, err).Message != "Unsupported parameter: 'temperature'" {
		t.Errorf("%v", err)
	}
}
