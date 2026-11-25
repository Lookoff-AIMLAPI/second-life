package translate

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/Lookoff-AIMLAPI/second-life/internal/dto"
)

func stopRun(seqs []string, chunks ...string) (stream string, reason, seq any, m *dto.MessageResponse) {
	s := NewStream(dto.StreamContext{PublicModel: "m", UpstreamModel: "u", StopSeqs: seqs, InputEstimate: 77})
	push := func(ev map[string]any) {
		for _, e := range s.Push(ev) {
			switch e.Name {
			case "content_block_delta":
				if d := e.Data["delta"].(map[string]any); d["type"] == "text_delta" {
					stream += d["text"].(string)
				}
			case "message_delta":
				d := e.Data["delta"].(map[string]any)
				reason, seq = d["stop_reason"], d["stop_sequence"]
			}
		}
	}
	full := strings.Join(chunks, "")
	push(map[string]any{"type": "response.created", "response": map[string]any{"id": "r"}})
	push(map[string]any{"type": "response.output_item.added", "output_index": 0.0, "item": map[string]any{"type": "message", "id": "m", "role": "assistant"}})
	for _, c := range chunks {
		push(map[string]any{"type": "response.output_text.delta", "output_index": 0.0, "item_id": "m", "content_index": 0.0, "delta": c})
	}
	push(map[string]any{"type": "response.output_item.done", "output_index": 0.0, "item": map[string]any{"type": "message", "id": "m", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": full}}}})
	push(map[string]any{"type": "response.output_item.done", "output_index": 1.0, "item": map[string]any{"type": "function_call", "id": "f", "call_id": "c", "name": "n", "arguments": "{}"}})
	push(map[string]any{"type": "response.completed", "response": map[string]any{"id": "r", "usage": map[string]any{"input_tokens": 5.0, "output_tokens": 2.0}}})
	return stream, reason, seq, s.Message()
}

func text(m *dto.MessageResponse) (out string, calls int) {
	for _, b := range m.Content {
		if v, ok := b["text"].(string); ok {
			out += v
		}
		if b["type"] == "tool_use" {
			calls++
		}
	}
	return
}

func TestStopSequences(t *testing.T) {
	for _, c := range []struct {
		name   string
		seqs   []string
		chunks []string
		want   string
		seq    any
	}{
		{"inside a chunk", []string{"END"}, []string{"a END b"}, "a ", "END"},
		{"split over chunks", []string{"</block>"}, []string{"yes</", "blo", "ck> more", " and more"}, "yes", "</block>"},
		{"earliest wins", []string{"ZZ", "Y"}, []string{"1Y2ZZ"}, "1", "Y"},
		{"false start", []string{"</block>"}, []string{"a</bl", "ue>b"}, "a</blue>b", nil},
		{"held at the end", []string{"END"}, []string{"abc EN"}, "abc EN", nil},
		{"at the start", []string{"END"}, []string{"END tail"}, "", "END"},
		{"none set", nil, []string{"a END b"}, "a END b", nil},
		{"empty one ignored", []string{"", "END"}, []string{"aENDb"}, "a", "END"},
		{"unicode", []string{"стоп"}, []string{"да ст", "оп нет"}, "да ", "стоп"},
	} {
		t.Run(c.name, func(t *testing.T) {
			stream, reason, seq, m := stopRun(c.seqs, c.chunks...)
			body, calls := text(m)
			if stream != c.want || body != c.want || seq != c.seq {
				t.Fatalf("stream %q message %q seq %v", stream, body, seq)
			}
			if c.seq != nil && (reason != "stop_sequence" || calls != 0 || m.StopSequence == nil || *m.StopSequence != c.seq) {
				t.Fatal("not stopped", reason, calls, m.StopSequence)
			}
			if c.seq == nil && (reason != "tool_use" || calls != 1 || m.StopSequence != nil) {
				t.Fatal("stopped", reason, calls)
			}
			if c.seq != nil && (m.Usage.InputTokens != 77 || m.Usage.OutputTokens < 1) || c.seq == nil && (m.Usage.InputTokens != 5 || m.Usage.OutputTokens != 2) {
				t.Fatal("usage lost", m.Usage)
			}
		})
	}
}

func TestStopSequencesRandom(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	for i := range 5000 {
		var b strings.Builder
		for range r.Intn(30) {
			b.WriteByte("ab|"[r.Intn(3)])
		}
		full, seqs := b.String(), []string{"ab|", "||a"}
		var chunks []string
		for rest := full; rest != ""; {
			n := 1 + r.Intn(len(rest))
			chunks, rest = append(chunks, rest[:n]), rest[n:]
		}
		at := len(full)
		for _, q := range seqs {
			if j := strings.Index(full, q); j >= 0 {
				at = min(at, j)
			}
		}
		want := full[:at]
		stream, _, _, m := stopRun(seqs, chunks...)
		body, _ := text(m)
		if stream != want || body != want {
			t.Fatal(fmt.Sprintf("%d: %q %v -> %q %q want %q", i, full, chunks, stream, body, want))
		}
	}
}
