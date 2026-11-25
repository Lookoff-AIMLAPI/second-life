package aimlapi

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const claude, glm = "anthropic/claude-opus-5.5", "zhipu/glm-5.3"

func prepared(t *testing.T, raw, model string) (string, []string) {
	t.Helper()
	out, warnings, err := Prepare([]byte(raw), model)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if !json.Valid(out) {
		t.Fatalf("not JSON: %s", out)
	}
	return string(out), warnings
}

func TestNative(t *testing.T) {
	for id, want := range map[string]bool{
		"anthropic/claude-opus-5.5": true, "claude-haiku-5-5": true, "Anthropic/Claude-Opus-5": true,
		"anthropic/claude-opus-latest": false, "anthropic/deepseek/deepseek-v4": false, "zhipu/glm-5.3": false,
		"aimlapi/dynamic-router": false, "anthropic/claude/x": false, "": false,
	} {
		if Native(id) != want {
			t.Errorf("Native(%q) = %v", id, !want)
		}
	}
}

// What the endpoint accepts keeps its bytes, for both kinds of model.
func TestPrepareKeepsBytes(t *testing.T) {
	raw := `{"model":"MODEL","max_tokens":128000,"stream":true,"future_field":{"a":[1, 2]},` +
		`"thinking":{"type":"adaptive","display":"omitted"},"context_management":{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]},` +
		`"system":[{"type":"text","text":"a\u0026<b>\ud83d\ude00"},{"type":"text","text":"s","cache_control":{"type":"ephemeral","ttl":"1h"}}],` +
		`"tools":[{"name":"A","description":"d","input_schema":{"type":"object","properties":{"n":{"default":1e400}}},"defer_loading":true},{"type":"web_search_20250305","name":"web_search"}],` +
		`"messages":[{"role":"user","content":"hi \"q\" \\"},` +
		`{"role":"assistant","content":[{"type":"thinking","thinking":"t","signature":"EqQB"},{"type":"redacted_thinking","data":"x"},` +
		`{"type":"tool_use","id":"t1","name":"S","input":{"z":12345678901234567890123,"a":1.10,"z2":1E+2}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"tool_reference","tool_name":"A"}]}]},` +
		`{"role":"system","content":[{"type":"text","text":"r","cache_control":{"type":"ephemeral"}}],"output_config":{"effort":"high"}},` +
		`{"role":"system","content":[]},{"role":"assistant","content":[{"type":"compaction","content":"c"}]}]}`
	for _, model := range []string{claude, glm} {
		got, warnings := prepared(t, raw, model)
		if want := strings.Replace(raw, "MODEL", model, 1); got != want || warnings != nil {
			t.Errorf("%s: %v\n got %s\nwant %s", model, warnings, got, want)
		}
	}
}

func TestPrepareModel(t *testing.T) {
	for raw, want := range map[string]string{
		`{"messages":[]}`:                  `{"messages":[],"model":"m/x"}`,
		` { "model" : "a" , "n" : 1.0 } `:  `{"model":"m/x","n":1.0}`,
		`{"\u006dodel":"a","model":"b"}`:   `{"\u006dodel":"m/x","model":"m/x"}`,
		`{"a":{"model":"keep"},"model":7}`: `{"a":{"model":"keep"},"model":"m/x"}`,
	} {
		if got, _ := prepared(t, raw, "m/x"); got != want {
			t.Errorf("%s: got %s", raw, got)
		}
	}
	for _, raw := range []string{"", "[]", "{", `"x"`, "null", `{"a":1}{}`} {
		if _, _, err := Prepare([]byte(raw), "m"); err == nil || err.Status != 400 || err.Type != "invalid_request_error" {
			t.Errorf("%q: %v", raw, err)
		}
	}
}

