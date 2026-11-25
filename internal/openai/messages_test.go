package openai

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Lookoff-AIMLAPI/second-life/internal/translate"
)

func same(t *testing.T, got []byte, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("%v: %s", err, got)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("bad expectation: %v", err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("\n got %s\nwant %s", got, want)
	}
}

func TestPrepareMessages(t *testing.T) {
	msgs := func(blocks string) string {
		return `{"model":"x","max_tokens":9,"messages":[{"role":"user","content":"a"},{"role":"assistant","content":[` + blocks + `]},{"role":"user","content":"b"}]}`
	}
	with := func(model, blocks string) string {
		return strings.Replace(msgs(blocks), `"model":"x"`, `"model":"`+model+`"`, 1)
	}
	const text = `{"type":"text","text":"t"}`
	const signed = `{"type":"thinking","thinking":"x","signature":"REAL"}`
	const bare = `{"type":"thinking","thinking":"x"}`
	const empty = `{"type":"thinking","thinking":"x","signature":""}`
	const codex = `{"type":"thinking","thinking":"","signature":"c2c1.zz"}`
	const ours = `{"type":"thinking","thinking":"","signature":"c2r1.zz"}`
	const tools = `"tools":[{"name":"A","input_schema":{}},{"name":"B","defer_loading":true},{"name":"C","defer_loading":true},{"type":"advisor_1","name":"advisor"}]`
	const ref = `{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":[{"type":"tool_reference","tool_name":"B"}]}]}`
	const sys = `{"role":"system","content":"s"}`
	const folded = `{"type":"text","text":"<system-reminder>\ns\n</system-reminder>"}`
	for _, c := range []struct {
		name, model, in string
		o               translate.Options
		want            string
		warnings        int
	}{
		{"the model", "glm", msgs(text), translate.Options{}, with("glm", text), 0},
		{"another model's thinking", "glm", msgs(codex + "," + ours + "," + signed + "," + text), translate.Options{}, with("glm", signed+","+text), 1},
		{"and nothing else", "claude-x", msgs(ours), translate.Options{}, with("claude-x", `{"type":"text","text":"[content omitted]"}`), 2},
		{"thinking without a signature", "glm", msgs(bare + "," + empty + "," + text), translate.Options{}, with("glm", bare+","+bare+","+text), 0},
		{"which Claude refuses", "claude-x", msgs(bare + "," + text), translate.Options{}, with("claude-x", text), 1},
		{"fields AIMLAPI refuses stay", "claude-x", `{"max_tokens":1,"messages":[],"fallbacks":["y"],` + tools + `}`, translate.Options{}, `{"model":"claude-x","max_tokens":1,"messages":[],"fallbacks":["y"],` + tools + `}`, 0},
		{"deferred tools", "glm", `{"messages":[],` + tools + `}`, translate.Options{}, `{"model":"glm","messages":[],"tools":[{"name":"A","input_schema":{}}]}`, 2},
		{"a loaded tool", "glm", `{"messages":[` + ref + `],` + tools + `}`, translate.Options{},
			`{"model":"glm","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":[{"type":"text","text":"Tool B is now available."}]}]}],"tools":[{"name":"A","input_schema":{}},{"name":"B"}]}`, 2},
		{"Claude reads references itself", "anthropic/claude-x", `{"messages":[` + ref + `],` + tools + `}`, translate.Options{}, `{"model":"anthropic/claude-x","messages":[` + ref + `],` + tools + `}`, 0},
		{"a tool the history calls", "glm", `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"t","name":"C","input":{"n":12345678901234567890}}]}],"tools":[{"name":"C","defer_loading":true}]}`, translate.Options{},
			`{"model":"glm","messages":[{"role":"assistant","content":[{"type":"tool_use","id":"t","name":"C","input":{"n":12345678901234567890}}]}],"tools":[{"name":"C"}]}`, 1},
		{"billing", "glm", `{"messages":[],"system":[{"type":"text","text":"x-anthropic-billing-header: 1"},{"type":"text","text":"a<b"}]}`, translate.Options{}, `{"model":"glm","messages":[],"system":[{"type":"text","text":"a<b"}]}`, 0},
		{"billing alone", "glm", `{"messages":[],"system":[{"type":"text","text":"x-anthropic-billing-header: 1"}]}`, translate.Options{}, `{"model":"glm","messages":[]}`, 0},
		{"omitted fields", "glm", `{"messages":[],"max_tokens":9,"thinking":{},"context_management":{}}`, translate.Options{Omit: []string{"thinking", "context_management", "messages", "max_tokens"}}, `{"model":"glm","messages":[],"max_tokens":9}`, 0},
		{"output limit", "glm", `{"messages":[],"max_tokens":128000}`, translate.Options{MaxOutput: 8192}, `{"model":"glm","messages":[],"max_tokens":8192}`, 1},
		{"system stays", "glm", `{"messages":[{"role":"user","content":"a"},` + sys + `]}`, translate.Options{}, `{"model":"glm","messages":[{"role":"user","content":"a"},` + sys + `]}`, 0},
		{"system folds", "glm", `{"messages":[{"role":"user","content":"a"},` + sys + `,{"role":"user","content":"b"},{"role":"assistant","content":"c"},` + sys + `]}`, translate.Options{SystemFirstOnly: true},
			`{"model":"glm","messages":[{"role":"user","content":[{"type":"text","text":"a"},` + folded + `,{"type":"text","text":"b"}]},{"role":"assistant","content":"c"},{"role":"user","content":[` + folded + `]}]}`, 0},
		{"results stay first", "glm", `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"t","name":"A","input":{}}]},` + sys + `,{"role":"user","content":[{"type":"text","text":"b"},{"type":"tool_result","tool_use_id":"t","content":"r"}]}]}`, translate.Options{SystemFirstOnly: true},
			`{"model":"glm","messages":[{"role":"assistant","content":[{"type":"tool_use","id":"t","name":"A","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"r"},` + folded + `,{"type":"text","text":"b"}]}]}`, 0},
		{"tool changes", "glm", `{"messages":[{"role":"user","content":"a"},{"role":"system","content":[{"type":"tool_addition","tool":{"type":"tool_reference","name":"B"}}]}],"tools":[{"name":"A"},{"name":"B","defer_loading":true}]}`, translate.Options{SystemFirstOnly: true},
			`{"model":"glm","messages":[{"role":"user","content":"a"}],"tools":[{"name":"A"},{"name":"B"}]}`, 1},
	} {
		body, warnings, err := PrepareMessages([]byte(c.in), c.model, c.o)
		if err != nil {
			t.Fatal(c.name, err)
		}
		t.Run(c.name, func(t *testing.T) { same(t, body, c.want) })
		if len(warnings) != c.warnings {
			t.Errorf("%s: %q", c.name, warnings)
		}
	}
	for _, in := range []string{"", "x", "[]", "null", `{"thread":{"id":"a"},"messages":[]}`} {
		if _, _, err := PrepareMessages([]byte(in), "glm", translate.Options{}); err == nil || err.Status != 400 {
			t.Errorf("%q: %v", in, err)
		}
	}
	if body, _, _ := PrepareMessages([]byte(`{"messages":[{"role":"user","content":[{"type":"tool_use","input":{"n":12345678901234567890,"s":"<&>"}}]}]}`), "glm", translate.Options{}); !strings.Contains(string(body), `{"n":12345678901234567890,"s":"<&>"}`) {
		t.Error(string(body))
	}
}
