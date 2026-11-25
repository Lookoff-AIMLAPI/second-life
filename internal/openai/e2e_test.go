package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Lookoff-AIMLAPI/second-life/internal/dto"
	"github.com/Lookoff-AIMLAPI/second-life/internal/route"
	"github.com/Lookoff-AIMLAPI/second-life/internal/translate"
)

// The whole way: a Claude request, its translation, a server that imitates a real one, and the answer. The servers are
// as strict as the real ones are about what they refuse.

type body = map[string]any

func say(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func refuse(w http.ResponseWriter, msg, param string) {
	say(w, 400, body{"error": body{"message": msg, "type": "invalid_request_error", "param": param, "code": nil}})
}

func sse(w http.ResponseWriter, lines ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, l := range lines {
		io.WriteString(w, l+"\n\n")
		w.(http.Flusher).Flush()
	}
}

func chunk(delta string, finish string) string {
	if finish != "null" {
		finish = `"` + finish + `"`
	}
	return `data: {"id":"chatcmpl-7","object":"chat.completion.chunk","choices":[{"index":0,"delta":{` + delta + `},"finish_reason":` + finish + `}]}`
}

const usageChunk = `data: {"id":"chatcmpl-7","choices":[],"usage":{"prompt_tokens":100,"completion_tokens":9,"prompt_tokens_details":{"cached_tokens":60}}}`

// strict checks what every Chat server demands and returns the messages.
func strict(t *testing.T, b body) []any {
	t.Helper()
	msgs, _ := b["messages"].([]any)
	var waiting []any
	for i, v := range msgs {
		m := v.(body)
		if len(waiting) > 0 && m["tool_call_id"] != waiting[0] {
			t.Errorf("message %d: %v, want the result of %v", i, m, waiting[0])
		}
		if m["role"] == "tool" {
			if len(waiting) == 0 {
				t.Fatalf("message %d: a result without a call", i)
			}
			waiting = waiting[1:]
		}
		calls, _ := m["tool_calls"].([]any)
		for _, c := range calls {
			waiting = append(waiting, c.(body)["id"])
		}
	}
	if len(waiting) > 0 || b["stream"] != true {
		t.Errorf("waiting %v, stream %v", waiting, b["stream"])
	}
	return msgs
}

// agent plays Claude Code and the bridge: it retries without what was refused, and remembers it.
type agent struct {
	t       *testing.T
	c       *Client
	o       translate.Options
	model   string
	refused []string
	msgs    []any
	fields  string
}

func newAgent(t *testing.T, protocol string, h http.HandlerFunc) *agent {
	return &agent{t: t, c: serve(t, protocol, h), model: "m-1", o: translate.Options{Endpoint: "ep-0123abcd", Effort: true, Encrypted: true},
		fields: `"max_tokens":32000,"temperature":0.5,"output_config":{"effort":"max"},"thinking":{"type":"adaptive"},"system":"Be brief.","tools":[{"name":"Read","input_schema":{"type":"object"}},{"name":"Bash"}]`}
}

func (a *agent) say(role string, content any) {
	a.msgs = append(a.msgs, body{"role": role, "content": content})
}

type translated interface {
	Push(map[string]any) []dto.Event
	Close() []dto.Event
	Ended() bool
	Failed() *dto.APIError
	Message() *dto.MessageResponse
	Usage() dto.Usage
	Estimated() bool
}

func (a *agent) turn() (translated, error) {
	a.t.Helper()
	list, _ := json.Marshal(a.msgs)
	var req dto.MessagesRequest
	if err := json.Unmarshal([]byte(`{"model":"claude-x",`+a.fields+`,"messages":`+string(list)+`}`), &req); err != nil {
		a.t.Fatal(err)
	}
	for range 8 {
		var sent any
		var names map[string]string
		var e *dto.APIError
		if a.c.Endpoint.Protocol == route.Responses {
			var tr *dto.Translated
			if tr, e = translate.ToResponsesGeneric(&req, a.model, a.o); e == nil {
				sent, names = &tr.Body, tr.ToolNames
			}
		} else {
			var tr *dto.ChatTranslated
			if tr, e = translate.ToChat(&req, a.model, a.o); e == nil {
				sent, names = &tr.Body, tr.ToolNames
			}
		}
		if e != nil {
			return nil, e
		}
		raw, err := json.Marshal(sent)
		if err != nil {
			a.t.Fatal(err)
		}
		resp, err := a.c.Send(context.Background(), raw, nil)
		var u *Unsupported
		if errors.As(err, &u) {
			next, ok := a.o.Without(u.Field)
			if !ok {
				return nil, u.API
			}
			a.o, a.refused = next, append(a.refused, u.Field)
			continue
		}
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		ctx := dto.StreamContext{PublicModel: "claude-x", UpstreamModel: a.o.Identity(a.model), ToolNames: names, InputEstimate: 5}
		var s translated = translate.NewChatStream(ctx)
		if a.c.Endpoint.Protocol == route.Responses {
			s = translate.NewStream(ctx)
		}
		stop := errors.New("stop")
		_ = translate.ReadSSE(resp.Body, func(ev map[string]any) error {
			if s.Push(ev); s.Ended() {
				return stop
			}
			return nil
		}, translate.Lenient)
		s.Close()
		if f := s.Failed(); f != nil {
			return nil, f
		}
		a.say("assistant", s.Message().Content)
		return s, nil
	}
	a.t.Fatal("too many retries")
	return nil, nil
}

// must plays a turn and answers every call with its name.
func (a *agent) must(want string) translated {
	a.t.Helper()
	s, err := a.turn()
	if err != nil {
		a.t.Fatal(err)
	}
	m := s.Message()
	var got []string
	var results []any
	for _, b := range m.Content {
		switch b["type"] {
		case "text":
			got = append(got, b["text"].(string))
		case "thinking":
			got = append(got, "thinking")
		case "tool_use":
			raw, _ := json.Marshal(b["input"])
			got = append(got, fmt.Sprintf("%v%s", b["name"], raw))
			results = append(results, body{"type": "tool_result", "tool_use_id": b["id"], "content": fmt.Sprint(b["name"], " done")})
		}
	}
	if s := strings.Join(got, " | ") + " " + *m.StopReason; s != want {
		a.t.Fatalf("got %q, want %q", s, want)
	}
	if results != nil {
		a.say("user", results)
	}
	return s
}

func decode(t *testing.T, r *http.Request) body {
	var b body
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		t.Fatal(err)
	}
	return b
}

