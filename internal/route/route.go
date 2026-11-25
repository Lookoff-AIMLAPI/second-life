// Package route decides which model answers for each Claude name. The owner picks it; the defaults are Codex models.
package route

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Lookoff-AIMLAPI/second-life/internal/dto"
)

const (
	Codex   = "codex"
	AIMLAPI = "aimlapi"
)

// What a custom endpoint speaks.
const (
	Chat      = "chat"      // OpenAI Chat Completions: BaseURL + /chat/completions
	Responses = "responses" // OpenAI Responses: BaseURL + /responses
	Anthropic = "anthropic" // Anthropic Messages: BaseURL + /messages
)

// Endpoint is a server the owner added. Its key, if it has one, is in the key store under its id.
type Endpoint struct {
	ID       string `json:"id"`       // "ep-" and 8 hex digits; also the provider of its targets
	Name     string `json:"name"`     // for people
	Protocol string `json:"protocol"` // Chat, Responses or Anthropic
	BaseURL  string `json:"baseUrl"`  // up to and including the version, such as https://host/v1
}

var endpointID = regexp.MustCompile(`^ep-[0-9a-f]{8}$`)

// Valid allows https anywhere and plain http only to this computer: the key would travel in the open.
func (e Endpoint) Valid() error {
	if !endpointID.MatchString(e.ID) {
		return errors.New("an endpoint has no valid id")
	}
	if e.Name == "" || len(e.Name) > 60 || strings.ContainsFunc(e.Name, func(r rune) bool { return r < ' ' || r == 0x7f }) {
		return errors.New("an endpoint needs a short name")
	}
	if e.Protocol != Chat && e.Protocol != Responses && e.Protocol != Anthropic {
		return fmt.Errorf("unknown protocol %q", e.Protocol)
	}
	u, err := url.Parse(e.BaseURL)
	if err != nil || len(e.BaseURL) > 300 || u.Hostname() == "" || strings.ContainsAny(e.BaseURL, "?#\\ ") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || strings.HasSuffix(u.Path, "/") {
		return errors.New("the address must look like https://host/v1, without a slash at the end")
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	local := strings.EqualFold(host, "localhost") || ip != nil && ip.IsLoopback()
	if u.Scheme != "https" && !(u.Scheme == "http" && local) {
		return errors.New("the address must be https; plain http works only for this computer")
	}
	return nil
}

// Slots are the Claude families, in display order.
var Slots = []string{"fable", "opus", "sonnet", "haiku"}

// Target is a model that answers.
type Target struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Name     string `json:"name,omitempty"`    // for people, such as "GLM 5.3"
	Context  int    `json:"context,omitempty"` // tokens the model holds, 0 when unknown
}

// Config is what the picker saves. Extra models are reachable by their own id.
type Config struct {
	Slots map[string]Target `json:"slots"`
	Extra []Target          `json:"extra,omitempty"`

	Endpoints []Endpoint `json:"endpoints,omitempty"`
}

// CodexModels are the models a ChatGPT login serves.
var CodexModels = []Target{
	{Codex, "gpt-6-astra", "GPT-6 Astra", 272_000},
	{Codex, "gpt-5.6-sol", "GPT-5.6 Sol", 272_000},
	{Codex, "gpt-5.6-terra", "GPT-5.6 Terra", 272_000},
	{Codex, "gpt-5.6-luna", "GPT-5.6 Luna", 272_000},
}

// Default is Fable on Astra, Opus on Sol, Sonnet on Terra, Haiku on Luna.
func Default() Config {
	c := Config{Slots: map[string]Target{}}
	for i, s := range Slots {
		c.Slots[s] = CodexModels[i]
	}
	return c
}

