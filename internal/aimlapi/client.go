// Package aimlapi talks to api.aimlapi.com, which serves Anthropic Messages itself: requests and answers pass almost
// unchanged.
package aimlapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"mime"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Lookoff-AIMLAPI/second-life/internal/dto"
)

const (
	DefaultBaseURL = "https://api.aimlapi.com"

	maxModels      = 16 << 20
	maxErrBody     = 1 << 20
	modelsFresh    = 10 * time.Minute
	modelsTimeout  = 30 * time.Second // the list only, never an answer
	maxBetas       = 64
	defaultVersion = "2023-06-01"
)

// Client calls the API. Use a pointer; it is safe for concurrent use.
type Client struct {
	BaseURL string       // DefaultBaseURL when empty
	HTTP    *http.Client // its redirects, cookies and overall timeout are not used; nil is a client without timers
	Key     func() (string, error)
	Version string

	mu     sync.Mutex
	cached []Model
	at     time.Time
}

// Model is one model served on Messages. Zero means unknown.
type Model struct {
	ID, Name, Developer, Description string
	Context, OutputMax               int
	ReleasedAt                       string // 2026-11-21
}

var (
	idShape      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/\[\]-]{0,127}$`) // as in package route
	dateShape    = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	betaShape    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	versionShape = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)

	// The endpoint takes every well-formed beta value but these two: it drops them, since they change how a key
	// authenticates and ask for a tool it refuses.
	droppedBetas = []string{"oauth-", "advisor-tool-"}

	now = time.Now
)

// base has no timers at all: a provider may be silent for 15 minutes, and the caller's context ends what it gives up.
var base = sync.OnceValue(func() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = (&net.Dialer{}).DialContext
	tr.TLSHandshakeTimeout, tr.ResponseHeaderTimeout = 0, 0
	return &http.Client{Transport: tr}
})

func (c *Client) client() *http.Client {
	h := base()
	if c.HTTP != nil {
		h = c.HTTP
	}
	return &http.Client{
		Transport:     h.Transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// address is the API address of a path. The key must not travel in the clear.
func (c *Client) address(path string) (string, error) {
	b := c.BaseURL
	if b == "" {
		b = DefaultBaseURL
	}
	u, err := url.Parse(b)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("the AIMLAPI address is not valid")
	}
	ip := net.ParseIP(u.Hostname())
	local := u.Hostname() == "localhost" || ip != nil && ip.IsLoopback()
	if u.Scheme != "https" && !(u.Scheme == "http" && local) {
		return "", errors.New("the AIMLAPI address must use https")
	}
	return strings.TrimSuffix(strings.TrimSuffix(b, "/"), "/v1") + path, nil
}

func (c *Client) agent() string {
	if versionShape.MatchString(c.Version) {
		return "second-life/" + c.Version
	}
	return "second-life/dev"
}

// Models lists what POST /v1/messages serves. It needs no key. The list is kept for 10 minutes; when a refresh fails,
// the old one is returned.
func (c *Client) Models(ctx context.Context) ([]Model, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cached == nil || now().Sub(c.at) >= modelsFresh {
		list, err := c.fetch(ctx)
		if err != nil && c.cached == nil {
			return nil, err
		}
		if err == nil {
			c.cached, c.at = list, now()
		}
	}
	return append([]Model(nil), c.cached...), nil
}

func (c *Client) fetch(ctx context.Context) ([]Model, error) {
	ctx, cancel := context.WithTimeout(ctx, modelsTimeout)
	defer cancel()
	to, err := c.address("/v1/models?type=anthropic/messages")
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", to, nil)
	if err != nil {
		return nil, errors.New("the AIMLAPI address is not valid")
	}
	// No anthropic-version here: it asks for another format.
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.agent())
	resp, err := c.client().Do(req)
	if err != nil {
		return nil, errors.New("could not reach AIMLAPI")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, errors.New("AIMLAPI did not list its models: HTTP " + resp.Status[:3])
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxModels+1))
	if err != nil {
		return nil, errors.New("could not read the AIMLAPI model list")
	}
	if len(raw) > maxModels {
		return nil, errors.New("the AIMLAPI model list is over 16 MB")
	}
	return parseModels(raw)
}

func parseModels(raw []byte) ([]Model, error) {
	var list struct {
		Data []json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &list) != nil || list.Data == nil {
		return nil, errors.New("the AIMLAPI model list is not valid")
	}
	out, seen := []Model{}, map[string]bool{}
	for _, item := range list.Data {
		var m struct {
			ID   string `json:"id"`
			Type string `json:"type"`
			Info struct {
				Name          any `json:"name"`
				Developer     any `json:"developer"`
				Description   any `json:"description"`
				ReleasedAt    any `json:"releasedAt"`
				ContextLength any `json:"contextLength"`
				OutputMax     any `json:"outputMax"`
			} `json:"info"`
		}
		// One bad entry must not hide the rest.
		if json.Unmarshal(item, &m) != nil || m.Type != "anthropic/messages" || !idShape.MatchString(m.ID) || seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		released, _ := m.Info.ReleasedAt.(string)
		if !dateShape.MatchString(released) {
			released = ""
		}
		out = append(out, Model{
			ID: m.ID, Name: label(m.Info.Name, 100), Developer: label(m.Info.Developer, 100), Description: label(m.Info.Description, 2000),
			Context: tokens(m.Info.ContextLength), OutputMax: tokens(m.Info.OutputMax), ReleasedAt: released,
		})
	}
	return out, nil
}

// label is one line of printable text, at most limit bytes. Invisible characters could make one model look like another.
func label(v any, limit int) string {
	s, _ := v.(string)
	s = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsSpace(r):
			return ' '
		case !unicode.IsPrint(r): // controls, direction marks, zero widths
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, ""))
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > limit {
		s = s[:limit]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return strings.TrimSpace(s)
}

func tokens(v any) int {
	f, ok := v.(float64)
	if !ok || f < 1 || f > 10_000_000 || f != math.Trunc(f) {
		return 0
	}
	return int(f)
}

// Send posts a prepared request. in is the header of Claude's request: only its Anthropic version and betas are used.
// The response is HTTP 200, not read; the caller closes it.
func (c *Client) Send(ctx context.Context, body []byte, in http.Header) (*http.Response, *dto.APIError) {
	key, e := c.key()
	if e != nil {
		return nil, e
	}
	to, err := c.address("/v1/messages")
	if err != nil {
		return nil, &dto.APIError{Status: 500, Type: "api_error", Message: err.Error()}
	}
	req, err := http.NewRequestWithContext(ctx, "POST", to, bytes.NewReader(body))
	if err != nil {
		return nil, &dto.APIError{Status: 500, Type: "api_error", Message: "the AIMLAPI address is not valid"}
	}
	version := strings.TrimSpace(in.Get("Anthropic-Version"))
	if !dateShape.MatchString(version) {
		version = defaultVersion
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Accept-Encoding", "identity") // a compressed stream may be held back
	req.Header.Set("User-Agent", c.agent())
	req.Header.Set("Anthropic-Version", version)
	if b := betas(in); len(b) > 0 {
		req.Header.Set("Anthropic-Beta", strings.Join(b, ","))
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return nil, &dto.APIError{Status: 502, Type: "api_error", Message: "could not reach AIMLAPI"}
	}
	kind, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if resp.StatusCode == 200 && kind != "text/html" {
		return resp, nil
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBody))
	return nil, upstreamError(resp.StatusCode, resp.Header, raw, key)
}

func (c *Client) key() (string, *dto.APIError) {
	missing := &dto.APIError{Status: 401, Type: "authentication_error", Message: "no AIMLAPI key is set"}
	if c.Key == nil {
		return "", missing
	}
	key, err := c.Key()
	if err != nil {
		missing.Message = "AIMLAPI key: " + clean(err.Error())
		return "", missing
	}
	if key == "" {
		return "", missing
	}
	if strings.ContainsFunc(key, func(r rune) bool { return r <= ' ' || r > '~' }) {
		missing.Message = "the AIMLAPI key has characters that are not allowed"
		return "", missing
	}
	return key, nil
}

func betas(in http.Header) []string {
	var out []string
	seen := map[string]bool{}
	for _, line := range in.Values("Anthropic-Beta") {
	next:
		for _, b := range strings.Split(line, ",") {
			b = strings.TrimSpace(b)
			if !betaShape.MatchString(b) || seen[b] {
				continue
			}
			for _, p := range droppedBetas {
				if strings.HasPrefix(b, p) {
					continue next
				}
			}
			if seen[b] = true; len(out) < maxBetas {
				out = append(out, b)
			}
		}
	}
	return out
}
