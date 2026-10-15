package acceptance_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

// safety: these independent paths exercise Server registrations, not a generated policy inventory.
var consoleRequests = [][2]string{
	{"GET", "/api/v1/operator/teams"},
	{"GET", "/api/v1/operator/teams/customer"},
	{"DELETE", "/api/v1/operator/teams/customer"},
	{"POST", "/api/v1/operator/teams/customer/trust"},
	{"POST", "/api/v1/operator/teams/customer/grants"},
	{"POST", "/api/v1/operator/teams/customer/freeze"},
	{"POST", "/api/v1/operator/teams/customer/unfreeze"},
	{"DELETE", "/api/v1/operator/accounts/account"},
	{"GET", "/api/v1/operator/waitlist"},
	{"POST", "/api/v1/operator/waitlist/approve"},
	{"DELETE", "/api/v1/operator/github-app/installations/123"},
	{"PUT", "/api/v1/operator/credits/settings"},
	{"POST", "/api/v1/operator/runners/swk_ABCDEFGHIJKL/metered"},
}

func consoleBoundary() *credentialBoundary {
	b := newCredentialBoundary()
	p := capability.Principal{
		Class:     capability.CredentialConsole,
		Authority: capability.AuthorityRef{Kind: capability.AuthoritySession, ID: "console-session"},
		Capability: capability.Capability{
			Digest: sha256.Sum256([]byte(b.raw)), Audience: capability.Controller,
			Subject: capability.Subject{Kind: capability.SubjectOperator, ID: "operator-account"},
			Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.OperatorConsole},
		},
	}
	p.Authority = sessionReference(p)
	b.snapshot.Authorities = []capability.AuthorityState{{Principal: p, MembershipValid: true}}
	return b
}

func consoleAuthorization(b *credentialBoundary) controller.RouteAuthorization {
	return controller.RouteAuthorization{
		Authenticator: capability.NewAuthenticator(b), Bindings: b,
		Identify: func(r *http.Request) (capability.Presentation, error) {
			if p := b.snapshot.Authorities[0].Principal; p.Class == capability.CredentialPeer {
				return capability.Presentation{Class: p.Class, Peer: p.Capability.Subject}, nil
			}
			cookie, err := r.Cookie("console-session")
			if err != nil {
				return capability.Presentation{}, &apierr.Error{Code: apierr.Unauthenticated, Message: "session required"}
			}
			return capability.Presentation{Raw: cookie.Value, Class: b.snapshot.Authorities[0].Principal.Class}, nil
		},
		CheckCSRF: func(r *http.Request, _ capability.Principal) error {
			if r.Header.Get("X-CSRF-Token") != "session-csrf" {
				return &apierr.Error{Code: apierr.Forbidden, Message: "invalid CSRF token"}
			}
			return nil
		},
	}
}

func TestOperatorHTTPRefusesNonConsoleAuthority(t *testing.T) {
	for _, route := range consoleRequests {
		for _, name := range []string{"anonymous", "deployment bearer", "delegated bearer", "account session", "local peer", "revoked session", "expired session", "membership lost", "wrong audience", "missing grant", "missing csrf"} {
			if name == "missing csrf" && route[0] == http.MethodGet {
				continue
			}
			t.Run(route[0]+" "+route[1]+"/"+name, func(t *testing.T) {
				b := consoleBoundary()
				p := &b.snapshot.Authorities[0].Principal
				want := apierr.Forbidden
				r := httptest.NewRequest(route[0], route[1], strings.NewReader(`{}`))
				r.AddCookie(&http.Cookie{Name: "console-session", Value: b.raw})
				r.Header.Set("X-CSRF-Token", "session-csrf")
				switch name {
				case "anonymous":
					r.Header.Del("Cookie")
					want = apierr.Unauthenticated
				case "deployment bearer", "delegated bearer":
					p.Class, p.Authority.Kind, p.Authority.ID = capability.CredentialBearer, capability.AuthorityCapability, "deployment"
					p.Capability.Prefix = p.Authority.ID
					p.Capability.Grants = append(p.Capability.Grants, capability.OperatorTokens, capability.OperatorLimits)
					if name == "delegated bearer" {
						p.Capability.Issuance = capability.Delegate
					}
					r.Header.Set("Authorization", "Bearer "+b.raw)
				case "account session":
					p.Class, p.Capability.Subject.Kind = capability.CredentialSession, capability.SubjectAccount
				case "local peer":
					p.Class, p.Capability.Subject.Kind = capability.CredentialPeer, capability.SubjectLocal
					p.Capability.Digest = [32]byte{}
				case "revoked session":
					p.Capability.RevokedAt = &b.snapshot.Now
					want = apierr.Unauthenticated
				case "expired session":
					p.Capability.ExpiresAt = &b.snapshot.Now
					want = apierr.Unauthenticated
				case "membership lost":
					b.snapshot.Authorities[0].MembershipValid = false
					want = apierr.Unauthenticated
				case "wrong audience":
					p.Capability.Audience = capability.NodeHost
					p.Capability.Binding = capability.Binding{Kind: capability.BindingNode, RunID: "run", NodeID: "job", Holder: "process", Generation: 1, Attempt: 1}
					b.snapshot.Binding = capability.BindingState{Current: p.Capability.Binding, ProcessAlive: true}
				case "missing grant":
					p.Capability.Grants = nil
				case "missing csrf":
					r.Header.Del("X-CSRF-Token")
				}
				s := controller.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil))).WithAuthorization(consoleAuthorization(b))
				w := httptest.NewRecorder()
				s.Handler().ServeHTTP(w, r)
				var body apierr.Envelope
				if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if w.Code != apierr.Status(want) || body.Error == nil || body.Error.Code != want {
					t.Fatalf("got %d %s, want %s", w.Code, w.Body.String(), want)
				}
			})
		}
	}
}