func TestPrepareHistory(t *testing.T) {
	msg := func(blocks string) string {
		return `{"model":"m","messages":[{"role":"assistant","content":[` + blocks + `]}]}`
	}
	for _, c := range []struct {
		name, in, want string
		warn           []string
	}{
		{"codex thinking", `{"type":"thinking","thinking":"a","signature":"c2c1.abc"},{"type":"text", "text":"b"}`, `{"type":"text", "text":"b"}`, []string{warnCodexThinking}},
		{"only codex thinking", `{"type":"thinking","thinking":"a","signature":"c2c1.abc"}`, omitted, []string{warnCodexThinking, warnEmptied}},
		{"no signature", `{"type":"thinking","thinking":"a","signature":""},{"type":"thinking","thinking":"b"},{"type":"text","text":"b"}`, `{"type":"text","text":"b"}`, []string{warnUnsigned}},
		{"the gateway's own thinking", `{"type":"thinking","thinking":"a","signature":"aiml1.abc"}`, `{"type":"thinking","thinking":"a","signature":"aiml1.abc"}`, nil},
		{"codex search", `{"type":"server_tool_use","id":"s1","name":"web_search","input":{"query":"q <1>"}},` +
			`{"type":"web_search_tool_result","tool_use_id":"s1","content":[{"type":"web_search_result","url":"https://a/b","title":"a/b","encrypted_content":"","page_age":null}],"cache_control":{"type":"ephemeral"}},{"type":"text","text":"x"}`,
			`{"type":"text","text":"[web search: q <1>]\na/b https://a/b","cache_control":{"type":"ephemeral"}},{"type":"text","text":"x"}`, []string{warnCodexSearch}},
		{"real search", `{"type":"server_tool_use","id":"s1","name":"web_search","input":{}},{"type":"web_search_tool_result","tool_use_id":"s1","content":[{"type":"web_search_result","url":"u","title":"t","encrypted_content":"E","page_age":null}]}`,
			`{"type":"server_tool_use","id":"s1","name":"web_search","input":{}},{"type":"web_search_tool_result","tool_use_id":"s1","content":[{"type":"web_search_result","url":"u","title":"t","encrypted_content":"E"}]}`, []string{warnNull}},
		{"nulls", `{"type":"document","title":null,"context":null,"cache_control":null,"source":{"type":"text","data":null}},{"type":"tool_use","input":{"cache_control":null,"title":null}},{"type":"text","text":"a","citations":null}`,
			`{"type":"document","source":{"type":"text","data":null}},{"type":"tool_use","input":{"cache_control":null,"title":null}},{"type":"text","text":"a"}`, []string{warnNull}},
		{"tool result", `{"type":"tool_result","tool_use_id":"a","is_error":null,"content":[{"type":"text","text":"x","cache_control":null}]}`, `{"type":"tool_result","tool_use_id":"a","content":[{"type":"text","text":"x"}]}`, []string{warnNull}},
		{"fetch", `{"type":"web_fetch_tool_result","content":{"type":"web_fetch_result","retrieved_at":null,"url":"u"}}`, `{"type":"web_fetch_tool_result","content":{"type":"web_fetch_result","url":"u"}}`, []string{warnNull}},
		{"odd shapes", `1,null,"x",[],{},{"type":7}`, `1,null,"x",[],{},{"type":7}`, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, warnings := prepared(t, msg(c.in), "m")
			if got != msg(c.want) || !reflect.DeepEqual(warnings, c.warn) {
				t.Errorf("%q\n got %s\nwant %s", warnings, got, msg(c.want))
			}
		})
	}
}

