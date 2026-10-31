package session

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// Redaction markers written at render time.
const (
	// RedactedMarker replaces an exact value or a pattern match at render time.
	RedactedMarker = "[redacted]"
	// RedactionFailedMarker replaces a whole string when a value survived redaction (fail closed).
	RedactionFailedMarker = "[skill-ci: withheld, redaction failed]"
	// minSecretLen is the shortest exact value the redactor looks for.
	minSecretLen = 8
)

// Redactor replaces secrets in strings at render time: exact values (and
// their base64, URL-encoded and JSON-escaped forms) plus the patterns of the
// runner's redaction v1. It is the controller's second line of defense; the
// runner's redaction is the first. A nil Redactor applies the patterns only.
type Redactor struct {
	// forms are every form of every exact value, longest first.
	forms []string
}

// NewRedactor returns a Redactor for the given exact values; values shorter
// than 8 characters (after trimming spaces) are ignored.
func NewRedactor(values []string) *Redactor {
	seen := map[string]bool{}
	var forms []string
	for _, v := range values {
		v = strings.TrimSpace(v)
		if len(v) < minSecretLen {
			continue
		}
		for _, f := range secretForms(v) {
			if len(f) >= minSecretLen && !seen[f] {
				seen[f] = true
				forms = append(forms, f)
			}
		}
	}
	sort.SliceStable(forms, func(i, j int) bool { return len(forms[i]) > len(forms[j]) })
	return &Redactor{forms: forms}
}

// secretForms returns v in every encoding the runner's redaction v1 knows.
func secretForms(v string) []string {
	forms := []string{
		v,
		base64.StdEncoding.EncodeToString([]byte(v)),
		base64.RawStdEncoding.EncodeToString([]byte(v)),
		base64.URLEncoding.EncodeToString([]byte(v)),
		base64.RawURLEncoding.EncodeToString([]byte(v)),
		url.QueryEscape(v),
		url.PathEscape(v),
	}
	if b, err := json.Marshal(v); err == nil {
		forms = append(forms, string(b[1:len(b)-1]))
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err == nil {
		b := bytes.TrimSpace(buf.Bytes())
		forms = append(forms, string(b[1:len(b)-1]))
	}
	return forms
}

// String returns s with every secret replaced and the number of replacements.
// When a form of an exact value is still present afterwards, it fails closed
// and returns RedactionFailedMarker instead of s.
func (r *Redactor) String(s string) (string, int) {
	if len(s) < 6 {
		return s, 0
	}
	n := 0
	if r != nil {
		for _, f := range r.forms {
			if c := strings.Count(s, f); c > 0 {
				s = strings.ReplaceAll(s, f, RedactedMarker)
				n += c
			}
		}
	}
	for _, rule := range patternRules {
		var c int
		s, c = rule.apply(s)
		n += c
	}
	if r.Leaks([]byte(s)) {
		return RedactionFailedMarker, n + 1
	}
	return s, n
}

// Leaks reports whether data still holds any form of an exact value. The
// handlers run it over every rendered response as the last check.
func (r *Redactor) Leaks(data []byte) bool {
	if r == nil {
		return false
	}
	for _, f := range r.forms {
		if bytes.Contains(data, []byte(f)) {
			return true
		}
	}
	return false
}

// redactJSON walks every string value of a JSON document (keys and member
// order stay as they are, so pointers stay valid) and returns the redacted
// document and the number of replacements.
func (r *Redactor) redactJSON(data []byte) ([]byte, int, error) {
	if !json.Valid(data) {
		return nil, 0, fmt.Errorf("session: redact: invalid JSON")
	}
	var buf bytes.Buffer
	n := 0
	if err := r.walk(&buf, bytes.TrimSpace(data), &n); err != nil {
		return nil, 0, err
	}
	return buf.Bytes(), n, nil
}

// walk writes raw to buf with every string value redacted.
func (r *Redactor) walk(buf *bytes.Buffer, raw []byte, n *int) error {
	if len(raw) == 0 {
		return nil
	}
	switch raw[0] {
	case '"':
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return fmt.Errorf("session: redact: %w", err)
		}
		red, c := r.String(s)
		*n += c
		return writeString(buf, red)
	case '{':
		buf.WriteByte('{')
		for i, m := range objectMembers(raw) {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeString(buf, m.Key); err != nil {
				return err
			}
			buf.WriteByte(':')
			if err := r.walk(buf, bytes.TrimSpace(m.Val), n); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	case '[':
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return fmt.Errorf("session: redact: %w", err)
		}
		buf.WriteByte('[')
		for i, it := range items {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := r.walk(buf, bytes.TrimSpace(it), n); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	default:
		buf.Write(raw)
	}
	return nil
}

// writeString writes s as a JSON string without HTML escaping.
func writeString(buf *bytes.Buffer, s string) error {
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return fmt.Errorf("session: redact: %w", err)
	}
	buf.Truncate(buf.Len() - 1) // the newline Encode adds
	return nil
}

