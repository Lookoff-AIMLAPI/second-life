package route

import "testing"

func TestEndpointValid(t *testing.T) {
	ok := Endpoint{ID: "ep-0123abcd", Name: "My server", Protocol: Chat, BaseURL: "https://host.example/v1"}
	for _, u := range []string{"https://host.example/v1", "https://host.example", "https://host.example:8443/api/openai/v1", "http://127.0.0.1:11434/v1", "http://localhost:1234/v1", "http://[::1]:8080/v1"} {
		e := ok
		e.BaseURL = u
		if err := e.Valid(); err != nil {
			t.Error(u, err)
		}
	}
	for _, u := range []string{"", "host.example/v1", "http://host.example/v1", "http://10.0.0.5/v1", "http://127.0.0.1.evil.example/v1", "https://u:p@host/v1", "https://host/v1?x=1", "https://host/v1#f", "https://host/v1/", "ftp://host/v1", "file:///etc", "https://", "https://host/v1?", "https://host/v1#", "https://:8080/v1", "https://host/v1 ", "https://host\\v1"} {
		e := ok
		e.BaseURL = u
		if e.Valid() == nil {
			t.Error("accepted", u)
		}
	}
	for name, f := range map[string]func(*Endpoint){"id": func(e *Endpoint) { e.ID = "codex" }, "name": func(e *Endpoint) { e.Name = "a\nb" }, "no name": func(e *Endpoint) { e.Name = "" }, "protocol": func(e *Endpoint) { e.Protocol = "soap" }} {
		e := ok
		f(&e)
		if e.Valid() == nil {
			t.Error("accepted bad", name)
		}
	}
	c := Default()
	c.Slots["opus"] = Target{Provider: ok.ID, Model: "llama-4"}
	if c.Valid() == nil {
		t.Error("a slot on an endpoint that is not there")
	}
	c.Endpoints = []Endpoint{ok}
	if err := c.Valid(); err != nil {
		t.Error(err)
	}
	if got, err := c.Resolve("claude-opus-5-5"); err != nil || got.Provider != ok.ID {
		t.Error(got, err)
	}
	c.Endpoints = []Endpoint{ok, ok}
	if c.Valid() == nil {
		t.Error("twice")
	}
}
