package translate

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"

	"github.com/Lookoff-AIMLAPI/second-life/internal/dto"
)

func stJSON(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("bad fixture %s: %v", s, err)
	}
	return m
}

// stClient replays events the way an Anthropic SDK does and fails on any protocol violation.
type stClient struct {
	t        *testing.T
	started  bool
	stopped  bool
	errored  bool
	open     bool
	blocks   []*stSeen
	stop     string
	usage    map[string]any
	message  map[string]any
	deltaErr string
}

type stSeen struct{ kind, text, sig, id, name string }

func (b stSeen) String() string {
	return strings.Join([]string{b.kind, b.id, b.name, b.text, b.sig}, "|")
}

func (c *stClient) feed(evs []dto.Event) {
	c.t.Helper()
	for _, e := range evs {
		// Through JSON, so the test sees what goes over the wire.
		raw, err := json.Marshal(e.Data)
		if err != nil {
			c.t.Fatal(err)
		}
		d := stJSON(c.t, string(raw))
		if d["type"] != e.Name {
			c.t.Fatalf("event %s carries type %v", e.Name, d["type"])
		}
		if c.stopped || c.errored {
			c.t.Fatalf("%s after the end", e.Name)
		}
		if e.Name != "message_start" && e.Name != "error" && !c.started {
			c.t.Fatalf("%s before message_start", e.Name)
		}
		switch e.Name {
		case "message_start":
			if c.started {
				c.t.Fatal("second message_start")
			}
			c.started = true
			c.message = d["message"].(map[string]any)
		case "content_block_start":
			if c.open || d["index"] != float64(len(c.blocks)) {
				c.t.Fatalf("block_start: open %v, index %v, want %d", c.open, d["index"], len(c.blocks))
			}
			c.open = true
			cb := d["content_block"].(map[string]any)
			b := &stSeen{kind: cb["type"].(string)}
			want := map[string]any{"type": b.kind}
			switch b.kind {
			case "text":
				want["text"] = ""
			case "thinking":
				want["thinking"], want["signature"] = "", ""
			case "tool_use":
				b.id, b.name = cb["id"].(string), cb["name"].(string)
				want["id"], want["name"], want["input"] = b.id, b.name, map[string]any{}
			case "server_tool_use":
				b.id, b.name = cb["id"].(string), "web_search"
				want["id"], want["name"], want["input"] = b.id, b.name, map[string]any{}
			case "web_search_tool_result":
				prev := &stSeen{}
				if len(c.blocks) > 0 {
					prev = c.blocks[len(c.blocks)-1]
				}
				if prev.kind != "server_tool_use" || prev.text == "" {
					c.t.Fatalf("search result after %q", prev.kind)
				}
				b.id = prev.id
				pages, ok := cb["content"].([]any)
				if !ok {
					c.t.Fatalf("search content %v", cb["content"])
				}
				raw, _ := json.Marshal(pages)
				b.text = string(raw)
				want["tool_use_id"], want["content"] = b.id, pages
			}
			if n := len(c.blocks); n > 0 && c.blocks[n-1].kind == "server_tool_use" && b.kind != "web_search_tool_result" {
				c.t.Fatalf("%s between a search and its result", b.kind)
			}
			if !reflect.DeepEqual(cb, want) {
				c.t.Fatalf("content_block %v, want %v", cb, want)
			}
			c.blocks = append(c.blocks, b)
		case "content_block_delta":
			c.atOpen(d)
			b, dl := c.blocks[len(c.blocks)-1], d["delta"].(map[string]any)
			field := map[string][2]string{
				"text_delta": {"text", "text"}, "thinking_delta": {"thinking", "thinking"},
				"input_json_delta": {"tool_use", "partial_json"}, "signature_delta": {"thinking", "signature"},
			}[dl["type"].(string)]
			v := dl[field[1]].(string)
			if field[0] != strings.TrimPrefix(b.kind, "server_") || v == "" || b.sig != "" {
				c.t.Fatalf("%v in a %s block", dl, b.kind)
			}
			if field[1] == "signature" {
				b.sig = v
			} else {
				b.text += v
			}
		case "content_block_stop":
			c.atOpen(d)
			c.open = false
		case "message_delta":
			if c.open || c.stop != "" {
				c.t.Fatal("message_delta out of place")
			}
			c.stop = d["delta"].(map[string]any)["stop_reason"].(string)
			if v, ok := d["delta"].(map[string]any)["stop_sequence"]; !ok || v != nil {
				c.t.Fatal("stop_sequence must be null")
			}
			c.usage = d["usage"].(map[string]any)
		case "message_stop":
			if c.stop == "" {
				c.t.Fatal("message_stop before message_delta")
			}
			c.stopped = true
		case "error":
			c.errored = true
			c.deltaErr = d["error"].(map[string]any)["type"].(string)
		default:
			c.t.Fatalf("unexpected event %s", e.Name)
		}
	}
}

func (c *stClient) atOpen(d map[string]any) {
	c.t.Helper()
	if !c.open || d["index"] != float64(len(c.blocks)-1) {
		c.t.Fatalf("event %v while open=%v, last index %d", d, c.open, len(c.blocks)-1)
	}
}

func (c *stClient) strings() []string {
	out := []string{}
	for _, b := range c.blocks {
		out = append(out, b.String())
	}
	return out
}

var stTestCtx = dto.StreamContext{
	PublicModel: "claude-opus", UpstreamModel: "gpt-x",
	ToolNames: map[string]string{"short": "mcp__a__very_long"}, ShowThinking: true,
}

func stRun(t *testing.T, ctx dto.StreamContext, evs ...string) (*Stream, *stClient) {
	t.Helper()
	s, c := NewStream(ctx), &stClient{t: t}
	for _, e := range evs {
		c.feed(s.Push(stJSON(t, e)))
	}
	c.feed(s.Close())
	if !s.Ended() {
		t.Fatal("not ended")
	}
	if got := s.Close(); got != nil {
		t.Fatalf("Close is not idempotent: %v", got)
	}
	if got := s.Push(stJSON(t, stDone)); got != nil {
		t.Fatalf("events after the end: %v", got)
	}
	return s, c
}

// stFromMessage renders Message() like stClient.strings renders the stream.
func stFromMessage(t *testing.T, m *dto.MessageResponse) []string {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		Content []struct {
			Type, Text, Thinking, Signature, ID, Name string
			Input, Content                            json.RawMessage
			ToolUseID                                 string `json:"tool_use_id"`
		}
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	if d.Content == nil {
		t.Fatal("content must be [], not null")
	}
	out := []string{}
	for _, b := range d.Content {
		out = append(out, stSeen{b.Type, b.Text + b.Thinking + string(b.Input) + string(b.Content), b.Signature, b.ID + b.ToolUseID, b.Name}.String())
	}
	return out
}

const (
	stCreated = `{"type":"response.created","response":{"id":"resp_1","usage":null}}`
	stDone    = `{"type":"response.completed","response":{"id":"resp_1","output":[],"usage":{"input_tokens":100,"input_tokens_details":{"cached_tokens":40},"output_tokens":7}}}`
)

func stAdded(i int, item string) string {
	return fmt.Sprintf(`{"type":"response.output_item.added","output_index":%d,"item":%s}`, i, item)
}

func stItemDone(i int, item string) string {
	return fmt.Sprintf(`{"type":"response.output_item.done","output_index":%d,"item":%s}`, i, item)
}

func stDelta(i int, typ, delta, extra string) string {
	q, _ := json.Marshal(delta)
	return fmt.Sprintf(`{"type":"response.%s.delta","output_index":%d,"delta":%s%s}`, typ, i, q, extra)
}

func stMsg(text string) string {
	if text == "" {
		return `{"type":"message","role":"assistant","content":[]}`
	}
	q, _ := json.Marshal(text)
	return `{"type":"message","role":"assistant","content":[{"type":"output_text","text":` + string(q) + `}]}`
}

func stFn(call, name, args string) string {
	q, _ := json.Marshal(args)
	return fmt.Sprintf(`{"type":"function_call","call_id":%q,"name":%q,"arguments":%s}`, call, name, q)
}

func stReason(enc string, summary ...string) string {
	parts := []map[string]any{}
	for _, s := range summary {
		parts = append(parts, map[string]any{"type": "summary_text", "text": s})
	}
	item := map[string]any{"type": "reasoning", "summary": parts}
	if enc != "" {
		item["encrypted_content"] = enc
	}
	raw, _ := json.Marshal(item)
	return string(raw)
}

