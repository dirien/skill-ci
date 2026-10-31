package session

import (
	"encoding/base64"
	"net/url"
	"strings"
	"testing"
)

// Fake values only.
const (
	fakeKey   = "sk-ant-test-0000000000000000000000000000000000000000"
	fakeOther = "plain-secret-value&more<0000>"
)

func TestRedactorExactForms(t *testing.T) {
	r := NewRedactor([]string{fakeKey, " " + fakeOther + " ", "short"})
	tests := []struct {
		name, in string
	}{
		{"raw", "key is " + fakeKey},
		{"base64", base64.StdEncoding.EncodeToString([]byte(fakeOther))},
		{"base64 without padding", base64.RawStdEncoding.EncodeToString([]byte(fakeOther))},
		{"url-safe base64", base64.URLEncoding.EncodeToString([]byte(fakeOther))},
		{"url-encoded", "https://example.test/?k=" + url.QueryEscape(fakeOther)},
		{"json-escaped", `{"v":"plain-secret-value&more<0000>"}`},
		{"json-escaped without html", `{"v":"plain-secret-value&more<0000>"}`},
	}
	for _, tt := range tests {
		out, n := r.String(tt.in)
		if n == 0 || strings.Contains(out, "0000") {
			t.Errorf("%s: %q gave %q (%d)", tt.name, tt.in, out, n)
		}
	}
	if out, n := r.String("this is short and stays"); n != 0 || out != "this is short and stays" {
		t.Errorf("a value under 8 characters was redacted: %q", out)
	}
}

func TestRedactorPatterns(t *testing.T) {
	tests := []struct {
		name, in, gone string
	}{
		{"anthropic", "use sk-ant-api03-abcdefghijklmnop now", "abcdefghijklmnop"},
		{"nvidia", "nvapi-abcdefghijkl", "abcdefghijkl"},
		{"github", "token ghp_" + strings.Repeat("0test", 7) + "0 here", "0test0test0test"},
		{"bearer", "curl -H 'X: Bearer abcdefghijklmnop'", "abcdefghijklmnop"},
		{"auth header", "Authorization: Basic dXNlcjpwYXNz", "dXNlcjpwYXNz"},
		{"assignment", "api_key=abcdef123456", "abcdef123456"},
		{"quoted assignment", `password: "hunter2hunter2"`, "hunter2hunter2"},
		{"ollama", "key 0123456789abcdef0123456789abcdef.AbCdEfGhIjKlMnOpQrStUvWx", "AbCdEfGhIjKlMnOpQrStUvWx"},
		{"env dump", "HOME=/root\nSESSION_SECRET=canary-session-secret-value-0000\nPATH=/bin", "canary-session-secret"},
		{"export", "export MY_TOKEN=abc123abc123", "abc123abc123"},
		{"uri userinfo", "git clone https://user:pa55word@example.test/repo", "pa55word"},
		{"jwt", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U", "dozjgNryP4J3jVmNHl0w5N"},
		{"aws", "AKIA" + "ABCDEFGHIJKLMNOP", "ABCDEFGHIJKLMNOP"},
		{"pem", pemHeader("RSA ") + "\nMIIabc\n-----END RSA PRIVATE KEY-----", "MIIabc"},
		{"pem unterminated", "x " + pemHeader("") + "\nMIIabc", "MIIabc"},
	}
	var nilRedactor *Redactor
	for _, tt := range tests {
		out, n := nilRedactor.String(tt.in)
		if n == 0 || strings.Contains(out, tt.gone) {
			t.Errorf("%s: %q gave %q (%d)", tt.name, tt.in, out, n)
		}
		again, m := nilRedactor.String(out)
		if m != 0 || again != out {
			t.Errorf("%s: redaction is not idempotent: %q gave %q (%d)", tt.name, out, again, m)
		}
	}
	keep := []string{
		"prompt_tokens: 27481",
		"total_tokens: 4096",
		"tool_efficiency: 1/2 productive calls (50%)",
		"Usage: 27481 in, 165 out",
		"The key is in the drawer.",
		"/workspace/RELEASE_NOTES.md",
	}
	for _, s := range keep {
		if out, n := nilRedactor.String(s); n != 0 || out != s {
			t.Errorf("%q was redacted to %q", s, out)
		}
	}
}

// pemHeader builds a private key header at run time, so the file holds no key-shaped literal.
func pemHeader(kind string) string { return "-----BEGIN " + kind + "PRIVATE" + " KEY-----" }

// TestRedactorFailsClosed: a pattern rewrites another key into the exact
// value "sk-<redacted>"; the redactor must not return it.
func TestRedactorFailsClosed(t *testing.T) {
	r := NewRedactor([]string{"sk-<redacted>"})
	out, n := r.String("other key sk-abcdefgh12345678")
	if out != RedactionFailedMarker || n == 0 {
		t.Errorf("got %q (%d), want the fail-closed marker", out, n)
	}
	if !r.Leaks([]byte("x sk-<redacted> y")) || r.Leaks([]byte("nothing")) {
		t.Error("Leaks does not see the value")
	}
	var nilRedactor *Redactor
	if nilRedactor.Leaks([]byte(fakeKey)) {
		t.Error("a nil Redactor reports leaks")
	}
}

func TestRedactJSONKeepsOrderAndKeys(t *testing.T) {
	r := NewRedactor([]string{fakeKey})
	in := `{"z":1,"a":"` + fakeKey + `","api_key=x":[true,null,1.50,"ok"],"m":{"b":"<tag>&"}}`
	out, n, err := r.redactJSON([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"z":1,"a":"[redacted]","api_key=x":[true,null,1.50,"ok"],"m":{"b":"<tag>&"}}`
	if string(out) != want || n != 1 {
		t.Errorf("got %s (%d), want %s", out, n, want)
	}
	if _, _, err := r.redactJSON([]byte(`{`)); err == nil {
		t.Error("invalid JSON: no error")
	}
}
