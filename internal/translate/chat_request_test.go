package translate

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Lookoff-AIMLAPI/second-life/internal/dto"
)

const chatServed = `{"role":"system","content":"You are served by m/1 through second-life."}`

func chatRun(t *testing.T, body string, o Options) *dto.ChatTranslated {
	t.Helper()
	out, err := ToChat(reqParse(t, body), "m/1", o)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return out
}

func TestChatDefaults(t *testing.T) {
	out := chatRun(t, `{"model":"x","max_tokens":32000,"messages":[{"role":"user","content":"hi"}]}`, Options{})
	reqSame(t, out.Body, `{"model":"m/1","messages":[`+chatServed+`,{"role":"user","content":"hi"}],
		"max_tokens":32000,"stream":true,"stream_options":{"include_usage":true}}`)
	if len(out.Warnings) != 0 || out.ToolNames == nil || out.Compaction {
		t.Errorf("%+v", out)
	}
}

func TestChatMessages(t *testing.T) {
	sig := EncodeReasoning("m/1", chatFieldContent, "because")
	plain := EncodeReasoning("m/1", chatFieldPlain, "since")
	other := EncodeReasoning("m/2", chatFieldContent, "no")
	const png = `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}`
	const pngPart = `{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}`
	const pdf = `{"type":"document","title":"a.pdf","source":{"type":"base64","media_type":"application/pdf","data":"BBBB"}}`
	call := func(id, name string) string {
		return `{"type":"tool_use","id":"` + id + `","name":"` + name + `","input":{"a":12345678901234567890}}`
	}
	result := func(id, content string) string {
		return `{"type":"tool_result","tool_use_id":"` + id + `","content":` + content + `}`
	}
	called := func(ids ...string) string {
		var list []string
		for _, id := range ids {
			list = append(list, `{"id":"`+id+`","type":"function","function":{"name":"Read","arguments":"{\"a\":12345678901234567890}"}}`)
		}
		return `"tool_calls":[` + strings.Join(list, ",") + `]`
	}
	user := func(blocks ...string) string { return `{"role":"user","content":[` + strings.Join(blocks, ",") + `]}` }
	asst := func(blocks ...string) string {
		return `{"role":"assistant","content":[` + strings.Join(blocks, ",") + `]}`
	}
	text := func(s string) string { return `{"type":"text","text":"` + s + `"}` }
	tool := func(id, s string) string { return `{"role":"tool","tool_call_id":"` + id + `","content":"` + s + `"}` }
	inside := `{"role":"system","content":"Mind the gap."}`
	cases := []struct {
		name, in string
		o        Options
		want     string
	}{
		{"texts of one turn", user(text("a"), text(""), text("b")), Options{}, `{"role":"user","content":"a\n\nb"}`},
		{"user turns are joined", user(text("a")) + "," + user(text("b")), Options{}, `{"role":"user","content":"a\n\nb"}`},
		{"image", user(text("see"), png), Options{}, `{"role":"user","content":[{"type":"text","text":"see"},` + pngPart + `]}`},
		{"image by url", user(`{"type":"image","source":{"type":"url","url":"https://x/y.png"}}`), Options{}, `{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://x/y.png"}}]}`},
		{"no images", user(text("see"), png), Options{NoImages: true}, `{"role":"user","content":"see\n\n` + chatNoImage + `"}`},
		{"text document", user(`{"type":"document","title":"T","source":{"type":"text","media_type":"text/plain","data":"body"}}`), Options{}, `{"role":"user","content":"Document: T\n\nbody"}`},
		{"pdf", user(pdf), Options{}, `{"role":"user","content":[{"type":"file","file":{"filename":"a.pdf","file_data":"data:application/pdf;base64,BBBB"}}]}`},
		{"call and result", user(text("go")) + "," + asst(text("ok"), call("toolu_1", "Read")) + "," + user(result("toolu_1", `"done"`), text("next")), Options{},
			`{"role":"user","content":"go"},{"role":"assistant","content":"ok",` + called("toolu_1") + `},` + tool("toolu_1", "done") + `,{"role":"user","content":"next"}`},
		{"a call alone has null content", user(text("go")) + "," + asst(call("toolu_1", "Read")) + "," + user(result("toolu_1", `""`)), Options{},
			`{"role":"user","content":"go"},{"role":"assistant","content":null,` + called("toolu_1") + `},` + tool("toolu_1", chatNoOutput)},
		{"results come in call order, before the text", asst(call("a", "Read"), call("b", "Read")) + "," + user(text("x"), result("b", `"B"`), result("a", `"A"`)), Options{},
			`{"role":"assistant","content":null,` + called("a", "b") + `},` + tool("a", "A") + `,` + tool("b", "B") + `,{"role":"user","content":"x"}`},
		{"results spread over user turns", asst(call("a", "Read"), call("b", "Read")) + "," + user(result("a", `"A"`), text("x")) + "," + user(result("b", `"B"`)), Options{},
			`{"role":"assistant","content":null,` + called("a", "b") + `},` + tool("a", "A") + `,` + tool("b", "B") + `,{"role":"user","content":"x"}`},
		{"assistant turns in a row are one", asst(call("a", "Read")) + "," + asst(text("wait")) + "," + user(result("a", `"A"`)), Options{},
			`{"role":"assistant","content":"wait",` + called("a") + `},` + tool("a", "A")},
		{"an unanswered call", asst(call("a", "Read")) + "," + user(text("stop")), Options{},
			`{"role":"assistant","content":null,` + called("a") + `},` + tool("a", reqNoResult) + `,{"role":"user","content":"stop"}`},
		{"a result without a call", user(result("zz", `"old"`)), Options{}, `{"role":"user","content":"[result of an earlier tool call]\nold"}`},
		{"the same id twice", asst(call("a", "Read")) + "," + user(result("a", `"1"`)) + "," + asst(call("a", "Read")) + "," + user(result("a", `"2"`)), Options{},
			`{"role":"assistant","content":null,` + called("a") + `},` + tool("a", "1") + `,{"role":"assistant","content":null,` + called("a") + `},` + tool("a", "2")},
		{"an error", asst(call("a", "Read")) + "," + user(`{"type":"tool_result","tool_use_id":"a","is_error":true,"content":"bad"}`), Options{},
			`{"role":"assistant","content":null,` + called("a") + `},` + tool("a", "[tool error] bad")},
		{"images of results follow all results", asst(call("a", "Read"), call("b", "Read")) + "," + user(result("a", `[`+text("pic")+`,`+png+`]`), result("b", `[`+png+`]`), text("look")), Options{},
			`{"role":"assistant","content":null,` + called("a", "b") + `},` + tool("a", "pic") + `,` + tool("b", chatSeeNext) +
				`,{"role":"user","content":[{"type":"text","text":"[attached to the result of Read]"},` + pngPart + `,{"type":"text","text":"[attached to the result of Read]"},` + pngPart + `,{"type":"text","text":"look"}]}`},
		{"pdf in a result", asst(call("a", "Read")) + "," + user(result("a", `[`+pdf+`]`)), Options{},
			`{"role":"assistant","content":null,` + called("a") + `},` + tool("a", chatSeeNext) + `,{"role":"user","content":[{"type":"text","text":"[attached to the result of Read]"},{"type":"file","file":{"filename":"a.pdf","file_data":"data:application/pdf;base64,BBBB"}}]}`},
		{"system in place", user(text("a")) + "," + inside + "," + user(text("b")), Options{}, `{"role":"user","content":"a"},{"role":"system","content":"Mind the gap."},{"role":"user","content":"b"}`},
		{"system folded", user(text("a")) + "," + inside + "," + user(text("b")), Options{SystemFirstOnly: true}, `{"role":"user","content":"a\n\n<system-reminder>\nMind the gap.\n</system-reminder>\n\nb"}`},
		{"system already wrapped", user(text("a")) + `,{"role":"system","content":"<system-reminder>x</system-reminder>"}`, Options{SystemFirstOnly: true}, `{"role":"user","content":"a\n\n<system-reminder>x</system-reminder>"}`},
		{"system between a call and its result", asst(call("a", "Read")) + "," + inside + "," + user(result("a", `"A"`)), Options{},
			`{"role":"assistant","content":null,` + called("a") + `},` + tool("a", "A") + `,{"role":"system","content":"Mind the gap."}`},
		{"own reasoning goes back", user(text("a")) + "," + asst(`{"type":"thinking","thinking":"","signature":"`+sig+`"}`, text("b")) + "," + user(text("c")), Options{},
			`{"role":"user","content":"a"},{"role":"assistant","content":"b","reasoning_content":"because"},{"role":"user","content":"c"}`},
		{"in the field it came in", user(text("a")) + "," + asst(`{"type":"thinking","thinking":"x","signature":"`+plain+`"}`, call("a", "Read")), Options{},
			`{"role":"user","content":"a"},{"role":"assistant","content":null,"reasoning":"since",` + called("a") + `},` + tool("a", reqNoResult)},
		{"foreign reasoning does not", user(text("a")) + "," + asst(`{"type":"thinking","thinking":"x","signature":"`+other+`"}`, `{"type":"thinking","thinking":"x","signature":"c2c1.zz"}`, `{"type":"redacted_thinking","data":"z"}`, text("b")), Options{},
			`{"role":"user","content":"a"},{"role":"assistant","content":"b"}`},
		{"refused reasoning does not", user(text("a")) + "," + asst(`{"type":"thinking","thinking":"","signature":"`+sig+`"}`, text("b")), Options{NoReasoning: true}, `{"role":"user","content":"a"},{"role":"assistant","content":"b"}`},
		{"reasoning alone is no turn", user(text("a")) + "," + asst(`{"type":"thinking","thinking":"","signature":"`+sig+`"}`) + "," + user(text("b")), Options{}, `{"role":"user","content":"a\n\nb"}`},
		{"short ids", asst(call("toolu_1", "Read")) + "," + user(result("toolu_1", `"A"`)), Options{ShortCallIDs: true},
			`{"role":"assistant","content":null,` + called(shortCallID("toolu_1")) + `},` + tool(shortCallID("toolu_1"), "A")},
		{"a server search in the history", user(text("a")) + "," + asst(text("b"), `{"type":"server_tool_use","id":"s","name":"web_search","input":{}}`, `{"type":"web_search_tool_result","tool_use_id":"s","content":[{"type":"web_search_result","url":"https://u","title":"T"}]}`, text("c")), Options{},
			`{"role":"user","content":"a"},{"role":"assistant","content":"b"},{"role":"user","content":"[web search results]\nT https://u"},{"role":"assistant","content":"c"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := chatRun(t, reqMsgs("["+c.in+"]"), c.o)
			reqSame(t, got.Body.Messages, "["+chatServed+","+c.want+"]")
		})
	}
}

func TestChatSystem(t *testing.T) {
	out := chatRun(t, reqWith(`"system":[{"type":"text","text":"x-anthropic-billing-header: 1"},{"type":"text","text":"Be brief."}]`), Options{})
	if s := out.Body.Messages[0]; s.Role != "system" || s.Content != "Be brief.\n\nYou are served by m/1 through second-life." {
		t.Errorf("%+v", s)
	}
}

func TestChatCallIDs(t *testing.T) {
	long := "chatcmpl-tool-" + strings.Repeat("0123456789abcdef", 3)
	extra := json.RawMessage(`{"google":{"thought_signature":"abc"}}`)
	sealed := sealCall("call_1", "m/1", extra)
	again := sealCall("call_1", "m/1", nil)
	if sealed == again || !chatPlainID.MatchString(again[:40]) || sealCall("toolu_0123456789", "m/1", nil) != "toolu_0123456789" {
		t.Fatal(sealed, again)
	}
	for _, bad := range []string{"functions.Bash:0", "call_0", "x y", chatCall + "aaaaaaaaaa"} {
		s := sealCall(bad, "m/1", nil)
		if got, _ := openCall(s, "m/1"); s == bad || got != bad || reqClean(s) != s {
			t.Errorf("%q -> %q -> %q", bad, s, got)
		}
		if got, x := openCall(s, "m/2"); got == bad || len(got) > chatMaxID || x != nil {
			t.Errorf("another model got %q %s", got, x)
		}
	}
	msgs := func(id string) string {
		return reqMsgs(`[{"role":"assistant","content":[{"type":"tool_use","id":"` + id + `","name":"a","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + id + `","content":"r"}]}]`)
	}
	for _, c := range []struct {
		id, want, extra string
		o               Options
	}{
		{long, reqShorten(long, long, 23, 8), "", Options{}},
		{sealed, "call_1", string(extra), Options{}},
		{sealed, shortCallID("call_1"), string(extra), Options{ShortCallIDs: true}},
		{"toolu_1", "toolu_1", chatPlaceholder, Options{PlaceholdSigs: true}},
		{sealed, "call_1", string(extra), Options{PlaceholdSigs: true}},
	} {
		m := chatRun(t, msgs(c.id), c.o).Body.Messages
		got := m[1].ToolCalls[0]
		if got.ID != c.want || m[2].ToolCallID != c.want || string(got.ExtraContent) != c.extra || len(got.ID) > chatMaxID {
			t.Errorf("%+v", m)
		}
	}
	// Another endpoint with the same model is another model.
	m := chatRun(t, msgs(sealed), Options{Endpoint: "ep-00000001"}).Body.Messages
	if m[1].ToolCalls[0].ExtraContent != nil || m[1].ToolCalls[0].ID == "call_1" || m[2].ToolCallID != m[1].ToolCalls[0].ID {
		t.Errorf("%+v", m)
	}
}

func TestChatReasoningSignature(t *testing.T) {
	sig := EncodeReasoning("m", "reasoning", "ünï\n")
	if f, s, ok := DecodeReasoning(sig, "m"); !ok || f != "reasoning" || s != "ünï\n" {
		t.Error(f, s, ok)
	}
	for _, bad := range []string{"", sig[5:], "c2c1." + sig[5:], sig + "!", EncodeReasoning("m2", "reasoning", "x"), EncodeReasoning("m", "", ""), "c2r1.bm8"} {
		if _, _, ok := DecodeReasoning(bad, "m"); ok {
			t.Errorf("accepted %q", bad)
		}
	}
	if _, ok := DecodeSignature(sig, "m"); ok {
		t.Error("Codex would take it")
	}
}

func TestChatTools(t *testing.T) {
	const tools = `"tools":[{"name":"Read","description":"d","input_schema":{"type":"object"}},{"name":"Late","defer_loading":true},{"name":"Never","defer_loading":true},{"type":"advisor_1","name":"advisor"}]`
	ref := `{"model":"x",` + tools + `,"messages":[{"role":"user","content":"hi"},
		{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"Read","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":[{"type":"tool_reference","tool_name":"Late"}]}]}]}`
	read := `{"type":"function","function":{"name":"Read","description":"d","parameters":{"type":"object","properties":{}}}}`
	late := `{"type":"function","function":{"name":"Late","parameters":{"type":"object","properties":{}}}}`
	out := chatRun(t, reqWith(tools), Options{})
	reqSame(t, out.Body.Tools, "["+read+"]")
	if !reflect.DeepEqual(out.Warnings, []string{"a server tool that the endpoint cannot run was left out"}) {
		t.Error(out.Warnings)
	}
	out = chatRun(t, ref, Options{})
	reqSame(t, out.Body.Tools, "["+read+","+late+"]")
	if c := out.Body.Messages[3].Content; c != "Tool Late is now available." {
		t.Error(c)
	}
	name := "mcp__" + strings.Repeat("n", 80)
	out = chatRun(t, reqWith(`"tools":[{"name":"`+name+`"}],"tool_choice":{"type":"tool","name":"`+name+`"}`), Options{})
	short := out.Body.Tools[0].Function.Name
	if out.ToolNames[short] != name {
		t.Error(out.ToolNames)
	}
	reqSame(t, out.Body.ToolChoice, `{"type":"function","function":{"name":"`+short+`"}}`)

	const one = `"tools":[{"name":"Read"}],`
	for _, c := range []struct {
		fields   string
		o        Options
		choice   string
		parallel string
		tools    int
	}{
		{one + `"tool_choice":{"type":"auto"}`, Options{}, "null", "null", 1},
		{one + `"tool_choice":{"type":"any"}`, Options{}, `"required"`, "null", 1},
		{one + `"tool_choice":{"type":"none"}`, Options{}, `"none"`, "null", 1},
		{one + `"tool_choice":{"type":"auto","disable_parallel_tool_use":true}`, Options{}, "null", "false", 1},
		{one + `"tool_choice":{"type":"any","disable_parallel_tool_use":true}`, Options{Omit: []string{"tool_choice", "parallel_tool_calls"}}, "null", "null", 1},
		{one + `"tool_choice":{"type":"none"}`, Options{Omit: []string{"tool_choice"}}, "null", "null", 0},
		{`"tool_choice":{"type":"auto","disable_parallel_tool_use":true}`, Options{}, "null", "null", 0},
		{`"tool_choice":{"type":"none"}`, Options{}, "null", "null", 0},
	} {
		b := chatRun(t, reqWith(c.fields), c.o).Body
		reqSame(t, b.ToolChoice, c.choice)
		reqSame(t, b.ParallelToolCalls, c.parallel)
		if len(b.Tools) != c.tools {
			t.Errorf("%s: %d tools", c.fields, len(b.Tools))
		}
	}
}

func TestChatFields(t *testing.T) {
	for _, c := range []struct {
		fields string
		o      Options
		want   string // fields besides model, messages, stream and stream_options
	}{
		{`"max_tokens":9`, Options{}, `"max_tokens":9`},
		{`"max_tokens":9`, Options{MaxTokensField: "max_completion_tokens"}, `"max_completion_tokens":9`},
		{`"max_tokens":9`, Options{MaxOutput: 4}, `"max_tokens":4`},
		{`"max_tokens":9`, Options{MaxOutput: 40}, `"max_tokens":9`},
		{`"max_tokens":9`, Options{Omit: []string{"max_tokens"}}, `"max_completion_tokens":9`},
		{`"max_tokens":9`, Options{Omit: []string{"max_completion_tokens"}, MaxTokensField: "max_completion_tokens"}, `"max_tokens":9`},
		{`"max_tokens":9`, Options{Omit: []string{"max_tokens", "max_completion_tokens"}}, ``},
		{`"max_tokens":0`, Options{MaxOutput: 4}, ``},
		{`"temperature":0,"top_p":0.5,"top_k":4`, Options{}, `"temperature":0,"top_p":0.5`},
		{`"temperature":0,"top_p":0.5`, Options{Omit: []string{"temperature", "top_p"}}, ``},
		{`"stop_sequences":["a","","b","c","d","e"]`, Options{}, `"stop":["a","b","c","d"]`},
		{`"stop_sequences":["a"]`, Options{Omit: []string{"stop"}}, ``},
		{`"output_config":{"effort":"max"}`, Options{}, ``},
		{`"output_config":{"effort":"max"}`, Options{Effort: true}, `"reasoning_effort":"max"`},
		{`"output_config":{"effort":"max"}`, Options{Effort: true, HighEffort: true}, `"reasoning_effort":"high"`},
		{`"output_config":{"effort":"low"}`, Options{Effort: true, HighEffort: true}, `"reasoning_effort":"low"`},
		{`"thinking":{"type":"disabled"}`, Options{Effort: true}, `"reasoning_effort":"low"`},
		{`"thinking":{"type":"adaptive"}`, Options{Effort: true}, ``},
		{`"output_config":{"format":{"type":"json_schema","schema":{"type":"object"}}}`, Options{}, `"response_format":{"type":"json_schema","json_schema":{"name":"output","schema":{"type":"object"},"strict":false}}`},
	} {
		raw, _ := json.Marshal(chatRun(t, reqWith(c.fields), c.o).Body)
		var got map[string]any
		json.Unmarshal(raw, &got)
		for _, k := range []string{"model", "messages", "stream", "stream_options"} {
			if _, ok := got[k]; !ok {
				t.Errorf("no %s", k)
			}
			delete(got, k)
		}
		reqSame(t, got, "{"+c.want+"}")
	}
	out := chatRun(t, reqWith(`"output_config":{"format":{"type":"json_schema","schema":{"type":"object"}}}`), Options{Omit: []string{"response_format", "stream_options"}})
	if out.Body.ResponseFormat != nil || out.Body.StreamOptions != nil || !strings.HasSuffix(out.Body.Messages[0].Content.(string), `{"type":"object"}`) || len(out.Warnings) != 1 {
		t.Errorf("%+v", out)
	}
	// Only an empty reasoning_content, and only with tools.
	in := `{"model":"x","tools":[{"name":"Read"}],"messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"}]}`
	if m := chatRun(t, in, Options{EchoReasoning: true}).Body.Messages[2]; m.ReasoningContent == nil || *m.ReasoningContent != "" {
		t.Errorf("%+v", m)
	}
	if m := chatRun(t, strings.Replace(in, `"tools":[{"name":"Read"}],`, "", 1), Options{EchoReasoning: true}).Body.Messages[2]; m.ReasoningContent != nil {
		t.Errorf("%+v", m)
	}
}

func TestChatErrors(t *testing.T) {
	pdf := `{"role":"user","content":[{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"B"}}]}`
	for _, c := range []struct {
		body string
		o    Options
		want string
	}{
		{`{"messages":[]}`, Options{}, "messages: at least one message is required"},
		{reqMsgs(`[{"role":"user","content":""}]`), Options{}, "messages: there is no content"},
		{reqMsgs(`[{"role":"system","content":"x"}]`), Options{}, "messages: there is no content"},
		{reqMsgs(`[{"role":"user","content":[{"type":"document","source":{"type":"url","url":"https://x/a.pdf"}}]}]`), Options{}, "messages.0.content.0: a document by URL is not supported by this endpoint; send it as base64 or text"},
		{reqMsgs("[" + pdf + "]"), Options{NoFiles: true}, "messages.0.content.0: PDF documents are not supported by this endpoint; send the text"},
		{reqWith(`"tools":[{"type":"web_search_20250305","name":"web_search"}]`), Options{WebSearch: true}, `tools.0: tool type "web_search_20250305" is not supported`},
		{reqWith(`"thread":{"id":"x"}`), Options{}, "thread: message threads are not supported; send the whole conversation"},
		{reqMsgs(`[{"role":"user","content":[{"type":"magic"}]}]`), Options{}, `messages.0.content.0: content block type "magic" is not supported here`},
		{reqWith(`"output_config":{"effort":"huge"}`), Options{}, `output_config: effort "huge" is not supported`},
	} {
		_, err := ToChat(reqParse(t, c.body), "m/1", c.o)
		if err == nil || err.Status != 400 || err.Message != c.want {
			t.Errorf("%s: %v", c.body, err)
		}
	}
	if _, err := ToChat(nil, "m", Options{}); err == nil {
		t.Error("nil request")
	}
}

func TestChatCompaction(t *testing.T) {
	out := chatRun(t, reqMsgs(`[{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"n","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"r"},{"type":"text","text":"Respond with TEXT ONLY ... <summary>"}]}]`), Options{})
	var roles []string
	for _, m := range out.Body.Messages {
		roles = append(roles, m.Role)
	}
	if !out.Compaction || fmt.Sprint(roles) != "[system assistant tool assistant user]" || out.Body.Messages[3].Content != reqPause {
		t.Errorf("%v %+v", roles, out.Body.Messages)
	}
}

// Requests Claude Code really sends; they are private and not in the repository.
func TestChatCaptured(t *testing.T) {
	dir := os.Getenv("SLIFE_CAPTURES")
	if dir == "" {
		t.Skip("SLIFE_CAPTURES is not set")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "req-*.json"))
	for _, f := range files {
		raw, _ := os.ReadFile(f)
		var r dto.MessagesRequest
		if json.Unmarshal(raw, &r) != nil || len(r.Messages) == 0 {
			continue
		}
		for _, o := range []Options{{}, {SystemFirstOnly: true, Effort: true, MaxOutput: 8192}} {
			out, err := ToChat(&r, "m/1", o)
			if err != nil {
				t.Fatalf("%s: %v", filepath.Base(f), err)
			}
			if err := chatCheck(out.Body, o); err != nil {
				t.Errorf("%s: %v", filepath.Base(f), err)
			}
			if _, err := ToResponsesGeneric(&r, "m/1", o); err != nil {
				t.Errorf("%s: %v", filepath.Base(f), err)
			}
			raw, _ := json.Marshal(out.Body)
			if strings.Contains(string(raw), "x-anthropic-billing-header") {
				t.Errorf("%s: billing line", filepath.Base(f))
			}
		}
	}
}

