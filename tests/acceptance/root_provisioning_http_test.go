package acceptance_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

type rootIssuer struct {
	specs []capability.ProvisionSpec
	err   error
}

func (f *rootIssuer) ProvisionCapability(_ context.Context, spec capability.ProvisionSpec) (capability.Issued, error) {
	f.specs = append(f.specs, spec)
	return capability.Issued{Raw: "private-provisioned-root", Capability: capability.Capability{
		Prefix: "root-prefix", Team: spec.Team, Subject: spec.Subject, Audience: capability.Controller,
		Grants: spec.Grants, Binding: spec.Binding, Issuance: capability.Delegate,
		Repositories: spec.Repositories, ReservedPools: spec.ReservedPools, ExpiresAt: spec.ExpiresAt,
	}}, f.err
}

func (*rootIssuer) DelegateCapability(context.Context, capability.DelegateSpec) (capability.Issued, error) {
	return capability.Issued{}, errors.New("root enrollment cannot delegate")
}

func rootServer(b *credentialBoundary, issuer *rootIssuer) http.Handler {
	return controller.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil))).WithAuthorization(consoleAuthorization(b)).WithCredentialIssuer(issuer).Handler()
}

func rootFixture(service bool) (*credentialBoundary, string, string) {
	if service {
		return deploymentBoundary(capability.OperatorTokens), "/api/v1/operator/tokens", `{"name":"launcher","grants":["claims.launch"]}`
	}
	b := issuanceSession()
	b.snapshot.Authorities[0].Principal.Capability.Grants = append(b.snapshot.Authorities[0].Principal.Capability.Grants, capability.RunsWrite)
	return b, "/api/v1/runners", `{"name":"builder","repos":["GitHub.com/acme/*"],"reserved_pools":["trusted"]}`
}

func TestRootProvisioningHTTPOrigin(t *testing.T) {
	for _, service := range []bool{false, true} {
		for _, session := range []bool{false, true} {
			b, path, body := rootFixture(service)
			if !session && !service {
				b = authorityMember()
				b.snapshot.Authorities[0].Principal.Capability.Grants = append(b.snapshot.Authorities[0].Principal.Capability.Grants, capability.RunsWrite)
			}
			if session {
				if service {
					b = consoleBoundary()
				} else {
					b = issuanceSession()
					b.snapshot.Authorities[0].Principal.Capability.Grants = append(b.snapshot.Authorities[0].Principal.Capability.Grants, capability.RunsWrite)
				}
			}
			f := &rootIssuer{}
			w := authorityHTTP(rootServer(b, f), b, "POST", path+"?team=foreign", body)
			if !service && !session {
				assertEngineError(t, w, http.StatusForbidden, apierr.Forbidden)
				if len(f.specs) != 0 {
					t.Fatal("bearer reached enrollment issuer")
				}
				continue
			}
			var got struct {
				Capability, Prefix, Team string
				Grants                   []capability.Grant
			}
			if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Capability != "private-provisioned-root" || got.Prefix != "root-prefix" || len(f.specs) != 1 || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("HTTP %d %s specs=%+v", w.Code, w.Body.String(), f.specs)
			}
			spec, p := f.specs[0], b.snapshot.Authorities[0].Principal
			if spec.Authority == nil || *spec.Authority != p.Authority || spec.Subject.ID == "" || spec.RegistrationID == "" || spec.Metered || spec.Data != nil || spec.Binding != (capability.Binding{Kind: capability.BindingNone}) || got.Team != spec.Team || !reflect.DeepEqual(got.Grants, spec.Grants) {
				t.Fatalf("spec=%+v", spec)
			}
			if service {
				if spec.Kind != capability.ProvisionService || spec.Subject != (capability.Subject{Kind: capability.SubjectService, ID: "launcher"}) || spec.Team != "" || len(spec.Repositories) != 0 || len(spec.ReservedPools) != 0 || !reflect.DeepEqual(spec.Grants, []capability.Grant{capability.ClaimsLaunch}) {
					t.Fatalf("service=%+v", spec)
				}
			} else if spec.Kind != capability.ProvisionMachine || spec.Subject != (capability.Subject{Kind: capability.SubjectMachine, ID: "builder"}) || spec.Team != "team" || !reflect.DeepEqual(spec.Repositories, []string{"github.com/acme/*"}) || !reflect.DeepEqual(spec.ReservedPools, []string{"trusted"}) || !reflect.DeepEqual(spec.Grants, []capability.Grant{capability.NodesClaim, capability.TriggersClaim}) {
				t.Fatalf("machine=%+v", spec)
			}
		}
	}
}

