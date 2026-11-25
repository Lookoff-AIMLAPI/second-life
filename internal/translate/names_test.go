package translate

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"testing"
)

func TestReqToolName(t *testing.T) {
	hash := func(s string) string {
		sum := sha256.Sum256([]byte(s))
		return hex.EncodeToString(sum[:])[:8]
	}
	long := "mcp__" + strings.Repeat("x", 66) // 71 chars
	cases := []struct{ name, in, want string }{
		{"plain", "Read", "Read"},
		{"64 chars kept", strings.Repeat("a", 64), strings.Repeat("a", 64)},
		{"65 chars", strings.Repeat("a", 65), strings.Repeat("a", 55) + "_" + hash(strings.Repeat("a", 65))},
		{"71 chars", long, long[:55] + "_" + hash(long)},
		{"short with a dot", "a.b", "a_b_" + hash("a.b")},
		{"multibyte", "инструмент", strings.Repeat("_", 10) + "_" + hash("инструмент")},
	}
	valid := regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := reqToolName(c.in)
			if got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
			if !valid.MatchString(got) {
				t.Fatalf("%q is not a valid upstream name", got)
			}
		})
	}
	if reqToolName("a.b") == reqToolName("a b") {
		t.Error("different names must not collide after sanitizing")
	}
}
