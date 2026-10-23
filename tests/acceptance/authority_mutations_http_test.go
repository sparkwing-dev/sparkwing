package acceptance_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

func authorityServer(t *testing.T, b *credentialBoundary, f *engineTransactions) http.Handler {
	t.Helper()
	e := fakeEngine(t, f)
	e.ResolveOperator = func(context.Context) (controller.AuthorityRevoker, error) { return f, nil }
	return controller.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil))).WithEngineController(e).WithAuthorization(consoleAuthorization(b)).Handler()
}

func authorityHTTP(handler http.Handler, b *credentialBoundary, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if b.raw != "" {
		if b.snapshot.Authorities[0].Principal.Class == capability.CredentialBearer {
			r.Header.Set("Authorization", "Bearer "+b.raw)
		} else {
			r.AddCookie(&http.Cookie{Name: "console-session", Value: b.raw})
			r.Header.Set("X-CSRF-Token", "session-csrf")
		}
	}
	r.Header.Set("Content-Type", "application/json")
	// safety: routing and actors must use committed identity despite caller hints.
	r.Header.Set("X-Sparkwing-Team", "foreign")
	r.Header.Set("X-Sparkwing-Holder", "forged")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func authorityMember() *credentialBoundary {
	b := intakeBoundary()
	b.snapshot.Authorities[0].Principal.Capability.Grants = []capability.Grant{capability.RunsRead, capability.TeamAdmin}
	return b
}

func TestAuthorityMemberDirectoryHTTP(t *testing.T) {
	for _, name := range []string{"reader", "empty", "anonymous", "no grant", "machine root", "bound claim", "membership lost", "private error"} {
		t.Run(name, func(t *testing.T) {
			b := authorityMember()
			b.snapshot.Authorities[0].Principal.Capability.Grants = []capability.Grant{capability.RunsRead}
			f := &engineTransactions{members: []store.Membership{{AccountID: "member", Email: "member@example.invalid", Name: "Member", Role: store.RoleOwner}}}
			code, reads := apierr.Code(""), 1
			switch name {
			case "empty":
				f.members = nil
			case "anonymous":
				b.raw, code, reads = "", apierr.Unauthenticated, 0
			case "no grant":
				b.snapshot.Authorities[0].Principal.Capability.Grants, code, reads = nil, apierr.Forbidden, 0
			case "machine root":
				b.snapshot.Authorities[0].Principal.Capability.Subject.Kind, code, reads = capability.SubjectMachine, apierr.Forbidden, 0
			case "bound claim":
				b = newCredentialBoundary()
				b.snapshot.Authorities[0].Principal.Capability.Grants = []capability.Grant{capability.RunsRead}
				code, reads = apierr.Forbidden, 0
			case "membership lost":
				b.snapshot.Authorities[0].Principal.Capability.RevokedAt, code, reads = &b.snapshot.Now, apierr.Unauthenticated, 0
			case "private error":
				f.readErr, code = errors.New("private credential"), apierr.Internal
			}
			w := authorityHTTP(authorityServer(t, b, f), b, "GET", "/api/v1/team/members?team=foreign", "")
			if f.memberReads != reads || f.writes != 0 {
				t.Fatalf("reads %d, writes %d; HTTP %d %s", f.memberReads, f.writes, w.Code, w.Body.String())
			}
			if code != "" {
				assertEngineError(t, w, apierr.Status(code), code)
				return
			}
			var members []struct {
				UserID string `json:"user_id"`
				Email  string `json:"email"`
				Name   string `json:"name"`
				Role   string `json:"role"`
			}
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &members) != nil || members == nil || len(members) != len(f.members) {
				t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
			}
			if len(members) != 0 && (members[0].UserID != "member" || members[0].Email != "member@example.invalid" || members[0].Name != "Member" || members[0].Role != "owner") {
				t.Fatalf("members = %+v", members)
			}
		})
	}
}

