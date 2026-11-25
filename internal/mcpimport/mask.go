package mcpimport

import (
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

const hidden = "***"

// Clean makes text from elsewhere safe to show in one line of a terminal.
func Clean(s string) string {
	return strings.Map(func(r rune) rune {
		if odd(r) {
			return ' '
		}
		return r
	}, s)
}

// odd is a character that does not show as itself: control, invisible, reordering, line breaks.
func odd(r rune) bool {
	return r == unicode.ReplacementChar || unicode.In(r, unicode.Cc, unicode.Cf, unicode.Co, unicode.Cs, unicode.Zl, unicode.Zp)
}

func showable(s string) bool { return !strings.ContainsFunc(s, odd) }

var (
	secretName = regexp.MustCompile(`(?i)token|secret|passw|passphrase|pwd|api[-_]?key|access[-_]?key|private[-_]?key|auth|bearer|credential|cookie|signature|(?:^|[^a-z])(?:key|sig|pat|jwt|pass|sas|dsn|code|session)s?$`)
	flagOnly   = regexp.MustCompile(`^--?[A-Za-z][\w.-]*$`)
	urlLike    = regexp.MustCompile("[A-Za-z][A-Za-z0-9+.-]*://[^\\s\"'`<>]*")
	authValue  = regexp.MustCompile(`(?i)(authorization["']?\s*[:=]\s*["']?)[^"']+`)
	scheme     = regexp.MustCompile(`(?i)\b(bearer|basic)\s+\S+`)
	flagValue  = regexp.MustCompile(`(?i)(^|[\s"'])(--?[a-z][\w.-]*)(\s+)("[^"]*"|'[^']*'|[^\s"']+)`)
	pairValue  = regexp.MustCompile(`([A-Za-z_][\w.-]*)(["']?\s*[:=]\s*)("[^"]*"|'[^']*'|[^\s,;&"'}]+)`)
	run        = regexp.MustCompile(`[A-Za-z0-9_+=~-]+`)
	camel      = regexp.MustCompile(`^(?:[-_]?[A-Z]?[a-z]+)+[-_]?$`)
	envPair    = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*=`)
	known      = regexp.MustCompile(`^(?:[spr]k[-_]|xox[a-z]-|glpat-|github_pat_|pypi-A)[\w-]{6,}$|^(?:gh[opsur]|npm|hf|ntn|lin_api|secret)_[A-Za-z0-9]{8,}$|^AKIA[0-9A-Z]{8,}$|^eyJ[\w-]{6,}$`)
)

// maskArgs shows arguments with what looks like a secret replaced.
func maskArgs(args []string) []string {
	out := make([]string, 0, len(args))
	prev := ""
	for _, a := range args {
		out = append(out, maskArg(prev, a))
		prev = a
	}
	return out
}

// maskArg knows the argument before, which may be a flag that takes a secret.
func maskArg(prev, a string) string {
	switch {
	case !flagOnly.MatchString(prev):
	case prev == "-H" || prev == "--header":
		if name, _, ok := strings.Cut(a, ":"); ok {
			return maskText(name) + ": " + hidden
		}
		return hidden
	case prev == "-e" || prev == "--env":
		if name := envPair.FindString(a); name != "" {
			return name + hidden
		}
	case prev == "-u" || prev == "--user":
		if name, _, ok := strings.Cut(a, ":"); ok {
			return maskText(name) + ":" + hidden
		}
	case secretName.MatchString(prev) && !flagOnly.MatchString(a):
		return hidden
	}
	return maskText(a)
}

// maskText masks a piece of text: addresses by their parts, the rest by names and by look.
func maskText(s string) string {
	var b strings.Builder
	last := 0
	for _, at := range urlLike.FindAllStringIndex(s, -1) {
		before := s[last:at[0]]
		b.WriteString(maskWords(before))
		b.WriteString(MaskURL(s[at[0]:at[1]]))
		last = at[1]
	}
	b.WriteString(maskWords(s[last:]))
	return b.String()
}

func maskWords(s string) string {
	s = authValue.ReplaceAllString(s, "${1}"+hidden)
	s = scheme.ReplaceAllString(s, "${1} "+hidden)
	s = pairValue.ReplaceAllStringFunc(s, func(m string) string {
		p := pairValue.FindStringSubmatch(m)
		if secretName.MatchString(p[1]) {
			return p[1] + p[2] + hidden
		}
		return m
	})
	s = flagValue.ReplaceAllStringFunc(s, func(m string) string {
		p := flagValue.FindStringSubmatch(m)
		if secretName.MatchString(p[2]) {
			return p[1] + p[2] + p[3] + hidden
		}
		return m
	})
	return run.ReplaceAllStringFunc(s, func(m string) string {
		if random(m) {
			return hidden
		}
		return m
	})
}

// random tells a key from words: a known prefix, or long with many changes between digits, capitals and small letters.
func random(s string) bool {
	if known.MatchString(s) {
		return true
	}
	if len(s) < 16 {
		return false
	}
	changes, digits, word, longest := 0, 0, 0, 0
	var kind byte
	for i := range len(s) {
		var k byte
		switch c := s[i]; {
		case c >= '0' && c <= '9':
			k = '0'
			digits++
		case c >= 'A' && c <= 'Z':
			k = 'A'
		case c >= 'a' && c <= 'z':
			k = 'a'
		}
		if k == 0 || k == '0' || k == 'A' && kind == 'a' {
			word = 0
		}
		if k == 'a' || k == 'A' {
			word++
			longest = max(longest, word)
		}
		if k != 0 {
			if kind != 0 && k != kind {
				changes++
			}
			kind = k
		}
	}
	if digits == 0 && camel.MatchString(s) && len(s) >= 2*(changes+2) { // words of four letters or more
		return longest >= 24
	}
	return changes >= 5 || digits >= 16 || longest >= 24 || digits >= 6 && changes >= 3
}

// MaskURL keeps the scheme, the host and the plain parts of a path.
func MaskURL(s string) string {
	u, err := url.Parse(s)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return hidden
	}
	var b strings.Builder
	b.WriteString(u.Scheme + "://")
	if u.User != nil {
		b.WriteString(hidden + "@")
	}
	b.WriteString(u.Host)
	prev := ""
	for i, part := range strings.Split(u.EscapedPath(), "/") {
		if i > 0 {
			b.WriteByte('/')
		}
		plain, err := url.PathUnescape(part)
		if err != nil || prev != "" && secretName.MatchString(prev) || maskWords(plain) != plain {
			if part != "" {
				part = hidden
			}
		}
		b.WriteString(part)
		prev = plain
	}
	if u.RawQuery != "" {
		b.WriteByte('?')
		for i, pair := range strings.Split(u.RawQuery, "&") {
			if i > 0 {
				b.WriteByte('&')
			}
			b.WriteString(maskQuery(pair))
		}
	}
	if u.Fragment != "" {
		b.WriteString("#" + hidden)
	}
	return b.String()
}

func maskQuery(pair string) string {
	key, val, ok := strings.Cut(pair, "=")
	name, err := url.QueryUnescape(key)
	if err != nil || maskWords(name) != name {
		return hidden
	}
	if !ok {
		return key
	}
	plain, err := url.QueryUnescape(val)
	if err != nil || secretName.MatchString(name) || strings.Contains(plain, "://") || maskWords(plain) != plain {
		return key + "=" + hidden
	}
	return pair
}
