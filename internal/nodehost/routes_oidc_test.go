package nodehost_test

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestOIDCTokenIsMaskedBeforeTheNodeGetsIt(t *testing.T) {
	s, logs := start(t)
	c := newController(t)
	var audience map[string]string
	c.handle("POST /api/v1/runs/run-1/oidc-token", func(w http.ResponseWriter, r *http.Request) {
		_ = decodeJSON(r, &audience)
		answer(http.StatusOK, map[string]any{"token": "eyJ.signed.jwt", "expires_at": time.Unix(1e9, 0).UTC()})(w, r)
	})
	bearer, masker := admitClaim(t, s, "deploy", claimFor(t, c))

	got := ask(t, s, http.MethodPost, "/node/v1/oidc-token", bearer, `{"audience":"sts.amazonaws.com"}`)
	if got.status != http.StatusOK || got.body["token"] != "eyJ.signed.jwt" || got.body["expires_at"] == nil {
		t.Fatalf("status %d body %v", got.status, got.body)
	}
	if audience["audience"] != "sts.amazonaws.com" {
		t.Fatalf("forwarded audience %v", audience)
	}
	if strings.Contains(masker.Mask("token eyJ.signed.jwt"), "eyJ.signed.jwt") {
		t.Fatal("the OIDC token was answered without being masked")
	}
	if strings.Contains(logs.String(), "eyJ.signed.jwt") {
		t.Fatalf("logs leak the OIDC token:\n%s", logs.String())
	}
}

func TestOIDCTokenRefusals(t *testing.T) {
	cases := []struct {
		name, body string
		upstream   http.HandlerFunc
		status     int
		code       string
	}{
		{
			"no signing key", `{"audience":"a"}`, answer(http.StatusNotFound, map[string]string{"error": "no key"}),
			http.StatusNotFound, "oidc_unavailable",
		},
		{
			"not issued to claims", `{"audience":"a"}`, answer(http.StatusUnprocessableEntity, map[string]string{"error": "later"}),
			http.StatusNotFound, "oidc_unavailable",
		},
		{"controller error", `{"audience":"a"}`, leak, http.StatusBadGateway, "upstream_unavailable"},
		{
			"empty token", `{"audience":"a"}`, answer(http.StatusOK, map[string]string{"token": ""}),
			http.StatusBadGateway, "upstream_unavailable",
		},
		{"no audience", `{}`, nil, http.StatusBadRequest, "bad_request"},
		{"not JSON", `audience=a`, nil, http.StatusBadRequest, "bad_request"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, logs := start(t)
			c := newController(t)
			if tc.upstream != nil {
				c.handle("POST /api/v1/runs/run-1/oidc-token", tc.upstream)
			}
			bearer, _ := admitClaim(t, s, "deploy", claimFor(t, c))
			wantRefusal(t, ask(t, s, http.MethodPost, "/node/v1/oidc-token", bearer, tc.body), tc.status, tc.code)
			if tc.upstream == nil && len(c.asked()) > 0 {
				t.Fatalf("a malformed request reached the controller: %v", c.asked())
			}
			noLeak(t, logs)
		})
	}
}