func stSearchItem(id, query string, urls ...string) string {
	sources := []any{}
	for _, u := range urls {
		sources = append(sources, map[string]any{"type": "url", "url": u})
	}
	raw, _ := json.Marshal(map[string]any{"type": "web_search_call", "id": id, "status": "completed",
		"action": map[string]any{"type": "search", "query": query, "sources": sources}})
	return string(raw)
}

// stSearchWant renders the two blocks of one search; a page is "url title".
func stSearchWant(id, query string, pages ...string) []string {
	q, _ := json.Marshal(map[string]any{"query": query})
	list := []any{}
	for _, p := range pages {
		url, title, _ := strings.Cut(p, " ")
		list = append(list, map[string]any{"type": "web_search_result", "url": url, "title": title, "encrypted_content": "", "page_age": nil})
	}
	raw, _ := json.Marshal(list)
	return []string{"server_tool_use|" + id + "|web_search|" + string(q) + "|", "web_search_tool_result|" + id + "||" + string(raw) + "|"}
}

func stJoin(lists ...[]string) []string {
	out := []string{}
	for _, l := range lists {
		out = append(out, l...)
	}
	return out
}

func TestStStreamBlocks(t *testing.T) {
	const (
		text = "output_text"
		args = "function_call_arguments"
		sum  = "reasoning_summary_text"
	)
	sig := EncodeSignature("gpt-x", "ENC")
	hidden := stTestCtx
	hidden.ShowThinking = false
	noThinking := stTestCtx // HideThinking wins over ShowThinking
	noThinking.HideThinking = true
	one := func(s string) []string { return []string{s} }
	search := func(i int, id, query string, urls ...string) []string {
		return []string{stAdded(i, `{"type":"web_search_call","id":"`+id+`","status":"in_progress"}`),
			`{"type":"response.web_search_call.searching","output_index":` + fmt.Sprint(i) + `,"item_id":"` + id + `"}`,
			stItemDone(i, stSearchItem(id, query, urls...))}
	}
	cases := []struct {
		name string
		ctx  *dto.StreamContext
		evs  []string
		want []string
		stop string
	}{
		{"nothing", nil, nil, []string{}, "end_turn"},
		{"text deltas", nil, []string{stAdded(0, stMsg("")), stDelta(0, text, "Hel", ""), stDelta(0, text, "", ""), stDelta(0, text, "lo", ""), stItemDone(0, stMsg("Hello"))},
			[]string{"text|||Hello|"}, "end_turn"},
		{"text only in done", nil, []string{stAdded(0, stMsg("")), stItemDone(0, stMsg("Hello"))}, []string{"text|||Hello|"}, "end_turn"},
		{"done without added", nil, []string{stItemDone(0, stMsg("Hello"))}, []string{"text|||Hello|"}, "end_turn"},
		{"keyed delta of an unannounced item", nil, []string{stDelta(0, text, "Hello", "")}, []string{}, "end_turn"},
		{"keyless delta only", nil, []string{`{"type":"response.output_text.delta","delta":"Hello"}`}, []string{"text|||Hello|"}, "end_turn"},
		{"announced after its first delta", nil, []string{stDelta(0, text, "Hel", ""), stAdded(0, stMsg("")), stDelta(0, text, "lo", "")}, []string{"text|||Hello|"}, "end_turn"},
		{"added, never done, shown", nil, []string{stAdded(0, stMsg("")), stDelta(0, text, "Hello", "")}, []string{"text|||Hello|"}, "end_turn"},
		{"added, never done, nothing shown", nil, []string{stAdded(0, stMsg("")), stAdded(1, stMsg("")), stDelta(1, text, "Hello", ""), stItemDone(2, stMsg("x"))}, []string{"text|||x|"}, "end_turn"},
		{"partial deltas, rest in done", nil, []string{stAdded(0, stMsg("")), stDelta(0, text, "Hel", ""), stItemDone(0, stMsg("Hello"))}, []string{"text|||Hello|"}, "end_turn"},
		{"output_text.done and item.done", nil, []string{stAdded(0, stMsg("")), `{"type":"response.output_text.done","output_index":0,"content_index":0,"text":"Hello"}`, stItemDone(0, stMsg("Hello"))},
			[]string{"text|||Hello|"}, "end_turn"},
		{"item.done repeated", nil, []string{stItemDone(0, stMsg("Hello")), stItemDone(0, stMsg("Hello"))}, []string{"text|||Hello|"}, "end_turn"},
		{"late delta after done", nil, []string{stItemDone(0, stMsg("Hello")), stDelta(0, text, "Hello", "")}, []string{"text|||Hello|"}, "end_turn"},
		{"empty message", nil, []string{stAdded(0, stMsg("")), stItemDone(0, stMsg("")), stItemDone(1, stMsg("x"))}, []string{"text|||x|"}, "end_turn"},
		{"two parts, keyed", nil, []string{stAdded(0, stMsg("")), stDelta(0, text, "A", `,"content_index":0`),
			stItemDone(0, `{"type":"message","content":[{"type":"output_text","text":"A"},{"type":"output_text","text":"B"}]}`)}, []string{"text|||AB|"}, "end_turn"},
		{"two parts, deltas without content_index", nil, []string{stAdded(0, stMsg("")), stDelta(0, text, "A", ""), stDelta(0, text, "B", ""),
			stItemDone(0, `{"type":"message","content":[{"type":"output_text","text":"A"},{"type":"output_text","text":"B"}]}`)}, []string{"text|||AB|"}, "end_turn"},
		{"keyless items (codex fixture)", nil, []string{
			`{"type":"response.output_item.done","item":` + stMsg("Hello") + `}`,
			`{"type":"response.output_item.done","item":` + stMsg("World") + `}`}, []string{"text|||Hello|", "text|||World|"}, "end_turn"},
		{"keyless deltas, done with an id", nil, []string{
			`{"type":"response.output_text.delta","delta":"Hel"}`, `{"type":"response.output_text.delta","delta":"lo"}`,
			`{"type":"response.output_item.done","item":{"type":"message","id":"m1","content":[{"type":"output_text","text":"Hello"}]}}`}, []string{"text|||Hello|"}, "end_turn"},
		{"keyed by item_id only", nil, []string{
			`{"type":"response.output_item.added","item":{"type":"function_call","id":"f1","call_id":"c1","name":"a"}}`,
			`{"type":"response.output_item.added","item":{"type":"function_call","id":"f2","call_id":"c2","name":"b"}}`,
			`{"type":"response.function_call_arguments.delta","item_id":"f2","delta":"{\"b\":1}"}`,
			`{"type":"response.function_call_arguments.delta","item_id":"f1","delta":"{\"a\":1}"}`,
			`{"type":"response.output_item.done","item":{"type":"function_call","id":"f2","call_id":"c2","name":"b","arguments":"{\"b\":1}"}}`,
			`{"type":"response.output_item.done","item":{"type":"function_call","id":"f1","call_id":"c1","name":"a","arguments":"{\"a\":1}"}}`,
		}, []string{`tool_use|c1|a|{"a":1}|`, `tool_use|c2|b|{"b":1}|`}, "tool_use"},
		{"ids change between events, index wins", nil, []string{
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"x1","content":[]}}`,
			`{"type":"response.output_text.delta","output_index":0,"item_id":"x2","delta":"Hi"}`,
			`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"x3","content":[{"type":"output_text","text":"Hi"}]}}`}, []string{"text|||Hi|"}, "end_turn"},
		{"index reused for another item", nil, []string{
			`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"x1","content":[{"type":"output_text","text":"A"}]}}`,
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"x2","content":[]}}`,
			`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"x2","content":[{"type":"output_text","text":"B"}]}}`}, []string{"text|||A|", "text|||B|"}, "end_turn"},

		{"refusal delta", nil, []string{stAdded(0, stMsg("")), stDelta(0, "refusal", "No.", ""), stItemDone(0, `{"type":"message","content":[{"type":"refusal","refusal":"No."}]}`)},
			[]string{"text|||No.|"}, "refusal"},
		{"refusal delta, item never done", nil, []string{stAdded(0, stMsg("")), stDelta(0, "refusal", "No.", "")}, []string{"text|||No.|"}, "refusal"},
		{"one id under two indexes", nil, []string{
			`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"m","content":[{"type":"output_text","text":"A"}]}}`,
			`{"type":"response.output_item.done","output_index":1,"item":{"type":"message","id":"m","content":[{"type":"output_text","text":"A"}]}}`}, []string{"text|||A|"}, "end_turn"},
		{"one call under two indexes", nil, []string{stItemDone(0, stFn("c", "f", "{}")), stItemDone(1, stFn("c", "f", "{}"))}, []string{"tool_use|c|f|{}|"}, "tool_use"},
		{"two calls under one index", nil, []string{stAdded(0, stFn("c", "f", "")), stItemDone(0, stFn("c", "f", "{}")), stItemDone(0, stFn("d", "f", "{}"))}, []string{"tool_use|c|f|{}|", "tool_use|d|f|{}|"}, "tool_use"},
		{"interleaved indexes, done missing", nil, []string{stAdded(0, stMsg("")), stAdded(1, stMsg("")), stDelta(0, text, "A", ""), stDelta(1, text, "B", ""), stDelta(0, text, "a", ""), stDelta(1, text, "b", ""), stItemDone(1, stMsg("Bb"))},
			[]string{"text|||Aa|", "text|||Bb|"}, "end_turn"},
		{"keyed by call_id only", nil, []string{
			`{"type":"response.output_item.added","item":` + stFn("c1", "a", "") + `}`,
			`{"type":"response.output_item.added","item":` + stFn("c2", "b", "") + `}`,
			`{"type":"response.function_call_arguments.delta","call_id":"c1","delta":"{\"a\":1}"}`,
			`{"type":"response.function_call_arguments.delta","call_id":"c2","delta":"{\"b\":1}"}`,
			`{"type":"response.output_item.done","item":` + stFn("c1", "a", `{"a":1}`) + `}`,
			`{"type":"response.output_item.done","item":` + stFn("c2", "b", `{"b":1}`) + `}`,
		}, []string{`tool_use|c1|a|{"a":1}|`, `tool_use|c2|b|{"b":1}|`}, "tool_use"},
		{"refusal only in done", nil, []string{stItemDone(0, `{"type":"message","content":[{"type":"refusal","refusal":"No."}]}`)}, []string{"text|||No.|"}, "refusal"},
		{"refusal, then more text", nil, []string{stItemDone(0, `{"type":"message","content":[{"type":"refusal","refusal":"No."}]}`), stItemDone(1, stMsg("x"))}, []string{"text|||No.|", "text|||x|"}, "refusal"},
		{"tool use beats refusal", nil, []string{stItemDone(0, `{"type":"message","content":[{"type":"refusal","refusal":"No."}]}`), stItemDone(1, stFn("c", "f", "{}"))},
			[]string{"text|||No.|", "tool_use|c|f|{}|"}, "tool_use"},

		{"function call", nil, []string{stAdded(0, stFn("c1", "f", "")), stDelta(0, args, `{"a":`, ""), stDelta(0, args, `"b"}`, ""),
			`{"type":"response.function_call_arguments.done","output_index":0,"arguments":"{\"a\":\"b\"}"}`, stItemDone(0, stFn("c1", "f", `{"a":"b"}`))},
			[]string{`tool_use|c1|f|{"a":"b"}|`}, "tool_use"},
		{"function call name mapped back", nil, []string{stItemDone(0, stFn("c1", "short", `{}`))}, []string{`tool_use|c1|mcp__a__very_long|{}|`}, "tool_use"},
		{"arguments only in arguments.done", nil, []string{stAdded(0, stFn("c1", "f", "")), `{"type":"response.function_call_arguments.done","output_index":0,"arguments":"{\"a\":1}"}`, stItemDone(0, stFn("c1", "f", ""))},
			[]string{`tool_use|c1|f|{"a":1}|`}, "tool_use"},
		{"empty arguments", nil, []string{stAdded(0, stFn("c1", "f", "")), stItemDone(0, stFn("c1", "f", ""))}, []string{`tool_use|c1|f|{}|`}, "tool_use"},
		{"function call never done", nil, []string{stAdded(0, stFn("c1", "f", "")), stDelta(0, args, `{"a":1}`, "")}, []string{`tool_use|c1|f|{"a":1}|`}, "tool_use"},
		{"name arrives only in done", nil, []string{stDelta(0, args, `{"a":1}`, ""), stItemDone(0, stFn("c1", "f", `{"a":1}`))}, []string{`tool_use|c1|f|{"a":1}|`}, "tool_use"},
		{"big integers survive", nil, []string{stItemDone(0, stFn("c1", "f", `{"n":12345678901234567890}`))}, []string{`tool_use|c1|f|{"n":12345678901234567890}|`}, "tool_use"},

		{"thinking shown", nil, []string{stAdded(0, stReason("PARTIAL")), stDelta(0, sum, "One", `,"summary_index":0`), stDelta(0, sum, " more", `,"summary_index":0`), stDelta(0, sum, "Two", `,"summary_index":1`),
			stItemDone(0, stReason("ENC", "One more", "Two")), stItemDone(1, stMsg("Hi"))},
			[]string{"thinking|||One more\n\nTwo|" + sig, "text|||Hi|"}, "end_turn"},
		{"summary only in done", nil, []string{stItemDone(0, stReason("ENC", "One", "Two"))}, []string{"thinking|||One\n\nTwo|" + sig}, "end_turn"},
		{"summary text.done", nil, []string{`{"type":"response.reasoning_summary_text.done","output_index":0,"summary_index":0,"text":"One"}`, stItemDone(0, stReason("ENC", "One"))},
			[]string{"thinking|||One|" + sig}, "end_turn"},
		{"thinking shown, nothing encrypted", nil, []string{stDelta(0, sum, "One", `,"summary_index":0`), stItemDone(0, stReason("", "One"))}, []string{"thinking|||One|"}, "end_turn"},
		{"thinking hidden", &hidden, []string{stAdded(0, stReason("")), stDelta(0, sum, "One", `,"summary_index":0`), stItemDone(0, stReason("ENC", "One")), stItemDone(1, stMsg("Hi"))},
			[]string{"thinking||||" + sig, "text|||Hi|"}, "end_turn"},
		{"thinking hidden, nothing encrypted", &hidden, []string{stAdded(0, stReason("")), stDelta(0, sum, "One", ""), stItemDone(0, stReason("", "One")), stItemDone(1, stMsg("Hi"))},
			[]string{"text|||Hi|"}, "end_turn"},
		{"partial encrypted_content from added is not used", nil, []string{stAdded(0, stReason("PARTIAL"))}, []string{}, "end_turn"},
		{"raw reasoning is not shown", nil, []string{stAdded(0, stReason("")), stDelta(0, "reasoning_text", "raw", ""), stItemDone(0, stReason("ENC"))}, []string{"thinking||||" + sig}, "end_turn"},

		{"unknown items and events", nil, []string{`{"type":"response.new_tool_event"}`, `{"type":"response.metadata","headers":{}}`, `{}`, `{"type":7}`,
			stAdded(0, `{"type":"image_generation_call","id":"w"}`), stAdded(1, stMsg("")), stDelta(0, text, "lost?", ""), stDelta(1, text, "Hi", ""),
			stItemDone(0, `{"type":"image_generation_call","id":"w"}`), stItemDone(1, stMsg("Hi")), `{"type":"response.output_item.done"}`, stItemDone(2, `{"type":"message","content":"odd"}`), stItemDone(3, stFn("c", "f", "{}"))},
			[]string{"text|||Hi|", "tool_use|c|f|{}|"}, "tool_use"},
		{"search between text and a call", nil, stJoin(one(stAdded(0, stMsg(""))), one(stDelta(0, text, "Look", "")), one(stItemDone(0, stMsg("Look"))),
			search(1, "ws_1", "q", "https://e.x/a", "https://e.x/b/?u=1", "https://e.x/a"), one(stItemDone(2, stFn("c", "f", `{"a":1}`))), one(stItemDone(3, stMsg("End")))),
			stJoin(one("text|||Look|"), stSearchWant("srvtoolu_1", "q", "https://e.x/a e.x/a", "https://e.x/b/?u=1 e.x/b"), []string{`tool_use|c|f|{"a":1}|`, "text|||End|"}), "tool_use"},
		{"search first, then text", nil, stJoin(search(0, "ws_1", "q"), one(stItemDone(1, stMsg("Hi")))), stJoin(stSearchWant("srvtoolu_1", "q"), one("text|||Hi|")), "end_turn"},
		{"two searches", nil, stJoin(search(0, "ws_1", "q"), search(1, "ws_2", "r", "https://e.x")), stJoin(stSearchWant("srvtoolu_1", "q"), stSearchWant("srvtoolu_2", "r", "https://e.x e.x")), "end_turn"},
		{"search finished before the text above it", nil, stJoin(one(stAdded(0, stMsg(""))), search(1, "ws_1", "q"), one(stDelta(0, text, "Hi", "")), one(stItemDone(0, stMsg("Hi"))), one(stItemDone(2, stFn("c", "f", "{}")))),
			stJoin(one("text|||Hi|"), stSearchWant("srvtoolu_1", "q"), one("tool_use|c|f|{}|")), "tool_use"},
		{"unfinished search holds the text below it", nil, []string{stAdded(0, `{"type":"web_search_call","id":"ws_1"}`), stItemDone(1, stMsg("Hi")), stItemDone(0, stSearchItem("ws_1", "q"))},
			stJoin(stSearchWant("srvtoolu_1", "q"), one("text|||Hi|")), "end_turn"},
		{"search done twice", nil, stJoin(search(0, "ws_1", "q"), one(stItemDone(0, stSearchItem("ws_1", "q")))), stSearchWant("srvtoolu_1", "q"), "end_turn"},
		{"opened page: the address stands for the query", nil, []string{stItemDone(0, `{"type":"web_search_call","id":"ws_1","action":{"type":"open_page","url":"https://e.x/p"}}`)},
			stSearchWant("srvtoolu_1", "https://e.x/p", "https://e.x/p e.x/p"), "end_turn"},
		{"sources without a query", nil, search(0, "ws_1", "", "https://e.x/p"), stSearchWant("srvtoolu_1", "https://e.x/p", "https://e.x/p e.x/p"), "end_turn"},
		{"search with no query and no url", nil, stJoin(one(stItemDone(0, stMsg("A"))), search(1, "ws_1", ""), one(stItemDone(2, stMsg("B")))), []string{"text|||A|", "text|||B|"}, "end_turn"},
		{"search without an action", nil, []string{stItemDone(0, `{"type":"web_search_call","id":"ws_1"}`), stItemDone(1, stFn("c", "f", "{}"))}, []string{"tool_use|c|f|{}|"}, "tool_use"},
		{"search never done", nil, []string{stAdded(0, `{"type":"web_search_call","id":"ws_1"}`), stItemDone(1, stMsg("Hi"))}, []string{"text|||Hi|"}, "end_turn"},
		{"search alone, odd sources", nil, []string{stItemDone(0, `{"type":"web_search_call","id":"ws_1","action":{"query":"q","sources":[null,3,{"url":4},{},{"url":"no scheme"}]}}`)},
			[]string{stSearchWant("srvtoolu_1", "q")[0], `web_search_tool_result|srvtoolu_1||[{"encrypted_content":"","page_age":null,"title":"no scheme","type":"web_search_result","url":"no scheme"}]|`}, "end_turn"},

		{"no thinking at all", &noThinking, []string{stAdded(0, stReason("")), stDelta(0, sum, "One", `,"summary_index":0`), stItemDone(0, stReason("ENC", "One")), stItemDone(1, stMsg("Hi")),
			stItemDone(2, stReason("ENC", "Two")), stItemDone(3, stFn("c", "f", "{}"))}, []string{"text|||Hi|", "tool_use|c|f|{}|"}, "tool_use"},
		{"no thinking, only reasoning came", &noThinking, []string{stItemDone(0, stReason("ENC", "One"))}, []string{}, "end_turn"},
		{"no thinking, search", &noThinking, stJoin(one(stItemDone(0, stReason("ENC", "One"))), search(1, "ws_1", "q"), one(stItemDone(2, stReason("ENC"))), one(stItemDone(3, stMsg("Hi")))),
			stJoin(stSearchWant("srvtoolu_1", "q"), one("text|||Hi|")), "end_turn"},
		{"parallel calls interleave", nil, []string{stAdded(0, stFn("c0", "a", "")), stAdded(1, stFn("c1", "b", "")),
			stDelta(1, args, `{"b":`, ""), stDelta(0, args, `{"a":`, ""), stDelta(1, args, `1}`, ""), stItemDone(1, stFn("c1", "b", `{"b":1}`)), stDelta(0, args, `1}`, ""), stItemDone(0, stFn("c0", "a", `{"a":1}`))},
			[]string{`tool_use|c0|a|{"a":1}|`, `tool_use|c1|b|{"b":1}|`}, "tool_use"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := stTestCtx
			if c.ctx != nil {
				ctx = *c.ctx
			}
			for _, created := range []bool{true, false} {
				evs := append([]string{}, c.evs...)
				if created {
					evs = append([]string{stCreated}, evs...)
				}
				s, cl := stRun(t, ctx, append(evs, stDone)...)
				if s.Failed() != nil || !cl.stopped {
					t.Fatalf("failed %v, stopped %v", s.Failed(), cl.stopped)
				}
				if got := cl.strings(); !reflect.DeepEqual(got, c.want) {
					t.Errorf("stream  %q\nwant    %q", got, c.want)
				}
				if cl.stop != c.stop {
					t.Errorf("stop_reason %s, want %s", cl.stop, c.stop)
				}
				m := s.Message()
				if m == nil {
					t.Fatal("no message")
				}
				if got := stFromMessage(t, m); !reflect.DeepEqual(got, c.want) {
					t.Errorf("message %q\nwant    %q", got, c.want)
				}
				if *m.StopReason != c.stop || m.Type != "message" || m.Role != "assistant" || m.Model != "claude-opus" || m.ID != cl.message["id"] || m.Usage != s.Usage() {
					t.Errorf("message header %+v", m)
				}
			}
		})
	}
}

