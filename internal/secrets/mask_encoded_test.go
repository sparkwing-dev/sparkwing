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
	for _, secret := range []string{`Tr0ub4dor&3"x`, `p@ss w0rd/+"` + "\t" + `ok&=`, "line-one-secret\nline-two-secret"} {
		m := NewMasker()
		m.Register(secret)
		goJSON, _ := json.Marshal(map[string]string{"password": secret})
		cases := map[string]string{
			"base64 std":       "b64 " + base64.StdEncoding.EncodeToString([]byte(secret)),
			"base64 raw url":   base64.RawURLEncoding.EncodeToString([]byte(secret)),
			"url query escape": "urlenc " + url.QueryEscape(secret),
			"uri component":    strings.ReplaceAll(url.QueryEscape(secret), "+", "%20"),
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
			blob := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("a", lead) + "user:" + secret + ":end"))
			got := m.Mask(blob)
			if !strings.Contains(got, "***") {
				t.Errorf("%q embedded base64 lead %d: not masked: %q", secret, lead, got)
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
