package translate

import (
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/Lookoff-AIMLAPI/second-life/internal/dto"
)

var chatTestCtx = dto.StreamContext{PublicModel: "claude-opus", UpstreamModel: "m/1", ToolNames: map[string]string{"short": "mcp__a__very_long"}, ShowThinking: true, InputEstimate: 77}

const chatDone = `{"type":"slife.done"}`

func cDelta(fields string) string {
	return `{"id":"chatcmpl-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{` + fields + `},"finish_reason":null}]}`
}

func cText(s string) string {
	q, _ := json.Marshal(s)
	return cDelta(`"content":` + string(q))
}

func cThink(s string) string {
	q, _ := json.Marshal(s)
	return cDelta(`"reasoning_content":` + string(q))
}

func cFinish(reason string) string {
	return `{"id":"chatcmpl-1","choices":[{"index":0,"delta":{},"finish_reason":"` + reason + `"}]}`
}

const cUsage = `{"id":"chatcmpl-1","choices":[],"usage":{"prompt_tokens":100,"completion_tokens":7,"prompt_tokens_details":{"cached_tokens":40}}}`

// cCall is one entry of tool_calls; "-" leaves a field out.
func cCall(index, id, name, args string) string {
	var f, fn []string
	if index != "-" {
		f = append(f, `"index":`+index)
	}
	if id != "-" {
		f = append(f, `"id":"`+id+`"`)
	}
	if name != "-" {
		fn = append(fn, `"name":"`+name+`"`)
	}
	if args != "-" {
		q, _ := json.Marshal(args)
		fn = append(fn, `"arguments":`+string(q))
	}
	f = append(f, `"type":"function","function":{`+strings.Join(fn, ",")+`}`)
	return "{" + strings.Join(f, ",") + "}"
}

func cCalls(calls ...string) string { return cDelta(`"tool_calls":[` + strings.Join(calls, ",") + `]`) }

func chatPlay(t *testing.T, ctx dto.StreamContext, evs ...string) (*ChatStream, *stClient) {
	t.Helper()
	s, c := NewChatStream(ctx), &stClient{t: t}
	for _, e := range evs {
		c.feed(s.Push(stJSON(t, e)))
	}
	c.feed(s.Close())
	if !s.Ended() || s.Close() != nil || s.Push(stJSON(t, cText("x"))) != nil {
		t.Fatal("the end is not final")
	}
	return s, c
}

// chatIDs names generated ids G and sealed ids S:<original>.
func chatIDs(list []string) []string {
	out := []string{}
	for _, b := range list {
		f := strings.SplitN(b, "|", 3)
		if f[0] == "tool_use" {
			if orig, _ := openCall(f[1], "m/1"); orig != f[1] {
				if strings.HasPrefix(orig, "call_") && len(orig) == 29 {
					orig = "G"
				}
				f[1] = "S:" + orig
			} else if strings.HasPrefix(f[1], "call_") && len(f[1]) == 29 {
				f[1] = "G"
			}
		}
		out = append(out, strings.Join(f, "|"))
	}
	return out
}

