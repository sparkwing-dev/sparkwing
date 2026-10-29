package nodehost

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func TestOIDCWithoutIssuerExplainsControllerSetup(t *testing.T) {
	noIssuer := &apierr.Error{Code: apierr.NotFound, Message: "this controller holds no OIDC signing key, so it issues no ID tokens"}
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "controller has no issuer", err: noIssuer},
		{name: "wrapped controller error", err: errors.Join(noIssuer, errors.New("http://ctl.internal/?token="+claimToken))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := start(t)
			service := newService(t)
			service.OIDC = func(context.Context, capability.Authorized, string) (ProcessOIDCToken, error) {
				return ProcessOIDCToken{}, tc.err
			}
			got := askRoute(t, s, http.MethodPost, "/node/v1/oidc-token", admitService(t, s, service), `{"audience":"sts.amazonaws.com"}`)
			wantRefusal(t, got, http.StatusNotFound, "not_found")
			failure := got.body["error"].(map[string]any)
			message, _ := failure["message"].(string)
			for _, part := range []string{"OIDC", "controller", "issuer", "docs/oidc.md"} {
				if !strings.Contains(message, part) {
					t.Errorf("OIDC setup error lacks %q: %v", part, failure)
				}
			}
		})
	}
}

func TestOIDCTokenIsMaskedBeforeTheNodeGetsIt(t *testing.T) {
	s := start(t)
	service := newService(t)
	masker := secrets.NewMasker()
	service.Masks = NewMaskRegistry(masker)
	service.OIDC = func(_ context.Context, auth capability.Authorized, audience string) (ProcessOIDCToken, error) {
		if audience != "sts.amazonaws.com" || auth.Binding.RunID != "run-1" || auth.Binding.NodeID != "deploy" {
			t.Fatalf("OIDC audience=%q binding=%+v", audience, auth.Binding)
		}
		return ProcessOIDCToken{Token: "eyJ.signed.jwt", ExpiresAt: time.Unix(1e9, 0).UTC()}, nil
	}
	got := askRoute(t, s, http.MethodPost, "/node/v1/oidc-token", admitService(t, s, service), `{"audience":"sts.amazonaws.com"}`)
	if got.status != http.StatusOK || got.body["token"] != "eyJ.signed.jwt" || got.body["expires_at"] == nil {
		t.Fatalf("status %d body %v", got.status, got.body)
	}
	if strings.Contains(masker.Mask("token eyJ.signed.jwt"), "eyJ.signed.jwt") {
		t.Fatal("token answered before registration")
	}
}

func TestOIDCTokenRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		token      ProcessOIDCToken
		err        error
		status     int
		code       string
	}{
		{name: "backend", body: `{"audience":"a"}`, err: errors.New("boom at http://ctl.internal/?token=" + claimToken), status: http.StatusServiceUnavailable, code: "unavailable"},
		{name: "empty token", body: `{"audience":"a"}`, status: http.StatusBadRequest, code: "protocol_error"},
		{name: "no expiry", body: `{"audience":"a"}`, token: ProcessOIDCToken{Token: "token"}, status: http.StatusBadRequest, code: "protocol_error"},
		{name: "no audience", body: `{}`, status: http.StatusBadRequest, code: "bad_request"},
		{name: "not JSON", body: `audience=a`, status: http.StatusBadRequest, code: "bad_request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := start(t)
			service := newService(t)
			calls := 0
			service.OIDC = func(context.Context, capability.Authorized, string) (ProcessOIDCToken, error) {
				calls++
				return tc.token, tc.err
			}
			wantRefusal(t, askRoute(t, s, http.MethodPost, "/node/v1/oidc-token", admitService(t, s, service), tc.body), tc.status, tc.code)
			if (tc.name == "no audience" || tc.name == "not JSON") && calls != 0 {
				t.Fatal("malformed request reached issuer")
			}
		})
	}
}
