package translate

import (
	"bytes"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/Lookoff-AIMLAPI/second-life/internal/dto"
)

func stReadAll(r io.Reader) ([]map[string]any, error) {
	var out []map[string]any
	err := ReadSSE(r, func(ev map[string]any) error {
		out = append(out, ev)
		return nil
	})
	return out, err
}

func TestStReadSSE(t *testing.T) {
	a, b, bad := map[string]any{"type": "a"}, map[string]any{"type": "b"}, map[string]any{"type": stMalformed}
	cases := []struct {
		name, in string
		want     []map[string]any
	}{
		{"empty", "", nil},
		{"two events", "event: a\ndata: {\"type\":\"a\"}\n\nevent: b\ndata: {\"type\":\"b\"}\n\n", []map[string]any{a, b}},
		{"crlf", "event: a\r\ndata: {\"type\":\"a\"}\r\n\r\ndata: {\"type\":\"b\"}\r\n\r\n", []map[string]any{a, b}},
		{"multi-line data", "data: {\ndata: \"type\":\ndata: \"a\"}\n\n", []map[string]any{a}},
		{"comments and other fields", ": ping\n\nid: 7\nretry: 5\n: x\ndata: {\"type\":\"a\"}\n: y\n\n", []map[string]any{a}},
		{"no space after colon", "data:{\"type\":\"a\"}\n\n", []map[string]any{a}},
		{"event line without data", "event: a\n\ndata: {\"type\":\"b\"}\n\n", []map[string]any{b}},
		{"bom", "\xef\xbb\xbfdata: {\"type\":\"a\"}\n\n", []map[string]any{a}},
		{"final event, one newline", "data: {\"type\":\"a\"}\n", []map[string]any{a}},
		{"final event, no newline", "data: {\"type\":\"a\"}\n\ndata: {\"type\":\"b\"}", []map[string]any{a, b}},
		{"final event ends in CR", "data: {\"type\":\"a\"}\r", []map[string]any{a}},
		{"done marker", "data: {\"type\":\"a\"}\n\ndata: [DONE]\n\n", []map[string]any{a}},
		{"done marker at eof", "data: [DONE]", nil},
		{"empty data", "data:\n\ndata: {\"type\":\"a\"}\n\n", []map[string]any{a}},
		{"type taken from the event line", "event: a\ndata: {}\n\ndata: {}\n\n", []map[string]any{a, {}}},
		{"malformed", "data: {\"type\":\"a\"}\n\ndata: {oops\n\ndata: {\"type\":\"b\"}\n\n", []map[string]any{a, bad, b}},
		{"truncated at eof", "data: {\"type\":", []map[string]any{bad}},
		{"trailing garbage", "data: {\"type\":\"a\"} x\n\n", []map[string]any{bad}},
		{"not an object", "data: [1]\n\n", []map[string]any{bad}},
		{"null", "event: a\ndata: null\n\n", []map[string]any{bad}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for name, r := range map[string]io.Reader{
				"whole":   strings.NewReader(c.in),
				"by byte": iotest.OneByteReader(strings.NewReader(c.in)),
			} {
				got, err := stReadAll(r)
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				if !reflect.DeepEqual(got, c.want) {
					t.Errorf("%s: got %v, want %v", name, got, c.want)
				}
			}
		})
	}
}

func TestStReadSSELongLine(t *testing.T) {
	long := strings.Repeat("x", 6<<20)
	got, err := stReadAll(strings.NewReader("data: {\"type\":\"a\",\"delta\":\"" + long + "\"}\n\ndata: {\"type\":\"b\"}\n\n"))
	if err != nil || len(got) != 2 || got[0]["delta"] != long {
		t.Fatalf("err %v, %d events", err, len(got))
	}
}

// 32 MB is the cap for one event, in one line or many; events and comments do not add up.
func TestStReadSSECap(t *testing.T) {
	chunk := strings.Repeat("x", 1<<20)
	many := func(line string, n int) io.Reader { return strings.NewReader(strings.Repeat(line, n)) }
	for _, c := range []struct {
		name string
		in   io.Reader
		ok   bool
	}{
		{"one line", io.MultiReader(strings.NewReader("data: \""), many(chunk, 33)), false},
		{"many lines", many("data: "+chunk+"\n", 33), false},
		{"endless comment", many(":"+chunk, 33), false},
		{"several events", many("data: {\"d\":\""+chunk+"\"}\n\n", 33), true},
		{"many comments", many(":"+chunk+"\n", 33), true},
		{"under the cap", io.MultiReader(strings.NewReader("data: {\"d\":\""), many(chunk, 31), strings.NewReader("\"}\n\n")), true},
	} {
		n := 0
		err := ReadSSE(c.in, func(map[string]any) error { n++; return nil })
		if (err == nil) != c.ok || !c.ok && (!errors.Is(err, errStEventSize) || n != 0) {
			t.Errorf("%s: %d events, err %v", c.name, n, err)
		}
	}
}