func TestChatStreamBlocks(t *testing.T) {
	sig := func(field, s string) string { return EncodeReasoning("m/1", field, s) }
	think := func(s string) string { return "thinking|||" + s + "|" + sig("", s) }
	hidden, omitted := chatTestCtx, chatTestCtx
	hidden.HideThinking, hidden.ShowThinking = true, false
	omitted.ShowThinking = false
	const idA, idB = "call_aaaaaaaa", "call_bbbbbbbb"
	cases := []struct {
		name string
		ctx  *dto.StreamContext
		evs  []string // without the finish
		stop string   // finish reason sent
		want []string
		end  string
	}{
		{"nothing", nil, nil, "stop", []string{}, "end_turn"},
		{"text", nil, []string{cDelta(`"role":"assistant","content":""`), cText("Hel"), cText("lo")}, "stop", []string{"text|||Hello|"}, "end_turn"},
		{"parts", nil, []string{cDelta(`"content":[{"type":"text","text":"Hi"}]`)}, "stop", []string{"text|||Hi|"}, "end_turn"},
		{"only white space", nil, []string{cText("\n"), cText(" ")}, "stop", []string{}, "end_turn"},
		{"white space before a call", nil, []string{cText("\n\n"), cCalls(cCall("0", idA, "Bash", "{}"))}, "tool_calls", []string{"tool_use|" + idA + "|Bash|{}|"}, "tool_use"},
		{"reasoning_content", nil, []string{cThink("a"), cThink("b"), cText("c")}, "stop", []string{think("ab"), "text|||c|"}, "end_turn"},
		{"reasoning", nil, []string{cDelta(`"reasoning":"ab"`), cText("c")}, "stop", []string{"thinking|||ab|" + sig("reasoning", "ab"), "text|||c|"}, "end_turn"},
		{"both fields", nil, []string{cDelta(`"reasoning":"ab","reasoning_content":"ab"`), cText("c")}, "stop", []string{think("ab"), "text|||c|"}, "end_turn"},
		{"reasoning and its details", nil, []string{cDelta(`"reasoning":"ab","reasoning_details":[{"type":"reasoning.text","text":"ab"}]`)}, "stop", []string{"thinking|||ab|" + sig("reasoning", "ab")}, "end_turn"},
		{"details alone", nil, []string{cDelta(`"reasoning_details":[{"type":"reasoning.text","text":"a"},{"type":"reasoning.encrypted","data":"zz"},{"type":"reasoning.summary","summary":"b"}]`)}, "stop", []string{"thinking|||ab|" + sig("reasoning", "ab")}, "end_turn"},
		{"thinking parts", nil, []string{cDelta(`"content":[{"type":"thinking","thinking":[{"type":"text","text":"ab"}]},{"type":"text","text":"c"}]`)}, "stop", []string{think("ab"), "text|||c|"}, "end_turn"},
		{"reasoning and text in one chunk", nil, []string{cDelta(`"reasoning_content":"ab","content":"c"`)}, "stop", []string{think("ab"), "text|||c|"}, "end_turn"},
		{"reasoning between texts", nil, []string{cText("a"), cThink("b"), cText("c")}, "stop", []string{"text|||a|", think("b"), "text|||c|"}, "end_turn"},
		{"blank reasoning", nil, []string{cThink("\n"), cText("c")}, "stop", []string{"text|||c|"}, "end_turn"},
		{"omitted reasoning keeps its signature", &omitted, []string{cThink("ab"), cText("c")}, "stop", []string{"thinking||||" + sig("", "ab"), "text|||c|"}, "end_turn"},
		{"hidden reasoning", &hidden, []string{cThink("ab"), cText("<think>x</think>c")}, "stop", []string{"text|||c|"}, "end_turn"},
		{"think tag", nil, []string{cText("<think>ab</think>\n\nc")}, "stop", []string{think("ab"), "text|||c|"}, "end_turn"},
		{"think tag after white space", nil, []string{cText("\n"), cText(" <th"), cText("ink>a"), cText("b</"), cText("think>"), cText("\n"), cText("c")}, "stop", []string{think("ab"), "text|||c|"}, "end_turn"},
		{"empty think tag", nil, []string{cText("<think>\n\n</think>\n\nc")}, "stop", []string{"text|||c|"}, "end_turn"},
		{"not a think tag", nil, []string{cText("<thi"), cText("s>")}, "stop", []string{"text|||<this>|"}, "end_turn"},
		{"a later think tag is text", nil, []string{cText("a <think>b</think>")}, "stop", []string{"text|||a <think>b</think>|"}, "end_turn"},
		{"text that ends inside a tag", nil, []string{cText("<thin")}, "stop", []string{"text|||<thin|"}, "end_turn"},
		{"think tag without an end", nil, []string{cText("<think>ab</thi")}, "length", []string{think("ab</thi")}, "max_tokens"},
		{"think tag, then a call", nil, []string{cText("<think>ab</think>"), cCalls(cCall("0", idA, "Bash", "{}"))}, "stop", []string{think("ab"), "tool_use|" + idA + "|Bash|{}|"}, "tool_use"},

		{"call in pieces", nil, []string{cCalls(cCall("0", idA, "Bash", "")), cCalls(cCall("0", "-", "-", `{"a"`)), cCalls(cCall("0", "-", "-", `:1}`))}, "tool_calls", []string{`tool_use|` + idA + `|Bash|{"a":1}|`}, "tool_use"},
		{"id and name repeated", nil, []string{cCalls(cCall("0", idA, "Bash", `{"a"`)), cCalls(cCall("0", idA, "Bash", `:1}`)), cCalls(cCall("0", idA, "Bash", ``))}, "tool_calls", []string{`tool_use|` + idA + `|Bash|{"a":1}|`}, "tool_use"},
		{"a whole call repeated", nil, []string{cCalls(cCall("0", idA, "Bash", `{"a":1}`)), cCalls(cCall("0", idA, "Bash", `{"a":1}`))}, "tool_calls", []string{`tool_use|` + idA + `|Bash|{"a":1}|`}, "tool_use"},
		{"name repeated without id", nil, []string{cCalls(cCall("0", "-", "Bash", `{"a"`)), cCalls(cCall("0", "-", "Bash", `:1}`))}, "tool_calls", []string{`tool_use|G|Bash|{"a":1}|`}, "tool_use"},
		{"interleaved", nil, []string{cCalls(cCall("0", idA, "A", ""), cCall("1", idB, "B", "")), cCalls(cCall("1", "-", "-", `{"b":`)), cCalls(cCall("0", "-", "-", `{"a":`)), cCalls(cCall("1", "-", "-", `2}`), cCall("0", "-", "-", `1}`))}, "tool_calls",
			[]string{`tool_use|` + idA + `|A|{"a":1}|`, `tool_use|` + idB + `|B|{"b":2}|`}, "tool_use"},
		{"every index is 0", nil, []string{cCalls(cCall("0", idA, "A", `{"a":1}`)), cCalls(cCall("0", idB, "A", `{"a":2}`))}, "tool_calls", []string{`tool_use|` + idA + `|A|{"a":1}|`, `tool_use|` + idB + `|A|{"a":2}|`}, "tool_use"},
		{"every index is 0, no arguments", nil, []string{cCalls(cCall("0", idA, "A", "")), cCalls(cCall("0", idB, "A", ""))}, "tool_calls", []string{`tool_use|` + idA + `|A|{}|`, `tool_use|` + idB + `|A|{}|`}, "tool_use"},
		{"every index is 0, no ids", nil, []string{cCalls(cCall("0", "-", "A", `{"a":1}`), cCall("0", "-", "A", `{"a":2}`), cCall("0", "-", "B", ``))}, "stop", []string{`tool_use|G|A|{"a":1}|`, `tool_use|G|A|{"a":2}|`, `tool_use|G|B|{}|`}, "tool_use"},
		{"no index, with ids", nil, []string{cCalls(cCall("-", idA, "A", `{"a"`)), cCalls(cCall("-", idA, "-", `:1}`), cCall("-", idB, "B", `{}`))}, "tool_calls", []string{`tool_use|` + idA + `|A|{"a":1}|`, `tool_use|` + idB + `|B|{}|`}, "tool_use"},
		{"no index, no ids", nil, []string{cCalls(cCall("-", "-", "A", "")), cCalls(cCall("-", "-", "-", `{"a":1}`)), cCalls(cCall("-", "-", "A", `{"a":2}`), cCall("-", "-", "A", `{"a":3}`))}, "tool_calls",
			[]string{`tool_use|G|A|{"a":1}|`, `tool_use|G|A|{"a":2}|`, `tool_use|G|A|{"a":3}|`}, "tool_use"},
		{"the name comes late", nil, []string{cCalls(cCall("0", idA, "-", `{"a":`)), cCalls(cCall("0", "-", "A", `1}`))}, "tool_calls", []string{`tool_use|` + idA + `|A|{"a":1}|`}, "tool_use"},
		{"the id comes late", nil, []string{cCalls(cCall("0", "-", "A", ``)), cCalls(cCall("0", idA, "-", `{"a":1}`))}, "tool_calls", []string{`tool_use|` + idA + `|A|{"a":1}|`}, "tool_use"},
		{"empty ids", nil, []string{cCalls(cCall("0", "", "A", `{}`), cCall("1", "", "B", `{}`))}, "tool_calls", []string{`tool_use|G|A|{}|`, `tool_use|G|B|{}|`}, "tool_use"},
		{"one id for two calls", nil, []string{cCalls(cCall("0", idA, "A", `{}`), cCall("1", idA, "B", `{}`))}, "tool_calls", []string{`tool_use|` + idA + `|A|{}|`, `tool_use|G|B|{}|`}, "tool_use"},
		{"short id", nil, []string{cCalls(cCall("0", "call_0", "A", `{}`))}, "tool_calls", []string{`tool_use|S:call_0|A|{}|`}, "tool_use"},
		{"foreign characters in an id", nil, []string{cCalls(cCall("0", "functions.Bash:0", "A", `{}`))}, "tool_calls", []string{`tool_use|S:functions.Bash:0|A|{}|`}, "tool_use"},
		{"no arguments", nil, []string{cCalls(cCall("0", idA, "A", "-"))}, "tool_calls", []string{"tool_use|" + idA + "|A|{}|"}, "tool_use"},
		{"arguments null", nil, []string{cDelta(`"tool_calls":[{"index":0,"id":"` + idA + `","function":{"name":"A","arguments":null}}]`)}, "tool_calls", []string{"tool_use|" + idA + "|A|{}|"}, "tool_use"},
		{"arguments \"null\"", nil, []string{cCalls(cCall("0", idA, "A", "null"))}, "tool_calls", []string{"tool_use|" + idA + "|A|{}|"}, "tool_use"},
		{"object arguments", nil, []string{cDelta(`"tool_calls":[{"function":{"name":"A","arguments":{"a":1}}},{"function":{"name":"B","arguments":{}}}]`)}, "stop", []string{`tool_use|G|A|{"a":1}|`, `tool_use|G|B|{}|`}, "tool_use"},
		{"a string that holds the object", nil, []string{cCalls(cCall("0", idA, "A", `"{\"a\":`)), cCalls(cCall("0", "-", "-", `1}"`))}, "tool_calls", []string{`tool_use|` + idA + `|A|{"a":1}|`}, "tool_use"},
		{"a placeholder before the arguments", nil, []string{cCalls(cCall("0", idA, "A", `{}`)), cCalls(cCall("0", "-", "-", `{"a":1}`))}, "tool_calls", []string{`tool_use|` + idA + `|A|{"a":1}|`}, "tool_use"},
		{"garbage after the arguments", nil, []string{cCalls(cCall("0", idA, "A", `{"a":"}"}`)), cCalls(cCall("0", "-", "-", `}`))}, "tool_calls", []string{`tool_use|` + idA + `|A|{"a":"}"}|`}, "tool_use"},
		{"shortened name", nil, []string{cCalls(cCall("0", idA, "short", `{}`))}, "tool_calls", []string{"tool_use|" + idA + "|mcp__a__very_long|{}|"}, "tool_use"},
		{"text after a call", nil, []string{cText("a"), cCalls(cCall("0", idA, "A", `{"x":`)), cText("b"), cCalls(cCall("0", "-", "-", `1}`))}, "tool_calls", []string{"text|||a|", `tool_use|` + idA + `|A|{"x":1}|`, "text|||b|"}, "tool_use"},
		{"reasoning after a call", nil, []string{cCalls(cCall("0", idA, "A", `{}`)), cThink("b")}, "tool_calls", []string{"tool_use|" + idA + "|A|{}|"}, "tool_use"},
		{"function_call", nil, []string{cDelta(`"function_call":{"name":"A","arguments":""}`), cDelta(`"function_call":{"arguments":"{\"a\":1}"}`)}, "function_call", []string{`tool_use|G|A|{"a":1}|`}, "tool_use"},
		{"tool_calls without a call", nil, []string{cText("a")}, "tool_calls", []string{"text|||a|"}, "end_turn"},
		{"a call without a name or arguments", nil, []string{cText("a"), cCalls(cCall("0", idA, "-", ""))}, "tool_calls", []string{"text|||a|"}, "end_turn"},
		{"a call cut before its arguments", nil, []string{cText("a"), cCalls(cCall("0", idA, "A", ""))}, "length", []string{"text|||a|"}, "max_tokens"},
		{"a call cut inside its arguments", nil, []string{cCalls(cCall("0", idA, "A", `{"a`))}, "length", []string{`tool_use|` + idA + `|A|{"a|`}, "max_tokens"},
		{"refusal", nil, []string{cDelta(`"refusal":"I can"`), cDelta(`"refusal":"not"`)}, "stop", []string{"text|||I cannot|"}, "refusal"},
		{"content filter", nil, []string{cText("a")}, "content_filter", []string{"text|||a|"}, "refusal"},
		{"unknown reason", nil, []string{cText("a")}, "because", []string{"text|||a|"}, "end_turn"},
		{"another choice", nil, []string{`{"choices":[{"index":1,"delta":{"content":"no"}},{"index":0,"delta":{"content":"yes"}}]}`}, "stop", []string{"text|||yes|"}, "end_turn"},
		{"empty errors", nil, []string{`{"error":null,"choices":[{"delta":{"content":"a"}}]}`, `{"error":{},"base_resp":{"status_code":0},"choices":[{"delta":{"content":"b"}}]}`}, "stop", []string{"text|||ab|"}, "end_turn"},
		{"whole message at the end", nil, []string{cText("a"), `{"object":"chat.completion","choices":[{"message":{"role":"assistant","content":"a"}}]}`}, "stop", []string{"text|||a|"}, "end_turn"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := chatTestCtx
			if c.ctx != nil {
				ctx = *c.ctx
			}
			ends := map[string][]string{
				"finish, usage, done": {cFinish(c.stop), cUsage, chatDone},
				"finish, usage":       {cFinish(c.stop), cUsage},
				"usage in the finish": {strings.Replace(cFinish(c.stop), `"choices"`, `"usage":{"prompt_tokens":60,"prompt_cache_hit_tokens":40,"completion_tokens":7},"choices"`, 1)},
				"finish and then EOF": {cFinish(c.stop)},
				"finish twice, done":  {cFinish(c.stop), cFinish(c.stop), chatDone},
			}
			for name, end := range ends {
				s, cl := chatPlay(t, ctx, append(append([]string{}, c.evs...), end...)...)
				if got := chatIDs(cl.strings()); !reflect.DeepEqual(got, c.want) || cl.stop != c.end || !cl.stopped {
					t.Fatalf("%s: got %q %s, want %q %s", name, got, cl.stop, c.want, c.end)
				}
				if s.Estimated() == strings.Contains(name, "usage") {
					t.Errorf("%s: estimated %v", name, s.Estimated())
				}
				if m := s.Message(); c.name == "a call cut inside its arguments" {
					if m != nil {
						t.Error("a message with broken arguments")
					}
				} else if got := chatIDs(stFromMessage(t, m)); !reflect.DeepEqual(got, c.want) || *m.StopReason != c.end {
					t.Errorf("%s: message %q", name, got)
				}
			}
			if c.stop == "stop" { // a bare [DONE] is an end too
				if _, cl := chatPlay(t, ctx, append(append([]string{}, c.evs...), chatDone)...); cl.stop != c.end {
					t.Errorf("done: %s", cl.stop)
				}
			}
			// The same answer cut off.
			s, cl := chatPlay(t, ctx, c.evs...)
			if s.Failed() == nil || !cl.errored || s.Message() != nil {
				t.Error("a cut answer is not a failure")
			}
		})
	}
}

