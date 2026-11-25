package translate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Lookoff-AIMLAPI/second-life/internal/dto"
)

const (
	stMaxMessage     = 2000
	stContextPrefix  = "prompt is too long: "
	stCyberFallback  = "This request has been flagged for possible cybersecurity risk."
	stImagePrefix    = "Could not process image: "
	stBadSignature   = "messages: Invalid `signature` in `thinking` block"
	stMaxRetrySecond = 366 * 24 * 3600
)

var (
	stNow      = time.Now
	stTryAgain = regexp.MustCompile(`(?i)try again in(.*)`)
	stDuration = regexp.MustCompile(`^\s*(\d+(?:\.\d+)?)\s*([a-z]+)`)
	stUnits    = map[string]float64{
		"ms": 0.001, "s": 1, "sec": 1, "secs": 1, "second": 1, "seconds": 1, "m": 60, "min": 60, "mins": 60, "minute": 60,
		"minutes": 60, "h": 3600, "hr": 3600, "hrs": 3600, "hour": 3600, "hours": 3600, "d": 86400, "day": 86400, "days": 86400,
	}
)

// UpstreamError turns a failed HTTP response into an Anthropic error.
func UpstreamError(status int, header http.Header, body []byte) *dto.APIError {
	fallback := fmt.Sprintf("upstream returned HTTP %d", status)
	var top map[string]any
	if json.Unmarshal(body, &top) == nil && top != nil {
		return stClassify(status, header, top, fallback)
	}
	text := string(bytes.TrimSpace(body))
	if strings.Contains(strings.ToLower(header.Get("Content-Type")), "html") {
		text = ""
	}
	return stClassify(status, header, map[string]any{"message": text}, fallback)
}

// stStreamError classifies a response.failed (top is its response) or a top-level error event.
func stStreamError(top map[string]any) *dto.APIError {
	status := stInt(top["status"])
	if status == 0 {
		status = stInt(top["status_code"])
	}
	header := http.Header{}
	for k, v := range stMap(top["headers"]) {
		header.Set(k, stStr(v))
	}
	return stClassify(status, header, top, "upstream response failed")
}

// stClassify is the only place that maps upstream failures. status 0 means there was no HTTP status.
func stClassify(status int, header http.Header, top map[string]any, fallback string) *dto.APIError {
	// The error object sits under "error" or "detail", or is the body itself.
	obj, msg := top, stStr(top["message"])
	for _, key := range []string{"error", "detail"} {
		switch v := top[key].(type) {
		case map[string]any:
			obj, msg = v, stStr(v["message"])
		case string:
			msg = v
		case []any:
			raw, _ := json.Marshal(v)
			msg = string(raw)
		default:
			continue
		}
		break
	}
	code, typ := stStr(obj["code"]), stStr(obj["type"])
	if typ == "error" {
		typ = "" // the event name, not an error type
	}
	is := func(names ...string) bool {
		for _, n := range names {
			if code == n || typ == n {
				return true
			}
		}
		return false
	}

	msg = strings.TrimSpace(stClean(msg))
	if strings.HasPrefix(msg, "<") {
		msg = "" // an HTML page
	}
	overflow := is("context_length_exceeded") ||
		(status == 0 || status == 400 || status == 413) && strings.Contains(msg, "exceeds the context window") ||
		(status == 0 || status == 400 || status == 413 || status == 422 || status == 500) && Overflow(msg)
	image := (status == 0 || status == 400) && (strings.Contains(msg, "does not represent a valid image") || strings.Contains(msg, "unsupported image format") ||
		is("invalid_image", "invalid_image_format", "invalid_base64_image", "invalid_image_url", "invalid_image_mode",
			"image_too_large", "image_too_small", "image_file_too_large", "image_parse_error", "unsupported_image_media_type",
			"empty_image_file", "failed_to_download_image", "image_file_not_found"))
	if msg == "" {
		switch {
		case is("cyber_policy"):
			msg = stCyberFallback
		case code != "":
			msg = "upstream error: " + code
		case typ != "":
			msg = "upstream error: " + typ
		default:
			msg = fallback
		}
	}

	e := &dto.APIError{Status: 502, Type: "api_error"}
	set := func(s int, t string) { e.Status, e.Type = s, t }
	switch {
	case overflow:
		set(400, "invalid_request_error")
		if !strings.HasPrefix(msg, stContextPrefix) {
			msg = stContextPrefix + msg
		}
	case is("invalid_encrypted_content"):
		set(400, "invalid_request_error")
		msg = stBadSignature
	case image:
		set(400, "invalid_request_error")
		if !strings.HasPrefix(msg, stImagePrefix) {
			msg = stImagePrefix + msg
		}
	case is("cyber_policy", "bio_policy", "invalid_prompt", "content_policy_violation"):
		set(400, "invalid_request_error")
	case status == 429 || is("usage_limit_reached", "usage_not_included", "rate_limit_exceeded", "rate_limit_error", "insufficient_quota"):
		set(429, "rate_limit_error")
		e.RetryAfter = stRetryAfter(header, top, obj, msg)
	case status == 503 || status == 529 || is("server_is_overloaded", "slow_down", "overloaded_error"):
		set(529, "overloaded_error")
		e.RetryAfter = stRetryAfter(header, top, obj, "")
	case status == 401 || status == 0 && is("authentication_error", "invalid_api_key", "token_expired"):
		set(401, "authentication_error")
	case status == 403 || status == 0 && is("permission_error"):
		set(403, "permission_error")
	case status == 404 || status == 0 && is("not_found_error", "model_not_found"):
		set(404, "not_found_error")
	case status == 413:
		set(413, "request_too_large")
	case status == 408: // Claude Code retries 408 and 409, not 422
		set(408, "timeout_error")
	case status == 409:
		set(409, "conflict_error")
	case status == 422:
		set(422, "invalid_request_error")
	case status >= 400 && status < 500 || status == 0 && is("invalid_request_error", "invalid_request"):
		set(400, "invalid_request_error")
	}
	e.Message = stCap(msg)
	return e
}

