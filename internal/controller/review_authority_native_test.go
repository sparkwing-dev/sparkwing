package controller

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/deployment"
	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func reviewSeed(t *testing.T, st *store.Store) string {
	t.Helper()
	key := sha256.Sum256([]byte(t.Name()))
	raw := capability.ControllerPrefix + base64.RawURLEncoding.EncodeToString(key[:])
	if created, err := EnsureBootstrapAdminToken(t.Context(), st, raw); err != nil || !created {
		t.Fatalf("seed: %v %v", created, err)
	}
	return raw
}

func TestReviewSetupEndsBootstrapAuthority(t *testing.T) {
	e, _ := engineRuntimeStore(t)
	raw := reviewSeed(t, e.Store)
	parent, err := e.Store.LookupCapability(t.Context(), sha256.Sum256([]byte(raw)))
	if err != nil {
		t.Fatal(err)
	}
	child, err := e.Store.DelegateCapability(t.Context(), capability.DelegateSpec{Parent: parent.Authority, Audience: capability.Controller, Grants: []capability.Grant{capability.OperatorTokens}, Binding: capability.Binding{Kind: capability.BindingNone}})
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, e.Store, `INSERT INTO sessions(hash,principal,scopes,created_at,expires_at,capability_prefix,team) VALUES ('bootstrap-session','bootstrap:admin','',1,?,?,'a')`, time.Now().Add(time.Hour).UnixNano(), child.Capability.Prefix)
	h := New(e.Store, nil).Handler()
	w := engineClaimHTTP(t, h, raw, "POST", "/api/v1/operator/tokens", `{"name":"administrator","grants":["operator.tokens","operator.limits"]}`, 201)
	var issued credentialResponse
	if err := json.Unmarshal(w.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	engineClaimHTTP(t, h, issued.Capability, "GET", "/api/v1/operator/tokens", "", 200)
	if ready, err := e.Store.HasDeploymentAdministrator(t.Context()); err != nil || !ready {
		t.Fatalf("service administrator readiness: %v %v", ready, err)
	}
	var ended int
	if err := teststore.SQL(t, e.Store).QueryRow(`SELECT COUNT(*) FROM capabilities WHERE prefix IN (?,?) AND revoked_at IS NOT NULL AND revocation_generation>0`, parent.Authority.ID, child.Capability.Prefix).Scan(&ended); err != nil || ended != 2 {
		t.Fatalf("descendant revocation: %d %v", ended, err)
	}
	if err := teststore.SQL(t, e.Store).QueryRow(`SELECT COUNT(*) FROM sessions WHERE hash='bootstrap-session' AND revoked_at IS NOT NULL AND auth_generation>0`).Scan(&ended); err != nil || ended != 1 {
		t.Fatalf("session revocation: %d %v", ended, err)
	}
	for _, secret := range []string{raw, child.Raw} {
		engineClaimHTTP(t, h, secret, "GET", "/api/v1/operator/tokens", "", 401)
	}
	if created, err := EnsureBootstrapAdminToken(t.Context(), e.Store, raw); err == nil || created {
		t.Fatalf("used seed accepted: %v %v", created, err)
	}
	var generation int64
	if err := teststore.SQL(t, e.Store).QueryRow(`SELECT revocation_generation FROM capabilities WHERE prefix=?`, parent.Authority.ID).Scan(&generation); err != nil || generation == 0 {
		t.Fatalf("generation=%d %v", generation, err)
	}
}

