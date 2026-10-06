package secrets

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
)

func TestMaskerHidesEncodedSecrets(t *testing.T) {
	for _, secret := range []string{`Tr0ub4dor&3"x`, `p@ss w0rd/+"` + "\t" + `ok&=`, "line-one-secret\nline-two-secret", "private!token/123", `it's (a) *star* ~é`} {
		m := NewMasker()
		m.Register(secret)
		goJSON, _ := json.Marshal(map[string]string{"password": secret})
		cases := map[string]string{
			"base64 std":       "b64 " + base64.StdEncoding.EncodeToString([]byte(secret)),
			"base64 raw url":   base64.RawURLEncoding.EncodeToString([]byte(secret)),
			"url query escape": "urlenc " + url.QueryEscape(secret),
			"uri component":    strings.ReplaceAll(url.QueryEscape(secret), "+", "%20"),
			"js uri component": jsEncodeURIComponent(secret),
			"url path escape":  "/v1/" + url.PathEscape(secret),
			"json no html":     `{"password":"` + strings.ReplaceAll(strings.ReplaceAll(secret, `"`, `\"`), "\t", `\t`) + `"}`,
			"go json":          string(goJSON),
			"go %q":            fmt.Sprintf("%q", secret),
		}
		for name, line := range cases {
			got := m.Mask(line)
			if !strings.Contains(got, "***") {
				t.Errorf("%q %s: not masked: %q", secret, name, got)
			}
		}
		for lead := range 3 {
			raw := []byte(strings.Repeat("a", lead) + "user:" + secret + ":end")
			for name, enc := range map[string]*base64.Encoding{"std": base64.StdEncoding} {
				blob := enc.EncodeToString(raw)
				if got := m.Mask(blob); !strings.Contains(got, "***") {
					t.Errorf("%q embedded %s base64 lead %d: not masked: %q", secret, name, lead, got)
				}
			}
		}
	}
}

func TestMaskerEncodedFormsSkipShortAndIdenticalForms(t *testing.T) {
	m := NewMasker()
	m.Register("ab")
	m.Register("plainalnum")
	if got := m.Mask("a b ab YWI plainalnum"); got != "a b *** YWI ***" {
		t.Fatalf("Mask = %q", got)
	}
	for _, v := range m.Values() {
		if len(v) < 4 && v != "ab" {
			t.Fatalf("short encoded form registered: %q in %q", v, m.Values())
		}
	}
}

func jsEncodeURIComponent(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if c < 0x80 && (c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-_.!~*'()", rune(c))) {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

func TestMaskerHidesJavaScriptURIComponentEscapes(t *testing.T) {
	for _, tc := range []struct{ secret, printed string }{
		{"private!token/123", "private!token%2F123"},
		{"k=a'b(c)d*e~", "k%3Da'b(c)d*e~"},
		{"ke y'(*)!/é", "ke%20y'(*)!%2F%C3%A9"},
	} {
		m := NewMasker()
		m.Register(tc.secret)
		if got := m.Mask("url=" + tc.printed); got != "url=***" {
			t.Errorf("secret %q printed as %q: Mask = %q", tc.secret, tc.printed, got)
		}
	}
}