// stRetryAfter returns whole seconds, at least 1, or "" when upstream did not say.
func stRetryAfter(header http.Header, top, obj map[string]any, msg string) string {
	h := strings.TrimSpace(header.Get("Retry-After"))
	if secs, err := strconv.ParseFloat(h, 64); err == nil {
		if s := stSeconds(secs); s != "" {
			return s
		}
	} else if at, err := http.ParseTime(h); err == nil {
		return stUntil(float64(at.Unix()))
	}
	for _, m := range []map[string]any{obj, top} {
		if secs, ok := m["resets_in_seconds"].(float64); ok {
			if s := stSeconds(secs); s != "" {
				return s
			}
		}
		if at, ok := m["resets_at"].(float64); ok && at > 0 {
			if s := stUntil(at); s != "" {
				return s
			}
		}
	}
	if m := stTryAgain.FindStringSubmatch(msg); m != nil {
		return stSeconds(stParseDuration(strings.ToLower(m[1])))
	}
	return ""
}

// stParseDuration reads "11.054s", "1m30s" or "2 minutes 5 seconds" at the start of s; -1 when there is none.
func stParseDuration(s string) float64 {
	total := -1.0
	for m := stDuration.FindStringSubmatch(s); m != nil; m = stDuration.FindStringSubmatch(s) {
		unit, ok := stUnits[m[2]]
		if !ok {
			break
		}
		n, _ := strconv.ParseFloat(m[1], 64)
		total = math.Max(total, 0) + n*unit
		s = s[len(m[0]):]
	}
	return total
}

func stUntil(unix float64) string {
	return stSeconds(math.Max(0, unix-float64(stNow().Unix())))
}

func stSeconds(s float64) string {
	if !(s >= 0 && s <= stMaxRetrySecond) { // also rejects NaN
		return ""
	}
	return strconv.Itoa(int(math.Max(1, math.Ceil(s))))
}

// stClean makes upstream text safe to print.
func stClean(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, "\ufffd"))
}

func stCap(s string) string {
	s = stClean(s)
	if utf8.RuneCountInString(s) > stMaxMessage {
		s = string([]rune(s)[:stMaxMessage])
	}
	return s
}

func stMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func stStr(v any) string {
	s, _ := v.(string)
	return s
}

// stInt reads a JSON number; anything else, or a number out of range, is 0.
func stInt(v any) int {
	f, ok := v.(float64)
	if !ok || !(math.Abs(f) < 1<<53) {
		return 0
	}
	return int(f)
}