func TestStMalformedIsCounted(t *testing.T) {
	s, cl := NewStream(stTestCtx), &stClient{t: t}
	in := "data: " + stAdded(0, stMsg("")) + "\n\ndata: {SECRET\n\ndata: " + stDelta(0, "output_text", "Hel", "") + "\n\ndata: [1]\n\ndata: " +
		stItemDone(0, stMsg("Hello")) + "\n\ndata: " + stDone + "\n\n"
	if err := ReadSSE(strings.NewReader(in), func(ev map[string]any) error { cl.feed(s.Push(ev)); return nil }); err != nil {
		t.Fatal(err)
	}
	if got := cl.strings(); !cl.stopped || !reflect.DeepEqual(got, []string{"text|||Hello|"}) {
		t.Errorf("%q", got)
	}
	if got := s.Notes(); !reflect.DeepEqual(got, []string{stNoteMalformed + " (x2)"}) {
		t.Errorf("notes %q", got)
	}
}

func TestStReadSSEStops(t *testing.T) {
	stop := errors.New("stop")
	n := 0
	err := ReadSSE(strings.NewReader("data: {}\n\ndata: {}\n\n"), func(map[string]any) error {
		n++
		return stop
	})
	if !errors.Is(err, stop) || n != 1 {
		t.Fatalf("err %v after %d calls", err, n)
	}
}

func TestStReadSSEReadError(t *testing.T) {
	boom := errors.New("boom")
	// The unterminated second event must not be dispatched: the read broke inside it.
	got, err := stReadAll(io.MultiReader(strings.NewReader("data: {\"type\":\"a\"}\n\ndata: {\"type\":\"b\"}"), iotest.ErrReader(boom)))
	if !errors.Is(err, boom) || len(got) != 1 {
		t.Fatalf("err %v, %d events", err, len(got))
	}
}

// An event must be delivered when its blank line arrives, not when the next event does.
func TestStReadSSEDeliversPromptly(t *testing.T) {
	pr, pw := io.Pipe()
	got := make(chan struct{})
	fin := make(chan error, 1)
	go func() {
		fin <- ReadSSE(pr, func(map[string]any) error {
			close(got)
			return nil
		})
	}()
	if _, err := pw.Write([]byte("data: {}\n\n")); err != nil {
		t.Fatal(err)
	}
	<-got
	pw.Close()
	if err := <-fin; err != nil {
		t.Fatal(err)
	}
}

type stCountWriter struct {
	bytes.Buffer
	calls int
}

func (w *stCountWriter) Write(p []byte) (int, error) {
	w.calls++
	return w.Buffer.Write(p)
}

func TestStWriteEvent(t *testing.T) {
	var w stCountWriter
	ev := dto.Event{Name: "ping", Data: map[string]any{"type": "ping", "t": "a\nb <c> \u2028"}}
	if err := WriteEvent(&w, ev); err != nil {
		t.Fatal(err)
	}
	if w.calls != 1 {
		t.Errorf("%d Write calls", w.calls)
	}
	s := w.String()
	if !strings.HasPrefix(s, "event: ping\ndata: {") || !strings.HasSuffix(s, "}\n\n") || strings.Count(s, "\n") != 3 {
		t.Errorf("bad frame %q", s)
	}
	got, err := stReadAll(strings.NewReader(s))
	if err != nil || !reflect.DeepEqual(got[0], ev.Data) {
		t.Errorf("round trip: %v %v", got, err)
	}
	if err := WriteEvent(&w, dto.Event{Name: "x", Data: map[string]any{"f": func() {}}}); err == nil || w.calls != 1 {
		t.Errorf("unmarshalable data: err %v, %d calls", err, w.calls)
	}
	boom := errors.New("boom")
	if err := WriteEvent(stErrWriter{boom}, ev); !errors.Is(err, boom) {
		t.Errorf("write error lost: %v", err)
	}
}

type stErrWriter struct{ err error }

func (w stErrWriter) Write([]byte) (int, error) { return 0, w.err }