func reviewAccount(t *testing.T, role string) (*Server, string, string) {
	t.Helper()
	e, _ := engineRuntimeStore(t)
	engineSQL(t, e.Store, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('member','member@example.test',1,1)`)
	engineSQL(t, e.Store, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('a','member',?,1)`, role)
	cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	s := NewDeployment(e.Store, deploymenttest.Team(t), nil).WithSecretsCipher(cipher)
	raw, csrf, _, err := e.Store.CreateAccountSession(t.Context(), store.Account{ID: "member"}, "a", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return s, raw, csrf
}

func reviewSessionRequest(t *testing.T, s *Server, raw, csrf, method, path, body string, status int) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "https://controller.test"+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://controller.test")
	r.Header.Set(csrfHeaderName, csrf)
	r.AddCookie(&http.Cookie{Name: cookieName(sessionCookieName, s.cookiesSecure()), Value: raw})
	r.AddCookie(&http.Cookie{Name: cookieName(csrfCookieName, s.cookiesSecure()), Value: csrf})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != status {
		t.Fatalf("%s %s = %d %s want %d", method, path, w.Code, w.Body, status)
	}
	return w
}

func TestReviewConsoleCredentialCommits(t *testing.T) {
	s, raw, csrf := reviewAccount(t, "reader")
	s.WithOperatorAccounts([]string{"member"})
	w := reviewSessionRequest(t, s, raw, csrf, "POST", "/api/v1/operator/tokens", `{"name":"service","grants":["operator.tokens"]}`, 201)
	var issued credentialResponse
	if err := json.Unmarshal(w.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	reviewSessionRequest(t, s, raw, csrf, "GET", "/api/v1/operator/tokens/"+issued.Prefix, "", 200)
	reviewSessionRequest(t, s, raw, csrf, "DELETE", "/api/v1/operator/tokens/"+issued.Prefix, "", 204)
	s.WithOperatorAccounts(nil)
	reviewSessionRequest(t, s, raw, csrf, "POST", "/api/v1/operator/tokens", `{"name":"other","grants":["operator.tokens"]}`, 403)
}

func TestConsoleInventoryRevalidatesSessionProperty(t *testing.T) {
	s, raw, csrf := reviewAccount(t, "reader")
	s.WithOperatorAccounts([]string{"member"})
	w := reviewSessionRequest(t, s, raw, csrf, "POST", "/api/v1/operator/tokens", `{"name":"inventory","grants":["operator.tokens"]}`, 201)
	var issued credentialResponse
	if err := json.Unmarshal(w.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	route, ok := capability.LookupRoute("GET", "/api/v1/operator/tokens/{token}")
	if !ok {
		t.Fatal("missing inventory policy")
	}
	auth, err := s.authorization.Authenticator.Authenticate(t.Context(), capability.Presentation{Class: capability.CredentialSession, Raw: raw}, route)
	if err != nil {
		t.Fatal(err)
	}
	authorized, err := capability.Authorize(t.Context(), s.authorization.Bindings, route, auth, capability.Target{TokenPrefix: issued.Prefix})
	if err != nil {
		t.Fatal(err)
	}
	property := func(configured, derived, foreign bool) bool {
		s.WithOperatorAccounts(nil)
		if configured {
			s.WithOperatorAccounts([]string{"member"})
		}
		engineSQL(t, s.store, `UPDATE sessions SET capability_prefix='' WHERE hash=?`, auth.Principal.Authority.ID)
		if derived {
			engineSQL(t, s.store, `UPDATE sessions SET capability_prefix=? WHERE hash=?`, issued.Prefix, auth.Principal.Authority.ID)
		}
		a := authorized
		if foreign {
			a.Principal.Authority.ID = "foreign"
		}
		metadata, err := s.store.AsOperator().ReadCapability(t.Context(), a)
		return (err == nil && metadata.Prefix == issued.Prefix) == (configured && !derived && !foreign)
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 48}); err != nil {
		t.Fatal(err)
	}
}

func TestReviewMembersRevokeOwnTokens(t *testing.T) {
	for _, role := range []string{"reader", "editor"} {
		t.Run(role, func(t *testing.T) {
			s, raw, csrf := reviewAccount(t, role)
			w := reviewSessionRequest(t, s, raw, csrf, "POST", "/api/v1/tokens", `{"grants":["runs.read"]}`, 201)
			var issued credentialResponse
			if err := json.Unmarshal(w.Body.Bytes(), &issued); err != nil {
				t.Fatal(err)
			}
			reviewSessionRequest(t, s, raw, csrf, "GET", "/api/v1/tokens/"+issued.Prefix, "", 200)
			reviewSessionRequest(t, s, raw, csrf, "DELETE", "/api/v1/tokens/"+issued.Prefix, "", 204)
		})
	}
}

func TestReviewLicensedSelfHostRefusesDeploymentBearer(t *testing.T) {
	e, _ := engineRuntimeStore(t)
	raw := reviewSeed(t, e.Store)
	s := NewDeployment(e.Store, deploymenttest.Team(t), nil)
	engineClaimHTTP(t, s.Handler(), raw, "GET", "/api/v1/operator/tokens", "", 401)
}

func TestReviewConsoleCommitRevalidatesProofProperty(t *testing.T) {
	s, raw, _ := reviewAccount(t, "reader")
	s.WithOperatorAccounts([]string{"member"})
	var route capability.RoutePolicy
	for _, candidate := range capability.Routes() {
		if candidate.Method == "POST" && candidate.Path == "/api/v1/operator/tokens" {
			route = candidate
		}
	}
	auth, err := s.authorization.Authenticator.Authenticate(t.Context(), capability.Presentation{Class: capability.CredentialSession, Raw: raw}, route)
	if err != nil {
		t.Fatal(err)
	}
	ordinal := 0
	property := func(configured, derived, foreign bool) bool {
		ordinal++
		s.WithOperatorAccounts(nil)
		if configured {
			s.WithOperatorAccounts([]string{"member"})
		}
		proof := capability.ProofForConsole(auth.Principal)
		if foreign {
			proof.Session.ID = "foreign"
		}
		if derived {
			proof = nil
		}
		spec := capability.ProvisionSpec{Kind: capability.ProvisionService, Authority: &auth.Principal.Authority, ConsoleProof: proof, RegistrationID: fmt.Sprintf("service:property-%d", ordinal), Subject: capability.Subject{Kind: capability.SubjectService, ID: fmt.Sprintf("property-%d", ordinal)}, Grants: []capability.Grant{capability.OperatorTokens}, Binding: capability.Binding{Kind: capability.BindingNone}}
		issued, err := s.store.ProvisionCapability(t.Context(), spec)
		return (err == nil && issued.Raw != "") == (configured && !derived && !foreign)
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 48}); err != nil {
		t.Fatal(err)
	}
}

