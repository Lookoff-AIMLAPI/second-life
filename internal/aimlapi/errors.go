package aimlapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Lookoff-AIMLAPI/second-life/internal/dto"
)

const (
	maxMessage    = 2000
	maxRetryAfter = 366 * 24 * 3600
	noBalance     = "AIMLAPI balance is too low. Top up at https://aimlapi.com/app/billing/"
	billingError  = "billing_error"
	tooLong       = "prompt is too long: " // what Claude Code compacts on
)

// statusOf are the Anthropic error types.
var statusOf = map[string]int{
	"invalid_request_error": 400, "authentication_error": 401, billingError: 402, "permission_error": 403,
	"not_found_error": 404, "timeout_error": 408, "conflict_error": 409, "request_too_large": 413,
	"rate_limit_error": 429, "api_error": 500, "overloaded_error": 529,
}

// upstreamError reads a failed response: Anthropic's shape, OpenAI's, or the problem+json of the gateway.
func upstreamError(status int, header http.Header, body []byte, key string) *dto.APIError {
	var top map[string]any
	_ = json.Unmarshal(body, &top)
	e := classify(status, top, key)
	e.RetryAfter = retryAfter(header.Get("Retry-After"))
	return e
}

// classify maps one error object. status 0 means it came inside a stream.
func classify(status int, top map[string]any, key string) *dto.APIError {
	obj, _ := top["error"].(map[string]any)
	str := func(m map[string]any, k string) string { s, _ := m[k].(string); return s }
	msg, typ, code := str(obj, "message"), str(obj, "type"), str(obj, "code")
	if msg == "" {
		msg = str(top, "message")
	}
	if s, ok := top["error"].(string); ok && msg == "" {
		msg = s
	}
	// The reasons say which field was refused, and Claude Code looks for some of them.
	if list, _ := obj["details"].([]any); len(list) > 0 {
		for _, v := range list[:min(len(list), 3)] {
			d, _ := v.(map[string]any)
			if r := str(d, "reason"); r != "" && !strings.Contains(msg, r) {
				if p := str(d, "path"); p != "" && !strings.HasPrefix(r, p) {
					r = p + ": " + r
				}
				msg = strings.TrimSpace(msg + " " + r)
			}
		}
	}
	if n, ok := top["code"].(float64); ok && status == 0 && n >= 400 && n < 600 {
		status = int(n)
	}
	if n, ok := top["status"].(float64); ok && status == 0 && n >= 400 && n < 600 {
		status = int(n)
	}
	if key != "" {
		msg = strings.ReplaceAll(msg, key, "[key]")
	}
	msg = strings.TrimSpace(clean(msg))

	e := &dto.APIError{Status: status, Type: typ}
	if _, known := statusOf[typ]; !known {
		e.Type = typeOf(status)
	}
	if status == 0 {
		e.Status = statusOf[e.Type]
	}
	if e.Status < 400 || e.Status > 599 { // a redirect or an unexpected success
		e.Status, e.Type = 502, "api_error"
	}
	if msg == "" {
		msg = "AIMLAPI answered with an error"
		if status > 0 {
			msg = fmt.Sprintf("AIMLAPI returned HTTP %d", status)
		}
	}
	low := strings.ToLower(msg)
	switch {
	case status == 402 || typ == billingError || (status == 403 || status == 0) &&
		(strings.Contains(low, "out of funds") || strings.Contains(low, "insufficent resource")):
		// 402, not the 403 of the gateway: Claude takes that for a login that lost its rights.
		e.Status, e.Type, msg = 402, billingError, noBalance
	case e.Status == 401:
		e.Type, msg = "authentication_error", "the AIMLAPI key is wrong: "+msg
	case code == "context_length_exceeded" || e.Status == 400 && (strings.Contains(low, "maximum context length") || strings.Contains(low, "exceeds the context window")):
		e.Status, e.Type = 400, "invalid_request_error"
		if !strings.HasPrefix(low, tooLong) {
			msg = tooLong + msg
		}
	}
	e.Message = msg
	return e
}

func typeOf(status int) string {
	switch status {
	case 400, 422:
		return "invalid_request_error"
	case 503, 529:
		return "overloaded_error"
	case 500, 502, 504:
		return "api_error"
	}
	for t, s := range statusOf {
		if s == status {
			return t
		}
	}
	if status >= 400 && status < 500 {
		return "invalid_request_error"
	}
	return "api_error"
}

// retryAfter returns whole seconds, at least 1, or "".
func retryAfter(h string) string {
	h = strings.TrimSpace(h)
	secs, err := strconv.ParseFloat(h, 64)
	if err != nil {
		at, derr := http.ParseTime(h)
		if derr != nil {
			return ""
		}
		secs = max(0, at.Sub(now()).Round(time.Second).Seconds())
	}
	if !(secs >= 0 && secs <= maxRetryAfter) { // also NaN
		return ""
	}
	return strconv.Itoa(max(1, int(secs+0.999)))
}

// clean makes upstream text safe to print.
func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, "�"))
	if r := []rune(s); len(r) > maxMessage {
		s = string(r[:maxMessage])
	}
	return s
}
