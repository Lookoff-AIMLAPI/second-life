package translate

import (
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Lookoff-AIMLAPI/second-life/internal/dto"
)

func stFreezeTime(t *testing.T) time.Time {
	t.Helper()
	now := time.Unix(1_800_000_000, 0)
	old := stNow
	stNow = func() time.Time { return now }
	t.Cleanup(func() { stNow = old })
	return now
}

func TestStUpstreamError(t *testing.T) {
	now := stFreezeTime(t)
	cases := []struct {
		name   string
		status int
		header map[string]string
		body   string
		want   dto.APIError
	}{
		{"401", 401, nil, `{"error":{"message":"Your token expired.","code":"token_expired"}}`,
			dto.APIError{Status: 401, Type: "authentication_error", Message: "Your token expired."}},
		{"401 detail", 401, nil, `{"detail":"Unauthorized"}`,
			dto.APIError{Status: 401, Type: "authentication_error", Message: "Unauthorized"}},
		{"403", 403, nil, `{"error":{"message":"no"}}`,
			dto.APIError{Status: 403, Type: "permission_error", Message: "no"}},
		{"403 cloudflare html", 403, map[string]string{"Content-Type": "text/html; charset=UTF-8"}, "<html><body>Cloudflare error: Sorry, you have been blocked</body></html>",
			dto.APIError{Status: 403, Type: "permission_error", Message: "upstream returned HTTP 403"}},
		{"html without a content type", 502, nil, "\n <!DOCTYPE html><html></html>",
			dto.APIError{Status: 502, Type: "api_error", Message: "upstream returned HTTP 502"}},
		{"html inside json", 502, nil, `{"detail":"<html>bad gateway</html>"}`,
			dto.APIError{Status: 502, Type: "api_error", Message: "upstream returned HTTP 502"}},
		{"json wins over an html content type", 400, map[string]string{"Content-Type": "text/html"}, `{"detail":"bad"}`,
			dto.APIError{Status: 400, Type: "invalid_request_error", Message: "bad"}},
		{"404", 404, nil, `{"detail":"Not Found"}`,
			dto.APIError{Status: 404, Type: "not_found_error", Message: "Not Found"}},
		{"usage limit, resets_in_seconds", 429, nil, `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","plan_type":"pro","resets_at":1800009999,"resets_in_seconds":120}}`,
			dto.APIError{Status: 429, Type: "rate_limit_error", Message: "The usage limit has been reached", RetryAfter: "120"}},
		{"usage limit, resets_at", 429, nil, `{"error":{"type":"usage_limit_reached","plan_type":"pro","resets_at":1800000090}}`,
			dto.APIError{Status: 429, Type: "rate_limit_error", Message: "upstream error: usage_limit_reached", RetryAfter: "90"}},
		{"resets_at in the past", 429, nil, `{"error":{"type":"usage_limit_reached","message":"m","resets_at":1}}`,
			dto.APIError{Status: 429, Type: "rate_limit_error", Message: "m", RetryAfter: "1"}},
		{"usage limit without a 429", 400, nil, `{"error":{"code":"usage_limit_reached","message":"m"}}`,
			dto.APIError{Status: 429, Type: "rate_limit_error", Message: "m"}},
		{"retry-after header first", 429, map[string]string{"Retry-After": "7"}, `{"error":{"code":"rate_limit_exceeded","message":"m","resets_in_seconds":120}}`,
			dto.APIError{Status: 429, Type: "rate_limit_error", Message: "m", RetryAfter: "7"}},
		{"retry-after fraction rounds up", 429, map[string]string{"Retry-After": "1.2"}, `x`,
			dto.APIError{Status: 429, Type: "rate_limit_error", Message: "x", RetryAfter: "2"}},
		{"retry-after date", 429, map[string]string{"Retry-After": now.Add(30 * time.Second).UTC().Format(http.TimeFormat)}, `x`,
			dto.APIError{Status: 429, Type: "rate_limit_error", Message: "x", RetryAfter: "30"}},
		{"retry-after garbage falls back", 429, map[string]string{"Retry-After": "soon"}, `{"error":{"message":"m","resets_in_seconds":3}}`,
			dto.APIError{Status: 429, Type: "rate_limit_error", Message: "m", RetryAfter: "3"}},
		{"absurd numbers are not a delay", 429, map[string]string{"Retry-After": "-5"}, `{"error":{"message":"m","resets_in_seconds":1e300,"resets_at":1e300}}`,
			dto.APIError{Status: 429, Type: "rate_limit_error", Message: "m"}},
		{"plain 429", 429, nil, ``,
			dto.APIError{Status: 429, Type: "rate_limit_error", Message: "upstream returned HTTP 429"}},
		{"insufficient quota", 403, nil, `{"error":{"code":"insufficient_quota","message":"q"}}`,
			dto.APIError{Status: 429, Type: "rate_limit_error", Message: "q"}},
		{"context", 400, nil, `{"error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"Your input exceeds the context window of this model."}}`,
			dto.APIError{Status: 400, Type: "invalid_request_error", Message: "prompt is too long: Your input exceeds the context window of this model."}},
		{"context, no code", 400, nil, `{"detail":"Your input exceeds the context window of this model."}`,
			dto.APIError{Status: 400, Type: "invalid_request_error", Message: "prompt is too long: Your input exceeds the context window of this model."}},
		{"other 400", 400, nil, `{"error":{"message":"Some other bad request.","code":"some_other_policy"}}`,
			dto.APIError{Status: 400, Type: "invalid_request_error", Message: "Some other bad request."}},
		{"400 detail list", 400, nil, `{"detail":[{"msg":"field required"}]}`,
			dto.APIError{Status: 400, Type: "invalid_request_error", Message: `[{"msg":"field required"}]`}},
		{"foreign reasoning", 400, nil, `{"error":{"message":"The encrypted content gAAA could not be verified.","type":"invalid_request_error","param":null,"code":"invalid_encrypted_content"}}`,
			dto.APIError{Status: 400, Type: "invalid_request_error", Message: "messages: Invalid `signature` in `thinking` block"}},
		{"invalid image", 400, nil, `{"error":{"message":"The image data you provided does not represent a valid image. Please check","type":"invalid_request_error","code":"invalid_value"}}`,
			dto.APIError{Status: 400, Type: "invalid_request_error", Message: "Could not process image: The image data you provided does not represent a valid image. Please check"}},
		{"invalid image, plain text", 400, nil, `The image data you provided does not represent a valid image`,
			dto.APIError{Status: 400, Type: "invalid_request_error", Message: "Could not process image: The image data you provided does not represent a valid image"}},
		{"invalid image by code", 400, nil, `{"error":{"message":"bad","code":"invalid_image_format"}}`,
			dto.APIError{Status: 400, Type: "invalid_request_error", Message: "Could not process image: bad"}},
		{"image code under 500", 500, nil, `{"error":{"message":"bad","code":"failed_to_download_image"}}`,
			dto.APIError{Status: 502, Type: "api_error", Message: "bad"}},
		{"402", 402, nil, `{"detail":{"code":"deactivated_workspace"}}`,
			dto.APIError{Status: 400, Type: "invalid_request_error", Message: "upstream error: deactivated_workspace"}},
		{"408", 408, nil, ``, dto.APIError{Status: 408, Type: "timeout_error", Message: "upstream returned HTTP 408"}},
		{"409", 409, nil, ``, dto.APIError{Status: 409, Type: "conflict_error", Message: "upstream returned HTTP 409"}},
		{"422", 422, nil, `{"detail":[{"msg":"bad"}]}`, dto.APIError{Status: 422, Type: "invalid_request_error", Message: `[{"msg":"bad"}]`}},
		{"302", 302, nil, ``, dto.APIError{Status: 502, Type: "api_error", Message: "upstream returned HTTP 302"}},
		{"control characters", 400, nil, `{"detail":"\u001b[31ma\u0000\tb\r\nc\u007f\u0085d"}`,
			dto.APIError{Status: 400, Type: "invalid_request_error", Message: "[31ma\tb\ncd"}},
		{"413", 413, nil, `too big`,
			dto.APIError{Status: 413, Type: "request_too_large", Message: "too big"}},
		{"503", 503, map[string]string{"Retry-After": "4"}, `{"error":{"code":"server_is_overloaded"}}`,
			dto.APIError{Status: 529, Type: "overloaded_error", Message: "upstream error: server_is_overloaded", RetryAfter: "4"}},
		{"slow_down", 500, nil, `{"error":{"code":"slow_down","message":"m"}}`,
			dto.APIError{Status: 529, Type: "overloaded_error", Message: "m"}},
		{"cyber", 400, nil, `{"error":{"message":"  ","type":"invalid_request","param":null,"code":"cyber_policy"}}`,
			dto.APIError{Status: 400, Type: "invalid_request_error", Message: "This request has been flagged for possible cybersecurity risk."}},
		{"policy under 403", 403, nil, `{"error":{"code":"bio_policy","message":"bio"}}`,
			dto.APIError{Status: 400, Type: "invalid_request_error", Message: "bio"}},
		{"500", 500, nil, `{"error":{"type":"server_error","message":"oops"}}`,
			dto.APIError{Status: 502, Type: "api_error", Message: "oops"}},
		{"error as a string", 500, nil, `{"error":"oops"}`,
			dto.APIError{Status: 502, Type: "api_error", Message: "oops"}},
		{"json without a message", 500, nil, `{}`,
			dto.APIError{Status: 502, Type: "api_error", Message: "upstream returned HTTP 500"}},
		{"json that is not an object", 500, nil, `null`,
			dto.APIError{Status: 502, Type: "api_error", Message: "null"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := http.Header{}
			for k, v := range c.header {
				h.Set(k, v)
			}
			if got := UpstreamError(c.status, h, []byte(c.body)); *got != c.want {
				t.Errorf("got  %+v\nwant %+v", *got, c.want)
			}
		})
	}
	if got := UpstreamError(500, nil, nil); got.Message != "upstream returned HTTP 500" {
		t.Errorf("nil header: %+v", got)
	}
}