func TestStStreamMessageStart(t *testing.T) {
	zero := map[string]any{"input_tokens": 0.0, "output_tokens": 0.0, "cache_creation_input_tokens": 0.0, "cache_read_input_tokens": 0.0}
	s := NewStream(stTestCtx)
	evs := s.Push(stJSON(t, stCreated))
	if len(evs) != 1 || s.Push(stJSON(t, stCreated)) != nil {
		t.Fatalf("created: %v", evs)
	}
	c := &stClient{t: t}
	c.feed(evs)
	want := map[string]any{"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-opus", "content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": zero}
	if !reflect.DeepEqual(c.message, want) {
		t.Errorf("got %v\nwant %v", c.message, want)
	}
	s = NewStream(stTestCtx)
	if evs := s.Push(stJSON(t, stAdded(0, stReason("")))); evs != nil {
		t.Errorf("no content yet, got %v", evs)
	}
	_, c = stRun(t, stTestCtx, `{"type":"response.output_text.delta","delta":"x"}`, stDone)
	if id, _ := c.message["id"].(string); !strings.HasPrefix(id, "msg_") || len(id) < 12 || !reflect.DeepEqual(c.message["usage"], zero) {
		t.Errorf("lazy start %v", c.message)
	}
}

// Nothing may wait for the end of the stream when it can go out now.
func TestStStreamIsLive(t *testing.T) {
	s := NewStream(stTestCtx)
	names := func(ev string) string {
		out := []string{}
		for _, e := range s.Push(stJSON(t, ev)) {
			out = append(out, e.Name)
		}
		return strings.Join(out, " ")
	}
	for _, step := range [][2]string{
		{stCreated, "message_start"},
		{stAdded(0, stMsg("")), ""},
		{stDelta(0, "output_text", "a", ""), "content_block_start content_block_delta"},
		{stDelta(0, "output_text", "b", ""), "content_block_delta"},
		{stAdded(1, stFn("c", "f", "")), ""},
		{stDelta(1, "function_call_arguments", "{", ""), ""},
		{stItemDone(0, stMsg("ab")), "content_block_stop content_block_start content_block_delta"},
		{stDelta(1, "function_call_arguments", "}", ""), "content_block_delta"},
		{stItemDone(1, stFn("c", "f", "{}")), "content_block_stop"},
		{stAdded(2, stFn("c2", "f", "")), ""},
		{stDelta(2, "function_call_arguments", "{", ""), "content_block_start content_block_delta"},
		{stDone, "content_block_stop message_delta message_stop"},
	} {
		if got := names(step[0]); got != step[1] {
			t.Errorf("%s\n got %q\nwant %q", step[0], got, step[1])
		}
	}
}