func last(msgs []any) body { return msgs[len(msgs)-1].(body) }

func TestLikeOpenAI(t *testing.T) {
	known := []string{"model", "messages", "tools", "tool_choice", "parallel_tool_calls", "response_format", "reasoning_effort", "max_completion_tokens", "top_p", "stop", "stream", "stream_options"}
	a := newAgent(t, route.Chat, func(w http.ResponseWriter, r *http.Request) {
		b := decode(t, r)
		msgs := strict(t, b)
		switch {
		case b["max_tokens"] != nil:
			refuse(w, "Unsupported parameter: 'max_tokens' is not supported with this model. Use 'max_completion_tokens' instead.", "max_tokens")
			return
		case b["temperature"] != nil:
			refuse(w, "Unsupported value: 'temperature' does not support 0.5 with this model. Only the default (1) value is supported.", "temperature")
			return
		case b["reasoning_effort"] == "max":
			refuse(w, "Unsupported value: 'reasoning_effort' does not support 'max' with this model. Supported values are: 'low', 'medium', and 'high'.", "reasoning_effort")
			return
		}
		for k := range b {
			if !slices.Contains(known, k) {
				refuse(w, "Unrecognized request argument supplied: "+k, "")
				return
			}
		}
		for _, v := range msgs {
			calls, _ := v.(body)["tool_calls"].([]any)
			for _, c := range calls {
				if id := c.(body)["id"].(string); len(id) > 40 {
					refuse(w, "Invalid 'tool_calls[0].id': string too long.", "messages")
					return
				}
			}
		}
		if last(msgs)["role"] == "tool" {
			sse(w, chunk(`"role":"assistant","content":"","refusal":null`, "null"), chunk(`"content":"Done"`, "null"), chunk(`"content":"."`, "null"), chunk("", "stop"), usageChunk, "data: [DONE]")
			return
		}
		sse(w, chunk(`"role":"assistant","content":null,"tool_calls":[{"index":0,"id":"call_AbCdEfGhIjKlMnOpQrStUvWx","type":"function","function":{"name":"Read","arguments":""}}]`, "null"),
			chunk(`"tool_calls":[{"index":0,"function":{"arguments":"{\"f\":"}}]`, "null"), chunk(`"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]`, "null"),
			chunk(`"tool_calls":[{"index":1,"id":"call_BbCdEfGhIjKlMnOpQrStUvWx","type":"function","function":{"name":"Bash","arguments":"{}"}}]`, "null"), chunk("", "tool_calls"), usageChunk, "data: [DONE]")
	})
	a.say("user", "go")
	s := a.must(`Read{"f":1} | Bash{} tool_use`)
	if u := s.Usage(); u.InputTokens != 40 || u.CacheReadInputTokens != 60 || u.OutputTokens != 9 || s.Estimated() {
		t.Errorf("%+v", u)
	}
	a.must(`Done. end_turn`)
	if fmt.Sprint(a.refused) != "[max_tokens temperature reasoning_effort<=high]" {
		t.Error(a.refused)
	}
}

