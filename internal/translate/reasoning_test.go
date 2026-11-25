package translate

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestReqSignature(t *testing.T) {
	b64 := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	good := EncodeSignature("gpt-5.6-terra", "gAAA+/=<&>")
	if !strings.HasPrefix(good, "c2c1.") || good != EncodeSignature("gpt-5.6-terra", "gAAA+/=<&>") {
		t.Fatalf("unexpected signature %q", good)
	}
	cases := []struct {
		name, sig, model, want string
		ok                     bool
	}{
		{"round trip", good, "gpt-5.6-terra", "gAAA+/=<&>", true},
		{"spec shape", "c2c1." + b64(`{"m":"a","e":"b"}`), "a", "b", true},
		{"other model", good, "gpt-5.6-luna", "", false},
		{"foreign", "EqQBCkYIBxgCKkDe", "gpt-5.6-terra", "", false},
		{"empty", "", "gpt-5.6-terra", "", false},
		{"prefix only", "c2c1.", "", "", false},
		{"bad base64", "c2c1.!!!", "a", "", false},
		{"bad json", "c2c1." + b64(`{"m":`), "a", "", false},
		{"json null", "c2c1." + b64(`null`), "", "", false},
		{"no content", "c2c1." + b64(`{"m":"a"}`), "a", "", false},
		{"other version", "c2c2." + b64(`{"m":"a","e":"b"}`), "a", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := DecodeSignature(c.sig, c.model)
			if got != c.want || ok != c.ok {
				t.Fatalf("got %q, %v", got, ok)
			}
		})
	}
}
