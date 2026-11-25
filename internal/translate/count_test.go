package translate

import (
	"strings"
	"testing"

	"github.com/Lookoff-AIMLAPI/second-life/internal/dto"
)

func TestReqCountTokens(t *testing.T) {
	count := func(body string) int { return CountTokens(reqParse(t, body)) }
	big := strings.Repeat("A", 400_000)
	s400 := strings.Repeat("ж", 400) // 100 tokens, counted in characters
	user := func(blocks string) string { return reqMsgs(`[{"role":"user","content":[` + blocks + `]}]`) }
	base := count(user(`{"type":"text","text":""}`))
	if base <= 0 || base > 40 {
		t.Fatalf("an empty request counts %d", base)
	}
	call := `{"role":"assistant","content":[{"type":"tool_use","id":"c","name":"A","input":{}}]},`
	cases := []struct {
		name, body string
		min, max   int // above base
	}{
		{"text", user(`{"type":"text","text":"` + s400 + `"}`), 99, 101},
		{"string content", reqMsgs(`[{"role":"user","content":"` + s400 + `"}]`), 99, 101},
		{"system", reqWith(`"system":"` + s400 + `"`), 99, 104},
		{"image", user(`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + big + `"}}`), 1600, 1600},
		{"image url", user(`{"type":"image","source":{"type":"url","url":"https://e.x/i"}}`), 1600, 1600},
		{"pdf", user(`{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"` + big + `"}}`), 3000, 3000},
		{"pdf url", user(`{"type":"document","source":{"type":"url","url":"https://e.x/a.pdf"}}`), 3000, 3000},
		{"text document", user(`{"type":"document","source":{"type":"text","data":"` + s400 + `"}}`), 99, 101},
		{"signature", reqMsgs(`[{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":"` + EncodeSignature("", big) + `"},{"type":"redacted_thinking","data":"` + big + `"}]}]`), 0, 0},
		{"arguments", reqMsgs(`[{"role":"assistant","content":[{"type":"tool_use","id":"c","name":"A","input":{"v":"` + s400 + `"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"c"}]}]`), 100, 106},
		{"tool result", reqMsgs(`[` + call + `{"role":"user","content":[{"type":"tool_result","tool_use_id":"c","content":"` + s400 + `"}]}]`), 99, 104},
		{"tool result with an image", reqMsgs(`[` + call + `{"role":"user","content":[{"type":"tool_result","tool_use_id":"c","content":[{"type":"text","text":"` + s400 + `"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + big + `"}}]}]}]`), 1699, 1704},
		{"tools", reqWith(`"tools":[{"name":"A","description":"` + s400 + `","input_schema":{"type":"object","properties":{"` + s400 + `":{}}}}]`), 200, 235},
		{"hidden deferred tools", reqWith(`"tools":[{"name":"A","defer_loading":true,"description":"` + big + `"}]`), 0, 2},
		{"search results", reqMsgs(`[{"role":"assistant","content":[{"type":"web_search_tool_result","tool_use_id":"s","content":[{"title":"T","url":"https://e.x","encrypted_content":"` + big + `"}]}]}]`), 1, 15},

		// A request that would be refused is still counted.
		{"refused: text", user(`{"type":"video"},{"type":"text","text":"` + s400 + `"}`), 99 - base, 101},
		{"refused: image and pdf", user(`{"type":"video"},{"type":"image","source":{"data":"` + big + `"}},{"type":"document","source":{"type":"base64","data":"` + big + `"}}`), 4600 - base, 4600},
		{"refused: documents", user(`{"type":"video"},{"type":"document","source":{"type":"text","data":"` + s400 + `"}},{"type":"document","source":{"type":"content","content":"` + s400 + `"}}`), 200 - base, 200},
		{"refused: signatures", reqMsgs(`[{"role":"assistant","content":[{"type":"video"},{"type":"thinking","signature":"` + big + `"},{"type":"tool_use","id":"c","name":"A","input":{"v":"` + s400 + `"}}]}]`), 100 - base, 106},
		{"refused: tool results", user(`{"type":"video"},{"type":"tool_result","tool_use_id":"c","content":[{"type":"text","text":"` + s400 + `"}]}`), 99 - base, 101},
		{"refused: no messages", `{"model":"m","messages":[],"system":"` + s400 + `","tools":[{"name":"A","description":"` + s400 + `"}]}`, 200 - base, 220},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := count(c.body) - base; got < c.min || got > c.max {
				t.Fatalf("got %d, want %d..%d", got, c.min, c.max)
			}
		})
	}
	if CountTokens(nil) != 0 || CountTokens(&dto.MessagesRequest{System: []byte(`[{`)}) != 0 {
		t.Error("nothing to count")
	}
}
