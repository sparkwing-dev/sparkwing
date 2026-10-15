package client

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestClassifyHTTPError_ReadsTheTokenState(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		state  string
	}{
		{"revoked", http.StatusUnauthorized, `{"error":"unauthenticated","token_state":"revoked","message":"token is revoked"}`, "revoked"},
		{"expired", http.StatusUnauthorized, `{"error":"unauthenticated","token_state":"expired","message":"token is expired"}`, "expired"},
		{"unknown token", http.StatusUnauthorized, `{"error":"unauthenticated","message":"unknown token"}`, ""},
		{"state outside the enum", http.StatusUnauthorized, `{"error":"unauthenticated","token_state":"sleepy"}`, ""},
		{"not json", http.StatusUnauthorized, `unauthorized`, ""},
		{"a 403 naming a state", http.StatusForbidden, `{"error":"forbidden","token_state":"revoked"}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := &http.Response{
				StatusCode: tc.status,
				Header:     http.Header{},
				Body:       io.NopCloser(strings.NewReader(tc.body)),
			}
			dead := AsTokenDead(classifyHTTPError(resp))
			ok := dead != nil
			if tc.state == "" {
				if ok {
					t.Fatalf("classified %q as a dead token (%s)", tc.body, dead.State)
				}
				return
			}
			if !ok || dead.State != tc.state {
				t.Fatalf("classified %q as %v, want a %s token", tc.body, dead, tc.state)
			}
		})
	}
}

func TestTokenPrefix_NeverPrintsTheSecret(t *testing.T) {
	if got := TokenPrefix("swr_abcdefgh_secretsecretsecret"); strings.Contains(got, "secret") || got == "" {
		t.Fatalf("TokenPrefix = %q", got)
	}
	if got := TokenPrefix("short"); got != "(malformed)" {
		t.Fatalf("TokenPrefix of a short token = %q, want (malformed)", got)
	}
}