func TestLikeOllama(t *testing.T) {
	a := newAgent(t, route.Chat, func(w http.ResponseWriter, r *http.Request) {
		b := decode(t, r)
		msgs := strict(t, b)
		if b["reasoning_effort"] != nil {
			say(w, 400, body{"error": body{"message": `"gemma" does not support thinking`, "type": "invalid_request_error", "param": nil, "code": nil}})
			return
		}
		if last(msgs)["role"] == "tool" {
			if m := msgs[2].(body); m["reasoning"] != "Let me see." {
				t.Errorf("reasoning is lost: %v", m)
			}
			sse(w, chunk(`"role":"assistant","content":"Fine"`, "null"), chunk(`"role":"assistant","content":""`, "stop"), "data: [DONE]")
			return
		}
		sse(w, chunk(`"role":"assistant","content":"","reasoning":"Let me "`, "null"), chunk(`"role":"assistant","content":"","reasoning":"see."`, "null"),
			chunk(`"role":"assistant","content":"","tool_calls":[{"id":"call_aaaaaaaa","index":0,"type":"function","function":{"name":"Read","arguments":"{\"f\":1}"}}]`, "null"),
			chunk(`"role":"assistant","content":"","tool_calls":[{"id":"call_bbbbbbbb","index":0,"type":"function","function":{"name":"Read","arguments":"{\"f\":2}"}}]`, "null"),
			chunk(`"role":"assistant","content":""`, "stop"), usageChunk, "data: [DONE]")
	})
	a.say("user", "go")
	a.must(`thinking | Read{"f":1} | Read{"f":2} tool_use`)
	if s := a.must(`Fine end_turn`); !s.Estimated() {
		t.Error("usage out of nowhere")
	}
	if fmt.Sprint(a.refused) != "[reasoning_effort]" {
		t.Error(a.refused)
	}
}

func TestLikeVLLM(t *testing.T) {
	a := newAgent(t, route.Chat, func(w http.ResponseWriter, r *http.Request) {
		b := decode(t, r)
		msgs := strict(t, b)
		for i, v := range msgs {
			if v.(body)["role"] == "system" && i > 0 {
				say(w, 400, body{"object": "error", "message": "System message must be at the beginning.", "type": "BadRequestError", "code": 400})
				return
			}
		}
		if n, _ := b["max_tokens"].(float64); n > 30000 {
			say(w, 400, body{"error": body{"message": "'max_tokens' or 'max_completion_tokens' is too large: 32000. This model's maximum context length is 32768 tokens and your request has 2000 input tokens (32000 > 32768 - 2000).", "type": "BadRequestError", "param": "max_tokens", "code": 400}})
			return
		}
		if last(msgs)["role"] == "tool" {
			sse(w, chunk(`"role":"assistant","content":""`, "null"), chunk(`"content":"<think>hm"`, "null"), chunk(`"content":"m</think>\n\nOk"`, "null"), strings.Replace(chunk(`"content":""`, "stop"), `"finish_reason"`, `"stop_reason":null,"finish_reason"`, 1), usageChunk, "data: [DONE]")
			return
		}
		sse(w, chunk(`"role":"assistant","content":""`, "null"), chunk(`"reasoning":"a","reasoning_content":"a"`, "null"),
			chunk(`"tool_calls":[{"id":"chatcmpl-tool-0123456789abcdef0123456789abcdef","type":"function","index":0,"function":{"name":"Bash","arguments":"{\"c\": \"ls\"}"}}]`, "null"),
			chunk(`"content":""`, "tool_calls"), usageChunk, "data: [DONE]")
	})
	a.say("user", "go")
	a.say("system", "Mind the gap.")
	a.say("user", "now")
	a.must(`thinking | Bash{"c":"ls"} tool_use`)
	a.must(`thinking | Ok end_turn`)
	if fmt.Sprint(a.refused) != "[system max_tokens:none]" {
		t.Error(a.refused)
	}
}

