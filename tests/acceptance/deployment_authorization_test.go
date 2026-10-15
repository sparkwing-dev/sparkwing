package acceptance_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
)

var deploymentRequests = []struct {
	method, path string
	grant        capability.Grant
}{
	{"GET", "/api/v1/operator/limits", capability.OperatorLimits},
	{"PUT", "/api/v1/operator/limits", capability.OperatorLimits},
	{"GET", "/api/v1/operator/signups", capability.OperatorLimits},
	{"PUT", "/api/v1/operator/signups", capability.OperatorLimits},
	{"GET", "/api/v1/operator/object-store", capability.OperatorObjectStore},
	{"GET", "/api/v1/operator/object-store/breaker", capability.OperatorObjectStore},
	{"POST", "/api/v1/operator/object-store/reset-breaker", capability.OperatorObjectStore},
	{"PUT", "/api/v1/operator/object-store/settings", capability.OperatorObjectStore},
	{"PUT", "/api/v1/operator/object-store/quotas/customer", capability.OperatorObjectStore},
	{"PUT", "/api/v1/operator/object-store/quotas/customer/allowance", capability.OperatorObjectStore},
}

func deploymentBoundary(grant capability.Grant) *credentialBoundary {
	b := consoleBoundary()
	p := &b.snapshot.Authorities[0].Principal
	p.Class = capability.CredentialBearer
	p.Authority = capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "deployment"}
	p.Capability.Prefix = p.Authority.ID
	p.Capability.Grants = []capability.Grant{grant}
	return b
}

func TestDeploymentHTTPAuthorization(t *testing.T) {
	for _, route := range deploymentRequests {
		for _, name := range []string{"anonymous", "wrong grant", "member", "service", "run-bound operator", "session", "revoked", "expired", "console without csrf"} {
			if name == "console without csrf" && route.method == "GET" {
				continue
			}
			t.Run(route.method+" "+route.path+"/"+name, func(t *testing.T) {
				b := deploymentBoundary(route.grant)
				p := &b.snapshot.Authorities[0].Principal
				r := httptest.NewRequest(route.method, route.path, strings.NewReader(`{}`))
				r.Header.Set("Authorization", "Bearer "+b.raw)
				want := apierr.Forbidden
				switch name {
				case "anonymous":
					r.Header.Del("Authorization")
					want = apierr.Unauthenticated
				case "wrong grant":
					p.Capability.Grants = []capability.Grant{capability.OperatorTokens}
				case "member":
					p.Capability.Subject.Kind = capability.SubjectAccount
				case "service":
					p.Capability.Subject.Kind = capability.SubjectService
				case "run-bound operator":
					p.Capability.Binding = capability.Binding{Kind: capability.BindingRun, RunID: "run"}
				case "session":
					p.Class, p.Authority.Kind, p.Capability.Subject.Kind = capability.CredentialSession, capability.AuthoritySession, capability.SubjectAccount
					r.Header.Del("Authorization")
					r.AddCookie(&http.Cookie{Name: "console-session", Value: b.raw})
					r.Header.Set("X-CSRF-Token", "session-csrf")
				case "revoked":
					p.Capability.RevokedAt = &b.snapshot.Now
					want = apierr.Unauthenticated
				case "expired":
					p.Capability.ExpiresAt = &b.snapshot.Now
					want = apierr.Unauthenticated
				case "console without csrf":
					b = consoleBoundary()
					r.Header.Del("Authorization")
					r.AddCookie(&http.Cookie{Name: "console-session", Value: b.raw})
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

func TestDeploymentHTTPRemovesLegacyRoutes(t *testing.T) {
	s := controller.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, route := range [][2]string{
		{"GET", "/api/v1/admin/usage-metrics"},
		{"PUT", "/api/v1/teams/customer/repos/owner/repo/dispatch"},
		{"GET", "/api/v1/compute-limits"}, {"PUT", "/api/v1/compute-limits"},
		{"GET", "/api/v1/signups"}, {"PUT", "/api/v1/signups"},
		{"GET", "/api/v1/object-store/breaker"}, {"POST", "/api/v1/object-store/reset-breaker"},
		{"PUT", "/api/v1/storage/settings"}, {"PUT", "/api/v1/storage/quotas/customer"},
		{"PUT", "/api/v1/storage/quotas/customer/allowance"},
	} {
		if s.RegisteredRoute(httptest.NewRequest(route[0], route[1], nil)) {
			t.Errorf("legacy route remains: %s %s", route[0], route[1])
		}
	}
}

func TestDeploymentHTTPBreakerPermitsExactAuthority(t *testing.T) {
	for _, console := range []bool{false, true} {
		b := deploymentBoundary(capability.OperatorObjectStore)
		if console {
			b = consoleBoundary()
		}
		s := controller.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil))).WithAuthorization(consoleAuthorization(b))
		for _, route := range [][2]string{{"GET", "/api/v1/operator/object-store/breaker"}, {"POST", "/api/v1/operator/object-store/reset-breaker"}} {
			r := httptest.NewRequest(route[0], route[1], nil)
			if console {
				r.AddCookie(&http.Cookie{Name: "console-session", Value: b.raw})
				r.Header.Set("X-CSRF-Token", "session-csrf")
			} else {
				r.Header.Set("Authorization", "Bearer "+b.raw)
			}
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, r)
			if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("console=%t %s: %d %s", console, route[1], w.Code, w.Body.String())
			}
			var result controller.ObjectStoreBreakerResponse
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || len(result.Classes) == 0 {
				t.Fatalf("breaker response = %+v, %v", result, err)
			}
		}
	}
}
