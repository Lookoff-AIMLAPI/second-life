package route

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestResolve(t *testing.T) {
	c := Default()
	c.Slots["opus"] = Target{AIMLAPI, "zhipu/glm-5.3", "GLM 5.3", 200_000}
	c.Extra = []Target{{AIMLAPI, "anthropic/claude-sonnet-5.5", "", 0}, {AIMLAPI, "Moonshot/Kimi-K3", "", 0}}
	if err := c.Valid(); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{
		"opus": "aimlapi zhipu/glm-5.3", "claude-opus-5-5": "aimlapi zhipu/glm-5.3", "claude-opus-5-5[1m]": "aimlapi zhipu/glm-5.3",
		"fable": "codex gpt-6-astra", "claude-sonnet-5-5": "codex gpt-5.6-terra", "Claude-Haiku-5-5": "codex gpt-5.6-luna",
		"anthropic/claude-sonnet-5.5": "aimlapi anthropic/claude-sonnet-5.5", "moonshot/kimi-k3": "aimlapi Moonshot/Kimi-K3",
		"gpt-5.6-sol": "codex gpt-5.6-sol", "gpt-9": "codex gpt-9",
		"gemini": "", "gpt-": "", "openai/gpt-9": "", "a b": "", "x\nopus": "", "": "", strings.Repeat("opus", 60): "",
	} {
		got, err := c.Resolve(id)
		s := ""
		if err == nil {
			s = got.Provider + " " + got.Model
		}
		if s != want {
			t.Errorf("%q: %q, want %q", id, s, want)
		}
	}
}

func TestValid(t *testing.T) {
	for name, f := range map[string]func(*Config){
		"provider":     func(c *Config) { c.Slots["opus"] = Target{Provider: "x", Model: "m"} },
		"missing slot": func(c *Config) { delete(c.Slots, "haiku") },
		"strange slot": func(c *Config) { c.Slots["mythos"] = c.Slots["opus"] },
		"id":           func(c *Config) { c.Slots["opus"] = Target{Provider: Codex, Model: "a b"} },
		"name":         func(c *Config) { c.Slots["opus"] = Target{Provider: Codex, Model: "a", Name: "x\ny"} },
		"context":      func(c *Config) { c.Slots["opus"] = Target{Provider: Codex, Model: "a", Context: -1} },
		"twice": func(c *Config) {
			c.Extra = []Target{{Provider: AIMLAPI, Model: "a/b"}, {Provider: AIMLAPI, Model: "A/B"}}
		},
	} {
		c := Default()
		f(&c)
		if c.Valid() == nil {
			t.Error(name, "accepted")
		}
	}
}

func TestLoadSave(t *testing.T) {
	h := t.TempDir() + "/h"
	c, err := Load(h)
	if err != nil || !reflect.DeepEqual(c, Default()) {
		t.Fatal(c, err)
	}
	c.Extra = []Target{{AIMLAPI, "a/b", "B", 1}}
	if err := Save(h, c); err != nil {
		t.Fatal(err)
	}
	if got, err := Load(h); err != nil || !reflect.DeepEqual(got, c) {
		t.Fatal(got, err)
	}
	if st, _ := os.Stat(Path(h)); os.PathSeparator == '/' && st.Mode().Perm() != 0o600 {
		t.Fatal(st.Mode())
	}
	if l, _ := os.ReadDir(h); len(l) != 1 {
		t.Fatal("leftovers", l)
	}
	for _, bad := range []string{"{", `{"slots":{}}`} {
		_ = os.WriteFile(Path(h), []byte(bad), 0o600)
		if _, err := Load(h); err == nil {
			t.Fatal("accepted", bad)
		}
	}
	if Save(h, Config{}) == nil {
		t.Fatal("saved an empty config")
	}
}