func TestRootProvisioningHTTPRefusals(t *testing.T) {
	for _, service := range []bool{false, true} {
		for _, fault := range []string{"absent", "expired", "revoked", "membership", "grant", "non-admin", "machine", "claim", "nodehost", "binding", "transaction", "private", "missing issuer"} {
			b, path, body := rootFixture(service)
			f := &rootIssuer{}
			code, calls := apierr.Forbidden, 0
			p := &b.snapshot.Authorities[0].Principal
			switch fault {
			case "absent":
				b.raw, code = "", apierr.Unauthenticated
			case "expired":
				p.Capability.ExpiresAt, code = &b.snapshot.Now, apierr.Unauthenticated
			case "revoked":
				p.Capability.RevokedAt, code = &b.snapshot.Now, apierr.Unauthenticated
			case "membership":
				if service {
					b = issuanceSession()
				}
				b.snapshot.Authorities[0].Principal.Capability.RevokedAt, code = &b.snapshot.Now, apierr.Unauthenticated
			case "grant":
				p.Capability.Grants = nil
			case "non-admin":
				p.Capability.Grants = []capability.Grant{capability.RunsRead, capability.RunsWrite}
			case "machine":
				p.Capability.Subject.Kind = capability.SubjectMachine
				if !service {
					code = apierr.Unauthenticated
				}
			case "claim":
				b = newCredentialBoundary()
			case "nodehost":
				b = newCredentialBoundary()
				p = &b.snapshot.Authorities[0].Principal
				p.Capability.Audience = capability.NodeHost
				b.raw = "swn_" + strings.TrimPrefix(b.raw, "swk_")
				p.Capability.Digest = sha256.Sum256([]byte(b.raw))
				b.snapshot.Binding.ProcessAlive = true
			case "binding":
				b.bindingErr = &apierr.Error{Code: apierr.Forbidden, Message: "binding refused"}
			case "transaction":
				f.err, calls, code = &apierr.Error{Code: apierr.Unauthenticated, Message: "origin ended before commit"}, 1, apierr.Unauthenticated
			case "private":
				f.err, calls, code = errors.New("private provider credential"), 1, apierr.Internal
			case "missing issuer":
				code = apierr.PolicyMissing
			}
			h := rootServer(b, f)
			if fault == "missing issuer" {
				h = controller.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil))).WithAuthorization(consoleAuthorization(b)).Handler()
			}
			w := authorityHTTP(h, b, "POST", path, body)
			if w.Code != apierr.Status(code) {
				t.Fatalf("service=%v fault=%s HTTP %d %s", service, fault, w.Code, w.Body.String())
			}
			assertEngineError(t, w, apierr.Status(code), code)
			if len(f.specs) != calls || strings.Contains(w.Body.String(), "private") {
				t.Fatalf("service=%v fault=%s calls=%d HTTP %d %s", service, fault, len(f.specs), w.Code, w.Body.String())
			}
		}
	}
}

func TestRootProvisioningHTTPInput(t *testing.T) {
	for _, service := range []bool{false, true} {
		invalid := []string{`null`, `{}`, `[]`, `{} {}`, `{"name":null}`, `{"name":""}`}
		if service {
			invalid = append(invalid, `{"name":"launcher","grants":null}`, `{"name":"launcher","grants":[]}`, `{"name":"launcher","grants":["claims.launch","claims.launch"]}`, `{"name":"launcher","grants":["unknown"]}`)
			for _, grant := range []capability.Grant{capability.OperatorConsole, capability.TeamAdmin, capability.NodesClaim, capability.TriggersClaim, capability.SourceRead, capability.DataRead, capability.NodeReport} {
				invalid = append(invalid, `{"name":"launcher","grants":["`+string(grant)+`"]}`)
			}
		} else {
			invalid = append(invalid, `{"name":"builder","repos":[]}`, `{"name":"builder","repos":null}`, `{"name":"builder","repos":["https://github.com/acme/*"]}`, `{"name":"builder","repos":["*/acme/*"]}`, `{"name":"box;id","repos":["github.com/acme/*"]}`, `{"name":"builder","repos":["github.com/acme/*"],"reserved_pools":["pool","pool"]}`, `{"name":"builder","repos":["github.com/acme/*"],"reserved_pools":[""]}`)
		}
		for _, body := range invalid {
			b, path, _ := rootFixture(service)
			f := &rootIssuer{}
			w := authorityHTTP(rootServer(b, f), b, "POST", path, body)
			assertEngineError(t, w, 400, apierr.BadRequest)
			if len(f.specs) != 0 {
				t.Fatal("invalid request reached issuer")
			}
		}
		for _, expiry := range []string{`null`, `"invalid"`, `"0001-01-01T00:00:00Z"`} {
			b, path, body := rootFixture(service)
			body = strings.TrimSuffix(body, "}") + `,"expires_at":` + expiry + `}`
			f := &rootIssuer{}
			assertEngineError(t, authorityHTTP(rootServer(b, f), b, "POST", path, body), 400, apierr.BadRequest)
			if len(f.specs) != 0 {
				t.Fatal("invalid expiry reached issuer")
			}
		}
	}
}