// redactTrial returns a redacted copy of t and the number of replacements.
func (r *Redactor) redactTrial(t *Trial) (*Trial, int, error) {
	data, err := json.Marshal(t)
	if err != nil {
		return nil, 0, fmt.Errorf("session: redact trial: %w", err)
	}
	red, n, err := r.redactJSON(data)
	if err != nil {
		return nil, 0, err
	}
	var out Trial
	if err := json.Unmarshal(red, &out); err != nil {
		return nil, 0, fmt.Errorf("session: redact trial: %w", err)
	}
	return &out, n, nil
}

// patternRule is one render-time pattern of redaction v1.
type patternRule struct {
	re *regexp.Regexp
	// replace returns the replacement of one match (groups[0] is the match,
	// start its offset in s), or false to keep the match.
	replace func(s string, start int, groups []string) (string, bool)
}

// apply replaces every match of the rule in s and counts the matches it changed.
func (p patternRule) apply(s string) (string, int) {
	idx := p.re.FindAllStringSubmatchIndex(s, -1)
	if idx == nil {
		return s, 0
	}
	var b strings.Builder
	last, n := 0, 0
	for _, m := range idx {
		groups := make([]string, len(m)/2)
		for i := range groups {
			if m[2*i] >= 0 {
				groups[i] = s[m[2*i]:m[2*i+1]]
			}
		}
		repl, ok := p.replace(s, m[0], groups)
		if !ok || repl == groups[0] {
			continue
		}
		b.WriteString(s[last:m[0]])
		b.WriteString(repl)
		last = m[1]
		n++
	}
	if n == 0 {
		return s, 0
	}
	b.WriteString(s[last:])
	return b.String(), n
}

// fixed replaces a whole match with repl.
func fixed(repl string) func(string, int, []string) (string, bool) {
	return func(string, int, []string) (string, bool) { return repl, true }
}

// keepFirst keeps group 1 (a boundary character) and appends repl.
func keepFirst(repl string) func(string, int, []string) (string, bool) {
	return func(_ string, _ int, g []string) (string, bool) { return g[1] + repl, true }
}

