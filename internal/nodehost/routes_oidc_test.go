package nodehost_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func TestOIDCTokenIsMaskedBeforeTheNodeGetsIt(t *testing.T) {
	s := start(t)
	service := newService(t)
	masker := secrets.NewMasker()
	service.Masks = nodehost.NewMaskRegistry(masker)
	service.OIDC = func(_ context.Context, auth capability.Authorized, audience string) (nodehost.ProcessOIDCToken, error) {
		if audience != "sts.amazonaws.com" || auth.Binding.RunID != "run-1" || auth.Binding.NodeID != "deploy" {
			t.Fatalf("OIDC audience=%q binding=%+v", audience, auth.Binding)
		}
		return nodehost.ProcessOIDCToken{Token: "eyJ.signed.jwt", ExpiresAt: time.Unix(1e9, 0).UTC()}, nil
	}
	got := ask(t, s, http.MethodPost, "/node/v1/oidc-token", admitService(t, s, service), `{"audience":"sts.amazonaws.com"}`)
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
		token      nodehost.ProcessOIDCToken
		err        error
		status     int
		code       string
	}{
		{name: "backend", body: `{"audience":"a"}`, err: errors.New("boom at http://ctl.internal/?token=" + claimToken), status: http.StatusServiceUnavailable, code: "unavailable"},
		{name: "empty token", body: `{"audience":"a"}`, status: http.StatusBadRequest, code: "protocol_error"},
		{name: "no expiry", body: `{"audience":"a"}`, token: nodehost.ProcessOIDCToken{Token: "token"}, status: http.StatusBadRequest, code: "protocol_error"},
		{name: "no audience", body: `{}`, status: http.StatusBadRequest, code: "bad_request"},
		{name: "not JSON", body: `audience=a`, status: http.StatusBadRequest, code: "bad_request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := start(t)
			service := newService(t)
			calls := 0
			service.OIDC = func(context.Context, capability.Authorized, string) (nodehost.ProcessOIDCToken, error) {
				calls++
				return tc.token, tc.err
			}
			wantRefusal(t, ask(t, s, http.MethodPost, "/node/v1/oidc-token", admitService(t, s, service), tc.body), tc.status, tc.code)
			if (tc.name == "no audience" || tc.name == "not JSON") && calls != 0 {
				t.Fatal("malformed request reached issuer")
			}
		})
	}
}
