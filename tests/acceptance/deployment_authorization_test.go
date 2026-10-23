package acceptance_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
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
	p.Capability.Subject.Kind = capability.SubjectOperator
	p.Authority = capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "deployment"}
	p.Capability.Prefix = p.Authority.ID
	p.Capability.Grants = []capability.Grant{grant}
	return b
}

type deploymentWrites struct {
	seen []capability.Authorized
}

func (f *deploymentWrites) WriteComputeLimits(_ context.Context, a capability.Authorized, _ map[string]int64) (store.ComputeLimits, error) {
	f.seen = append(f.seen, a)
	return store.ComputeLimits{}, nil
}

func (f *deploymentWrites) WriteSignUpSettings(_ context.Context, a capability.Authorized, _ store.SignUpSettingsChange) error {
	f.seen = append(f.seen, a)
	return nil
}

func (f *deploymentWrites) WriteStorageSettings(_ context.Context, a capability.Authorized, _ store.StorageSettings) error {
	f.seen = append(f.seen, a)
	return nil
}

func (f *deploymentWrites) WriteStorageQuota(_ context.Context, a capability.Authorized, value store.StorageQuota) (store.StorageQuota, error) {
	f.seen = append(f.seen, a)
	value.Team = store.Team(a.Target.Team)
	return value, nil
}

func (f *deploymentWrites) WriteStorageAllowance(_ context.Context, a capability.Authorized, value int64) (store.StorageQuota, error) {
	f.seen = append(f.seen, a)
	return store.StorageQuota{Team: store.Team(a.Target.Team), AllowanceBytes: value}, nil
}

func TestDeploymentHTTPAuthorization(t *testing.T) {
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	teststore.SeedTeam(t, st, "customer")

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
				r.Header.Set("Content-Type", "application/json")
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
					p.Class, p.Authority, p.Capability.Subject.Kind = capability.CredentialSession, sessionReference(*p), capability.SubjectAccount
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
				writes := &deploymentWrites{}
				s := controller.New(st, slog.New(slog.NewTextHandler(io.Discard, nil))).WithAuthorization(consoleAuthorization(b)).WithOperatorSettingsWriter(writes)
				if name == "service" && strings.HasSuffix(route.path, "/signups") && route.method == "PUT" {
					r.Body = io.NopCloser(strings.NewReader(`{"mode":"open"}`))
				}
				w := httptest.NewRecorder()
				s.Handler().ServeHTTP(w, r)
				if name == "service" {
					wantWrites := 0
					if route.method == "PUT" {
						wantWrites = 1
					}
					if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || len(writes.seen) != wantWrites {
						t.Fatalf("service: HTTP %d %s writes=%d", w.Code, w.Body, len(writes.seen))
					}
					if wantWrites > 0 && (writes.seen[0].Principal.Capability.Subject.Kind != capability.SubjectService || len(writes.seen[0].Access.Grants) != 1 || writes.seen[0].Access.Grants[0] != route.grant) {
						t.Fatal("settings transaction lost service authority")
					}
					return
				}
				if len(writes.seen) != 0 {
					t.Fatal("refused authority reached a settings transaction")
				}
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
		{"GET", "/api/v1/compute-limits"},
		{"PUT", "/api/v1/compute-limits"},
		{"GET", "/api/v1/signups"},
		{"PUT", "/api/v1/signups"},
		{"GET", "/api/v1/object-store/breaker"},
		{"POST", "/api/v1/object-store/reset-breaker"},
		{"PUT", "/api/v1/storage/settings"},
		{"PUT", "/api/v1/storage/quotas/customer"},
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