func TestStStreamOutputOnlyInCompleted(t *testing.T) {
	completed := func(items ...string) string {
		return `{"type":"response.completed","response":{"output":[` + strings.Join(items, ",") + `]}}`
	}
	idMsg := func(id, text string) string {
		return `{"type":"message","id":"` + id + `","content":[{"type":"output_text","text":"` + text + `"}]}`
	}
	cases := []struct {
		name string
		evs  []string
		want []string
	}{
		{"no item events at all", []string{completed(stReason("ENC"), stMsg("Hi"), stFn("c", "f", "{}"))},
			[]string{"thinking||||" + EncodeSignature("gpt-x", "ENC"), "text|||Hi|", "tool_use|c|f|{}|"}},
		{"search among the output", []string{completed(stMsg("Hi"), stSearchItem("ws_1", "q", "https://e.x"), stFn("c", "f", "{}"))},
			stJoin([]string{"text|||Hi|"}, stSearchWant("srvtoolu_1", "q", "https://e.x e.x"), []string{"tool_use|c|f|{}|"})},
		{"an unfinished search is dropped", []string{stAdded(0, `{"type":"web_search_call","id":"ws_1"}`), completed(stSearchItem("ws_1", "q"))}, []string{}},
		{"search is not repeated", []string{stItemDone(0, stSearchItem("ws_1", "q")), completed(stSearchItem("ws_1", "q"))}, stSearchWant("srvtoolu_1", "q")},
		{"already complete", []string{stItemDone(0, stMsg("Hi")), stItemDone(1, stFn("c", "f", "{}")), completed(stMsg("Hi"), stFn("c", "f", "{}"))},
			[]string{"text|||Hi|", "tool_use|c|f|{}|"}},
		{"an unfinished item stays as it is", []string{stAdded(0, stMsg("")), stDelta(0, "output_text", "H", ""), completed(stMsg("Hi"))}, []string{"text|||H|"}},
		{"after keyed deltas of an unannounced item", []string{stDelta(0, "output_text", "H", ""), completed(stMsg("Hi"))}, []string{"text|||Hi|"}},
		{"not after keyless deltas", []string{`{"type":"response.output_text.delta","delta":"Hi"}`, completed(stMsg("Hi"))}, []string{"text|||Hi|"}},
		{"not after an item of another kind", []string{stItemDone(0, `{"type":"mystery"}`), completed(stMsg("Hi"))}, []string{}},
		{"unseen items are not added", []string{stItemDone(0, idMsg("a", "A")), completed(idMsg("a", "A"), idMsg("b", "B"))}, []string{"text|||A|"}},
		{"the same rules hold for it", []string{completed(`{"type":"message","role":"developer","content":[{"type":"output_text","text":"D"}]}`, stPhase("final_answer", "A"), stPhase("commentary", "C"))}, []string{"text|||A|"}},
		{"keyless items are not repeated", []string{`{"type":"response.output_item.done","item":` + stMsg("Hi") + `}`, completed(stMsg("Hi"))}, []string{"text|||Hi|"}},
		{"shifted output is not repeated", []string{stItemDone(1, stMsg("Hi")), completed(stMsg("Hi"))}, []string{"text|||Hi|"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, cl := stRun(t, stTestCtx, c.evs...)
			if got := cl.strings(); !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %q\nwant %q", got, c.want)
			}
		})
	}
}

