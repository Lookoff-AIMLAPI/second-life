//go:build live

package bridge

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Lookoff-AIMLAPI/second-life/internal/codex"
)

type M = map[string]any
type L = []any

var liveB *Bridge
var liveURL string

// liveSession is a fresh uuid-shaped id: Codex keeps its cache per session.
var liveSession = fmt.Sprintf("00000000-0000-4000-8000-%012d", time.Now().UnixMilli()%1e12)

func TestMain(m *testing.M) {
	home, _ := os.MkdirTemp("", "slife")
	b, err := New(Options{Home: home, Auth: &codex.Client{Version: "test"}, Log: log.New(io.Discard, "", 0)})
	if err != nil {
		panic(err)
	}
	s := httptest.NewServer(b.handler())
	liveB, liveURL = b, s.URL
	code := m.Run()
	s.Close()
	b.auth.Close()
	os.RemoveAll(home)
	os.Exit(code)
}

func call(t *testing.T, body M) (int, M, string) {
	t.Helper()
	if _, ok := body["max_tokens"]; !ok {
		body["max_tokens"] = 2000
	}
	if _, ok := body["output_config"]; !ok {
		body["output_config"] = M{"effort": "low"}
	}
	raw, _ := json.Marshal(body)
	r, _ := http.NewRequest("POST", liveURL+"/v1/messages?beta=true", bytes.NewReader(raw))
	r.Header.Set("Authorization", "Bearer "+liveB.Token())
	r.Header.Set("X-Claude-Code-Session-Id", liveSession)
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	var m M
	_ = json.Unmarshal(out, &m)
	return resp.StatusCode, m, string(out)
}

func ok(t *testing.T, body M) M {
	t.Helper()
	st, m, raw := call(t, body)
	if st != 200 || m["type"] != "message" {
		t.Fatalf("status %d: %.600s", st, raw)
	}
	return m
}

func text(m M) string {
	s := ""
	for _, b := range m["content"].([]any) {
		if x, _ := b.(M)["text"].(string); x != "" {
			s += x
		}
	}
	return s
}

func uses(m M) (out []M) {
	for _, b := range m["content"].([]any) {
		if b.(M)["type"] == "tool_use" {
			out = append(out, b.(M))
		}
	}
	return
}

func u(c any) M      { return M{"role": "user", "content": c} }
func txt(s string) M { return M{"type": "text", "text": s} }

var weather = M{"name": "get_weather", "description": "Weather for a city", "input_schema": M{"type": "object", "properties": M{"city": M{"type": "string"}}, "required": L{"city"}}}