// chatCheck is what strict Chat servers demand of the messages.
func chatCheck(body dto.ChatRequest, o Options) error {
	var waiting []string
	last := ""
	for i, m := range body.Messages {
		if len(waiting) > 0 && (m.Role != "tool" || m.ToolCallID != waiting[0]) {
			return fmt.Errorf("message %d is %s %q, want the result of %q", i, m.Role, m.ToolCallID, waiting[0])
		}
		if i == 0 != (m.Role == "system") && (i == 0 || o.SystemFirstOnly) {
			return fmt.Errorf("message %d is %s", i, m.Role)
		}
		switch m.Role {
		case "tool":
			if len(waiting) == 0 {
				return fmt.Errorf("message %d: result without a call", i)
			}
			waiting = waiting[1:]
		case "assistant":
			for _, c := range m.ToolCalls {
				waiting = append(waiting, c.ID)
				if !json.Valid([]byte(c.Function.Arguments)) || c.Function.Name == "" {
					return fmt.Errorf("message %d: call %+v", i, c)
				}
			}
			if m.Content == nil && len(m.ToolCalls) == 0 || last == "assistant" {
				return fmt.Errorf("message %d: empty or repeated assistant", i)
			}
		case "user":
			if last == "user" || m.Content == nil || m.Content == "" {
				return fmt.Errorf("message %d: empty or repeated user", i)
			}
		}
		last = m.Role
	}
	if len(waiting) > 0 {
		return fmt.Errorf("calls without results: %v", waiting)
	}
	return nil
}

