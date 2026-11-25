package aimlapi

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/Lookoff-AIMLAPI/second-life/internal/dto"
)

const stream = ": hello\n\nevent: ping\ndata: {\"type\": \"ping\"}\n\n" +
	"event: message_start\ndata: {\"type\":\"message_start\", \"message\": {\"id\":\"m\", \"model\" : \"claude-opus-5-5\",\"usage\":{\"input_tokens\":10,\"cache_creation_input_tokens\":3,\"cache_read_input_tokens\":4,\"output_tokens\":1}}, \"n\":1.0}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"message_stop \\\"model\\\":\\\"x\\\" \\\"error\\\"\"}}\n\n" +
	"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":42,\"input_tokens\":null}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\",\"meta\":{\"model\":\"y\"}}\n\n"

const whole = `{ "id":"m","type":"message", "content":[{"type":"text","text":"\"model\":\"x\""}], "model":"claude-opus-5-5" ,"usage":{"input_tokens":5,"output_tokens":6}, "n":1.0}`

var streamUsage = dto.Usage{InputTokens: 10, OutputTokens: 42, CacheCreationInputTokens: 3, CacheReadInputTokens: 4}

func TestMeterSplits(t *testing.T) {
	bare := strings.ReplaceAll(stream, "event: content_block_delta\n", "")
	for name, in := range map[string]string{"lf": stream, "crlf": strings.ReplaceAll(stream, "\n", "\r\n"), "bom": "\xef\xbb\xbf" + stream, "no event line": bare} {
		for i := range len(in) + 1 {
			m := NewMeter()
			var seen []string
			m.Seen = func(e string) { seen = append(seen, e) }
			if name == "bom" && i < 3 {
				continue
			}
			io.WriteString(m, in[:i])
			if m.Done() && i < len(in)-2 {
				t.Fatalf("%s: done at %d of %d", name, i, len(in))
			}
			io.WriteString(m, in[i:])
			if m.Usage() != streamUsage || !m.Done() || m.Failed() != nil || len(seen) != 5 || seen[1] != "message_start" {
				t.Fatalf("%s split at %d: %+v %v %v %v", name, i, m.Usage(), m.Done(), m.Failed(), seen)
			}
		}
	}
	m := NewMeter()
	if _, err := io.Copy(m, iotest.OneByteReader(strings.NewReader(stream))); err != nil || m.Usage() != streamUsage || !m.Done() {
		t.Errorf("byte by byte: %+v", m)
	}
}

func TestMeterJSON(t *testing.T) {
	for i := range len(whole) + 1 {
		m := NewMeter()
		io.WriteString(m, whole[:i])
		if m.Done() != (i == len(whole)) {
			t.Fatalf("done at %d", i)
		}
		io.WriteString(m, whole[i:])
		if m.Usage() != (dto.Usage{InputTokens: 5, OutputTokens: 6}) || !m.Done() || m.Failed() != nil {
			t.Fatalf("split at %d: %+v", i, m)
		}
	}
	m := NewMeter()
	io.WriteString(m, `{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`)
	if f := m.Failed(); m.Done() || f == nil || f.Status != 529 || !m.Reported() {
		t.Errorf("%+v", f)
	}
}

func TestMeterErrors(t *testing.T) {
	for _, c := range []struct {
		name, in string
		want     *dto.APIError
		reported bool
	}{
		{"anthropic", "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"busy\"}}\n\n", &dto.APIError{Status: 529, Type: "overloaded_error", Message: "busy"}, true},
		{"native link", "event: error\ndata: {\"error\":{\"message\":\"gone\",\"type\":\"api_error\",\"code\":null}}\n\n", &dto.APIError{Status: 500, Type: "api_error", Message: "gone"}, false},
		{"filter", "event: error\ndata: {\"error\":{\"message\":\"gone\"},\"code\":503}\n\n", &dto.APIError{Status: 503, Type: "overloaded_error", Message: "gone"}, false},
		{"bare frame", "data: {\"error\":{\"message\":\"gone\",\"type\":\"rate_limit_error\"}}\n\n", &dto.APIError{Status: 429, Type: "rate_limit_error", Message: "gone"}, false},
		{"funds", "event: error\ndata: {\"error\":{\"message\":\"You've run out of funds.\"},\"code\":403}\n\n", &dto.APIError{Status: 402, Type: "billing_error", Message: noBalance}, false},
		{"not JSON", "event: error\ndata: oops\n\n", &dto.APIError{Status: 500, Type: "api_error", Message: "AIMLAPI answered with an error"}, false},
		{"malformed events", "event: message_start\ndata: {\n\nevent: message_delta\ndata: {\"usage\":7}\n\ndata: [1]\n\ndata: [DONE]\n\nevent: x\n\n", nil, false},
	} {
		m := NewMeter()
		io.Copy(m, iotest.OneByteReader(strings.NewReader(c.in)))
		f := m.Failed()
		if (f == nil) != (c.want == nil) || f != nil && *f != *c.want || m.Reported() != c.reported || m.Done() {
			t.Errorf("%s: %+v %v", c.name, f, m.Reported())
		}
	}
}