func TestAuthorityMutationHTTP(t *testing.T) {
	for _, route := range []struct {
		method, path, body string
	}{
		{"DELETE", "/api/v1/tokens/target", ""},
		{"DELETE", "/api/v1/operator/tokens/target", ""},
		{"PATCH", "/api/v1/team/members/member", `{"role":"reader"}`},
		{"DELETE", "/api/v1/team/members/member", ""},
	} {
		for _, name := range []string{"success", "anonymous", "revoked", "missing grant", "wrong subject", "bound claim", "wrong audience", "binding refusal", "transaction race", "missing target", "private error"} {
			t.Run(route.method+route.path+"/"+name, func(t *testing.T) {
				b, f := authorityMember(), &engineTransactions{}
				operator := strings.Contains(route.path, "/operator/")
				if operator {
					b = deploymentBoundary(capability.OperatorTokens)
				}
				status, code, writes := 204, apierr.Code(""), 1
				switch name {
				case "anonymous":
					b.raw, status, code, writes = "", 401, apierr.Unauthenticated, 0
				case "revoked":
					b.snapshot.Authorities[0].Principal.Capability.RevokedAt = &b.snapshot.Now
					status, code, writes = 401, apierr.Unauthenticated, 0
				case "missing grant":
					b.snapshot.Authorities[0].Principal.Capability.Grants = nil
					status, code, writes = 403, apierr.Forbidden, 0
				case "wrong subject":
					b.snapshot.Authorities[0].Principal.Capability.Subject.Kind = capability.SubjectMachine
					status, code, writes = 403, apierr.Forbidden, 0
				case "bound claim":
					b = newCredentialBoundary()
					b.snapshot.Authorities[0].Principal.Capability.Grants = []capability.Grant{capability.RunsRead, capability.TeamAdmin, capability.OperatorTokens}
					status, code, writes = 403, apierr.Forbidden, 0
				case "wrong audience":
					b = newCredentialBoundary()
					b.snapshot.Authorities[0].Principal.Capability.Audience = capability.NodeHost
					b.raw = "swn_" + strings.TrimPrefix(b.raw, "swk_")
					b.snapshot.Authorities[0].Principal.Capability.Digest = sha256.Sum256([]byte(b.raw))
					b.snapshot.Binding.ProcessAlive = true
					status, code, writes = 403, apierr.Forbidden, 0
				case "binding refusal":
					b.bindingErr = &apierr.Error{Code: apierr.Forbidden, Message: "resource does not belong to caller"}
					status, code, writes = 403, apierr.Forbidden, 0
				case "transaction race":
					f.err = &apierr.Error{Code: apierr.Unauthenticated, Message: "actor revoked before commit"}
					status, code = 401, apierr.Unauthenticated
				case "missing target":
					f.err = store.ErrNotFound
					status, code = 404, apierr.NotFound
				case "private error":
					f.err = errors.New("private credential")
					status, code = 500, apierr.Internal
				}
				w := authorityHTTP(authorityServer(t, b, f), b, route.method, route.path, route.body)
				if f.writes != writes {
					t.Fatalf("writes %d, want %d; HTTP %d %s", f.writes, writes, w.Code, w.Body.String())
				}
				if code != "" {
					assertEngineError(t, w, status, code)
					return
				}
				if w.Code != 204 || w.Body.Len() != 0 || w.Header().Get("Cache-Control") != "no-store" {
					t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
				}
				if !operator && b.bindingTarget.Team != "team" || operator && b.bindingTarget.Team != "" {
					t.Fatalf("binding target = %+v", b.bindingTarget)
				}
				if f.authorityActor.Authority != b.snapshot.Authorities[0].Principal.Authority || f.authorityActor.Kind != runstate.ActorOperator || f.authorityActor.Holder != "" || f.authorityActor.Generation != 0 {
					t.Fatalf("actor = %+v", f.authorityActor)
				}
				if strings.Contains(route.path, "/tokens/") {
					if b.bindingTarget.TokenPrefix != "target" || !operator && b.bindingRule != capability.BindOwnToken || operator && b.bindingRule != capability.BindNone {
						t.Fatalf("token binding = %s %+v", b.bindingRule, b.bindingTarget)
					}
					if f.revokedAuthority != (capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "target"}) {
						t.Fatalf("target = %+v", f.revokedAuthority)
					}
				} else {
					if f.membership.AccountID != "member" || (f.membership.Role == nil) != (route.method == "DELETE") || f.membership.Role != nil && *f.membership.Role != store.RoleReader {
						t.Fatalf("membership = %+v", f.membership)
					}
					if b.bindingTarget.AccountID != "member" || route.method == "DELETE" && b.bindingRule != capability.BindSelfOrTeamAdmin {
						t.Fatalf("membership binding = %s %+v", b.bindingRule, b.bindingTarget)
					}
				}
			})
		}
	}
}