func TestLikeDeepSeek(t *testing.T) {
	a := newAgent(t, route.Chat, func(w http.ResponseWriter, r *http.Request) {
		b := decode(t, r)
		msgs := strict(t, b)
		if n, _ := b["max_tokens"].(float64); n > 8192 {
			refuse(w, "Invalid max_tokens value, the valid range of max_tokens is [1, 8192]", "")
			return
		}
		if b["response_format"] != nil {
			refuse(w, "This response_format type is unavailable now", "")
			return
		}
		for _, v := range msgs {
			m := v.(body)
			if _, list := m["content"].([]any); list {
				refuse(w, "Failed to deserialize the JSON body into the target type: messages[1]: unknown variant `image_url`, expected `text`", "")
				return
			}
			if _, ok := m["reasoning_content"].(string); m["role"] == "assistant" && !ok {
				refuse(w, "The `reasoning_content` in the thinking mode must be passed back to the API.", "")
				return
			}
		}
		if last(msgs)["role"] == "tool" {
			if m := msgs[len(msgs)-2].(body); m["reasoning_content"] != "We think" {
				t.Errorf("reasoning is lost: %v", m)
			}
			sse(w, ": keep-alive", chunk(`"content":"{\"ok\":1}"`, "null"), strings.Replace(chunk(`"content":""`, "stop"), `"choices"`, `"usage":{"prompt_tokens":50,"completion_tokens":3,"prompt_cache_hit_tokens":40,"prompt_cache_miss_tokens":10},"choices"`, 1), "data: [DONE]")
			return
		}
		sse(w, chunk(`"role":"assistant","content":null,"reasoning_content":""`, "null"), chunk(`"content":null,"reasoning_content":"We think"`, "null"),
			chunk(`"tool_calls":[{"index":0,"id":"call_00_abcdefgh","type":"function","function":{"name":"Read","arguments":""}}]`, "null"), chunk(`"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]`, "null"),
			chunk(`"content":""`, "tool_calls"), "data: [DONE]")
	})
	a.fields = strings.Replace(a.fields, `"output_config":{`, `"output_config":{"format":{"type":"json_schema","schema":{"type":"object"}},`, 1)
	a.say("user", []any{body{"type": "text", "text": "see"}, body{"type": "image", "source": body{"type": "base64", "media_type": "image/png", "data": "AA"}}})
	a.say("assistant", "from another model") // has no reasoning of this one
	a.say("user", "go")
	a.must(`thinking | Read{} tool_use`)
	s := a.must(`{"ok":1} end_turn`)
	if u := s.Usage(); u.InputTokens != 10 || u.CacheReadInputTokens != 40 {
		t.Errorf("%+v", u)
	}
	if fmt.Sprint(a.refused) != "[max_tokens<=8192 response_format image_url reasoning_content:required]" {
		t.Error(a.refused)
	}
}