func TestStUpstreamErrorCap(t *testing.T) {
	for name, body := range map[string]string{
		"long":         strings.Repeat("я", 5000),
		"long context": `{"error":{"code":"context_length_exceeded","message":"` + strings.Repeat("я", 5000) + `"}}`,
		"invalid utf8": strings.Repeat("a\xff", 3000),
	} {
		m := UpstreamError(400, nil, []byte(body)).Message
		if n := utf8.RuneCountInString(m); n != 2000 || !utf8.ValidString(m) {
			t.Errorf("%s: %d chars, valid %v", name, n, utf8.ValidString(m))
		}
	}
}

// Payloads from codex-rs/codex-api/src/sse/responses.rs, plus the top-level error shapes.
func TestStStreamErrors(t *testing.T) {
	stFreezeTime(t)
	cases := []struct {
		name, ev string
		want     dto.APIError
	}{
		{"rate limit", `{"type":"response.failed","response":{"error":{"code":"rate_limit_exceeded","message":"Rate limit reached. Please try again in 11.054s. Visit x"}}}`,
			dto.APIError{Status: 429, Type: "rate_limit_error", Message: "Rate limit reached. Please try again in 11.054s. Visit x", RetryAfter: "12"}},
		{"rate limit in ms", `{"type":"response.failed","response":{"error":{"code":"rate_limit_exceeded","message":"try again in 28ms"}}}`,
			dto.APIError{Status: 429, Type: "rate_limit_error", Message: "try again in 28ms", RetryAfter: "1"}},
		{"rate limit in seconds", `{"type":"response.failed","response":{"error":{"code":"rate_limit_exceeded","message":"Try again in 35 seconds."}}}`,
			dto.APIError{Status: 429, Type: "rate_limit_error", Message: "Try again in 35 seconds.", RetryAfter: "35"}},
		{"foreign reasoning", `{"type":"response.failed","response":{"error":{"code":"invalid_encrypted_content","message":"m"}}}`,
			dto.APIError{Status: 400, Type: "invalid_request_error", Message: "messages: Invalid `signature` in `thinking` block"}},
		{"invalid image", `{"type":"response.failed","response":{"error":{"code":"invalid_image","message":"m"}}}`,
			dto.APIError{Status: 400, Type: "invalid_request_error", Message: "Could not process image: m"}},
		{"usage limit", `{"type":"response.failed","response":{"error":{"code":"usage_limit_reached","message":"m","resets_at":1800000030}}}`,
			dto.APIError{Status: 429, Type: "rate_limit_error", Message: "m", RetryAfter: "30"}},
		{"context", `{"type":"response.failed","response":{"error":{"code":"context_length_exceeded","message":"Please adjust your input and try\nagain."}}}`,
			dto.APIError{Status: 400, Type: "invalid_request_error", Message: "prompt is too long: Please adjust your input and try\nagain."}},
		{"quota", `{"type":"response.failed","response":{"error":{"code":"insufficient_quota","message":"q"}}}`,
			dto.APIError{Status: 429, Type: "rate_limit_error", Message: "q"}},
		{"usage not included", `{"type":"response.failed","response":{"error":{"code":"usage_not_included","message":"m"}}}`,
			dto.APIError{Status: 429, Type: "rate_limit_error", Message: "m"}},
		{"cyber", `{"type":"response.failed","response":{"error":{"code":"cyber_policy","message":"flagged"}}}`,
			dto.APIError{Status: 400, Type: "invalid_request_error", Message: "flagged"}},
		{"invalid prompt", `{"type":"response.failed","response":{"error":{"code":"invalid_prompt","message":"ip"}}}`,
			dto.APIError{Status: 400, Type: "invalid_request_error", Message: "ip"}},
		{"overloaded", `{"type":"response.failed","response":{"error":{"code":"server_is_overloaded","message":"o"}}}`,
			dto.APIError{Status: 529, Type: "overloaded_error", Message: "o"}},
		{"server error", `{"type":"response.failed","response":{"error":{"code":"server_error","message":"s"}}}`,
			dto.APIError{Status: 502, Type: "api_error", Message: "s"}},
		{"failed, no error", `{"type":"response.failed","response":{"error":null}}`,
			dto.APIError{Status: 502, Type: "api_error", Message: "upstream response failed"}},
		{"failed, no response", `{"type":"response.failed"}`,
			dto.APIError{Status: 502, Type: "api_error", Message: "upstream response failed"}},
		{"flat error event", `{"type":"error","code":"rate_limit_exceeded","message":"m","param":null}`,
			dto.APIError{Status: 429, Type: "rate_limit_error", Message: "m"}},
		{"nested error event", `{"type":"error","status":400,"error":{"message":"m","type":"invalid_request","code":"x"}}`,
			dto.APIError{Status: 400, Type: "invalid_request_error", Message: "m"}},
		{"nested error event, status_code 401", `{"type":"error","status_code":401,"error":{"message":"m"}}`,
			dto.APIError{Status: 401, Type: "authentication_error", Message: "m"}},
		{"error event with headers", `{"type":"error","status":429,"error":{"message":"m"},"headers":{"retry-after":"9"}}`,
			dto.APIError{Status: 429, Type: "rate_limit_error", Message: "m", RetryAfter: "9"}},
		{"type only, no status", `{"type":"error","error":{"type":"invalid_request_error","message":"m"}}`,
			dto.APIError{Status: 400, Type: "invalid_request_error", Message: "m"}},
		{"model not found", `{"type":"response.failed","response":{"error":{"code":"model_not_found","message":"m"}}}`,
			dto.APIError{Status: 404, Type: "not_found_error", Message: "m"}},
		{"bare error event", `{"type":"error"}`,
			dto.APIError{Status: 502, Type: "api_error", Message: "upstream response failed"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := NewStream(dto.StreamContext{PublicModel: "p"})
			evs := s.Push(stJSON(t, c.ev))
			if f := s.Failed(); f == nil || *f != c.want {
				t.Fatalf("got  %+v\nwant %+v", f, c.want)
			}
			if len(evs) != 1 || evs[0].Name != "error" || evs[0].Data["type"] != "error" {
				t.Fatalf("events %v", evs)
			}
			body := evs[0].Data["error"].(map[string]any)
			if body["type"] != c.want.Type || body["message"] != c.want.Message {
				t.Errorf("error body %v", body)
			}
			if s.Message() != nil || s.Close() != nil || s.Push(stJSON(t, c.ev)) != nil {
				t.Error("a failed stream must stay silent")
			}
		})
	}
}

func TestStTryAgain(t *testing.T) {
	for msg, want := range map[string]string{
		"Please try again in 1m30s.":                 "90",
		"try again in 1h2m3s":                        "3723",
		"Try again in 2 minutes 5 seconds.":          "125",
		"try again in 1.5 seconds.":                  "2",
		"try again in 250ms":                         "1",
		"try again in 1m0.5s or later":               "61",
		"try again in 3 days":                        "259200",
		"try again in 5 more tries":                  "",
		"try again in 1m 2 whatever":                 "60",
		"try again in a while":                       "",
		"try again in 99999999999999999999999 hours": "",
		"try again later":                            "",
	} {
		if got := UpstreamError(429, nil, []byte(msg)).RetryAfter; got != want {
			t.Errorf("%q: %q, want %q", msg, got, want)
		}
	}
}