func TestChatStreamFinish(t *testing.T) {
	for reason, want := range map[string]string{
		"stop": "end_turn", "STOP": "end_turn", "tool_calls": "end_turn", "eos": "end_turn", "end_turn": "end_turn", "length": "max_tokens", "max_tokens": "max_tokens",
		"model_context_window_exceeded": "max_tokens", "content_filter": "refusal", "sensitive": "refusal", "SAFETY": "refusal", "weird": "end_turn",
	} {
		if _, c := chatPlay(t, chatTestCtx, cText("a"), cFinish(reason)); c.stop != want {
			t.Errorf("%s: %s", reason, c.stop)
		}
	}
	for reason, want := range map[string]string{"error": "api_error", "network_error": "api_error", "insufficient_system_resource": "overloaded_error", "abort": "api_error"} {
		if s, c := chatPlay(t, chatTestCtx, cText("a"), cFinish(reason), chatDone); c.deltaErr != want || s.Failed() == nil {
			t.Errorf("%s: %s", reason, c.deltaErr)
		}
	}
	for _, none := range []string{"", "null"} {
		if _, c := chatPlay(t, chatTestCtx, cText("a"), cFinish(none)); !c.errored {
			t.Errorf("%q ends an answer", none)
		}
	}
	s := NewChatStream(chatTestCtx)
	s.Push(stJSON(t, cText("a")))
	if s.Push(stJSON(t, cFinish("stop"))); s.Ended() {
		t.Error("ended before the usage")
	}
	if s.Push(stJSON(t, cUsage)); !s.Ended() {
		t.Error("not ended after finish and usage")
	}
}