func TestLikeGeminiAndMistral(t *testing.T) {
	signed := false
	a := newAgent(t, route.Chat, func(w http.ResponseWriter, r *http.Request) {
		msgs := strict(t, decode(t, r))
		for _, v := range msgs {
			calls, _ := v.(body)["tool_calls"].([]any)
			for i, c := range calls {
				extra, _ := c.(body)["extra_content"].(body)
				if i == 0 && extra == nil {
					say(w, 400, []any{body{"error": body{"code": 400, "message": "Function call is missing a thought_signature in functionCall parts.", "status": "INVALID_ARGUMENT"}}})
					return
				}
				signed = signed || fmt.Sprint(extra) == "map[google:map[thought_signature:SIG+/]]"
			}
		}
		if last(msgs)["role"] == "tool" && signed {
			sse(w, chunk(`"content":"ok"`, "stop"), "data: [DONE]")
			return
		}
		sse(w, chunk(`"tool_calls":[{"index":0,"id":"function-call-1","type":"function","function":{"name":"Read","arguments":"{}"},"extra_content":{"google":{"thought_signature":"SIG+/"}}}]`, "stop"), "data: [DONE]")
	})
	a.say("assistant", []any{body{"type": "tool_use", "id": "toolu_01", "name": "Read", "input": body{}}})
	a.say("user", []any{body{"type": "tool_result", "tool_use_id": "toolu_01", "content": "x"}})
	a.must(`Read{} tool_use`)
	a.must(`ok end_turn`)
	if fmt.Sprint(a.refused) != "[thought_signature:required]" {
		t.Error(a.refused)
	}

	a = newAgent(t, route.Chat, func(w http.ResponseWriter, r *http.Request) {
		msgs := strict(t, decode(t, r))
		for _, v := range msgs {
			if id, ok := v.(body)["tool_call_id"].(string); ok && len(id) != 9 {
				say(w, 400, body{"object": "error", "message": "Tool call id was " + id + " but must be a-z, A-Z, 0-9, with a length of 9.", "type": "invalid_request_error"})
				return
			}
		}
		sse(w, chunk(`"content":"ok"`, "stop"), "data: [DONE]")
	})
	a.say("assistant", []any{body{"type": "tool_use", "id": "toolu_01", "name": "Read", "input": body{}}})
	a.say("user", []any{body{"type": "tool_result", "tool_use_id": "toolu_01", "content": "x"}})
	a.must(`ok end_turn`)
	if fmt.Sprint(a.refused) != "[tool_call_id:9]" {
		t.Error(a.refused)
	}
}

func TestLikeResponsesServers(t *testing.T) {
	a := newAgent(t, route.Responses, func(w http.ResponseWriter, r *http.Request) {
		b := decode(t, r)
		for _, f := range []string{"store", "include"} {
			if b[f] != nil {
				say(w, 422, body{"detail": []any{body{"loc": []any{"body", f}, "msg": "Extra inputs are not permitted", "type": "extra_forbidden"}}})
				return
			}
		}
		items := b["input"].([]any)
		for _, v := range items {
			if v.(body)["type"] == "additional_tools" || v.(body)["phase"] != nil {
				t.Error(v)
			}
		}
		if last(items)["type"] == "function_call_output" { // a server that does not stream
			say(w, 200, body{"id": "resp_2", "object": "response", "status": "completed", "output": []any{body{"type": "message", "role": "assistant", "content": []any{body{"type": "output_text", "text": "ok"}}}}, "usage": body{"input_tokens": 5, "output_tokens": 1}})
			return
		}
		sse(w, `data: {"type":"response.created","response":{"id":"resp_1"}}`, `data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"Read","arguments":"{}"}}`,
			`data: {"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":3,"output_tokens":2}}}`, "data: [DONE]")
	})
	a.say("user", "go")
	a.must(`Read{} tool_use`)
	a.must(`ok end_turn`)
	if fmt.Sprint(a.refused) != "[store include]" && fmt.Sprint(a.refused) != "[include store]" {
		t.Error(a.refused)
	}
}