func TestAuthorityMutationHTTPMissingStoreFailsClosed(t *testing.T) {
	for _, operator := range []bool{false, true} {
		b, path := authorityMember(), "/api/v1/tokens/target"
		if operator {
			b, path = deploymentBoundary(capability.OperatorTokens), "/api/v1/operator/tokens/target"
		}
		s := controller.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil))).WithAuthorization(consoleAuthorization(b))
		w := authorityHTTP(s.Handler(), b, "DELETE", path, "")
		assertEngineError(t, w, 500, apierr.PolicyMissing)
	}
}

func TestAuthorityMutationHTTPUnsupportedMethods(t *testing.T) {
	for _, route := range [][2]string{
		{"/api/v1/operator/tokens/target", "DELETE, GET"},
		{"/api/v1/team/members/member", "DELETE, PATCH"},
	} {
		for _, method := range []string{"POST", "GET", "PUT", "HEAD", "OPTIONS"} {
			if method == "GET" && strings.HasSuffix(route[0], "/tokens/target") {
				continue
			}
			w := httptest.NewRecorder()
			controller.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler().ServeHTTP(w, httptest.NewRequest(method, route[0], nil))
			assertEngineError(t, w, 405, apierr.MethodNotAllowed)
			allowed := route[1]
			if method == "HEAD" && strings.HasSuffix(route[0], "/tokens/target") {
				allowed = "GET"
			}
			if w.Header().Get("Allow") != allowed {
				t.Fatalf("%s %s: Allow = %q", method, route[0], w.Header().Get("Allow"))
			}
		}
	}
}

func TestMembershipHTTPRejectsMalformedUpdates(t *testing.T) {
	for _, body := range []string{`{}`, `null`, `{"role":null}`, `{"role":""}`, `{"role":"admin"}`, `{"role":"reader","actor":"forged"}`, `{"role":"reader"}{}`} {
		t.Run(body, func(t *testing.T) {
			b, f := authorityMember(), &engineTransactions{}
			w := authorityHTTP(authorityServer(t, b, f), b, "PATCH", "/api/v1/team/members/member", body)
			assertEngineError(t, w, 400, apierr.BadRequest)
			if f.writes != 0 {
				t.Fatal("invalid membership reached transaction")
			}
		})
	}
}

func TestAuthorityMutationHTTPPreservesSessionActor(t *testing.T) {
	for _, console := range []bool{false, true} {
		b := authorityMember()
		path := "/api/v1/tokens/target"
		p := &b.snapshot.Authorities[0].Principal
		p.Class, p.Authority = capability.CredentialSession, sessionReference(*p)
		if console {
			b, path = consoleBoundary(), "/api/v1/operator/tokens/target"
		}
		f := &engineTransactions{}
		w := authorityHTTP(authorityServer(t, b, f), b, "DELETE", path, "")
		if w.Code != 204 || f.authorityActor.Authority != b.snapshot.Authorities[0].Principal.Authority || f.authorityActor.CapabilityPrefix != "" {
			t.Fatalf("console=%t: HTTP %d %s, actor %+v", console, w.Code, w.Body.String(), f.authorityActor)
		}
	}
}