func TestOperatorHTTPRemovesDeploymentEntryPoints(t *testing.T) {
	s := controller.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, route := range [][2]string{
		{"DELETE", "/api/v1/accounts/account"},
		{"DELETE", "/api/v1/teams/customer"},
		{"DELETE", "/api/v1/github-app/installations/123"},
		{"POST", "/api/v1/maintenance/reconcile-orphans"},
	} {
		if s.RegisteredRoute(httptest.NewRequest(route[0], route[1], nil)) {
			t.Errorf("deployment entry point remains: %s %s", route[0], route[1])
		}
	}
}

func TestOperatorHTTPCommitsAuthorizedActions(t *testing.T) {
	for _, dashboard := range []bool{false, true} {
		t.Run(map[bool]string{false: "api", true: "dashboard"}[dashboard], func(t *testing.T) {
			st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			if err := st.AsOperator().CreateTeam(context.Background(), "customer"); err != nil {
				t.Fatal(err)
			}
			b := consoleBoundary()
			var audit bytes.Buffer
			s := controller.New(st, slog.New(slog.NewJSONHandler(&audit, nil))).WithAuthorization(consoleAuthorization(b))
			if dashboard {
				s.WithDashboard(controller.Dashboard{})
			}
			handler := s.Handler()
			call := func(method, path, body string, status int) []byte {
				t.Helper()
				r := httptest.NewRequest(method, path, strings.NewReader(body))
				r.AddCookie(&http.Cookie{Name: "console-session", Value: b.raw})
				r.Header.Set("X-CSRF-Token", "session-csrf")
				r.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				if w.Code != status {
					t.Fatalf("%s %s: %d %s, want %d", method, path, w.Code, w.Body.String(), status)
				}
				if w.Header().Get("Cache-Control") != "no-store" {
					t.Error("console response can be cached")
				}
				return w.Body.Bytes()
			}
			base := "/api/v1/operator/teams/customer"

			call("GET", "/api/v1/operator/teams?q=customer", "", 200)
			call("POST", base+"/trust", `{"trust":"granted","reason":"verified customer"}`, 200)
			call("POST", base+"/grants", `{"amount_cents":100,"reason":"promotion","key":"grant-once","actor":"forged"}`, 400)
			for range 2 {
				call("POST", base+"/grants", `{"amount_cents":100,"reason":"promotion","key":"grant-once"}`, 201)
			}
			call("POST", base+"/freeze", `{"reason":"review"}`, 200)
			call("POST", base+"/unfreeze", `{"reason":"resolved"}`, 200)
			tenant, err := st.ForTeam(context.Background(), "customer")
			if err != nil {
				t.Fatal(err)
			}
			grants, err := tenant.ListCreditGrants(context.Background(), 10)
			if err != nil {
				t.Fatal(err)
			}
			if len(grants) != 1 || grants[0].AmountMicro != 100*store.MicroCreditsPerCent || grants[0].CreatedBy != "operator-account" {
				t.Fatalf("grants = %+v", grants)
			}
			freeze, err := tenant.CreditFreeze(context.Background())
			if err != nil || freeze.Frozen {
				t.Fatalf("freeze = %+v, %v", freeze, err)
			}
			events, err := tenant.BusinessEvents(context.Background(), "")
			if err != nil {
				t.Fatal(err)
			}
			seen := map[string]int{}
			for _, event := range events {
				if event.Kind == "team.created" {
					continue
				}
				if event.Actor != "operator-account" {
					t.Fatalf("action lost its authenticated actor: %+v", event)
				}
				seen[event.Kind]++
			}
			for _, kind := range []string{"credit.granted", "team.frozen", "team.unfrozen", "billing.trust_changed"} {
				if seen[kind] != 1 {
					t.Errorf("%s recorded %d times, want 1", kind, seen[kind])
				}
			}
			call("DELETE", "/api/v1/operator/accounts/missing", "", 404)
			if strings.Contains(audit.String(), b.raw) {
				t.Fatal("audit log contains a reusable session")
			}
			decoder := json.NewDecoder(&audit)
			var records int
			for decoder.More() {
				var record map[string]any
				if err := decoder.Decode(&record); err != nil {
					t.Fatal(err)
				}
				if record["msg"] != "audit" {
					continue
				}
				records++
				if record["principal_id"] != "operator-account" || record["principal_kind"] != "operator" {
					t.Fatalf("audit lost authenticated identity: %+v", record)
				}
			}
			if records == 0 {
				t.Fatal("console actions were not audited")
			}
		})
	}
}

var (
	_ capability.AuthenticationReader = (*credentialBoundary)(nil)
	_ capability.BindingChecker       = (*credentialBoundary)(nil)
)