func pic() string {
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for i := range 64 * 64 {
		img.Set(i%64, i/64, color.RGBA{255, 0, 0, 255})
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return base64.StdEncoding.EncodeToString(b.Bytes())
}

func TestLiveSlugs(t *testing.T) {
	for _, id := range []string{"opus", "sonnet", "haiku", "fable", "claude-opus", "claude-sonnet", "claude-opus-5-5", "claude-fable-5-2[1m]", "claude-haiku-5-5"} {
		t.Run(id, func(t *testing.T) {
			m := ok(t, M{"model": id, "messages": L{u("Reply with exactly: pong")}})
			if m["model"] != id || !strings.Contains(strings.ToLower(text(m)), "pong") || m["stop_reason"] != "end_turn" {
				t.Fatal(m)
			}
			t.Log(m["usage"])
		})
	}
}

func TestLiveEffort(t *testing.T) {
	for _, id := range []string{"fable", "opus", "sonnet", "haiku"} {
		for _, e := range []string{"low", "medium", "high", "xhigh", "max"} {
			t.Run(id+"/"+e, func(t *testing.T) {
				ok(t, M{"model": id, "output_config": M{"effort": e}, "messages": L{u("2+2? digit only")}})
			})
		}
	}
}

func TestLiveThinking(t *testing.T) {
	q := L{u("Is 3599 prime? Work it out carefully, then answer yes or no.")}
	for _, c := range []struct {
		name string
		th   M
	}{{"omitted", M{"type": "adaptive", "display": "omitted"}}, {"summarized", M{"type": "adaptive", "display": "summarized"}}, {"budget", M{"type": "enabled", "budget_tokens": 1024}}, {"disabled", M{"type": "disabled"}}} {
		t.Run(c.name, func(t *testing.T) {
			m := ok(t, M{"model": "sonnet", "thinking": c.th, "output_config": M{"effort": "medium"}, "messages": q})
			for _, b := range m["content"].([]any) {
				b := b.(M)
				t.Logf("%v thinking=%d sig=%d", b["type"], len(fmt.Sprint(b["thinking"])), len(fmt.Sprint(b["signature"])))
			}
			if !strings.Contains(strings.ToLower(text(m)), "no") {
				t.Fatal(text(m))
			}
		})
	}
}

func TestLiveToolLoopReplaysReasoning(t *testing.T) {
	for _, id := range []string{"fable", "opus", "sonnet", "haiku"} {
		t.Run(id, func(t *testing.T) {
			msgs := L{u("Use the tool for Paris AND for Tokyo, both calls in one go, then tell me which is warmer.")}
			req := M{"model": id, "thinking": M{"type": "adaptive"}, "output_config": M{"effort": "medium"}, "tools": L{weather}, "messages": msgs}
			temps := M{"Paris": "11C", "Tokyo": "27C"}
			for turn := 0; turn < 4; turn++ {
				m := ok(t, req)
				us := uses(m)
				if len(us) == 0 {
					if !strings.Contains(text(m), "Tokyo") || m["stop_reason"] != "end_turn" {
						t.Fatal(m)
					}
					return
				}
				if m["stop_reason"] != "tool_use" {
					t.Fatal(m["stop_reason"])
				}
				t.Logf("turn %d calls %d blocks %d", turn, len(us), len(m["content"].([]any)))
				res := L{}
				for _, c := range us {
					res = append(res, M{"type": "tool_result", "tool_use_id": c["id"], "content": temps[c["input"].(M)["city"].(string)]})
				}
				msgs = append(msgs, M{"role": "assistant", "content": m["content"]}, u(res))
				req["messages"] = msgs
			}
			t.Fatal("no final answer")
		})
	}
}

func TestLiveModelSwitchKeepsHistory(t *testing.T) {
	a := ok(t, M{"model": "sonnet", "thinking": M{"type": "adaptive"}, "output_config": M{"effort": "medium"}, "messages": L{u("Remember the code word ZEBRA-41. Say noted.")}})
	m := ok(t, M{"model": "haiku", "messages": L{u("Remember the code word ZEBRA-41. Say noted."), M{"role": "assistant", "content": a["content"]}, u("What was the code word?")}})
	if !strings.Contains(text(m), "ZEBRA-41") {
		t.Fatal(text(m))
	}
}

func TestLiveForeignSignature(t *testing.T) {
	ok(t, M{"model": "haiku", "messages": L{u("hi"), M{"role": "assistant", "content": L{M{"type": "thinking", "thinking": "x", "signature": "EqQBCkYIBxgCKkDe"}, M{"type": "redacted_thinking", "data": "abc"}, txt("hello")}}, u("say ok")}})
}

func TestLiveToolChoice(t *testing.T) {
	second := M{"name": "other", "description": "other", "input_schema": M{"type": "object", "properties": M{}}}
	for _, c := range []struct {
		name string
		tc   M
		want string
	}{{"auto", M{"type": "auto"}, ""}, {"any", M{"type": "any"}, "*"}, {"tool", M{"type": "tool", "name": "get_weather"}, "get_weather"}, {"none", M{"type": "none"}, "-"}, {"serial", M{"type": "auto", "disable_parallel_tool_use": true}, "1"}} {
		t.Run(c.name, func(t *testing.T) {
			p := "Say hello."
			if c.name == "none" || c.name == "serial" {
				p = "Weather in Paris and Tokyo? Use the tool for both."
			}
			us := uses(ok(t, M{"model": "sonnet", "tools": L{weather, second}, "tool_choice": c.tc, "messages": L{u(p)}}))
			switch c.want {
			case "*":
				if len(us) == 0 {
					t.Fatal("no call")
				}
			case "-":
				if len(us) != 0 {
					t.Fatal("called")
				}
			case "1":
				if len(us) != 1 {
					t.Fatal(len(us))
				}
			case "get_weather":
				if len(us) == 0 || us[0]["name"] != "get_weather" {
					t.Fatal(us)
				}
			}
		})
	}
}

func TestLiveSchemas(t *testing.T) {
	for name, sc := range map[string]M{
		"noProps":     {"type": "object"},
		"draft":       {"$schema": "https://json-schema.org/draft/2020-12/schema", "type": "object", "additionalProperties": false, "properties": M{"a": M{"type": "array", "items": M{"anyOf": L{M{"type": "string"}, M{"type": "null"}}}, "minItems": 1}, "m": M{"type": "object", "additionalProperties": M{"type": "string"}, "propertyNames": M{"maxLength": 9}}, "n": M{"type": "integer", "minimum": -9007199254740991, "maximum": 9007199254740991}, "e": M{"const": "latest"}, "p": M{"type": "string", "pattern": "^[^\\0]*$"}}},
		"topAny":      {"anyOf": L{M{"type": "object", "properties": M{"a": M{"type": "string"}}}, M{"type": "object", "properties": M{"b": M{"type": "string"}}}}},
		"topAnyTyped": {"type": "object", "properties": M{"a": M{"type": "string"}}, "anyOf": L{M{"required": L{"a"}}}},
		"refs":        {"type": "object", "properties": M{"x": M{"$ref": "#/$defs/X"}}, "$defs": M{"X": M{"type": "object", "properties": M{"kids": M{"type": "array", "items": M{"$ref": "#/$defs/X"}}}}}},
		"bareAny":     {"type": "object", "properties": M{"v": M{"description": "anything"}, "w": M{"type": "array"}}},
		"types":       {"type": "object", "properties": M{"v": M{"type": L{"string", "null"}}}},
	} {
		t.Run(name, func(t *testing.T) {
			us := uses(ok(t, M{"model": "haiku", "tools": L{M{"name": "t", "description": "test tool", "input_schema": sc}}, "tool_choice": M{"type": "any"}, "messages": L{u("Call t with any valid input.")}}))
			if len(us) == 0 {
				t.Fatal("no call")
			}
			t.Log(us[0]["input"])
		})
	}
}

func TestLiveLongNameAndDeferred(t *testing.T) {
	long := "mcp__fcb2f1a4-4eeb-4bae-8620-a9a5d5541c83__data_agent_instructions_tool"
	us := uses(ok(t, M{"model": "sonnet", "tools": L{M{"name": long, "description": "reads instructions", "input_schema": M{"type": "object", "properties": M{}}}}, "tool_choice": M{"type": "tool", "name": long}, "messages": L{u("Read the instructions.")}}))
	if len(us) == 0 || us[0]["name"] != long {
		t.Fatal(us)
	}
	def := M{"name": "get_weather", "defer_loading": true, "description": "Weather", "input_schema": weather["input_schema"]}
	search := M{"name": "ToolSearch", "description": "find tools", "input_schema": M{"type": "object", "properties": M{"query": M{"type": "string"}}}}
	us = uses(ok(t, M{"model": "sonnet", "tools": L{search, def}, "messages": L{u("Weather in Paris? Find the tool, then call it."),
		M{"role": "assistant", "content": L{M{"type": "tool_use", "id": "toolu_1", "name": "ToolSearch", "input": M{"query": "weather"}}}},
		u(L{M{"type": "tool_result", "tool_use_id": "toolu_1", "content": L{M{"type": "tool_reference", "tool_name": "get_weather"}}}})}}))
	if len(us) == 0 || us[0]["name"] != "get_weather" {
		t.Fatal(us)
	}
}

func TestLiveInputs(t *testing.T) {
	img := M{"type": "image", "source": M{"type": "base64", "media_type": "image/png", "data": pic()}}
	for _, c := range []struct {
		name string
		msgs L
		want string
	}{
		{"image", L{u(L{img, txt("Main colour? One word.")})}, "red"},
		{"imageInToolResult", L{u("Take a screenshot and name the colour."), M{"role": "assistant", "content": L{M{"type": "tool_use", "id": "toolu_9", "name": "get_weather", "input": M{}}}}, u(L{M{"type": "tool_result", "tool_use_id": "toolu_9", "content": L{txt("shot:"), img}}})}, "red"},
		{"toolError", L{u("Run it, then report the error code only."), M{"role": "assistant", "content": L{M{"type": "tool_use", "id": "toolu_8", "name": "get_weather", "input": M{}}}}, u(L{M{"type": "tool_result", "tool_use_id": "toolu_8", "is_error": true, "content": "E_4417 failed"}})}, "4417"},
		{"midSystem", L{u(L{txt("hello")}), M{"role": "system", "content": L{txt("From now on answer only with the word BANANA.")}}, M{"role": "assistant", "content": "BANANA"}, u("How are you?")}, "banana"},
		{"textDoc", L{u(L{M{"type": "document", "title": "a.txt", "source": M{"type": "text", "media_type": "text/plain", "data": "The vault code is 8812."}}, txt("Vault code?")})}, "8812"},
		{"emptyResult", L{u("run"), M{"role": "assistant", "content": L{M{"type": "tool_use", "id": "toolu_7", "name": "get_weather", "input": M{}}}}, u(L{M{"type": "tool_result", "tool_use_id": "toolu_7"}, txt("Say done.")})}, "done"},
		{"orphanUse", L{u("run"), M{"role": "assistant", "content": L{M{"type": "tool_use", "id": "toolu_6", "name": "get_weather", "input": M{}}}}, u("Forget it. Say skipped.")}, "skipped"},
		{"unicode", L{u("Повтори дословно: Жёлтый 🦊 «кит»")}, "🦊"},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := ok(t, M{"model": "sonnet", "system": L{txt("x-anthropic-billing-header: z"), txt("Be terse.")}, "tools": L{weather}, "messages": c.msgs})
			if !strings.Contains(strings.ToLower(text(m)), c.want) {
				t.Fatalf("%v", m["content"])
			}
		})
	}
}