func TestPrepareTopLevel(t *testing.T) {
	for _, c := range []struct {
		name, in, model, want string
		warn                  []string
	}{
		{"fallbacks", `{"model":"m","fallbacks":[{"model":"x"}],"a":1}`, claude, `{"model":"` + claude + `","a":1}`, []string{warnFallbacks}},
		{"nulls", `{"model":"m","fallbacks":null,"thread":null,"container":null}`, claude, `{"model":"` + claude + `"}`, []string{warnNull}},
		{"native thread is the gateway's to refuse", `{"model":"m","thread":{"id":"x"},"container":"c"}`, claude, `{"model":"` + claude + `","thread":{"id":"x"},"container":"c"}`, nil},
		{"advisor", `{"model":"m","tools":[{"type":"advisor_20260301","name":"advisor"},{"name":"A","input_schema":{"type":"object"}, "type":null}]}`, claude, `{"model":"` + claude + `","tools":[{"name":"A","input_schema":{"type":"object"}}]}`, []string{warnAdvisor, warnNull}},
		{"format", `{"model":"m","output_config":{"format":{"type":"json_schema"}}}`, glm, `{"model":"` + glm + `","output_config":{"format":{"type":"json_schema"}}}`, []string{warnFormat}},
		{"native format", `{"model":"m","output_config":{"format":{"type":"json_schema"}}}`, claude, `{"model":"` + claude + `","output_config":{"format":{"type":"json_schema"}}}`, nil},
		{"system", `{"model":"m","system":[{"type":"text","text":"a","cache_control":null}]}`, claude, `{"model":"` + claude + `","system":[{"type":"text","text":"a"}]}`, []string{warnNull}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, warnings := prepared(t, c.in, c.model)
			if got != c.want || !reflect.DeepEqual(warnings, c.warn) {
				t.Errorf("%q\n got %s\nwant %s", warnings, got, c.want)
			}
		})
	}
	if _, _, err := Prepare([]byte(`{"thread":{"id":"x"}}`), glm); err == nil || !strings.HasPrefix(err.Message, "thread:") {
		t.Errorf("thread on a translated model: %v", err)
	}
}

func TestPrepareToolChanges(t *testing.T) {
	ref := func(kind, name string) string {
		return `{"role":"system","content":[{"type":"` + kind + `","tool":{"type":"tool_reference","name":"` + name + `"}}]}`
	}
	def := `{"role":"system","content":[{"type":"text","text":"new"},{"type":"tool_addition","tool":{"type":"tool_definition","definition":{"name":"D","input_schema":{"type":"object","x":1.0}}}}]}`
	tools := `{"name":"A","defer_loading":true,"n":1.0},{"name":"B"},{"name":"C","defer_loading":true}`
	req := func(tools, msgs string) string {
		return `{"model":"m","tools":[` + tools + `],"messages":[` + msgs + `]}`
	}
	run := func(msgs string) (string, string) {
		got, warnings := prepared(t, req(tools, msgs), glm)
		if len(warnings) != 1 || warnings[0] != warnToolChanges {
			t.Errorf("warnings %q", warnings)
		}
		a, b, _ := strings.Cut(got, `],"messages":[`)
		return strings.TrimPrefix(a, `{"model":"`+glm+`","tools":[`), strings.TrimSuffix(b, "]}")
	}
	empty := `{"role":"system","content":[]}`
	for _, c := range []struct{ name, msgs, tools, left string }{
		{"addition", ref("tool_addition", "A"), `{"name":"A","n":1.0},{"name":"B"},{"name":"C","defer_loading":true}`, empty},
		{"removal", ref("tool_removal", "B"), `{"name":"A","defer_loading":true,"n":1.0},{"name":"C","defer_loading":true}`, empty},
		{"removed then added", ref("tool_removal", "B") + "," + ref("tool_addition", "B"), tools, empty + "," + empty},
		{"added then removed", ref("tool_addition", "A") + "," + ref("tool_removal", "A"), `{"name":"B"},{"name":"C","defer_loading":true}`, empty + "," + empty},
		{"definition", def, tools + `,{"name":"D","input_schema":{"type":"object","x":1.0}}`, `{"role":"system","content":[{"type":"text","text":"new"}]}`},
	} {
		if gotTools, left := run(c.msgs); gotTools != c.tools || left != c.left {
			t.Errorf("%s:\n got %s | %s\nwant %s | %s", c.name, gotTools, left, c.tools, c.left)
		}
	}
	if got, _ := prepared(t, `{"model":"m","messages":[`+def+`]}`, glm); !strings.HasSuffix(got, `,"tools":[{"name":"D","input_schema":{"type":"object","x":1.0}}]}`) {
		t.Errorf("definition without tools: %s", got)
	}
	bad := `{"model":"m","messages":[{"role":"system","content":[{"type":"tool_addition","tool":{"type":"mcp_toolset_reference","server_name":"s"}}]}]}`
	if got, w := prepared(t, bad, glm); strings.Contains(got, "mcp_toolset") || len(w) != 1 || w[0] != warnToolChange {
		t.Errorf("toolset: %s %q", got, w)
	}
	// Anthropic reads them itself.
	if got, w := prepared(t, req(tools, def), claude); got != strings.Replace(req(tools, def), `"m"`, `"`+claude+`"`, 1) || w != nil {
		t.Errorf("native: %s %q", got, w)
	}
}