func TestStStreamIncomplete(t *testing.T) {
	inc := func(reason string) string {
		return `{"type":"response.incomplete","response":{"incomplete_details":` + reason + `,"usage":{"input_tokens":5,"output_tokens":3}}}`
	}
	for _, c := range []struct{ reason, stop, message string }{
		{`{"reason":"max_output_tokens"}`, "max_tokens", ""},
		{`{"reason":"content_filter"}`, "refusal", ""},
		{`{"reason":"other"}`, "", "upstream response incomplete: other"},
		{`null`, "", "upstream response incomplete: unknown"},
	} {
		s, cl := stRun(t, stTestCtx, stCreated, stAdded(0, stMsg("")), stDelta(0, "output_text", "par", ""), stAdded(1, stFn("c", "f", "")),
			stItemDone(2, `{"type":"function_call","call_id":"d","name":"f","arguments":"","status":"incomplete"}`), inc(c.reason))
		if s.Usage() != (dto.Usage{InputTokens: 5, OutputTokens: 3}) {
			t.Errorf("%s: usage %+v", c.reason, s.Usage())
		}
		if c.stop == "" {
			want := dto.APIError{Status: 502, Type: "api_error", Message: c.message}
			if f := s.Failed(); f == nil || *f != want || !cl.errored || cl.stopped || s.Message() != nil {
				t.Errorf("%s: failed %+v", c.reason, f)
			}
			continue
		}
		if cl.stop != c.stop || !cl.stopped || s.Failed() != nil || *s.Message().StopReason != c.stop {
			t.Errorf("%s: stop %s failed %v", c.reason, cl.stop, s.Failed())
		}
		if got, want := cl.strings(), []string{"text|||par|"}; !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %q", c.reason, got)
		}
	}
}

func TestStStreamClose(t *testing.T) {
	want := dto.APIError{Status: 502, Type: "api_error", Message: "upstream stream ended early"}
	for _, evs := range [][]string{nil, {stCreated}, {stCreated, stAdded(0, stMsg("")), stDelta(0, "output_text", "x", "")}, {stItemDone(0, stMsg("x"))}} {
		s, cl := stRun(t, stTestCtx, evs...)
		if f := s.Failed(); f == nil || *f != want || !cl.errored || cl.deltaErr != "api_error" || s.Message() != nil {
			t.Errorf("%v: %+v", evs, f)
		}
	}
	s := NewStream(stTestCtx)
	if s.Message() != nil || s.Failed() != nil {
		t.Error("an unfinished stream has neither a message nor a failure")
	}
}

func TestStStreamUsage(t *testing.T) {
	for _, c := range []struct {
		usage string
		want  dto.Usage
	}{
		{`{"input_tokens":100,"input_tokens_details":{"cached_tokens":40},"output_tokens":7}`, dto.Usage{InputTokens: 60, CacheReadInputTokens: 40, OutputTokens: 7}},
		{`{"input_tokens":100,"input_tokens_details":null,"output_tokens":7}`, dto.Usage{InputTokens: 100, OutputTokens: 7}},
		{`{"input_tokens":10,"input_tokens_details":{"cached_tokens":40},"output_tokens":-7}`, dto.Usage{CacheReadInputTokens: 40}},
		{`{"input_tokens":-10,"input_tokens_details":{"cached_tokens":-4}}`, dto.Usage{}},
		{`{"input_tokens":"x","output_tokens":1e300}`, dto.Usage{}},
		{`null`, dto.Usage{}},
	} {
		s, cl := stRun(t, stTestCtx, `{"type":"response.completed","response":{"usage":`+c.usage+`}}`)
		if s.Usage() != c.want {
			t.Errorf("%s: %+v", c.usage, s.Usage())
		}
		want := map[string]any{"input_tokens": float64(c.want.InputTokens), "output_tokens": float64(c.want.OutputTokens), "cache_creation_input_tokens": 0.0, "cache_read_input_tokens": float64(c.want.CacheReadInputTokens)}
		if !reflect.DeepEqual(cl.usage, want) {
			t.Errorf("%s: message_delta usage %v", c.usage, cl.usage)
		}
	}
}

func TestStMessageInvalidArguments(t *testing.T) {
	for _, args := range []string{`{"a":`, `nope`, `{} {}`, `[1]`, `null`} {
		s, cl := stRun(t, stTestCtx, stItemDone(0, stFn("c", "f", args)), stDone)
		if !cl.stopped || cl.blocks[0].text != args {
			t.Errorf("%s: the stream passes arguments through, got %q", args, cl.strings())
		}
		if s.Message() != nil || s.Failed() == nil || s.Failed().Type != "api_error" || s.Failed().Status != 502 {
			t.Errorf("%s: failed %+v", args, s.Failed())
		}
	}
}