func TestRootProvisioningHTTPAuthorityProperty(t *testing.T) {
	property := func(value string, selector uint8, service bool) bool {
		b, path, body := rootFixture(service)
		keys := []string{"team", "subject", "authority", "kind", "metered", "repositories", "binding", "parent", "data", "registration_id", "principal", "scopes"}
		if service {
			keys = append(keys, "reserved_pools", "repos")
		} else {
			keys = append(keys, "grants")
		}
		var request map[string]any
		if json.Unmarshal([]byte(body), &request) != nil {
			return false
		}
		request[keys[int(selector)%len(keys)]] = value
		raw, err := json.Marshal(request)
		if err != nil {
			return false
		}
		f := &rootIssuer{}
		w := authorityHTTP(rootServer(b, f), b, "POST", path, string(raw))
		return w.Code == 400 && len(f.specs) == 0 && !strings.Contains(w.Body.String(), "private-provisioned-root")
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 200}); err != nil {
		t.Fatal(err)
	}
}

func TestRootProvisioningHTTPExpiryAndCSRF(t *testing.T) {
	for _, service := range []bool{false, true} {
		b, path, body := rootFixture(service)
		body = strings.TrimSuffix(body, "}") + `,"expires_at":"2100-01-01T00:00:00Z"}`
		f := &rootIssuer{}
		w := authorityHTTP(rootServer(b, f), b, "POST", path, body)
		if w.Code != 201 || len(f.specs) != 1 || f.specs[0].ExpiresAt == nil || f.specs[0].ExpiresAt.Year() != 2100 {
			t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
		}
		if service {
			b = consoleBoundary()
		} else {
			b = issuanceSession()
			b.snapshot.Authorities[0].Principal.Capability.Grants = append(b.snapshot.Authorities[0].Principal.Capability.Grants, capability.RunsWrite)
		}
		a := consoleAuthorization(b)
		a.CheckCSRF = func(*http.Request, capability.Principal) error {
			return &apierr.Error{Code: apierr.Forbidden, Message: "invalid CSRF token"}
		}
		f = &rootIssuer{}
		h := controller.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil))).WithAuthorization(a).WithCredentialIssuer(f).Handler()
		assertEngineError(t, authorityHTTP(h, b, "POST", path, body), 403, apierr.Forbidden)
		if len(f.specs) != 0 {
			t.Fatal("CSRF refusal reached issuer")
		}
	}
}

func TestRootProvisioningHTTPEnrollmentConflicts(t *testing.T) {
	for _, service := range []bool{false, true} {
		for _, err := range []error{store.ErrAgentNameTaken, store.ErrRunnerTokenLimit} {
			b, path, body := rootFixture(service)
			issuer := &rootIssuer{err: err}
			w := authorityHTTP(rootServer(b, issuer), b, "POST", path, body)
			code := apierr.Conflict
			if errors.Is(err, store.ErrRunnerTokenLimit) {
				code = apierr.QuotaExceeded
			}
			assertEngineError(t, w, apierr.Status(code), code)
		}
	}
}

func TestRootProvisioningHTTPClaimGrantCeilings(t *testing.T) {
	for _, service := range []bool{false, true} {
		for _, grant := range capability.Grants() {
			b, path, body := rootFixture(service)
			var request map[string]any
			if err := json.Unmarshal([]byte(body), &request); err != nil {
				t.Fatal(err)
			}
			request["grants"] = []capability.Grant{grant}
			raw, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			f := &rootIssuer{}
			w := authorityHTTP(rootServer(b, f), b, "POST", path, string(raw))
			allowed := grant == capability.NodesClaim || grant == capability.TriggersClaim || grant == capability.ClaimsLaunch
			if service {
				allowed = grant == capability.ClaimsLaunch || grant == capability.OperatorLimits || grant == capability.OperatorObjectStore || grant == capability.OperatorTokens
			}
			if allowed {
				if w.Code != 201 || len(f.specs) != 1 || !reflect.DeepEqual(f.specs[0].Grants, []capability.Grant{grant}) {
					t.Fatalf("service=%v grant=%s HTTP %d %s", service, grant, w.Code, w.Body.String())
				}
			} else {
				assertEngineError(t, w, 400, apierr.BadRequest)
				if len(f.specs) != 0 {
					t.Fatal("grant above enrollment ceiling reached issuer")
				}
			}
		}
	}
}