func TestChatStreamUsage(t *testing.T) {
	u := func(in, read, write, out int) dto.Usage {
		return dto.Usage{InputTokens: in, OutputTokens: out, CacheReadInputTokens: read, CacheCreationInputTokens: write}
	}
	for _, c := range []struct {
		usage string
		want  dto.Usage
	}{
		{`"usage":{"prompt_tokens":100,"completion_tokens":7}`, u(100, 0, 0, 7)},
		{`"usage":{"prompt_tokens":100,"completion_tokens":7,"prompt_tokens_details":{"cached_tokens":40,"cache_write_tokens":10}}`, u(50, 40, 10, 7)},
		{`"usage":{"prompt_tokens":100,"completion_tokens":7,"prompt_cache_hit_tokens":40,"prompt_cache_miss_tokens":60}`, u(60, 40, 0, 7)},
		{`"usage":{"prompt_tokens":100,"completion_tokens":7,"cached_tokens":40}`, u(60, 40, 0, 7)},
		{`"usage":{"prompt_tokens":100,"completion_tokens":7,"cache_read_input_tokens":40,"cache_creation_input_tokens":5}`, u(55, 40, 5, 7)},
		{`"usage":{"input_tokens":100,"output_tokens":7}`, u(100, 0, 0, 7)},
		{`"usage":{"prompt_tokens":41,"completion_tokens":9,"total_tokens":437,"completion_tokens_details":{"reasoning_tokens":387}}`, u(41, 0, 0, 396)},
		{`"usage":{"prompt_tokens":41,"completion_tokens":9,"total_tokens":50,"completion_tokens_details":{"reasoning_tokens":5}}`, u(41, 0, 0, 9)},
		{`"x_groq":{"usage":{"prompt_tokens":100,"completion_tokens":7}}`, u(100, 0, 0, 7)},
		{`"prompt_eval_count":100,"eval_count":7`, u(100, 0, 0, 7)},
		{`"usage":{"prompt_tokens":10,"completion_tokens":7,"prompt_tokens_details":{"cached_tokens":40}}`, u(0, 40, 0, 7)},
	} {
		s, _ := chatPlay(t, chatTestCtx, cText("a"), cFinish("stop"), `{"choices":[],`+c.usage+`}`, chatDone)
		if s.Usage() != c.want || s.Estimated() {
			t.Errorf("%s: %+v", c.usage, s.Usage())
		}
	}
	in := func(choice string) string { return `{"choices":[{"index":0,` + choice + `}]}` }
	s, _ := chatPlay(t, chatTestCtx, cText("a"), in(`"delta":{},"finish_reason":"stop","usage":{"prompt_tokens":3,"completion_tokens":4}`))
	if s.Usage() != u(3, 0, 0, 4) {
		t.Errorf("%+v", s.Usage())
	}
	// Repeated: the last one counts, and empty ones do not.
	with := func(n int) string {
		return strings.Replace(cText("a"), `"choices"`, fmt.Sprintf(`"usage":{"prompt_tokens":9,"completion_tokens":%d},"choices"`, n), 1)
	}
	s, _ = chatPlay(t, chatTestCtx, with(1), with(2), strings.Replace(cText("a"), `"choices"`, `"usage":null,"choices"`, 1), cFinish("stop"), `{"choices":[],"usage":{"prompt_tokens":0,"completion_tokens":0}}`, chatDone)
	if s.Usage() != u(9, 0, 0, 2) || s.Estimated() {
		t.Errorf("%+v %v", s.Usage(), s.Estimated())
	}
	s, _ = chatPlay(t, chatTestCtx, cText("abcdefgh"), cFinish("stop"), chatDone)
	if s.Usage() != u(77, 0, 0, 3) || !s.Estimated() {
		t.Errorf("%+v", s.Usage())
	}
}