func TestStFunctionCallWithoutIdentity(t *testing.T) {
	for _, evs := range [][]string{
		{stItemDone(0, `{"type":"function_call","arguments":"{}"}`)},
		{stItemDone(0, `{"type":"function_call","call_id":"c","arguments":"{}"}`), stDone},
	} {
		s, cl := stRun(t, stTestCtx, evs...)
		if f := s.Failed(); f == nil || f.Status != 502 || !strings.Contains(f.Message, "function call") || !cl.errored {
			t.Errorf("%v: %+v", evs, f)
		}
	}
}

// Three items, each with its own ordered events, in a random global order.
func TestStStreamInterleaving(t *testing.T) {
	for seed := int64(0); seed < 3000; seed++ {
		r := rand.New(rand.NewSource(seed))
		ctx := stTestCtx
		ctx.ShowThinking = r.Intn(2) == 0
		ctx.HideThinking = r.Intn(4) == 0
		keyByID := r.Intn(3) == 0
		var queues [][]string
		want := map[string]bool{}
		for i := 0; i < 3; i++ {
			body := stRandText(r)
			var q []string
			deltas := func(typ, extra string) {
				if r.Intn(4) == 0 {
					return // everything arrives in done
				}
				for rest := []rune(body); len(rest) > 0; {
					n := 1 + r.Intn(len(rest))
					if r.Intn(6) == 0 {
						return // the tail arrives in done
					}
					q = append(q, stDelta(i, typ, string(rest[:n]), extra))
					rest = rest[n:]
				}
			}
			switch r.Intn(5) {
			case 0:
				q = append(q, stAdded(i, stMsg("")))
				deltas("output_text", `,"content_index":0`)
				q = append(q, stItemDone(i, stMsg(body)))
				want["text|||"+body+"|"] = true
			case 1:
				enc := fmt.Sprintf("ENC%d", i)
				q = append(q, stAdded(i, stReason("")))
				deltas("reasoning_summary_text", `,"summary_index":0`)
				q = append(q, stItemDone(i, stReason(enc, body)))
				shown := body
				if !ctx.ShowThinking {
					shown = ""
				}
				if !ctx.HideThinking {
					want["thinking|||"+shown+"|"+EncodeSignature("gpt-x", enc)] = true
				}
			case 2:
				q = append(q, stAdded(i, `{"type":"mystery"}`), stDelta(i, "output_text", body, ""), stItemDone(i, `{"type":"mystery"}`))
			case 3:
				query, id, url := body, fmt.Sprintf("ws_%d", i), fmt.Sprintf("https://e.x/%d", i)
				var urls, pages []string
				if r.Intn(2) == 0 {
					urls, pages = []string{url}, []string{url + " " + strings.TrimPrefix(url, "https://")}
				}
				if r.Intn(4) == 0 {
					query = ""
				}
				q = append(q, stAdded(i, `{"type":"web_search_call","id":"`+id+`"}`),
					fmt.Sprintf(`{"type":"response.web_search_call.searching","output_index":%d}`, i), stItemDone(i, stSearchItem(id, query, urls...)))
				if keyByID {
					id = fmt.Sprintf("it_%d", i)
				}
				if query == "" && urls != nil {
					query = url
				}
				if query != "" {
					for _, b := range stSearchWant("srvtoolu_"+strings.TrimPrefix(id, "ws_"), query, pages...) {
						want[b] = true
					}
				}
			default:
				raw, _ := json.Marshal(map[string]string{"v": body})
				body = string(raw)
				call := fmt.Sprintf("c%d", i)
				q = append(q, stAdded(i, stFn(call, "f", "")))
				deltas("function_call_arguments", "")
				q = append(q, stItemDone(i, stFn(call, "f", body)))
				want["tool_use|"+call+"|f|"+body+"|"] = true
			}
			queues = append(queues, q)
		}
		var evs []string
		for len(queues) > 0 {
			k := r.Intn(len(queues))
			evs = append(evs, queues[k][0])
			if queues[k] = queues[k][1:]; len(queues[k]) == 0 {
				queues = append(queues[:k], queues[k+1:]...)
			}
		}
		if keyByID {
			for i := range evs {
				evs[i] = stRekey(t, evs[i])
			}
		}
		s, cl := stRun(t, ctx, append(evs, stDone)...)
		got := map[string]bool{}
		for _, b := range cl.strings() {
			got[b] = true
		}
		if !reflect.DeepEqual(got, want) || len(cl.blocks) != len(want) || !cl.stopped {
			t.Fatalf("seed %d\nevents %s\ngot  %q\nwant %v", seed, strings.Join(evs, "\n"), cl.strings(), want)
		}
		if m := stFromMessage(t, s.Message()); !reflect.DeepEqual(m, cl.strings()) {
			t.Fatalf("seed %d: message %q, stream %q", seed, m, cl.strings())
		}
	}
}

// stRekey turns output_index into an item id.
func stRekey(t *testing.T, ev string) string {
	m := stJSON(t, ev)
	id := fmt.Sprintf("it_%v", m["output_index"])
	delete(m, "output_index")
	if item, ok := m["item"].(map[string]any); ok {
		item["id"] = id
	} else {
		m["item_id"] = id
	}
	raw, _ := json.Marshal(m)
	return string(raw)
}

func stRandText(r *rand.Rand) string {
	letters := []rune("abc \n\"\\{}<>яж世🙂")
	out := make([]rune, 1+r.Intn(30))
	for i := range out {
		out[i] = letters[r.Intn(len(letters))]
	}
	return string(out)
}

// Junk in any field must never panic.
func TestStStreamJunk(t *testing.T) {
	types := []string{"response.created", "response.output_item.added", "response.output_item.done", "response.output_text.delta", "response.output_text.done",
		"response.refusal.delta", "response.refusal.done", "response.function_call_arguments.delta", "response.function_call_arguments.done",
		"response.reasoning_summary_text.delta", "response.reasoning_summary_text.done", "response.completed", "response.incomplete", "response.failed", "error"}
	junk := []any{nil, 7.0, -1.0, 1e300, "x", true, []any{}, []any{nil, 3.0, map[string]any{"type": "message", "content": []any{nil, 1.0}}}, map[string]any{},
		map[string]any{"type": "message", "content": []any{nil, map[string]any{"type": "output_text", "text": 3.0}}},
		map[string]any{"type": "function_call", "arguments": map[string]any{"a": 1.0}, "call_id": 3.0, "name": "n"},
		map[string]any{"type": "reasoning", "summary": "x", "encrypted_content": 4.0},
		map[string]any{"type": "web_search_call", "action": "x"},
		map[string]any{"type": "web_search_call", "id": 5.0, "action": map[string]any{"query": "q", "url": 2.0, "sources": []any{nil, "x", map[string]any{"url": "u"}}}},
		map[string]any{"error": "x", "output": []any{nil, "x"}, "usage": 3.0, "incomplete_details": 1.0}}
	fields := []string{"item", "response", "delta", "text", "arguments", "refusal", "output_index", "content_index", "summary_index", "item_id", "call_id", "error", "headers", "status", "code", "message"}
	for seed := int64(0); seed < 3000; seed++ {
		r := rand.New(rand.NewSource(seed))
		s, c := NewStream(stTestCtx), &stClient{t: t}
		c.feed(s.Push(nil))
		for i := 0; i < 12; i++ {
			ev := map[string]any{"type": types[r.Intn(len(types))]}
			for _, f := range fields {
				if r.Intn(2) == 0 {
					ev[f] = junk[r.Intn(len(junk))]
				}
			}
			c.feed(s.Push(ev))
		}
		c.feed(s.Close())
		s.Message()
		if c.stopped == c.errored {
			t.Fatalf("seed %d: stopped %v errored %v", seed, c.stopped, c.errored)
		}
	}
}