func TestLivePDF(t *testing.T) {
	pdf := "%PDF-1.4\n1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj\n2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj\n3 0 obj<</Type/Page/Parent 2 0 R/MediaBox[0 0 300 200]/Contents 4 0 R/Resources<</Font<</F1 5 0 R>>>>>>endobj\n4 0 obj<</Length 48>>stream\nBT /F1 24 Tf 20 100 Td (Code is 70932 now) Tj ET\nendstream endobj\n5 0 obj<</Type/Font/Subtype/Type1/BaseFont/Helvetica>>endobj\ntrailer<</Root 1 0 R>>\n%%EOF"
	m := ok(t, M{"model": "sonnet", "messages": L{u(L{M{"type": "document", "source": M{"type": "base64", "media_type": "application/pdf", "data": base64.StdEncoding.EncodeToString([]byte(pdf))}}, txt("Which code is in the file?")})}})
	if !strings.Contains(text(m), "70932") {
		t.Fatal(text(m))
	}
}

func TestLiveFormatAndSampling(t *testing.T) {
	m := ok(t, M{"model": "haiku", "temperature": 0.2, "top_p": 0.9, "top_k": 5, "stop_sequences": L{"END"}, "metadata": M{"user_id": "x"}, "output_config": M{"effort": "low", "format": M{"type": "json_schema", "schema": M{"type": "object", "properties": M{"n": M{"type": "integer"}}, "required": L{"n"}, "additionalProperties": false}}}, "messages": L{u("Give n = 7")}})
	var v struct{ N int }
	if json.Unmarshal([]byte(text(m)), &v) != nil || v.N != 7 {
		t.Fatal(text(m))
	}
}