// The patterns of SkillEvaluator's redact_sensitive_text (ported to RE2,
// which has no lookbehind: boundaries are matched as a group and kept), the
// runner's Ollama-style keys and its env-dump rule, in the runner's order.
var patternRules = []patternRule{
	{regexp.MustCompile(`-----BEGIN [A-Z0-9 -]*PRIVATE KEY-----(?s:.*?)(?:-----END [A-Z0-9 -]*-----|\z)`), fixed("private-key-<redacted>")},
	{regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]{0,31}://)[^\s/?#@]+@`), keepFirst("<redacted>@")},
	{regexp.MustCompile(`(?im)\b((?:proxy-)?authorization)[ \t]*:[ \t]*([A-Za-z]+)[ \t]+[^\r\n]+`), func(_ string, _ int, g []string) (string, bool) {
		return g[1] + ": " + g[2] + " <redacted>", true
	}},
	{regexp.MustCompile(`(?i)([a-z0-9_.-]+)[ \t]*([:=])[ \t]*(?:"[^"\r\n]*"|'[^'\r\n]*')`), sensitiveAssignment},
	{regexp.MustCompile(`(?im)([a-z0-9_.-]+)[ \t]*(:)[ \t]*[^\r\n,;]+`), sensitiveAssignment},
	{regexp.MustCompile(`(?i)([a-z0-9_.-]+)[ \t]*(=)[ \t]*[^\s"',;]+`), sensitiveAssignment},
	{regexp.MustCompile(`eyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`), fixed("jwt-<redacted>")},
	{regexp.MustCompile(`(?:AKIA|ASIA)[A-Z0-9]{16}`), fixed("aws-access-key-<redacted>")},
	{regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]{8,}`), fixed("Bearer <redacted>")},
	{regexp.MustCompile(`(^|[^A-Za-z0-9])sk-[a-zA-Z0-9_-]{8,}`), keepFirst("sk-<redacted>")},
	{regexp.MustCompile(`nvapi-[a-zA-Z0-9_-]{8,}`), fixed("nvapi-<redacted>")},
	{regexp.MustCompile(`crsr_[a-f0-9]{16,}`), fixed("crsr_<redacted>")},
	{regexp.MustCompile(`sha256~[A-Za-z0-9._~-]+`), fixed("sha256~<redacted>")},
	{regexp.MustCompile(`(^|[^A-Za-z0-9_-])ya29\.[A-Za-z0-9_-]{20,}`), keepFirst("ya29.<redacted>")},
	{regexp.MustCompile(`(?i)gh[pour]_[A-Za-z0-9]{36}`), fixed("github-token-<redacted>")},
	{regexp.MustCompile(`(?i)ghs_[A-Za-z0-9._-]{36,}`), fixed("github-token-<redacted>")},
	{regexp.MustCompile(`(?i)github_pat_[A-Za-z0-9_]{20,}`), fixed("github-token-<redacted>")},
	{regexp.MustCompile(`(?i)xox[baprs]-[A-Za-z0-9-]{10,}`), fixed("slack-token-<redacted>")},
	{regexp.MustCompile(`AIza[A-Za-z0-9_-]{20,}`), fixed("google-api-key-<redacted>")},
	{regexp.MustCompile(`(?i)glpat-[A-Za-z0-9_-]{20,}`), fixed("gitlab-token-<redacted>")},
	{regexp.MustCompile(`\b[0-9a-f]{32}\.[A-Za-z0-9_-]{24,}`), fixed(RedactedMarker)},
	{regexp.MustCompile(`(?m)^([ \t]*(?:export[ \t]+)?)([A-Za-z_][A-Za-z0-9_]*)=(.+)$`), envDump},
}

// sensitiveKeyWord is the word a key must contain for an assignment to be redacted.
var sensitiveKeyWord = regexp.MustCompile(`(?i)api[_-]?key|secret|password|credential|authorization|bearer|token|access[_-]?key|session[_-]?token|private[_-]?key|service[_-]?account[_-]?key|cookie|session[_-]?cookie|client[_-]?certificate|certificate|cert|passphrase|oauth`)

// envDumpName is a variable name the env-dump rule redacts.
var envDumpName = regexp.MustCompile(`(?i)KEY|TOKEN|SECRET|PASSWORD|PASSWD|AUTH|CREDENTIAL`)

func isKeyChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == '-'
}

// sensitiveAssignment redacts the value of key=value, key: value and quoted
// assignments whose key is sensitive, keeping the key.
func sensitiveAssignment(s string, start int, g []string) (string, bool) {
	if start > 0 && isKeyChar(s[start-1]) {
		return "", false
	}
	run := g[1]
	key := strings.TrimLeft(run, ".-")
	lead := run[:len(run)-len(key)]
	if key == "" || !sensitiveKeyWord.MatchString(key) || !isSensitiveKey(key) {
		return "", false
	}
	return lead + key + g[2] + "<redacted>", true
}

// envDump redacts NAME=value lines whose name looks like a credential.
func envDump(_ string, _ int, g []string) (string, bool) {
	value := g[3]
	if !envDumpName.MatchString(g[2]) || strings.Contains(value, "redacted>") || strings.HasPrefix(value, "[redacted") {
		return "", false
	}
	return g[1] + g[2] + "=" + RedactedMarker, true
}

var secretKeyParts = map[string]bool{
	"auth": true, "authorization": true, "bearer": true, "cert": true, "certificate": true, "cookie": true,
	"credential": true, "credentials": true, "key": true, "oauth": true, "passphrase": true, "password": true,
	"private": true, "secret": true, "token": true, "auths": true, "authorizations": true, "bearers": true,
	"passwords": true, "secrets": true, "tokens": true,
}

var tokenCountKeys = map[string]bool{
	"cached_tokens": true, "completion_tokens": true, "expected_max_tokens": true, "frontmatter_tokens": true,
	"input_tokens": true, "instructions_tokens": true, "n_input_tokens": true, "n_cache_tokens": true,
	"n_output_tokens": true, "output_tokens": true, "prompt_tokens": true, "reasoning_output_tokens": true,
	"last_token_usage": true, "max_completion_tokens": true, "max_output_tokens": true, "max_tokens": true,
	"recommended_max_tokens": true, "token_count": true, "tokens": true, "total_cached_tokens": true,
	"total_completion_tokens": true, "total_prompt_tokens": true, "total_tokens": true,
}

var (
	camelBoundary = regexp.MustCompile(`([a-z0-9])([A-Z])`)
	nonAlnum      = regexp.MustCompile(`[^a-zA-Z0-9]+`)
)

// isSensitiveKey ports SkillEvaluator's is_sensitive_key.
func isSensitiveKey(key string) bool {
	normalized := strings.ToLower(strings.Trim(nonAlnum.ReplaceAllString(camelBoundary.ReplaceAllString(key, "${1}_${2}"), "_"), "_"))
	if tokenCountKeys[normalized] {
		return false
	}
	compact := strings.ReplaceAll(normalized, "_", "")
	if strings.Contains(normalized, "api_key") || strings.Contains(compact, "apikey") {
		return true
	}
	if strings.Contains(compact, "accesskey") || strings.Contains(compact, "privatekey") || strings.Contains(compact, "sessiontoken") {
		return true
	}
	parts := strings.Split(normalized, "_")
	for _, p := range parts {
		if p == "token" {
			return true
		}
	}
	if strings.HasSuffix(compact, "token") {
		return true
	}
	for _, p := range parts {
		if secretKeyParts[p] {
			return true
		}
	}
	return false
}