func TestStSearchIsLive(t *testing.T) {
	s := NewStream(stTestCtx)
	for _, step := range []struct {
		ev   string
		want string
	}{
		{stAdded(0, `{"type":"web_search_call","id":"ws_1"}`), ""},
		{stItemDone(0, stSearchItem("ws_1", "q", "https://e.x")), "message_start content_block_start:0 content_block_delta:0 content_block_stop:0 content_block_start:1 content_block_stop:1"},
		{stAdded(1, stMsg("")), ""},
		{stDelta(1, "output_text", "Hi", ""), "content_block_start:2 content_block_delta:2"},
		{stItemDone(1, stMsg("Hi")), "content_block_stop:2"},
		{stDone, "message_delta message_stop"},
	} {
		var got []string
		for _, e := range s.Push(stJSON(t, step.ev)) {
			name := e.Name
			if i, ok := e.Data["index"]; ok {
				name += fmt.Sprintf(":%v", i)
			}
			got = append(got, name)
		}
		if strings.Join(got, " ") != step.want {
			t.Errorf("%s\n got %q\nwant %q", step.ev, got, step.want)
		}
	}
}

// Codex items may come without ids; a client still has to match each result to its search.
func TestStSearchWithoutID(t *testing.T) {
	item := `{"type":"web_search_call","action":{"query":"q"}}`
	s, c := stRun(t, stTestCtx, stItemDone(0, item), stItemDone(1, item), stDone)
	if len(c.blocks) != 4 {
		t.Fatalf("got %q", c.strings())
	}
	if c.blocks[0].id == "srvtoolu_" || c.blocks[0].id == c.blocks[2].id {
		t.Errorf("ids %q and %q", c.blocks[0].id, c.blocks[2].id)
	}
	if m := stFromMessage(t, s.Message()); !reflect.DeepEqual(m, c.strings()) {
		t.Errorf("message %q\nstream  %q", m, c.strings())
	}
}

func stPhase(phase, text string) string {
	return fmt.Sprintf(`{"type":"message","role":"assistant","phase":%q,"content":[{"type":"output_text","text":%q}]}`, phase, text)
}

func stPhaseAdded(i int, phase string) string {
	return stAdded(i, fmt.Sprintf(`{"type":"message","role":"assistant","phase":%q}`, phase))
}

func stPhaseItem(i int, phase, text string) []string {
	return []string{stPhaseAdded(i, phase), stDelta(i, "output_text", text, ""), stItemDone(i, stPhase(phase, text))}
}

// What must not reach the reader, from the review (L1-L13).
func TestStStreamLeaks(t *testing.T) {
	const text = "output_text"
	one := func(s ...string) []string { return append([]string{}, s...) }
	foreign := func(role string) string {
		return `{"type":"message","role":"` + role + `","content":[{"type":"output_text","text":"HIDDEN"}]}`
	}
	sig := EncodeSignature("gpt-x", "E")
	cases := []struct {
		name    string
		evs     []string
		stream  []string
		message []string // nil: same as stream
		notes   []string
	}{
		{"commentary after the answer", stJoin(stPhaseItem(0, "final_answer", "Answer."), stPhaseItem(1, "commentary", "Notes")), one("text|||Answer.|"), nil, one(stNoteCommentary)},
		{"commentary before the answer", stJoin(stPhaseItem(0, "commentary", "Looking."), stPhaseItem(1, "final_answer", "Answer.")), one("text|||Looking.|", "text|||Answer.|"), nil, nil},
		{"commentary between calls", stJoin(stPhaseItem(0, "commentary", "One."), one(stItemDone(1, stFn("c", "f", "{}"))), stPhaseItem(2, "commentary", "Two.")), one("text|||One.|", "tool_use|c|f|{}|", "text|||Two.|"), nil, nil},
		{"a call after the answer still works", stJoin(stPhaseItem(0, "final_answer", "Answer."), stPhaseItem(1, "commentary", "Notes"), one(stItemDone(2, stFn("c", "f", "{}")))),
			one("text|||Answer.|", "tool_use|c|f|{}|"), nil, one(stNoteCommentary)},
		{"a second answer stays", stJoin(stPhaseItem(0, "final_answer", "A"), stPhaseItem(1, "final_answer", "B")), one("text|||A|", "text|||B|"), nil, nil},
		{"phase known only in done", stJoin(stPhaseItem(0, "final_answer", "A"), one(stAdded(1, stMsg("")), stDelta(1, text, "Notes", ""), stItemDone(1, stPhase("commentary", "Notes")))),
			one("text|||A|"), nil, one(stNoteCommentary)},
		{"no phase at all after the answer", stJoin(stPhaseItem(0, "final_answer", "A"), one(stAdded(1, stMsg("")), stDelta(1, text, "B", ""), stItemDone(1, stMsg("B")))),
			one("text|||A|", "text|||B|"), nil, nil},
		{"answer still open when commentary comes", one(stPhaseAdded(0, "final_answer"), stPhaseAdded(1, "commentary"), stDelta(1, text, "Notes", ""), stDelta(0, text, "A", ""), stItemDone(1, stPhase("commentary", "Notes")), stItemDone(0, stPhase("final_answer", "A"))),
			one("text|||A|"), nil, one(stNoteCommentary)},
		{"an empty answer hides nothing", stJoin(one(stItemDone(0, stPhase("final_answer", ""))), stPhaseItem(1, "commentary", "Text")), one("text|||Text|"), nil, nil},

		{"withdrawn tail", one(stAdded(0, stMsg("")), stDelta(0, text, "Answer. STRAY", ""), stItemDone(0, stMsg("Answer."))), one("text|||Answer. STRAY|"), one("text|||Answer.|"), one(stNoteDiffers)},
		{"withdrawn while waiting", one(stAdded(0, stMsg("")), stAdded(1, stMsg("")), stDelta(1, text, "Answer. STRAY", ""), stItemDone(1, stMsg("Answer.")), stItemDone(0, stMsg("First"))),
			one("text|||First|", "text|||Answer.|"), nil, nil},
		{"withdrawn whole", one(stAdded(0, stMsg("")), stDelta(0, text, "STRAY", ""), stItemDone(0, stMsg(""))), one("text|||STRAY|"), one(), one(stNoteDiffers)},
		{"done without a content list keeps the deltas", one(stAdded(0, stMsg("")), stDelta(0, text, "A", ""), stItemDone(0, `{"type":"message"}`)), one("text|||A|"), nil, nil},
		{"an earlier part changed", one(stAdded(0, stMsg("")), stDelta(0, text, "a", `,"content_index":0`), stDelta(0, text, "b", `,"content_index":1`),
			stItemDone(0, `{"type":"message","content":[{"type":"output_text","text":"aX"},{"type":"output_text","text":"bY"}]}`)), one("text|||abXY|"), one("text|||aXbY|"), one(stNoteDiffers)},
		{"one part changed, another grew", one(stAdded(0, stMsg("")), stDelta(0, text, "a", `,"content_index":0`), stDelta(0, text, "b", `,"content_index":1`),
			stItemDone(0, `{"type":"message","content":[{"type":"output_text","text":"X"},{"type":"output_text","text":"bY"}]}`)), one("text|||ab|"), one("text|||XbY|"), one(stNoteDiffers)},
		{"done twice, the first counts", one(stAdded(0, stMsg("")), stDelta(0, text, "A", ""), stItemDone(0, stMsg("A")), stItemDone(0, stMsg("B"))), one("text|||A|"), nil, one()},
		{"arguments differ", one(stAdded(0, stFn("c", "f", "")), stDelta(0, "function_call_arguments", `{"a":1}`, ""), stItemDone(0, stFn("c", "f", `{"a":2}`))),
			one(`tool_use|c|f|{"a":1}|`), one(`tool_use|c|f|{"a":2}|`), one(stNoteDiffers)},

		{"abandoned item, already shown", one(stAdded(0, stMsg("")), stAdded(1, stMsg("")), stDelta(1, text, "ABANDONED", ""), stItemDone(0, stMsg("Answer."))), one("text|||Answer.|", "text|||ABANDONED|"), nil, one(stNoteClosed)},
		{"abandoned buffered item", one(stAdded(0, `{"type":"web_search_call","id":"w"}`), stAdded(1, stMsg("")), stDelta(1, text, "ABANDONED", "")), one(), nil, one(stNoteUnfinished + " (x2)")},
		{"other part types", one(stItemDone(0, `{"type":"message","content":[{"type":"output_text","text":"A"},{"type":"reasoning_text","text":" HIDDEN"},{"type":"summary_text","text":" SUM"},{"text":"x"}]}`)), one("text|||A|"), nil, nil},
		{"developer role", one(stItemDone(0, foreign("developer"))), one(), nil, one(stNoteRole)},
		{"user role, streamed", one(stAdded(0, `{"type":"message","role":"user"}`), stDelta(0, text, "HIDDEN", ""), stItemDone(0, foreign("user"))), one(), nil, one(stNoteRole)},
		{"role known only in done", one(stAdded(0, `{"type":"message"}`), stDelta(0, text, "HIDDEN", ""), stItemDone(0, foreign("tool"))), one("text|||HIDDEN|"), one(), one(stNoteRole)},
		{"text on a reasoning item", one(stAdded(0, `{"type":"reasoning","id":"r"}`), stDelta(0, text, "HIDDEN", `,"item_id":"r"`), stItemDone(0, `{"type":"reasoning","id":"r","summary":[],"encrypted_content":"E"}`)), one("thinking||||" + sig), nil, nil},
		{"id of one kind, index of another", one(stAdded(0, `{"type":"reasoning","id":"r"}`), stAdded(1, `{"type":"message","id":"m"}`), stDelta(0, text, "X", `,"item_id":"m"`), stDelta(1, text, "Y", `,"item_id":"r"`),
			stItemDone(0, `{"type":"reasoning","id":"r"}`), stItemDone(1, `{"type":"message","id":"m","content":[]}`)), one(), nil, nil},
		{"agent_message", one(stAdded(0, `{"type":"agent_message","id":"g"}`), stDelta(0, text, "HIDDEN", `,"item_id":"g"`), stItemDone(0, `{"type":"agent_message","id":"g","content":[{"type":"output_text","text":"HIDDEN"}]}`)), one(), nil, nil},
		{"agent_message never announced", one(stDelta(0, text, "HIDDEN", `,"item_id":"g"`), stItemDone(0, `{"type":"agent_message","id":"g"}`), stItemDone(1, stMsg("A"))), one("text|||A|"), nil, one(stNoteUnknown)},
		{"arguments never announced", one(stDelta(0, "function_call_arguments", "{}", "")), one(), nil, one(stNoteUnknown)},
		{"raw reasoning next to a summary", one(stDelta(0, "reasoning_text", "RAW", `,"item_id":"r"`), stDelta(0, "reasoning_summary_text", "SUM", `,"item_id":"r","summary_index":0`),
			stItemDone(0, `{"type":"reasoning","id":"r","summary":[{"type":"summary_text","text":"SUM"}],"content":[{"type":"reasoning_text","text":"RAW"}],"encrypted_content":"E"}`)), one("thinking|||SUM|" + sig), nil, nil},

		{"unfinished call without arguments", one(stAdded(0, stFn("c", "f", "")), stItemDone(1, stMsg("A"))), one("text|||A|"), nil, one(stNoteUnfinished)},
		{"cut call without arguments", one(stItemDone(0, `{"type":"function_call","call_id":"c","name":"f","arguments":"","status":"incomplete"}`)), one(), nil, one(stNoteUnfinished)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, cl := stRun(t, stTestCtx, stJoin(one(stCreated), c.evs, one(stDone))...)
			if got := cl.strings(); !reflect.DeepEqual(got, c.stream) {
				t.Errorf("stream  %q\nwant    %q", got, c.stream)
			}
			want := c.message
			if want == nil {
				want = c.stream
			}
			m := s.Message()
			if m == nil {
				t.Fatalf("no message: %v", s.Failed())
			}
			if got := stFromMessage(t, m); !reflect.DeepEqual(got, append([]string{}, want...)) {
				t.Errorf("message %q\nwant    %q", got, want)
			}
			if c.notes != nil && !reflect.DeepEqual(append([]string{}, s.Notes()...), c.notes) {
				t.Errorf("notes %q, want %q", s.Notes(), c.notes)
			}
			for _, n := range s.Notes() {
				for _, leak := range []string{"HIDDEN", "Notes", "STRAY", "ABANDONED"} {
					if strings.Contains(n, leak) {
						t.Errorf("note with content: %q", n)
					}
				}
			}
		})
	}
}