var idShape = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/\[\]-]{0,127}$`)

// Valid checks a config from the picker or the disk.
func (c Config) Valid() error {
	check := func(t Target) error {
		if _, custom := c.Endpoint(t.Provider); t.Provider != Codex && t.Provider != AIMLAPI && !custom {
			return fmt.Errorf("unknown provider %q", t.Provider)
		}
		if !idShape.MatchString(t.Model) {
			return errors.New("a model id has characters that are not allowed")
		}
		if len(t.Name) > 100 || strings.ContainsFunc(t.Name, func(r rune) bool { return r < ' ' || r == 0x7f }) {
			return errors.New("a model name is too long or has control characters")
		}
		if t.Context < 0 || t.Context > 10_000_000 {
			return errors.New("a context size is out of range")
		}
		return nil
	}
	for _, s := range Slots {
		t, ok := c.Slots[s]
		if !ok {
			return fmt.Errorf("no model for %s", s)
		}
		if err := check(t); err != nil {
			return err
		}
	}
	if len(c.Slots) != len(Slots) {
		return errors.New("unknown slot")
	}
	if len(c.Endpoints) > 20 {
		return errors.New("too many endpoints")
	}
	ids := map[string]bool{}
	for _, e := range c.Endpoints {
		if err := e.Valid(); err != nil {
			return err
		}
		if ids[e.ID] {
			return errors.New("an endpoint is listed twice")
		}
		ids[e.ID] = true
	}
	if len(c.Extra) > 100 {
		return errors.New("too many extra models")
	}
	seen := map[string]bool{}
	for _, t := range c.Extra {
		if err := check(t); err != nil {
			return err
		}
		if k := strings.ToLower(t.Model); seen[k] {
			return errors.New("an extra model is listed twice")
		} else {
			seen[k] = true
		}
	}
	return nil
}

// Endpoint finds a custom endpoint by its id.
func (c Config) Endpoint(id string) (Endpoint, bool) {
	for _, e := range c.Endpoints {
		if e.ID == id {
			return e, true
		}
	}
	return Endpoint{}, false
}

// Path is the config file of a home directory.
func Path(home string) string { return filepath.Join(home, "models.json") }

// Load returns the defaults when there is no file. A damaged file is an error, not silently the defaults: the wrong
// model may cost money.
func Load(home string) (Config, error) {
	b, err := os.ReadFile(Path(home))
	if errors.Is(err, fs.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return Config{}, errors.New("models.json is damaged; delete it to get the defaults")
	}
	if err := c.Valid(); err != nil {
		return Config{}, fmt.Errorf("models.json: %w", err)
	}
	return c, nil
}

// Save writes the file in one step with 0600.
func Save(home string, c Config) error {
	if err := c.Valid(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(home, ".models-*") // 0600
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, werr := f.Write(append(b, '\n'))
	if err := errors.Join(werr, f.Close()); err != nil {
		return err
	}
	return os.Rename(f.Name(), Path(home))
}

// Resolve picks the model for the id a client sent: an extra model by its exact id, a Codex model by its own id, then
// the slot whose family the id names.
func (c Config) Resolve(id string) (Target, *dto.APIError) {
	if !idShape.MatchString(id) {
		return Target{}, &dto.APIError{Status: 404, Type: "not_found_error", Message: "model: not a valid model id"}
	}
	bare := strings.TrimSuffix(strings.ToLower(id), "[1m]")
	for _, t := range c.Extra {
		if strings.EqualFold(t.Model, bare) {
			return t, nil
		}
	}
	for _, t := range CodexModels {
		if t.Model == bare {
			return t, nil
		}
	}
	if strings.HasPrefix(bare, "gpt-") && len(bare) > len("gpt-") && !strings.Contains(bare, "/") {
		return Target{Provider: Codex, Model: bare}, nil
	}
	for _, s := range Slots {
		if strings.Contains(bare, s) {
			return c.Slots[s], nil
		}
	}
	return Target{}, &dto.APIError{Status: 404, Type: "not_found_error", Message: "model: " + id}
}

// Label is what people see.
func (t Target) Label() string {
	if t.Name != "" {
		return t.Name
	}
	return t.Model
}

// Mapping lists slot and model in display order.
func (c Config) Mapping() [][2]string {
	var out [][2]string
	for _, s := range Slots {
		out = append(out, [2]string{s, c.Slots[s].Model})
	}
	return out
}