func TestChatStreamErrors(t *testing.T) {
	for _, c := range []struct {
		ev, typ, msg string
	}{
		{`{"error":{"message":"slow down","type":"rate_limit_exceeded"}}`, "rate_limit_error", "slow down"},
		{`{"error":{"message":"Provider returned error","code":429,"metadata":{"raw":"too fast"}},"choices":[{"delta":{"content":""},"finish_reason":"error"}]}`, "rate_limit_error", "Provider returned error: too fast"},
		{`{"object":"error","message":"bad","type":"BadRequestError","code":400}`, "invalid_request_error", "bad"},
		{`{"error":"model runner has unexpectedly stopped"}`, "api_error", "model runner has unexpectedly stopped"},
		{`{"error":{"message":"the request exceeds the available context size. try increasing the context size","code":500}}`, "invalid_request_error", "prompt is too long: the request exceeds the available context size. try increasing the context size"},
		{`{"base_resp":{"status_code":1008,"status_msg":"insufficient balance"}}`, "api_error", "insufficient balance"},
		{`{"detail":"Not Found"}`, "api_error", "Not Found"},
		{`{"choices":[{"delta":{},"error":{"message":"broke","code":"503"}}]}`, "overloaded_error", "broke"},
		{`{"message":"no such model","status":404}`, "not_found_error", "no such model"},
	} {
		s, cl := chatPlay(t, chatTestCtx, cText("a"), c.ev)
		if f := s.Failed(); f == nil || f.Type != c.typ || f.Message != c.msg || cl.deltaErr != c.typ {
			t.Errorf("%s: %+v", c.ev, f)
		}
	}
	// A call that cannot be run is a failure, not a silent end.
	if s, _ := chatPlay(t, chatTestCtx, cCalls(cCall("0", "x", "-", `{"a":1}`)), cFinish("tool_calls"), chatDone); s.Failed() == nil {
		t.Error("a nameless call with arguments")
	}
}

func TestChatStreamWholeBody(t *testing.T) {
	const call = `{"id":"call_aaaaaaaa","type":"function","function":{"name":"A","arguments":"{\"a\":1}"}}`
	want := []string{"thinking|||r|" + EncodeReasoning("m/1", "", "r"), "text|||t|", `tool_use|call_aaaaaaaa|A|{"a":1}|`}
	body := `{"id":"chatcmpl-9","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","reasoning_content":"r","content":"t","tool_calls":[` + call + `]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":5,"completion_tokens":6}}`
	ollama := `{"model":"m","message":{"role":"assistant","content":"","thinking":"r"},"done":false}
{"model":"m","message":{"role":"assistant","content":"t"},"done":false}
{"model":"m","message":{"role":"assistant","content":"","tool_calls":[{"id":"call_aaaaaaaa","function":{"name":"A","arguments":{"a":1}}}]},"done":false}
{"model":"m","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop","prompt_eval_count":5,"eval_count":6}
`
	chunks := []string{cThink("r"), cText("t"), cCalls(call), cFinish("tool_calls"), strings.Replace(cUsage, `100`, `45`, 1)}
	sse := ""
	for _, c := range chunks {
		sse += "data: " + c + "\n\n"
	}
	indented := func() string {
		var v any
		json.Unmarshal([]byte(body), &v)
		b, _ := json.MarshalIndent(v, "", "  ")
		return string(b)
	}()
	for name, in := range map[string]string{
		"JSON": body, "indented": "\n" + indented + "\n", "in an array": "[" + body + "]", "no finish reason": strings.Replace(body, `"finish_reason":"tool_calls"`, `"finish_reason":null`, 1),
		"JSON lines": strings.Join(chunks, "\n") + "\n", "JSON lines and done": strings.Join(chunks[:4], "\r\n") + "\r\ndata: [DONE]\r\n", "Ollama's own": ollama,
		"SSE": sse + "data: [DONE]\n\n", "no done": sse, "no blank lines": strings.ReplaceAll(sse, "\n\n", "\n") + "data: [DONE]\n", "comments": ": OPENROUTER PROCESSING\n\n" + strings.ReplaceAll(sse, "\n\n", "\n\n: ping\n\n"),
		"bom": "\xef\xbb\xbf" + sse, "no space": strings.ReplaceAll(sse, "data: ", "data:"), "named events": strings.ReplaceAll(sse, "data: ", "event: message\ndata: "),
	} {
		for _, r := range []io.Reader{strings.NewReader(in), iotest.OneByteReader(strings.NewReader(in))} {
			s, cl := NewChatStream(chatTestCtx), &stClient{t: t}
			if err := ReadSSE(r, func(ev map[string]any) error { cl.feed(s.Push(ev)); return nil }, Lenient); err != nil {
				t.Fatal(name, err)
			}
			cl.feed(s.Close())
			if got := cl.strings(); !reflect.DeepEqual(got, want) || cl.stop != "tool_use" {
				t.Errorf("%s: %q %s %v", name, got, cl.stop, s.Failed())
			}
			if !strings.Contains(name, "done") && s.Usage().OutputTokens == 0 {
				t.Errorf("%s: %+v", name, s.Usage())
			}
		}
	}
	for name, in := range map[string]string{
		"empty": "", "cut": sse[:len(sse)/2], "half a body": body[:len(body)/2], "not JSON": "<html>no</html>", "error body": `{"error":{"message":"x"}}`, "error line": sse[:strings.Index(sse, "\n\n")+2] + `{"error":{"message":"x"}}` + "\n",
		"broken JSON": "{oops", "two lines of three": strings.Join(chunks[:2], "\n"),
	} {
		s, cl := NewChatStream(chatTestCtx), &stClient{t: t}
		if err := ReadSSE(strings.NewReader(in), func(ev map[string]any) error { cl.feed(s.Push(ev)); return nil }, Lenient); err != nil {
			t.Fatal(name, err)
		}
		cl.feed(s.Close())
		if s.Failed() == nil || strings.HasPrefix(name, "error") && s.Failed().Message != "x" {
			t.Errorf("%s: %v %q", name, s.Failed(), cl.strings())
		}
	}
}