func TestAuthorityMutationHTTPRequiresCSRF(t *testing.T) {
	for _, route := range [][3]string{
		{"DELETE", "/api/v1/tokens/target", ""},
		{"PATCH", "/api/v1/team/members/member", `{"role":"reader"}`},
		{"DELETE", "/api/v1/team/members/member", ""},
		{"DELETE", "/api/v1/operator/tokens/target", ""},
	} {
		t.Run(route[0]+route[1], func(t *testing.T) {
			b := authorityMember()
			p := &b.snapshot.Authorities[0].Principal
			p.Class, p.Authority = capability.CredentialSession, sessionReference(*p)
			if strings.Contains(route[1], "/operator/") {
				b = consoleBoundary()
			}
			f := &engineTransactions{}
			r := httptest.NewRequest(route[0], route[1], strings.NewReader(route[2]))
			r.AddCookie(&http.Cookie{Name: "console-session", Value: b.raw})
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			authorityServer(t, b, f).ServeHTTP(w, r)
			assertEngineError(t, w, 403, apierr.Forbidden)
			if f.writes != 0 {
				t.Fatal("missing CSRF proof reached transaction")
			}
		})
	}
}

func TestAuthorityMutationHTTPStoreGuards(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code apierr.Code
	}{
		{store.ErrNotMember, apierr.NotFound},
		{store.ErrLastOwner, apierr.Conflict},
		{store.ErrRoleAboveOwn, apierr.Forbidden},
	} {
		for _, method := range []string{"PATCH", "DELETE"} {
			t.Run(method+"/"+string(tc.code), func(t *testing.T) {
				b, f := authorityMember(), &engineTransactions{err: tc.err}
				w := authorityHTTP(authorityServer(t, b, f), b, method, "/api/v1/team/members/member", `{"role":"reader"}`)
				assertEngineError(t, w, apierr.Status(tc.code), tc.code)
				if f.writes != 1 {
					t.Fatalf("transactions = %d", f.writes)
				}
			})
		}
	}
}

func TestAuthorityMutationHTTPKeepsAuthenticatedActorDuringBodyRace(t *testing.T) {
	b, f := authorityMember(), &engineTransactions{}
	actor := b.snapshot.Authorities[0].Principal.Authority
	body := &reportBody{Reader: strings.NewReader(`{"role":"reader"}`), beforeRead: func() {
		b.snapshot.Authorities[0].Principal.Authority.ID = "replacement"
		f.err = &apierr.Error{Code: apierr.Unauthenticated, Message: "actor revoked before commit"}
	}}
	r := httptest.NewRequest("PATCH", "/api/v1/team/members/member", body)
	r.Header.Set("Authorization", "Bearer "+b.raw)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	authorityServer(t, b, f).ServeHTTP(w, r)
	assertEngineError(t, w, 401, apierr.Unauthenticated)
	if f.writes != 1 || f.authorityActor.Authority != actor {
		t.Fatalf("transaction replaced authenticated actor: %+v", f.authorityActor)
	}
}

func TestAuthorityMutationHTTPRemovesLegacyTeamRevoke(t *testing.T) {
	s := controller.New(nil, nil)
	if s.RegisteredRoute(httptest.NewRequest("DELETE", "/api/v1/team/tokens/target", nil)) {
		t.Fatal("legacy team token revocation remains registered")
	}
}