func TestLiveCache(t *testing.T) {
	sys := strings.Repeat("Rule: be brief and precise about everything you say. ", 400)
	for i := range 2 {
		m := ok(t, M{"model": "sonnet", "system": sys, "messages": L{u("Say ok")}})
		t.Log(i, m["usage"])
	}
}

func TestLiveRejects(t *testing.T) {
	for name, b := range map[string]M{
		"unknownModel": {"model": "gemini", "messages": L{u("x")}},
		"serverTool":   {"model": "haiku", "tools": L{M{"type": "computer_20250124", "name": "computer"}}, "messages": L{u("x")}},
		"unknownBlock": {"model": "haiku", "messages": L{u(L{M{"type": "hologram"}})}},
		"badUpstream":  {"model": "gpt-does-not-exist", "messages": L{u("x")}},
	} {
		t.Run(name, func(t *testing.T) {
			st, m, raw := call(t, b)
			if st < 400 || m["type"] != "error" {
				t.Fatal(st, raw)
			}
			t.Log(st, raw)
		})
	}
}

// TestLiveCaptured replays a request recorded from Claude Code; SLIFE_CAPTURE names the file.
func TestLiveCaptured(t *testing.T) {
	p := os.Getenv("SLIFE_CAPTURE")
	if p == "" {
		t.Skip("no capture")
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var f struct{ Body M }
	_ = json.Unmarshal(raw, &f)
	st, _, out := call(t, f.Body)
	if st != 200 || !strings.Contains(out, "event: message_stop") {
		t.Fatalf("%d %.800s", st, out)
	}
}

func TestLiveWebSearch(t *testing.T) {
	q := L{u("Search the web: latest stable Go release? One sentence.")}
	tool := M{"type": "web_search_20250305", "name": "web_search", "max_uses": 3, "allowed_domains": L{"go.dev"}}
	m := ok(t, M{"model": "sonnet", "tools": L{tool}, "tool_choice": M{"type": "tool", "name": "web_search"}, "messages": q})
	kinds := ""
	for _, b := range m["content"].([]any) {
		kinds += b.(M)["type"].(string) + " "
	}
	t.Log(kinds, text(m))
	if !strings.Contains(kinds, "server_tool_use web_search_tool_result") || !strings.Contains(kinds, "text") {
		t.Fatal(kinds)
	}
	ok(t, M{"model": "sonnet", "tools": L{tool}, "messages": L{q[0], M{"role": "assistant", "content": m["content"]}, u("Thanks. Say ok.")}})
	blocked := M{"type": "web_search_20250305", "name": "web_search", "blocked_domains": L{"example.com"}}
	ok(t, M{"model": "haiku", "tools": L{blocked}, "messages": L{u("Say ok without searching.")}})
}

func TestLiveNoThinkingUnlessAsked(t *testing.T) {
	for _, th := range []M{nil, {"type": "disabled"}} {
		b := M{"model": "sonnet", "output_config": M{"effort": "medium"}, "messages": L{u("Is 3599 prime? yes or no")}}
		if th != nil {
			b["thinking"] = th
		}
		m := ok(t, b)
		if m["content"].([]any)[0].(M)["type"] != "text" {
			t.Fatal(m["content"])
		}
	}
}

func TestLiveHistoryRepair(t *testing.T) {
	use := func(id string) M { return M{"type": "tool_use", "id": id, "name": "get_weather", "input": M{}} }
	res := func(id string) M { return M{"type": "tool_result", "tool_use_id": id, "content": "x1"} }
	for name, msgs := range map[string]L{
		"halfAnswered": {u("run"), M{"role": "assistant", "content": L{txt("Calling."), use("a1"), use("a2")}}, u(L{res("a2"), txt("Say ok.")})},
		"looseResult":  {u(L{res("gone"), txt("Say ok.")})},
		"twice":        {u("run"), M{"role": "assistant", "content": L{use("b1")}}, u(L{res("b1"), res("b1"), txt("Say ok.")})},
		"phases":       {u("run"), M{"role": "assistant", "content": L{txt("Checking."), use("c1")}}, u(L{res("c1")}), M{"role": "assistant", "content": L{txt("All good.")}}, u("Say ok.")},
	} {
		t.Run(name, func(t *testing.T) { ok(t, M{"model": "haiku", "tools": L{weather}, "messages": msgs}) })
	}
}

func TestLiveToolChanges(t *testing.T) {
	def := M{"type": "tool_definition", "definition": M{"name": "lookup_code", "description": "Returns the door code", "input_schema": M{"type": "object", "properties": M{}}}}
	us := uses(ok(t, M{"model": "sonnet", "tools": L{weather}, "messages": L{u("What tools do you have?"), M{"role": "assistant", "content": "Only weather."},
		u("Call the new tool."), M{"role": "system", "content": L{M{"type": "tool_addition", "tool": def}}}}}))
	if len(us) == 0 || us[0]["name"] != "lookup_code" {
		t.Fatal(us)
	}
	m := ok(t, M{"model": "sonnet", "tools": L{weather}, "messages": L{u("Weather in Paris via the tool?"), M{"role": "system", "content": L{M{"type": "tool_removal", "tool": M{"type": "tool_reference", "name": "get_weather"}}}}}})
	if len(uses(m)) != 0 {
		t.Fatal("called a removed tool")
	}
}

func TestLiveLongCallID(t *testing.T) {
	id := "toolu_" + strings.Repeat("x", 100)
	m := ok(t, M{"model": "haiku", "tools": L{weather}, "messages": L{u("run"), M{"role": "assistant", "content": L{M{"type": "tool_use", "id": id, "name": "get_weather", "input": M{}}}}, u(L{M{"type": "tool_result", "tool_use_id": id, "content": "code 6120"}, txt("Which code?")})}})
	if !strings.Contains(text(m), "6120") {
		t.Fatal(text(m))
	}
}

func TestLiveServerHistory(t *testing.T) {
	ok(t, M{"model": "haiku", "messages": L{u("hi"), M{"role": "assistant", "content": L{txt("Looking."), M{"type": "server_tool_use", "id": "srvtoolu_1", "name": "advisor", "input": M{}}, M{"type": "advisor_tool_result", "tool_use_id": "srvtoolu_1", "content": M{"type": "advisor_result", "text": "be brief"}}, txt("Done.")}}, u("Say ok.")}})
}

func TestLiveDocumentURL(t *testing.T) {
	st, _, raw := call(t, M{"model": "sonnet", "messages": L{u(L{M{"type": "document", "source": M{"type": "url", "url": "https://www.w3.org/WAI/ER/tests/xhtml/testfiles/resources/pdf/dummy.pdf"}}, txt("First words of the file?")})}})
	t.Logf("%d %.300s", st, raw)
	if st != 200 {
		t.Fatal(st)
	}
}

func TestLiveBadImage(t *testing.T) {
	st, _, raw := call(t, M{"model": "haiku", "messages": L{u(L{M{"type": "image", "source": M{"type": "base64", "media_type": "image/png", "data": "AAAA"}}, txt("?")})}})
	t.Logf("%d %.300s", st, raw)
	if st != 400 || !strings.Contains(raw, "Could not process image") {
		t.Fatal(st)
	}
}

func TestLiveBadReasoning(t *testing.T) {
	sig := "c2c1." + base64.RawURLEncoding.EncodeToString([]byte(`{"m":"gpt-5.6-luna","e":"gAAAAABnotreal"}`))
	st, _, raw := call(t, M{"model": "haiku", "thinking": M{"type": "adaptive"}, "messages": L{u("hi"), M{"role": "assistant", "content": L{M{"type": "thinking", "thinking": "", "signature": sig}, txt("hello")}}, u("say ok")}})
	t.Logf("%d %.300s", st, raw)
	if st != 400 || !strings.Contains(raw, "`signature` in `thinking` block") {
		t.Fatal(st)
	}
}

func TestLiveStopSequences(t *testing.T) {
	m := ok(t, M{"model": "haiku", "stop_sequences": L{"</verdict>"}, "messages": L{u("Reply with exactly: <verdict>yes</verdict> and then a long poem.")}})
	if m["stop_reason"] != "stop_sequence" || m["stop_sequence"] != "</verdict>" || text(m) != "<verdict>yes" {
		t.Fatal(m)
	}
}

// TestLiveLoadedToolKeepsCache loads a tool in the second request and looks for the cached prefix.
func TestLiveLoadedToolKeepsCache(t *testing.T) {
	sys := fmt.Sprintf("Session %p. ", t) + strings.Repeat("Rule: be brief and precise about everything you say. ", 600)
	search := M{"name": "ToolSearch", "description": "Find tools", "input_schema": M{"type": "object", "properties": M{"query": M{"type": "string"}}}}
	def := M{"name": "get_weather", "defer_loading": true, "description": "Weather", "input_schema": weather["input_schema"]}
	q := u("Weather in Paris? Find the tool first.")
	base := M{"model": "sonnet", "system": sys, "tools": L{search, def}, "messages": L{q}}
	for range 2 {
		ok(t, base)
		time.Sleep(2 * time.Second)
	}
	m := ok(t, M{"model": "sonnet", "system": sys, "tools": L{search, def}, "messages": L{q,
		M{"role": "assistant", "content": L{M{"type": "tool_use", "id": "toolu_1", "name": "ToolSearch", "input": M{"query": "weather"}}}},
		u(L{M{"type": "tool_result", "tool_use_id": "toolu_1", "content": L{M{"type": "tool_reference", "tool_name": "get_weather"}}}})}})
	t.Log(m["usage"])
	if us := uses(m); len(us) == 0 || us[0]["name"] != "get_weather" {
		t.Fatal(m["content"])
	}
	if m["usage"].(M)["cache_read_input_tokens"].(float64) < 3000 {
		t.Fatal("cache lost")
	}
}