func TestLenientResponses(t *testing.T) {
	for name, c := range map[string]struct {
		in   string
		fail bool
	}{
		"a whole body": {`{"id":"resp_1","object":"response","status":"completed","output":[` + stMsg("hi") + `],"usage":{"input_tokens":1,"output_tokens":2}}`, false},
		"a stream":     {"data: " + stItemDone(0, stMsg("hi")) + "\n\ndata: " + stDone + "\n\ndata: [DONE]\n\n", false},
		"a failure":    {`{"object":"response","status":"failed","error":{"message":"x"}}`, true},
		"an error":     {`{"error":{"message":"x"}}`, true},
	} {
		s, cl := NewStream(chatTestCtx), &stClient{t: t}
		if err := ReadSSE(strings.NewReader(c.in), func(ev map[string]any) error { cl.feed(s.Push(ev)); return nil }, Lenient); err != nil {
			t.Fatal(err)
		}
		cl.feed(s.Close())
		if c.fail && (s.Failed() == nil || s.Failed().Message != "x") || !c.fail && !reflect.DeepEqual(cl.strings(), []string{"text|||hi|"}) {
			t.Errorf("%s: %v %q", name, s.Failed(), cl.strings())
		}
	}
}

func TestLenientReadSSE(t *testing.T) {
	big := `{"a":"` + strings.Repeat("x", 1<<20) + `"}`
	if err := ReadSSE(strings.NewReader(strings.Repeat(big, 40)), func(map[string]any) error { return nil }, Lenient); err != nil {
		t.Error("objects add up:", err)
	}
	if err := ReadSSE(strings.NewReader(`{"a":"`+strings.Repeat("x", 33<<20)), func(map[string]any) error { return nil }, Lenient); err != errStEventSize {
		t.Error(err)
	}
	if err := ReadSSE(iotest.ErrReader(io.ErrUnexpectedEOF), func(map[string]any) error { return nil }, Lenient); err != io.ErrUnexpectedEOF {
		t.Error(err)
	}
	stop := fmt.Errorf("stop")
	for _, in := range []string{`{"a":1}{"b":2}`, "data: [DONE]\n\ndata: {}\n\n", `{"a":1}`} {
		n := 0
		if err := ReadSSE(strings.NewReader(in), func(map[string]any) error { n++; return stop }, Lenient); err != stop || n != 1 {
			t.Error(in, err, n)
		}
	}
	// A chunk is delivered before the next one comes.
	for _, in := range []string{`{"a":1}` + "\n", `data: {"a":1}` + "\n", "data: [DONE]\n"} {
		r, w := io.Pipe()
		got := make(chan bool, 1)
		go ReadSSE(r, func(map[string]any) error { got <- true; return nil }, Lenient)
		io.WriteString(w, in)
		<-got
		w.Close()
	}
}

func TestChatStreamIsLive(t *testing.T) {
	s := NewChatStream(chatTestCtx)
	names := func(ev string) (out []string) {
		for _, e := range s.Push(stJSON(t, ev)) {
			n := e.Name
			if d, ok := e.Data["delta"].(map[string]any); ok && d["type"] != nil {
				n = d["type"].(string)
			}
			out = append(out, n)
		}
		return
	}
	for _, c := range [][2]string{
		{cThink("a"), "[message_start content_block_start thinking_delta]"}, {cThink("b"), "[thinking_delta]"},
		{cText("<"), "[]"}, // may start a think tag
		{cText("c"), "[signature_delta content_block_stop content_block_start text_delta]"}, {cText("d"), "[text_delta]"},
		{cCalls(cCall("0", "call_aaaaaaaa", "A", "")), "[content_block_stop]"},
		{cCalls(cCall("0", "-", "-", `{"a":`)), "[content_block_start input_json_delta]"},
		{cCalls(cCall("0", "-", "-", `1}`)), "[input_json_delta]"},
		{cCalls(cCall("1", "call_bbbbbbbb", "B", `{}`)), "[]"},
	} {
		if got := fmt.Sprint(names(c[0])); got != c[1] {
			t.Errorf("%s: %s, want %s", c[0], got, c[1])
		}
	}
}