func TestOperatorTokenRevocationClient(t *testing.T) {
	b, f := deploymentBoundary(capability.OperatorTokens), &engineTransactions{}
	c, err := client.NewTokenManager(client.EngineOptions{URL: "http://controller.invalid", HTTP: &http.Client{Transport: reportTransport{authorityServer(t, b, f)}}, Bearer: func(context.Context) (string, error) { return b.raw, nil }}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.RevokeToken(t.Context(), "root"); err != nil {
		t.Fatal(err)
	}
	if f.writes != 1 || f.revokedAuthority.ID != "root" {
		t.Fatalf("revocation = %+v, writes %d", f.revokedAuthority, f.writes)
	}
}

type authorityDirectory map[[32]byte]*credentialBoundary

func (d authorityDirectory) ReadAuthentication(ctx context.Context, lookup capability.CredentialLookup, evidence capability.RequestEvidence) (capability.AuthenticationSnapshot, error) {
	if b := d[lookup.Digest]; b != nil {
		return b.ReadAuthentication(ctx, lookup, evidence)
	}
	return capability.AuthenticationSnapshot{}, &apierr.Error{Code: apierr.Unauthenticated, Message: "unknown credential"}
}

func TestAuthorityRevocationHTTPStopsDescendantsAcrossRouters(t *testing.T) {
	for _, mutation := range []string{"machine root", "root in rotation grace", "launcher root", "token session", "membership", "here parent loses runs.write"} {
		t.Run(mutation, func(t *testing.T) {
			admin, victim := authorityMember(), newCredentialBoundary()
			admin.raw = "swk_" + strings.Repeat("A", 43)
			admin.snapshot.Authorities[0].Principal.Capability.Digest = sha256.Sum256([]byte(admin.raw))
			method, path, body := "DELETE", "/api/v1/tokens/root", ""
			victimMethod, victimPath, victimBody := "POST", "/api/v1/runs/run/jobs/job/annotations", `{"message":"masked"}`
			ancestor := &victim.snapshot.Authorities[1]
			if mutation == "root in rotation grace" {
				ancestor.Principal.Capability.RevokedAt = victim.snapshot.Authorities[0].Principal.Capability.ExpiresAt
			}
			if mutation == "token session" {
				leaf := &victim.snapshot.Authorities[0].Principal
				leaf.Class, leaf.Authority = capability.CredentialSession, sessionReference(*leaf)
				leaf.Capability.Subject = capability.Subject{Kind: capability.SubjectAccount, ID: "member"}
				leaf.Capability.Grants = []capability.Grant{capability.RunsRead}
				leaf.Capability.Binding = capability.Binding{Kind: capability.BindingNone}
				leaf.Capability.Unit, leaf.Capability.UnitID, leaf.Capability.Purpose = "", "", ""
				ancestor.Principal.Capability.Subject = leaf.Capability.Subject
				victimMethod, victimPath, victimBody = "DELETE", "/api/v1/tokens/another", ""
			}
			if mutation == "launcher root" {
				ancestor.Principal.Capability.Subject.Kind, ancestor.Principal.Capability.Team = capability.SubjectService, ""
				ancestor.Principal.Capability.Grants = []capability.Grant{capability.ClaimsLaunch}
				admin = deploymentBoundary(capability.OperatorTokens)
				admin.raw = "swk_" + strings.Repeat("A", 43)
				admin.snapshot.Authorities[0].Principal.Capability.Digest = sha256.Sum256([]byte(admin.raw))
				path = "/api/v1/operator/tokens/root"
			}
			if mutation == "membership" || mutation == "here parent loses runs.write" {
				ancestor.Principal.Class, ancestor.Principal.Authority.Kind = capability.CredentialSession, capability.AuthoritySession
				ancestor.Principal.Capability.Subject = capability.Subject{Kind: capability.SubjectAccount, ID: "member"}
				ancestor.Principal.Capability.Grants = []capability.Grant{capability.RunsRead, capability.RunsWrite}
				victim.snapshot.Authorities[0].Principal.Capability.Parent = &ancestor.Principal.Authority
				method, path, body = "PATCH", "/api/v1/team/members/member", `{"role":"reader"}`
				if mutation == "membership" {
					method, body = "DELETE", ""
				}
				if mutation == "here parent loses runs.write" {
					here := ancestor.Principal
					here.Class, here.Authority = capability.CredentialBearer, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "here"}
					here.Capability.Prefix, here.Capability.Issuance = "here", capability.Award
					here.Capability.Unit, here.Capability.UnitID, here.Capability.Purpose = capability.UnitTrigger, "trigger", capability.PurposeHere
					here.Capability.Parent = &ancestor.Principal.Authority
					here.Capability.Binding = capability.Binding{Kind: capability.BindingTrigger, RunID: "run", Reservation: "here:run"}
					victim.snapshot.Authorities = []capability.AuthorityState{victim.snapshot.Authorities[0], {Principal: here}, *ancestor}
					victim.snapshot.Authorities[0].Principal.Capability.Parent = &here.Authority
				}
			}
			f := &engineTransactions{commitAuthority: func() {
				if mutation == "token session" {
					return
				}
				switch mutation {
				case "membership":
					victim.snapshot.Authorities[0].Principal.Capability.RevokedAt = &victim.snapshot.Now
				case "here parent loses runs.write":
					victim.snapshot.Authorities[2].Principal.Capability.Grants = []capability.Grant{capability.RunsRead}
				default:
					victim.snapshot.Authorities[1].Principal.Capability.RevokedAt = &victim.snapshot.Now
				}
			}}
			directory := authorityDirectory{
				admin.snapshot.Authorities[0].Principal.Capability.Digest:  admin,
				victim.snapshot.Authorities[0].Principal.Capability.Digest: victim,
			}
			var routers []http.Handler
			for range 2 {
				e := fakeEngine(t, f)
				e.ResolveOperator = func(context.Context) (controller.AuthorityRevoker, error) { return f, nil }
				authorization := consoleAuthorization(victim)
				authorization.Authenticator = capability.NewAuthenticator(directory)
				routers = append(routers, controller.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil))).WithEngineController(e).
					WithAuthorization(authorization).Handler())
			}
			// safety: warm both independent verifiers before the committed revocation.
			for _, router := range routers {
				w := authorityHTTP(router, victim, victimMethod, victimPath, victimBody)
				if w.Code != 204 {
					t.Fatalf("live descendant = %d %s", w.Code, w.Body.String())
				}
			}
			if mutation == "token session" {
				f.commitAuthority = func() { victim.snapshot.Authorities[1].Principal.Capability.RevokedAt = &victim.snapshot.Now }
			}
			w := authorityHTTP(routers[0], admin, method, path, body)
			if w.Code != 204 {
				t.Fatalf("mutation = %d %s", w.Code, w.Body.String())
			}
			writes := f.writes
			for _, router := range routers {
				w := authorityHTTP(router, victim, victimMethod, victimPath, victimBody)
				assertEngineError(t, w, 401, apierr.Unauthenticated)
			}
			if f.writes != writes {
				t.Fatal("dead descendant reached a transaction")
			}
		})
	}
}