func TestCapMaxTokens(t *testing.T) {
	for _, c := range []struct {
		in, want string
		limit    int
		warn     bool
	}{
		{`{"max_tokens":128000,"a":1.0}`, `{"max_tokens":64000,"a":1.0}`, 64000, true},
		{`{"max_tokens":128000,"thinking":{"type":"enabled","budget_tokens":100000}}`, `{"max_tokens":64000,"thinking":{"type":"enabled","budget_tokens":63999}}`, 64000, true},
		{`{"max_tokens":64000}`, `{"max_tokens":64000}`, 64000, false},
		{`{"max_tokens":128000}`, `{"max_tokens":128000}`, 0, false},
		{`{"max_tokens":"9999999"}`, `{"max_tokens":"9999999"}`, 10, false},
	} {
		got, w := CapMaxTokens([]byte(c.in), c.limit)
		if string(got) != c.want || (w != "") != c.warn {
			t.Errorf("%s: got %s %q", c.in, got, w)
		}
	}
}

// SLIFE_CAPTURE points at captured requests; they are private, so only sizes and verdicts are printed.
func TestPrepareCaptured(t *testing.T) {
	dir := os.Getenv("SLIFE_CAPTURE")
	files, _ := filepath.Glob(filepath.Join(dir, "req-*.json"))
	if dir == "" || len(files) == 0 {
		t.Skip("SLIFE_CAPTURE is not set")
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var cap struct {
			Body json.RawMessage `json:"body"`
		}
		if json.Unmarshal(raw, &cap) != nil {
			t.Fatalf("%s: not a capture", filepath.Base(f))
		}
		body := []byte(cap.Body)
		var s string
		if json.Unmarshal(body, &s) == nil {
			body = []byte(s)
		}
		var small bytes.Buffer
		if json.Compact(&small, body) != nil || small.Len() == 0 || small.Bytes()[0] != '{' {
			continue
		}
		var in map[string]json.RawMessage
		_ = json.Unmarshal(body, &in)
		for _, model := range []string{claude, glm} {
			out, warnings, e := Prepare(small.Bytes(), model)
			if e != nil {
				t.Fatalf("%s: %s", filepath.Base(f), e.Type)
			}
			var got map[string]json.RawMessage
			if json.Unmarshal(out, &got) != nil {
				t.Fatalf("%s: not JSON", filepath.Base(f))
			}
			changed := 0
			var want map[string]json.RawMessage
			_ = json.Unmarshal(small.Bytes(), &want)
			for k := range want {
				if k != "model" && !bytes.Equal(want[k], got[k]) {
					changed++
				}
			}
			t.Logf("%s %s: %d bytes, %d fields changed, warnings %q", filepath.Base(f), model, len(out), changed, warnings)
			if changed > 0 && len(warnings) == 0 {
				t.Errorf("%s: changed without a warning", filepath.Base(f))
			}
		}
	}
}

func FuzzPrepare(f *testing.F) {
	for _, s := range []string{`{}`, `{"messages":[{"role":"user","content":[{"type":"thinking","signature":"c2c1.x"}]}]}`, `{"tools":[null,{"type":"advisor_1"}],"a":"\\\""}`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		for _, m := range []string{claude, glm} {
			out, _, err := Prepare(raw, m)
			if err == nil && !json.Valid(out) {
				t.Fatalf("%q -> %q", raw, out)
			}
			if err == nil {
				capped, _ := CapMaxTokens(out, 10)
				if !json.Valid(capped) {
					t.Fatalf("%q -> %q", raw, capped)
				}
			}
		}
	})
}
