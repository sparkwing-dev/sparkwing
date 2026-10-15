package acceptance_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
)

type credentialIssuer struct {
	provisions  []capability.ProvisionSpec
	delegations []capability.DelegateSpec
	err         error
}

func (f *credentialIssuer) ProvisionCapability(_ context.Context, spec capability.ProvisionSpec) (capability.Issued, error) {
	f.provisions = append(f.provisions, spec)
	return f.result(spec.Team, spec.Grants, spec.ExpiresAt), f.err
}

func (f *credentialIssuer) DelegateCapability(_ context.Context, spec capability.DelegateSpec) (capability.Issued, error) {
	f.delegations = append(f.delegations, spec)
	return f.result("team", spec.Grants, spec.ExpiresAt), f.err
}

func (*credentialIssuer) result(team string, grants []capability.Grant, expires *time.Time) capability.Issued {
	return capability.Issued{Raw: "one-time-private-bearer", Capability: capability.Capability{Prefix: "new-prefix", Team: team, Grants: grants, ExpiresAt: expires}}
}

func issuanceServer(b *credentialBoundary, issuer *credentialIssuer) http.Handler {
	return controller.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil))).WithAuthorization(consoleAuthorization(b)).WithCredentialIssuer(issuer).Handler()
}

func issuanceSession() *credentialBoundary {
	b := authorityMember()
	p := &b.snapshot.Authorities[0].Principal
	p.Class, p.Authority = capability.CredentialSession, capability.AuthorityRef{Kind: capability.AuthoritySession, ID: "account-session"}
	return b
}

func TestCredentialIssuanceHTTPUsesAuthenticatedOrigin(t *testing.T) {
	for _, session := range []bool{true, false} {
		b := authorityMember()
		if session {
			b = issuanceSession()
		}
		issuer := &credentialIssuer{}
		w := authorityHTTP(issuanceServer(b, issuer), b, "POST", "/api/v1/tokens?team=foreign", `{"grants":["runs.read"],"expires_at":"2100-01-01T00:00:00Z"}`)
		var result struct {
			Capability, Prefix, Team string
			Grants                   []capability.Grant
			ExpiresAt                time.Time `json:"expires_at"`
		}
		if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Capability != "one-time-private-bearer" || result.Prefix != "new-prefix" || result.Team != "team" || !reflect.DeepEqual(result.Grants, []capability.Grant{capability.RunsRead}) || result.ExpiresAt.Year() != 2100 {
			t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
		}
		p := b.snapshot.Authorities[0].Principal
		if session {
			if len(issuer.provisions) != 1 || len(issuer.delegations) != 0 {
				t.Fatal("wrong issuance variant")
			}
			spec := issuer.provisions[0]
			if spec.Kind != capability.ProvisionMember || spec.Authority == nil || *spec.Authority != p.Authority || spec.Team != p.Capability.Team || spec.Subject != p.Capability.Subject || spec.Metered || spec.Binding != (capability.Binding{Kind: capability.BindingNone}) || spec.Data != nil || len(spec.ReservedPools) != 0 || len(spec.Repositories) != 0 || spec.RegistrationID != "" {
				t.Fatalf("spec=%+v", spec)
			}
		} else {
			if len(issuer.delegations) != 1 || len(issuer.provisions) != 0 {
				t.Fatal("bearer detached from its ancestry")
			}
			spec := issuer.delegations[0]
			if spec.Parent != p.Authority || spec.Audience != capability.Controller || spec.Binding != p.Capability.Binding || spec.Data != nil || len(spec.Repositories) != 0 {
				t.Fatalf("spec=%+v", spec)
			}
		}
		if w.Header().Get("Cache-Control") != "no-store" || b.bindingRule != capability.BindNone || b.bindingTarget.Team != "team" || strings.Contains(w.Body.String(), "authority") {
			t.Fatalf("headers=%v binding=%s %v", w.Header(), b.bindingRule, b.bindingTarget)
		}
	}
}

