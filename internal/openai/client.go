// Package openai talks to the custom endpoints: OpenAI Chat Completions, OpenAI Responses and Anthropic Messages
// servers of any make.
package openai

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Lookoff-AIMLAPI/second-life/internal/dto"
	"github.com/Lookoff-AIMLAPI/second-life/internal/route"
	"github.com/Lookoff-AIMLAPI/second-life/internal/translate"
)

const (
	maxErrBody     = 1 << 20
	maxBetas       = 64
	defaultVersion = "2023-06-01"
)

// headerLimit is how long a server may think before it answers at all.
var headerLimit = 15 * time.Minute

// Client calls one endpoint. Use a pointer; it is safe for concurrent use.
type Client struct {
	Endpoint route.Endpoint
	HTTP     *http.Client           // only its transport is used; nil is a transport without an answer timer
	Key      func() (string, error) // nil, or an empty key, means that the endpoint has none
	Version  string
}

// Unsupported is a refusal that names something the request can do without: translate.Options.Without(Field) says how.
type Unsupported struct {
	Field string
	API   *dto.APIError // what to tell the client when there is no retry
}

func (u *Unsupported) Error() string { return "the endpoint does not support " + u.Field }
func (u *Unsupported) Unwrap() error { return u.API }

var (
	versionShape = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)
	dateShape    = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	betaShape    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

var base = sync.OnceValue(func() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = 0 // headerLimit does it
	return &http.Client{Transport: tr}
})

func (c *Client) client() *http.Client {
	h := base()
	if c.HTTP != nil {
		h = c.HTTP
	}
	// A redirect would carry the key and the conversation to an address the owner did not choose.
	return &http.Client{Transport: h.Transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (c *Client) agent() string {
	if versionShape.MatchString(c.Version) {
		return "second-life/" + c.Version
	}
	return "second-life/dev"
}

func (c *Client) key() (string, *dto.APIError) {
	if c.Key == nil {
		return "", nil
	}
	key, err := c.Key()
	if err != nil {
		return "", &dto.APIError{Status: 401, Type: "authentication_error", Message: "the key of this endpoint could not be read"}
	}
	if strings.ContainsFunc(key, func(r rune) bool { return r <= ' ' || r > '~' }) {
		return "", &dto.APIError{Status: 401, Type: "authentication_error", Message: "the key of this endpoint has characters that are not allowed"}
	}
	return key, nil
}

// request makes a request with the headers every call has. The address was checked, so it is https or this computer.
func (c *Client) request(ctx context.Context, method, path, key string, body []byte) (*http.Request, *dto.APIError) {
	if err := c.Endpoint.Valid(); err != nil {
		return nil, &dto.APIError{Status: 500, Type: "api_error", Message: "the endpoint is not valid: " + err.Error()}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Endpoint.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, &dto.APIError{Status: 500, Type: "api_error", Message: "the address of the endpoint is not valid"}
	}
	req.Header.Set("User-Agent", c.agent())
	req.Header.Set("Accept-Encoding", "identity") // a compressed stream may be held back
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	if c.Endpoint.Protocol == route.Anthropic {
		req.Header.Set("Anthropic-Version", defaultVersion)
		if key != "" {
			req.Header.Set("X-Api-Key", key)
		}
	}
	return req, nil
}

// Send posts a translated or prepared request. in is the header of Claude's request: only its Anthropic version and
// betas are used, and only for a Messages endpoint. The response is HTTP 200, not read; the caller closes it. The error
// is *dto.APIError or *Unsupported.
func (c *Client) Send(ctx context.Context, body []byte, in http.Header) (*http.Response, error) {
	key, e := c.key()
	if e != nil {
		return nil, e
	}
	path := map[string]string{route.Chat: "/chat/completions", route.Responses: "/responses", route.Anthropic: "/messages"}[c.Endpoint.Protocol]
	ctx, cancel := context.WithCancel(ctx)
	req, e := c.request(ctx, "POST", path, key, body)
	if e != nil {
		cancel()
		return nil, e
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream, application/json")
	if c.Endpoint.Protocol == route.Anthropic {
		if v := strings.TrimSpace(in.Get("Anthropic-Version")); dateShape.MatchString(v) {
			req.Header.Set("Anthropic-Version", v)
		}
		if b := betas(in); len(b) > 0 {
			req.Header.Set("Anthropic-Beta", strings.Join(b, ","))
		}
	}
	timer := time.AfterFunc(headerLimit, cancel)
	resp, err := c.client().Do(req)
	late := !timer.Stop()
	if err == nil && late {
		resp.Body.Close()
		err = context.DeadlineExceeded
	}
	if err != nil {
		cancel()
		if late {
			return nil, &dto.APIError{Status: 504, Type: "api_error", Message: "the endpoint did not answer in time"}
		}
		return nil, &dto.APIError{Status: 502, Type: "api_error", Message: "could not reach " + c.name()}
	}
	kind, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if resp.StatusCode == 200 && kind != "text/html" {
		resp.Body = &answer{resp.Body, cancel}
		return resp, nil
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBody))
	resp.Body.Close()
	cancel()
	return nil, c.failure(resp, raw, body, key)
}

// answer frees the request's context with the body.
type answer struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *answer) Close() error {
	defer b.cancel()
	return b.ReadCloser.Close()
}

func (c *Client) name() string {
	if c.Endpoint.Name != "" {
		return c.Endpoint.Name
	}
	return "the endpoint"
}

// failure turns a refusal into an error. The key never appears in it.
func (c *Client) failure(resp *http.Response, raw, sent []byte, key string) error {
	if key != "" {
		raw = bytes.ReplaceAll(raw, []byte(key), []byte("[key]"))
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return &dto.APIError{Status: 502, Type: "api_error", Message: fmt.Sprintf("%s answered with a redirect (HTTP %d); use the final address", c.name(), resp.StatusCode)}
	}
	e := translate.ServerError(resp.StatusCode, resp.Header, raw)
	if key != "" {
		e.Message = strings.ReplaceAll(e.Message, key, "[key]") // it may have been escaped in the body
	}
	if e.Status == 401 && key == "" {
		e.Message = c.name() + " wants a key: " + e.Message
	}
	if f := refusedField(c.Endpoint.Protocol, resp.StatusCode, sent, raw); f != "" {
		return &Unsupported{f, e}
	}
	return e
}

func betas(in http.Header) []string {
	var out []string
	seen := map[string]bool{}
	for _, line := range in.Values("Anthropic-Beta") {
		for _, b := range strings.Split(line, ",") {
			b = strings.TrimSpace(b)
			// oauth- changes how a key authenticates.
			if betaShape.MatchString(b) && !seen[b] && !strings.HasPrefix(b, "oauth-") && len(out) < maxBetas {
				seen[b] = true
				out = append(out, b)
			}
		}
	}
	return out
}

var errModels = errors.New("the model list is not valid")