func TestStStreamEndTurnFalse(t *testing.T) {
	done := `{"type":"response.completed","response":{"end_turn":false}}`
	s, cl := stRun(t, stTestCtx, stItemDone(0, stMsg("I will run it.")), done)
	if cl.stop != "end_turn" || !reflect.DeepEqual(s.Notes(), []string{stNoteEndTurn}) {
		t.Errorf("%s %q", cl.stop, s.Notes())
	}
	s, cl = stRun(t, stTestCtx, stItemDone(0, stFn("c", "f", "{}")), done)
	if cl.stop != "tool_use" || s.Notes() != nil {
		t.Errorf("%s %q", cl.stop, s.Notes())
	}
}

func TestStStreamEnded(t *testing.T) {
	for _, ev := range []string{stDone, `{"type":"response.incomplete"}`, `{"type":"response.failed"}`, `{"type":"error"}`} {
		s := NewStream(stTestCtx)
		s.Push(stJSON(t, stCreated))
		if s.Ended() {
			t.Error("ended too early")
		}
		if s.Push(stJSON(t, ev)); !s.Ended() {
			t.Errorf("not ended after %s", ev)
		}
	}
}

// The reviewer's fuzz: items may lack their done event, and the final output repeats everything. Finished items come
// out whole and once, in order; an unfinished one only as a prefix of itself, and never a second time.
func TestStStreamUnfinishedFuzz(t *testing.T) {
	for seed := int64(0); seed < 5000; seed++ {
		r := rand.New(rand.NewSource(seed))
		type tItem struct {
			evs  []string
			text string
			done bool
		}
		var items []tItem
		var output []string
		for i := 0; i < 1+r.Intn(4); i++ {
			id, done := fmt.Sprintf("i%d", i), r.Intn(4) > 0
			var it tItem
			var head, full, delta string
			switch r.Intn(4) {
			case 0:
				it.text = fmt.Sprintf("t%d-%d", i, r.Intn(1000))
				head = `{"type":"message","id":"` + id + `"}`
				full = `{"type":"message","id":"` + id + `","content":[{"type":"output_text","text":"` + it.text + `"}]}`
				delta = "output_text"
			case 1:
				it.text = fmt.Sprintf(`{"a%d":%d}`, i, r.Intn(1000))
				head = stFn("c"+id, "N", "")
				full = stFn("c"+id, "N", it.text)
				delta = "function_call_arguments"
			case 2:
				it.text = EncodeSignature("gpt-x", "E"+id)
				head = `{"type":"reasoning","id":"` + id + `"}`
				full = `{"type":"reasoning","id":"` + id + `","summary":[],"encrypted_content":"E` + id + `"}`
			default:
				it.text = "q" + id
				head = `{"type":"web_search_call","id":"` + id + `"}`
				full = stSearchItem(id, it.text)
			}
			it.evs = append(it.evs, stAdded(i, head))
			if delta != "" {
				it.evs = append(it.evs, stDelta(i, delta, it.text[:r.Intn(len(it.text)+1)], ""))
			}
			if done {
				it.evs = append(it.evs, stItemDone(i, full))
			}
			it.done = done
			items, output = append(items, it), append(output, full)
		}
		pos := make([]int, len(items))
		evs := []string{stCreated}
		for {
			var can []int
			for i := range items {
				if pos[i] < len(items[i].evs) && (i == 0 || pos[i-1] > 0) {
					can = append(can, i)
				}
			}
			if len(can) == 0 {
				break
			}
			i := can[r.Intn(len(can))]
			evs = append(evs, items[i].evs[pos[i]])
			pos[i]++
		}
		evs = append(evs, `{"type":"response.completed","response":{"output":[`+strings.Join(output, ",")+`]}}`)
		_, cl := stRun(t, stTestCtx, evs...)
		var got []string
		for _, b := range cl.blocks {
			switch b.kind {
			case "web_search_tool_result":
			case "server_tool_use":
				got = append(got, stStr(stJSON(t, b.text)["query"]))
			default:
				got = append(got, b.text+b.sig)
			}
		}
		for _, it := range items {
			switch {
			case len(got) > 0 && got[0] == it.text:
				got = got[1:]
			case it.done:
				t.Fatalf("seed %d: lost %q, left %q\n%s", seed, it.text, got, strings.Join(evs, "\n"))
			case len(got) > 0 && strings.HasPrefix(it.text, got[0]):
				got = got[1:]
			}
		}
		if len(got) > 0 {
			t.Fatalf("seed %d: extra %q\n%s", seed, got, strings.Join(evs, "\n"))
		}
	}
}