func TestCredentialIssuanceHTTPRefusesInvalidAndDeadAuthority(t *testing.T) {
	for _, state := range []string{"anonymous", "revoked", "membership", "machine", "claim", "grant", "binding", "csrf", "transaction", "private error", "missing issuer"} {
		t.Run(state, func(t *testing.T) {
			b, issuer := issuanceSession(), &credentialIssuer{}
			status, code, calls := 403, apierr.Forbidden, 0
			switch state {
			case "anonymous":
				b.raw, status, code = "", 401, apierr.Unauthenticated
			case "revoked":
				b.snapshot.Authorities[0].Principal.Capability.RevokedAt = &b.snapshot.Now
				status, code = 401, apierr.Unauthenticated
			case "membership":
				b.snapshot.Authorities[0].MembershipValid = false
				status, code = 401, apierr.Unauthenticated
			case "machine":
				b = authorityMember()
				b.snapshot.Authorities[0].Principal.Capability.Subject.Kind = capability.SubjectMachine
			case "claim":
				b = newCredentialBoundary()
			case "grant":
				b.snapshot.Authorities[0].Principal.Capability.Grants = nil
			case "binding":
				b.bindingErr = &apierr.Error{Code: apierr.Forbidden, Message: "outside team"}
			case "transaction":
				issuer.err = &apierr.Error{Code: apierr.Unauthenticated, Message: "membership changed"}
				status, code, calls = 401, apierr.Unauthenticated, 1
			case "private error":
				issuer.err = errors.New("private credential")
				status, code, calls = 500, apierr.Internal, 1
			case "missing issuer":
				status, code = 500, apierr.PolicyMissing
			}
			server := controller.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil))).WithAuthorization(consoleAuthorization(b))
			if state != "missing issuer" {
				server.WithCredentialIssuer(issuer)
			}
			if state == "csrf" {
				a := consoleAuthorization(b)
				a.CheckCSRF = nil
				server.WithAuthorization(a)
				status, code = 500, apierr.PolicyMissing
			}
			w := authorityHTTP(server.Handler(), b, "POST", "/api/v1/tokens", `{"grants":["runs.read"]}`)
			assertEngineError(t, w, status, code)
			if len(issuer.provisions)+len(issuer.delegations) != calls || strings.Contains(w.Body.String(), "one-time-private-bearer") {
				t.Fatalf("issuer=%+v body=%s", issuer, w.Body.String())
			}
		})
	}
}

func TestCredentialIssuanceHTTPRejectsInvalidInput(t *testing.T) {
	for _, body := range []string{`{}`, `null`, `{"grants":null}`, `{"grants":[null]}`, `{"grants":["unknown"]}`, `{"grants":["runs.read","runs.read"]}`, `{"grants":[],"expires_at":"invalid"}`, `{"grants":[]} {}`, `{"scopes":["runs.read"]}`} {
		b, issuer := issuanceSession(), &credentialIssuer{}
		w := authorityHTTP(issuanceServer(b, issuer), b, "POST", "/api/v1/tokens", body)
		assertEngineError(t, w, 400, apierr.BadRequest)
		if len(issuer.provisions)+len(issuer.delegations) != 0 {
			t.Fatal("invalid input reached issuer")
		}
	}
}

func FuzzCredentialIssuanceHTTPRejectsCallerAuthority(f *testing.F) {
	f.Add("operator", uint8(0))
	f.Fuzz(func(t *testing.T, value string, field uint8) {
		keys := []string{"team", "subject", "authority", "kind", "metered", "reserved_pools", "repositories", "binding", "parent", "data", "registration_id", "principal"}
		body, err := json.Marshal(map[string]any{"grants": []string{"runs.read"}, keys[int(field)%len(keys)]: value})
		if err != nil {
			t.Fatal(err)
		}
		b, issuer := issuanceSession(), &credentialIssuer{}
		w := authorityHTTP(issuanceServer(b, issuer), b, "POST", "/api/v1/tokens", string(body))
		assertEngineError(t, w, 400, apierr.BadRequest)
		if len(issuer.provisions)+len(issuer.delegations) != 0 {
			t.Fatal("caller-selected authority reached issuer")
		}
	})
}