func TestChatStreamID(t *testing.T) {
	for in, want := range map[string]string{"chatcmpl-1": "msg_1", "abc": "msg_abc", "a b/c": "msg_a_b_c"} {
		s := NewChatStream(chatTestCtx)
		s.Push(map[string]any{"id": in, "choices": []any{map[string]any{"delta": map[string]any{}}}})
		if s.id != want {
			t.Error(in, s.id)
		}
	}
	_, c := chatPlay(t, chatTestCtx, `{"choices":[{"delta":{"content":"a"}}]}`, cFinish("stop"))
	if id := c.message["id"].(string); !strings.HasPrefix(id, "msg_") || len(id) < 20 || c.message["model"] != "claude-opus" {
		t.Error(c.message)
	}
}

func TestChatStreamExtra(t *testing.T) {
	s, c := chatPlay(t, chatTestCtx, cDelta(`"tool_calls":[{"index":0,"id":"call_aaaaaaaa","function":{"name":"A","arguments":"{}"},"extra_content":{"google":{"thought_signature":"SIG"}}}]`), cFinish("stop"))
	id, extra := openCall(c.blocks[0].id, "m/1")
	if id != "call_aaaaaaaa" || string(extra) != `{"google":{"thought_signature":"SIG"}}` || reqClean(c.blocks[0].id) != c.blocks[0].id || s.Failed() != nil {
		t.Error(id, string(extra), c.blocks[0].id)
	}
}

func TestChatStopSequences(t *testing.T) {
	run := func(seqs []string, evs ...string) (*ChatStream, string, any, any) {
		ctx := chatTestCtx
		ctx.StopSeqs = seqs
		s := NewChatStream(ctx)
		text := ""
		var reason, seq any
		push := func(list []dto.Event) {
			for _, e := range list {
				d, _ := e.Data["delta"].(map[string]any)
				if d["type"] == "text_delta" {
					text += d["text"].(string)
				}
				if e.Name == "message_delta" {
					reason, seq = d["stop_reason"], d["stop_sequence"]
				}
			}
		}
		for _, e := range evs {
			push(s.Push(stJSON(t, e)))
		}
		push(s.Close())
		return s, text, reason, seq
	}
	five := []string{"1", "2", "3", "4", "END"}
	s, text, reason, seq := run(five, cText("a E"), cText("ND b"), cText("c"))
	if text != "a " || reason != "stop_sequence" || seq != "END" || !s.Estimated() || s.Usage().InputTokens != 77 {
		t.Error(text, reason, seq, s.Usage())
	}
	if m := s.Message(); m == nil || m.Content[0]["text"] != "a " || *m.StopSequence != "END" {
		t.Error(m)
	}
	if _, text, reason, _ := run(five, cText("a E"), cFinish("stop"), cUsage); text != "a E" || reason != "end_turn" {
		t.Error(text, reason)
	}
	if _, text, reason, seq := run(five, cText("<think>END</think>b"), cFinish("stop"), cUsage); text != "b" || reason != "end_turn" || seq != nil {
		t.Error("a stop sequence in the reasoning", text, reason, seq)
	}
	if _, text, _, seq := run([]string{"\n\n"}, cText("\n"), cText("\nx")); text != "" || seq != "\n\n" {
		t.Errorf("%q %v", text, seq)
	}
	named := strings.Replace(cFinish("stop"), `"finish_reason"`, `"stop_reason":"END","finish_reason"`, 1)
	if _, text, reason, seq := run(five, cText("a"), named, cUsage); text != "a" || reason != "stop_sequence" || seq != "END" {
		t.Error(text, reason, seq)
	}
	if _, _, reason, _ := run([]string{"x"}, cText("a"), named, cUsage); reason != "end_turn" {
		t.Error(reason)
	}
}

// Any answer, cut into any chunks in any order that a server may choose, gives the same blocks.
func TestChatStreamRandom(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	piece := func(s string) (a, b string) {
		if len(s) == 0 {
			return "", ""
		}
		n := 1 + r.Intn(len(s))
		return s[:n], s[n:]
	}
	for n := 0; n < 15000; n++ {
		think, text := "", ""
		if r.Intn(2) == 0 {
			think = "r" + stASCII(r)
		}
		if r.Intn(4) > 0 {
			text = "t" + stASCII(r)
		}
		type call struct{ name, args, left string }
		calls := make([]*call, r.Intn(4))
		want := []string{}
		if think != "" {
			want = append(want, "thinking|||"+think+"|"+EncodeReasoning("m/1", "", think))
		}
		if text != "" {
			want = append(want, "text|||"+text+"|")
		}
		for i := range calls {
			raw, _ := json.Marshal(map[string]any{"a": stASCII(r), "n": i})
			calls[i] = &call{fmt.Sprintf("T%d", r.Intn(3)), string(raw), string(raw)}
		}
		var evs []string
		tagged := r.Intn(2) == 0
		if tagged && think != "" {
			for rest := strings.Repeat("\n", r.Intn(2)) + "<think>" + think + "</think>" + strings.Repeat("\n", r.Intn(3)) + text; rest != ""; {
				var p string
				p, rest = piece(rest)
				evs = append(evs, cText(p))
			}
		} else {
			for rest := think; rest != ""; {
				var p string
				p, rest = piece(rest)
				evs = append(evs, cThink(p))
			}
			for rest := text; rest != ""; {
				var p string
				p, rest = piece(rest)
				evs = append(evs, cText(p))
			}
		}
		ids := r.Intn(3)                   // sent once, on every piece, never
		repeat := r.Intn(2) == 0           // the name on every piece
		zero := ids == 1 && r.Intn(2) == 0 // every index is 0
		serial := r.Intn(2) == 0 || ids == 2
		for i, c := range calls {
			id := fmt.Sprintf("call_%08d", i)
			want = append(want, "tool_use|"+map[bool]string{true: "G", false: id}[ids == 2]+"|"+c.name+"|"+c.args+"|")
		}
		sent := map[int]bool{}
		for {
			var open []int
			for i, c := range calls {
				if c.left != "" {
					open = append(open, i)
				}
			}
			if len(open) == 0 {
				break
			}
			i := open[0]
			if !serial {
				i = open[r.Intn(min(len(open), 1+len(sent)))] // first pieces keep their order
				for j := 0; j < i; j++ {
					if !sent[j] {
						i = j
						break
					}
				}
			}
			c := calls[i]
			id, name, index := "-", "-", fmt.Sprint(i)
			if ids == 1 || ids == 0 && !sent[i] {
				id = fmt.Sprintf("call_%08d", i)
			}
			if repeat || !sent[i] {
				name = c.name
			}
			if zero {
				index = "0"
				if !serial {
					index = "-"
				}
			}
			var p string
			if p, c.left = piece(c.left); r.Intn(4) == 0 && !sent[i] {
				evs = append(evs, cCalls(cCall(index, id, name, "")))
				if ids == 0 {
					id = "-"
				}
			}
			sent[i] = true
			evs = append(evs, cCalls(cCall(index, id, name, p)))
		}
		finish := []string{"stop", "tool_calls"}[r.Intn(2)]
		evs = append(evs, cFinish(finish), cUsage)
		_, cl := chatPlay(t, chatTestCtx, evs...)
		end := "end_turn"
		if len(calls) > 0 {
			end = "tool_use"
		}
		if got := chatIDs(cl.strings()); !reflect.DeepEqual(got, want) || cl.stop != end {
			t.Fatalf("run %d\n got %q %s\nwant %q %s\n%s", n, got, cl.stop, want, end, strings.Join(evs, "\n"))
		}
	}
}