func TestAuthorityMutationHTTPDeadTokensUseCanonicalRefusal(t *testing.T) {
	for _, name := range []string{"revoked", "expired", "unknown", "malformed"} {
		t.Run(name, func(t *testing.T) {
			b, f := authorityMember(), &engineTransactions{}
			switch name {
			case "revoked":
				b.snapshot.Authorities[0].Principal.Capability.RevokedAt = &b.snapshot.Now
			case "expired":
				b.snapshot.Authorities[0].Principal.Capability.ExpiresAt = &b.snapshot.Now
			case "unknown":
				b.raw = "swk_" + strings.Repeat("A", 42) + "Q"
			case "malformed":
				b.raw = "garbage"
			}
			w := authorityHTTP(authorityServer(t, b, f), b, "DELETE", "/api/v1/tokens/target", "")
			assertEngineError(t, w, 401, apierr.Unauthenticated)
			if f.writes != 0 || w.Header().Get("Retry-After") != "" || strings.Contains(w.Body.String(), "token_state") {
				t.Fatalf("dead credential response: %d %s, writes %d", w.Code, w.Body.String(), f.writes)
			}
		})
	}
}

func TestAuthorityMutationHTTPAuditUsesRegisteredPattern(t *testing.T) {
	b, f := authorityMember(), &engineTransactions{}
	var logs strings.Builder
	s := controller.New(nil, slog.New(slog.NewJSONHandler(&logs, nil))).WithEngineController(fakeEngine(t, f)).WithAuthorization(consoleAuthorization(b))
	w := authorityHTTP(s.Handler(), b, "PATCH", "/api/v1/team/members/0123456789abcdef", `{"role":"reader"}`)
	if w.Code != 204 {
		t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
	}
	var row map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(logs.String())), &row); err != nil {
		t.Fatal(err)
	}
	if row["route"] != "/api/v1/team/members/{account}" || row["member_id"] != "0123456789abcdef" || row["role"] != "reader" || strings.Contains(logs.String(), b.raw) {
		t.Fatalf("audit = %s", logs.String())
	}
}