func TestMeterBounds(t *testing.T) {
	m := NewMeter()
	io.WriteString(m, "data: ")
	for range 40 {
		m.Write(bytes.Repeat([]byte("x"), 1<<20))
	}
	if len(m.line) > maxEvent {
		t.Errorf("%d bytes kept", len(m.line))
	}
	io.WriteString(m, "\n\n"+stream)
	if m.Usage() != streamUsage || !m.Done() || len(m.data) != 0 {
		t.Errorf("a huge event broke the rest: %+v", m.Usage())
	}
	m = NewMeter()
	io.WriteString(m, "{")
	for range 70 {
		m.Write(bytes.Repeat([]byte(" "), 1<<20))
	}
	if len(m.body) > maxJSON || m.Done() {
		t.Errorf("%d bytes kept", len(m.body))
	}
}

func TestRewriteModel(t *testing.T) {
	const old = "claude-opus-5-5"
	for name, in := range map[string]string{"stream": stream, "crlf": strings.ReplaceAll(stream, "\n", "\r\n"), "json": whole, "no last newline": strings.Split(stream, "\n\nevent: content")[0]} {
		want := strings.Replace(in, old, `a<"b`, 1)
		want = strings.Replace(want, `a<"b`, `a<\"b`, 1)
		for i, r := range []io.Reader{
			strings.NewReader(in), iotest.OneByteReader(strings.NewReader(in)), iotest.HalfReader(strings.NewReader(in)), iotest.DataErrReader(strings.NewReader(in)),
		} {
			got, err := io.ReadAll(iotest.HalfReader(RewriteModel(r, `a<"b`)))
			if err != nil || string(got) != want {
				t.Errorf("%s %d: %v\n got %s\nwant %s", name, i, err, got, want)
			}
		}
	}
	for _, in := range []string{
		"", "{", `{"model":7}`, `{"type":"error","error":{"model":"x"}}`, "event: error\ndata: {}\n\n", "data: {\"type\":\"message_start\"}\n\n",
		"data: {\"type\":\"message_start\",\"message\":7}\n\n", "data: message_start\n\n", "data: {\"type\":\"x\",\"message\":{\"model\":\"message_start\"}}\n\n", " \n\xef\xbb", "plain",
	} {
		if got, err := io.ReadAll(RewriteModel(strings.NewReader(in), "z")); err != nil || string(got) != in {
			t.Errorf("%q became %q, %v", in, got, err)
		}
	}
	boom := errors.New("boom")
	got, err := io.ReadAll(RewriteModel(io.MultiReader(strings.NewReader(whole[:50]), iotest.ErrReader(boom)), "z"))
	if string(got) != whole[:50] || !errors.Is(err, boom) {
		t.Errorf("a cut answer: %q %v", got, err)
	}
}

// A stream must not wait for bytes that come later.
func TestRewriteModelDoesNotHold(t *testing.T) {
	pr, pw := io.Pipe()
	defer pr.Close()
	r := RewriteModel(pr, "z")
	buf := make([]byte, 1<<16)
	for _, part := range []string{": ping\n\n", "event: message_start\n", "data: {\"type\":\"message_start\",\"message\":{\"model\":\"a\"}}\n", "\nevent: x", "y\n"} {
		go io.WriteString(pw, part)
		n, err := r.Read(buf)
		if want := strings.Replace(part, `"a"`, `"z"`, 1); err != nil || string(buf[:n]) != want {
			t.Fatalf("%q %v, want %q", buf[:n], err, want)
		}
	}
	long := "data: " + strings.Repeat("x", maxHold+1<<20)
	got, _ := io.ReadAll(RewriteModel(strings.NewReader(long), "z"))
	if string(got) != long {
		t.Error("a long line changed")
	}
}

func FuzzRewriteModel(f *testing.F) {
	f.Add([]byte(stream), 3)
	f.Add([]byte(whole), 7)
	f.Fuzz(func(t *testing.T, in []byte, cut int) {
		cut = max(0, min(cut, len(in)))
		a, _ := io.ReadAll(RewriteModel(bytes.NewReader(in), "z"))
		b, _ := io.ReadAll(RewriteModel(io.MultiReader(bytes.NewReader(in[:cut]), bytes.NewReader(in[cut:])), "z"))
		if cut > 2 && !bytes.Equal(a, b) { // a byte order mark may be cut
			t.Fatalf("cut at %d: %q and %q", cut, a, b)
		}
		m := NewMeter()
		m.Write(in[:cut])
		m.Write(in[cut:])
		_ = fmt.Sprint(m.Usage(), m.Done(), m.Failed())
	})
}