func TestChatRandomHistories(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	for n := 0; n < 4000; n++ {
		var words []string
		word := func() string {
			w := fmt.Sprintf("w%dx", len(words))
			words = append(words, w)
			return w
		}
		var ids []string
		var msgs []string
		for i := 0; i < 1+r.Intn(8); i++ {
			role := []string{"user", "assistant", "system"}[r.Intn(3)]
			var blocks []string
			for j := 0; j < 1+r.Intn(4); j++ {
				text := func() string { return `{"type":"text","text":"` + word() + `"}` }
				switch k := r.Intn(4); {
				case role == "assistant" && k < 2:
					id := fmt.Sprintf("id%d", r.Intn(6))
					ids = append(ids, id)
					blocks = append(blocks, `{"type":"tool_use","id":"`+id+`","name":"n","input":{"v":"`+word()+`"}}`)
				case role == "assistant" && k == 2:
					blocks = append(blocks, `{"type":"thinking","thinking":"","signature":"`+EncodeReasoning("m/1", "", "t")+`"}`, text())
				case role == "user" && k < 2:
					id := fmt.Sprintf("id%d", r.Intn(6))
					if len(ids) > 0 && r.Intn(4) > 0 {
						id = ids[r.Intn(len(ids))]
					}
					blocks = append(blocks, `{"type":"tool_result","tool_use_id":"`+id+`","content":[`+text()+`,{"type":"image","source":{"type":"url","url":"https://`+word()+`"}}]}`)
				default:
					blocks = append(blocks, text())
				}
			}
			msgs = append(msgs, `{"role":"`+role+`","content":[`+strings.Join(blocks, ",")+`]}`)
		}
		msgs = append(msgs, `{"role":"user","content":"end"}`)
		o := Options{SystemFirstOnly: r.Intn(2) == 0, ShortCallIDs: r.Intn(4) == 0, EchoReasoning: r.Intn(4) == 0}
		in := reqMsgs("[" + strings.Join(msgs, ",") + "]")
		out := chatRun(t, in, o)
		if err := chatCheck(out.Body, o); err != nil {
			t.Fatalf("%v\n%s", err, in)
		}
		raw, _ := json.Marshal(out.Body.Messages)
		for _, w := range words {
			if c := strings.Count(string(raw), w); c != 1 {
				t.Fatalf("%q appears %d times\n%s\n%s", w, c, in, raw)
			}
		}
		again := chatRun(t, in, o)
		if b, _ := json.Marshal(again.Body.Messages); string(b) != string(raw) {
			t.Fatal("the translation is not stable")
		}
		if _, err := ToResponsesGeneric(reqParse(t, in), "m/1", o); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGenericResponses(t *testing.T) {
	enc := EncodeSignature("m/1", "ENC")
	const hist = `"messages":[{"role":"user","content":"a"},{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":"SIG"},{"type":"text","text":"b"},{"type":"tool_use","id":"c1","name":"Read","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","content":[{"type":"tool_reference","tool_name":"Late"}]}]},{"role":"system","content":"s"}]`
	in := strings.Replace(`{"model":"x","max_tokens":9,"temperature":0.5,"top_p":0.1,"tools":[{"name":"Read"},{"name":"Late","defer_loading":true}],`+hist+`}`, "SIG", enc, 1)
	run := func(body string, o Options) *dto.Translated {
		t.Helper()
		out, err := ToResponsesGeneric(reqParse(t, body), "m/1", o)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	const items = `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"b"}]},{"type":"function_call","call_id":"c1","name":"Read","arguments":"{}"},{"type":"function_call_output","call_id":"c1","output":"Tool Late is now available."}`
	const user = `{"type":"message","role":"user","content":[{"type":"input_text","text":"a"}]}`
	const tools = `[{"type":"function","name":"Read","parameters":{"type":"object","properties":{}},"strict":false},{"type":"function","name":"Late","parameters":{"type":"object","properties":{}},"strict":false}]`
	const head = `"model":"m/1","instructions":"You are served by m/1 through second-life.","tools":` + tools + `,"store":false,"stream":true,"max_output_tokens":9,"temperature":0.5,"top_p":0.1`
	reqSame(t, run(in, Options{}).Body, `{`+head+`,"input":[`+user+`,`+items+`,{"type":"message","role":"developer","content":[{"type":"input_text","text":"s"}]}]}`)
	reqSame(t, run(in, Options{Encrypted: true, SystemFirstOnly: true}).Body, `{`+head+`,"include":["reasoning.encrypted_content"],"input":[`+user+`,{"type":"reasoning","summary":[],"encrypted_content":"ENC"},`+items+
		`,{"type":"message","role":"user","content":[{"type":"input_text","text":"<system-reminder>\ns\n</system-reminder>"}]}]}`)

	for _, c := range []struct {
		fields string
		o      Options
		want   string // fields besides model, instructions, input, store and stream
	}{
		{`"max_tokens":9`, Options{MaxOutput: 4}, `"max_output_tokens":4`},
		{`"max_tokens":9`, Options{Omit: []string{"max_output_tokens", "store"}}, ``},
		{`"output_config":{"effort":"max"},"thinking":{"type":"adaptive","display":"summarized"}`, Options{Effort: true, HighEffort: true}, `"reasoning":{"effort":"high","summary":"auto"}`},
		{`"thinking":{"type":"adaptive","display":"summarized"}`, Options{Effort: true, Omit: []string{"reasoning.summary"}}, ``},
		{`"output_config":{"effort":"max"}`, Options{}, ``},
		{`"tools":[{"name":"R"}],"tool_choice":{"type":"tool","name":"R","disable_parallel_tool_use":true}`, Options{}, `"tools":[{"type":"function","name":"R","parameters":{"type":"object","properties":{}},"strict":false}],"tool_choice":{"type":"function","name":"R"},"parallel_tool_calls":false`},
		{`"tools":[{"type":"web_search_20250305","name":"web_search"}]`, Options{WebSearch: true}, `"tools":[{"type":"web_search"}],"include":["web_search_call.action.sources"]`},
		{`"output_config":{"format":{"type":"json_schema","schema":{"type":"object"}}}`, Options{}, `"text":{"format":{"type":"json_schema","name":"output","schema":{"type":"object"},"strict":false}}`},
		{`"output_config":{"format":{"type":"json_schema","schema":{"type":"object"}}}`, Options{Omit: []string{"text"}}, ``},
	} {
		raw, _ := json.Marshal(run(reqWith(c.fields), c.o).Body)
		var got map[string]any
		json.Unmarshal(raw, &got)
		for _, k := range []string{"model", "instructions", "input", "store", "stream"} {
			delete(got, k)
		}
		reqSame(t, got, "{"+c.want+"}")
	}
	if _, err := ToResponsesGeneric(reqParse(t, reqWith(`"tools":[{"type":"web_search_20250305","name":"web_search"}]`)), "m/1", Options{}); err == nil || err.Message != `tools.0: tool type "web_search_20250305" is not supported` {
		t.Error(err)
	}
	img := reqMsgs(`[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"https://x"}}]}]`)
	reqSame(t, run(img, Options{NoImages: true}).Body.Input, `[{"type":"message","role":"user","content":[{"type":"input_text","text":"`+chatNoImage+`"}]}]`)
	if _, err := ToResponsesGeneric(reqParse(t, `{"messages":[{"role":"user","content":""}]}`), "m", Options{}); err == nil {
		t.Error("no content")
	}
}

func TestOptionsWithout(t *testing.T) {
	base := Options{Omit: []string{"stop"}}
	for field, want := range map[string]Options{
		"temperature": {Omit: []string{"stop", "temperature"}}, NoMaxTokens: {Omit: []string{"stop", "max_tokens", "max_completion_tokens", "max_output_tokens"}},
		LowerMaxTokens + "8192": {MaxOutput: 8192, Omit: base.Omit}, NoSystemInside: {SystemFirstOnly: true, Omit: base.Omit},
		NeedReasoning: {EchoReasoning: true, Omit: base.Omit}, NoReasoningBack: {NoReasoning: true, Omit: base.Omit}, NeedShortIDs: {ShortCallIDs: true, Omit: base.Omit},
		NeedSignatures: {PlaceholdSigs: true, Omit: base.Omit}, NoImageParts: {NoImages: true, Omit: base.Omit}, NoFileParts: {NoFiles: true, Omit: base.Omit},
		"include": {Omit: []string{"stop", "include"}},
	} {
		got, ok := base.Without(field)
		if !ok || !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %v %+v", field, ok, got)
		}
		if again, ok := got.Without(field); ok || !reflect.DeepEqual(again, got) {
			t.Errorf("%s twice: %+v", field, again)
		}
	}
	for _, field := range []string{"", "stop", "messages", "tools", "model", "stream", "reasoning_effort", LowerEffort, NoWebSearch, LowerMaxTokens, LowerMaxTokens + "0", LowerMaxTokens + "x", "made_up"} {
		if got, ok := base.Without(field); ok || !reflect.DeepEqual(got, base) {
			t.Errorf("%s: %+v", field, got)
		}
	}
	on := Options{Effort: true, WebSearch: true, Encrypted: true, MaxOutput: 100}
	on, a := on.Without(LowerEffort)
	on, b := on.Without("reasoning_effort")
	on, c := on.Without(NoWebSearch)
	on, d := on.Without("include")
	on, e := on.Without(LowerMaxTokens + "200")
	if !a || !b || !c || !d || e || on.Effort || on.WebSearch || on.Encrypted || on.MaxOutput != 100 {
		t.Errorf("%+v", on)
	}
	// Copies do not share what they omit.
	x := Options{Omit: make([]string, 0, 8)}
	y, _ := x.Without("top_p")
	z, _ := x.Without("stop")
	if y.Omit[0] != "top_p" || z.Omit[0] != "stop" {
		t.Error(y, z)
	}
}