func TestReviewSetupRollbackPreservesBootstrap(t *testing.T) {
	e, _ := engineRuntimeStore(t)
	raw := reviewSeed(t, e.Store)
	h := New(e.Store, nil).Handler()
	engineSQL(t, e.Store, `CREATE TRIGGER refuse_bootstrap_revoke BEFORE UPDATE OF revoked_at ON capabilities WHEN OLD.subject_id='bootstrap:admin' BEGIN SELECT RAISE(ABORT,'refuse setup'); END`)
	engineClaimHTTP(t, h, raw, "POST", "/api/v1/operator/tokens", `{"name":"administrator","grants":["operator.tokens"]}`, 500)
	var count int
	if err := teststore.SQL(t, e.Store).QueryRow(`SELECT COUNT(*) FROM capabilities WHERE subject_id='administrator'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial setup: %d %v", count, err)
	}
	engineClaimHTTP(t, h, raw, "GET", "/api/v1/operator/tokens", "", 200)
}

func TestReviewRotationUsesProductionManager(t *testing.T) {
	s, raw, csrf := reviewAccount(t, "reader")
	w := reviewSessionRequest(t, s, raw, csrf, "POST", "/api/v1/tokens", `{"grants":["runs.read"]}`, 201)
	var original credentialResponse
	if err := json.Unmarshal(w.Body.Bytes(), &original); err != nil {
		t.Fatal(err)
	}
	w = reviewSessionRequest(t, s, raw, csrf, "POST", "/api/v1/tokens/"+original.Prefix+"/rotate", `{"grace_secs":0}`, 200)
	var rotation capabilityRotationResponse
	if err := json.Unmarshal(w.Body.Bytes(), &rotation); err != nil {
		t.Fatal(err)
	}
	if rotation.New.Subject != (capability.Subject{Kind: capability.SubjectAccount, ID: "member"}) || rotation.OldPrefix != original.Prefix {
		t.Fatalf("rotation identity: %+v", rotation)
	}
	engineClaimHTTP(t, s.Handler(), original.Capability, "GET", "/api/v1/runs/run/jobs", "", 401)
	engineClaimHTTP(t, s.Handler(), rotation.Capability, "GET", "/api/v1/runs/run/jobs", "", 200)
}

func TestSetupAdministratorIdentityUsesSubjectKind(t *testing.T) {
	e, _ := engineRuntimeStore(t)
	raw := reviewSeed(t, e.Store)
	h := New(e.Store, nil).Handler()
	w := engineClaimHTTP(t, h, raw, "POST", "/api/v1/operator/tokens", `{"name":"bootstrap:admin","grants":["operator.tokens"]}`, 201)
	var issued credentialResponse
	if err := json.Unmarshal(w.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	engineClaimHTTP(t, h, issued.Capability, "GET", "/api/v1/operator/tokens", "", 200)
	engineClaimHTTP(t, h, raw, "GET", "/api/v1/operator/tokens", "", 401)
}

func TestNativeTokenOwnershipRelationProperty(t *testing.T) {
	s, raw, csrf := reviewAccount(t, "reader")
	engineSQL(t, s.store, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('other','other@example.test',1,1)`)
	engineSQL(t, s.store, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('a','other','owner',1)`)
	other, otherCSRF, _, err := s.store.CreateAccountSession(t.Context(), store.Account{ID: "other"}, "a", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	sequence := 0
	property := func(owned, administrator, personal bool, hint uint64) bool {
		engineSQL(t, s.store, `UPDATE memberships SET role='owner' WHERE team='a' AND account_id='member'`)
		creator, key := other, otherCSRF
		if owned {
			creator, key = raw, csrf
		}
		body := fmt.Sprintf(`{"name":"property-%d","repos":["github.com/acme/*"],"personal":%t}`, sequence, personal)
		sequence++
		issued := reviewSessionRequest(t, s, creator, key, "POST", "/api/v1/runners", body, 201)
		var token credentialResponse
		if err := json.Unmarshal(issued.Body.Bytes(), &token); err != nil {
			t.Fatal(err)
		}
		if !administrator {
			engineSQL(t, s.store, `UPDATE memberships SET role='reader' WHERE team='a' AND account_id='member'`)
		}
		expected := 403
		allowed := owned && personal || administrator
		if allowed {
			expected = 204
		}
		reviewSessionRequest(t, s, raw, csrf, "DELETE", fmt.Sprintf("/api/v1/tokens/%s?owner=member&team=%d", token.Prefix, hint), "", expected)
		_, err := s.store.LookupCapability(t.Context(), sha256.Sum256([]byte(token.Capability)))
		return (err != nil) == allowed
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 32}); err != nil {
		t.Fatal(err)
	}
}

func TestNativeDeploymentEligibilityProperty(t *testing.T) {
	e, _ := engineRuntimeStore(t)
	raw := reviewSeed(t, e.Store)
	deployments := []deployment.Config{{}, deploymenttest.Team(t), deploymenttest.Cloud(t, "https://controller.test")}
	property := func(pick uint8, hint uint64) bool {
		d := deployments[int(pick)%len(deployments)]
		s := NewDeployment(e.Store, d, nil)
		r := httptest.NewRequest("GET", fmt.Sprintf("/api/v1/operator/tokens?mode=cloud&eligible=%d", hint), nil)
		r.Header.Set("Authorization", "Bearer "+raw)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		expected := 200
		if d.Kind() == deployment.KindTeam {
			expected = 401
		}
		return w.Code == expected
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 32}); err != nil {
		t.Fatal(err)
	}
}