func stASCII(r *rand.Rand) string {
	const set = "ab <>/{}[]\":,\\\n\tthink"
	b := make([]byte, r.Intn(12))
	for i := range b {
		b[i] = set[r.Intn(len(set))]
	}
	return strings.TrimSpace(strings.NewReplacer("</think>", "").Replace(string(b)))
}

// Nonsense never panics and never breaks the protocol; every stream ends once.
func TestChatStreamJunk(t *testing.T) {
	r := rand.New(rand.NewSource(8))
	values := []string{`null`, `1`, `-3`, `1e300`, `"x"`, `""`, `"<think>"`, `"</think>"`, `"{"`, `"}"`, `"{}"`, `{}`, `[]`, `true`, `[{}]`, `[null]`, `{"a":1}`, `[{"type":"thinking","thinking":[{"text":"x"}]}]`, `" "`, `"END"`, `"\"{}\""`, `"c2x1_x"`}
	pick := func() string { return values[r.Intn(len(values))] }
	obj := func(keys ...string) string {
		var f []string
		for _, k := range keys {
			if r.Intn(3) > 0 {
				f = append(f, `"`+k+`":`+pick())
			}
		}
		return "{" + strings.Join(f, ",") + "}"
	}
	with := func(o string, key, v string) string {
		sep := ","
		if o == "{}" {
			sep = ""
		}
		return strings.TrimSuffix(o, "}") + sep + `"` + key + `":` + v + "}"
	}
	contexts := []dto.StreamContext{chatTestCtx, {HideThinking: true}, {}, {ShowThinking: true, StopSeqs: []string{"END", "</think>", " "}}}
	for n := 0; n < 60000; n++ {
		ctx := contexts[r.Intn(len(contexts))]
		s, c := NewChatStream(ctx), &stClient{t: t}
		feed := func(evs []dto.Event) {
			if len(ctx.StopSeqs) > 0 { // stClient refuses stop sequences
				for i, e := range evs {
					if d, ok := e.Data["delta"].(map[string]any); ok && e.Name == "message_delta" {
						evs[i].Data = map[string]any{"type": e.Name, "usage": e.Data["usage"], "delta": map[string]any{"stop_reason": d["stop_reason"], "stop_sequence": nil}}
					}
				}
			}
			c.feed(evs)
		}
		for i := 0; i < r.Intn(9); i++ {
			delta := obj("content", "reasoning_content", "reasoning", "reasoning_details", "refusal", "function_call")
			var calls []string
			for j := 0; j < r.Intn(3); j++ {
				calls = append(calls, with(obj("index", "id", "extra_content"), "function", obj("name", "arguments")))
			}
			if calls != nil {
				delta = with(delta, "tool_calls", "["+strings.Join(calls, ",")+"]")
			}
			kind := []string{"delta", "delta", "delta", "message"}[r.Intn(4)]
			choice := with(obj("index", "stop_reason"), kind, delta)
			if r.Intn(6) == 0 {
				choice = with(choice, "finish_reason", []string{`"stop"`, `"length"`, `"tool_calls"`, `"content_filter"`, `"error"`, pick()}[r.Intn(6)])
			}
			ev := with("{}", "choices", "["+choice+"]")
			switch r.Intn(14) {
			case 0:
				ev = chatDone
			case 1:
				ev = obj("id", "choices", "usage", "error", "message", "type", "object", "detail", "base_resp", "done")
			case 2:
				ev = with(ev, "usage", obj("prompt_tokens", "completion_tokens", "prompt_tokens_details"))
			}
			feed(s.Push(stJSON(t, ev)))
		}
		feed(s.Close())
		if !s.Ended() || c.stopped == c.errored || c.stopped != (s.Failed() == nil) {
			t.Fatal("no single end")
		}
		ids := map[string]bool{}
		for _, b := range c.blocks {
			if b.kind == "tool_use" && (ids[b.id] || reqClean(b.id) != b.id || b.id == "") {
				t.Fatalf("id %q", b.id)
			}
			ids[b.id] = true
			if b.kind == "thinking" && ctx.HideThinking {
				t.Fatal("hidden thinking")
			}
		}
		s.Message()
		s.Notes()
	}
}