func TestRootProvisioningHTTPCommitsThroughSoleIssuer(t *testing.T) {
	f := newSessionFlow(t)
	if _, err := teststore.SQL(t, f.store).ExecContext(t.Context(), `UPDATE memberships SET role='owner'`); err != nil {
		t.Fatal(err)
	}
	h := f.server.Handler()
	body := `{"name":"builder","repos":["github.com/acme/*"],"reserved_pools":["trusted"]}`
	w := httptest.NewRecorder()
	h.ServeHTTP(w, f.request("POST", "/api/v1/runners", body))
	var got struct{ Capability, Prefix string }
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Capability == "" {
		t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
	}
	p, err := f.store.AsOperator().LookupCapability(t.Context(), sha256.Sum256([]byte(got.Capability)))
	if err != nil || p.Capability.Prefix != got.Prefix || p.Capability.Parent != nil || p.Capability.Subject != (capability.Subject{Kind: capability.SubjectMachine, ID: "builder"}) || p.Capability.Team != "team" || !reflect.DeepEqual(p.Capability.Repositories, []string{"github.com/acme/*"}) || !reflect.DeepEqual(p.Capability.ReservedPools, []string{"trusted"}) || !reflect.DeepEqual(p.Capability.Grants, []capability.Grant{capability.NodesClaim, capability.TriggersClaim}) {
		t.Fatalf("root=%+v err=%v", p, err)
	}
	for _, status := range []int{403, 201} {
		role := "reader"
		if status == 201 {
			role = "owner"
		}
		if _, err := teststore.SQL(t, f.store).ExecContext(t.Context(), `UPDATE memberships SET role=?`, role); err != nil {
			t.Fatal(err)
		}
		w = httptest.NewRecorder()
		h.ServeHTTP(w, f.request("POST", "/api/v1/runners", `{"name":"second","repos":["github.com/acme/*"]}`))
		if status == 403 {
			assertEngineError(t, w, 403, apierr.Forbidden)
			var count int
			if err := teststore.SQL(t, f.store).QueryRowContext(t.Context(), `SELECT COUNT(*) FROM capabilities`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("count=%d err=%v", count, err)
			}
			continue
		}
		if w.Code != status {
			t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
		}
	}
	if strings.Contains(f.logs.String(), got.Capability) {
		t.Fatal("bearer entered audit")
	}
}

func TestRootProvisioningHTTPMethodsAndRetry(t *testing.T) {
	for _, service := range []bool{false, true} {
		b, path, _ := rootFixture(service)
		policy, ok := capability.LookupRoute("POST", path)
		if !ok || policy.Retry != capability.RetryNever {
			t.Fatalf("policy=%+v", policy)
		}
		f := &rootIssuer{}
		for _, method := range []string{"HEAD", "PUT", "PATCH"} {
			assertEngineError(t, authorityHTTP(rootServer(b, f), b, method, path, `{}`), 405, apierr.MethodNotAllowed)
		}
		if len(f.specs) != 0 {
			t.Fatal("unsupported method reached issuer")
		}
	}
}

func TestRootProvisioningHTTPServiceCommitsThroughSoleIssuer(t *testing.T) {
	f := newDeploymentSessionFlow(t, deploymenttest.Cloud(t, "https://controller.test"))
	secret := sha256.Sum256([]byte(t.Name()))
	seed := capability.ControllerPrefix + base64.RawURLEncoding.EncodeToString(secret[:])
	if created, err := controller.EnsureBootstrapAdminToken(t.Context(), f.store.AsOperator(), seed); err != nil || !created {
		t.Fatalf("bootstrap: %v %v", created, err)
	}
	h := f.server.Handler()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/operator/tokens", strings.NewReader(`{"name":"launcher","grants":["claims.launch"]}`))
	r.Header.Set("Authorization", "Bearer "+seed)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var got struct{ Capability, Prefix, Team string }
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Capability == "" || got.Team != "" {
		t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
	}
	p, err := f.store.AsOperator().LookupCapability(t.Context(), sha256.Sum256([]byte(got.Capability)))
	if err != nil || p.Capability.Prefix != got.Prefix || p.Capability.Parent != nil || p.Capability.Team != "" || p.Capability.Subject != (capability.Subject{Kind: capability.SubjectService, ID: "launcher"}) || !reflect.DeepEqual(p.Capability.Grants, []capability.Grant{capability.ClaimsLaunch}) {
		t.Fatalf("root=%+v err=%v", p, err)
	}
	if strings.Contains(f.logs.String(), got.Capability) || strings.Contains(f.logs.String(), seed) {
		t.Fatal("service bearer entered audit")
	}
}