func TestLikeBrokenServers(t *testing.T) {
	ok := chunk(`"content":"hello"`, "null")
	call := chunk(`"tool_calls":[{"index":0,"id":"call_aaaaaaaa","function":{"name":"Read","arguments":"{\"a"}}]`, "null")
	for name, c := range map[string]struct {
		h    func(w http.ResponseWriter)
		want string // type and message of the failure
	}{
		"a web page": {func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(502)
			io.WriteString(w, "<html><title>Bad gateway</title>")
		}, "api_error: the server answered with a web page (HTTP 502): Bad gateway"},
		"nothing":       {func(w http.ResponseWriter) {}, "api_error: upstream stream ended early"},
		"cut":           {func(w http.ResponseWriter) { sse(w, ok) }, "api_error: upstream stream ended early"},
		"cut in a call": {func(w http.ResponseWriter) { sse(w, ok, call) }, "api_error: upstream stream ended early"},
		"half an event": {func(w http.ResponseWriter) { sse(w, ok); io.WriteString(w, `data: {"choi`) }, "api_error: upstream stream ended early"},
		"an abort":      {func(w http.ResponseWriter) { sse(w, ok); panic(http.ErrAbortHandler) }, "api_error: upstream stream ended early"},
		"an error event": {func(w http.ResponseWriter) {
			sse(w, ok, `data: {"error":{"message":"overloaded","type":"overloaded_error"}}`)
		}, "overloaded_error: overloaded"},
		"a bare error":  {func(w http.ResponseWriter) { sse(w, ok, `{"error":{"message":"out of memory","code":500}}`) }, "api_error: out of memory"},
		"an error, 200": {func(w http.ResponseWriter) { say(w, 200, body{"error": body{"message": "no such model", "code": 404}}) }, "not_found_error: no such model"},
		"no finish":     {func(w http.ResponseWriter) { sse(w, ok, usageChunk) }, "api_error: upstream stream ended early"},
		"finish error":  {func(w http.ResponseWriter) { sse(w, chunk("", "error")) }, "api_error: upstream ended the answer: error"},
	} {
		a := newAgent(t, route.Chat, func(w http.ResponseWriter, r *http.Request) { c.h(w) })
		a.say("user", "go")
		if _, err := a.turn(); err == nil || err.Error() != c.want {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Bodies that still make sense.
	for name, h := range map[string]func(w http.ResponseWriter){
		"JSON": func(w http.ResponseWriter) {
			say(w, 200, body{"choices": []any{body{"message": body{"content": "hello"}, "finish_reason": "stop"}}})
		},
		"JSON lines": func(w http.ResponseWriter) {
			io.WriteString(w, strings.TrimPrefix(ok, "data: ")+"\n"+strings.TrimPrefix(chunk("", "stop"), "data: ")+"\n")
		},
		"garbage": func(w http.ResponseWriter) {
			sse(w, "data: {oops", ok, "what", "data: [1,2]", chunk("", "stop"), "data: [DONE]")
		},
		"only done": func(w http.ResponseWriter) { sse(w, ok, "data: [DONE]") },
		"no type": func(w http.ResponseWriter) {
			w.Header()["Content-Type"] = nil
			io.WriteString(w, ok+"\n\n"+chunk("", "stop"))
		},
		"after done":  func(w http.ResponseWriter) { sse(w, ok, chunk("", "stop"), usageChunk, "data: [DONE]", ok) },
		"early usage": func(w http.ResponseWriter) { sse(w, usageChunk, ok, chunk("", "stop")) },
	} {
		a := newAgent(t, route.Chat, func(w http.ResponseWriter, r *http.Request) { h(w) })
		a.say("user", "go")
		if s, err := a.turn(); err != nil || s.Message().Content[0]["text"] != "hello" || *s.Message().StopReason != "end_turn" {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A server that never closes the body.
	hung := make(chan bool)
	defer close(hung)
	a := newAgent(t, route.Chat, func(w http.ResponseWriter, r *http.Request) {
		sse(w, ok, chunk("", "stop"), usageChunk)
		select {
		case <-hung:
		case <-r.Context().Done():
		}
	})
	a.say("user", "go")
	began := time.Now()
	if _, err := a.turn(); err != nil || time.Since(began) > 5*time.Second {
		t.Error(err)
	}
	// One that refuses the same thing for ever.
	n := 0
	a = newAgent(t, route.Chat, func(w http.ResponseWriter, r *http.Request) {
		n++
		refuse(w, "Unsupported parameter: 'temperature'", "temperature")
	})
	a.say("user", "go")
	if _, err := a.turn(); err == nil || n != 2 || err.Error() != "invalid_request_error: Unsupported parameter: 'temperature'" {
		t.Error(n, err)
	}
}

func TestMessagesEndpoint(t *testing.T) {
	var got body
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = decode(t, r)
		io.WriteString(w, "event: message_stop\ndata: {}\n\n")
	}))
	defer srv.Close()
	c := &Client{Endpoint: route.Endpoint{ID: "ep-0123abcd", Name: "M", Protocol: route.Anthropic, BaseURL: srv.URL}}
	raw, _, e := PrepareMessages([]byte(`{"model":"claude-x","stream":true,"messages":[{"role":"user","content":"a"}]}`), "glm", translate.Options{})
	if e != nil {
		t.Fatal(e)
	}
	resp, err := c.Send(context.Background(), raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got["model"] != "glm" || got["stream"] != true {
		t.Error(got)
	}
}
