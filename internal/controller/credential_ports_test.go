package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
)

func TestCredentialMemberHTTPRoleCeilingProperty(t *testing.T) {
	property := func(roleIndex, selection uint8) bool {
		s, session := credentialSessionServer(t)
		role := []store.Role{store.RoleReader, store.RoleEditor, store.RoleOwner}[roleIndex%3]
		engineSQL(t, s.store, `UPDATE memberships SET role=? WHERE team='a' AND account_id='member'`, role)
		allowed := []capability.Grant{capability.RunsRead, capability.LogsRead}
		if role != store.RoleReader {
			allowed = append(allowed, capability.RunsWrite, capability.RunsControl, capability.ApprovalsWrite)
		}
		if role == store.RoleOwner {
			allowed = append(allowed, capability.TeamAdmin)
		}
		grant := []capability.Grant{capability.RunsRead, capability.RunsWrite, capability.RunsControl, capability.LogsRead, capability.ApprovalsWrite, capability.TeamAdmin, capability.NodesClaim}[selection%7]
		body, err := json.Marshal(credentialRequest{Grants: []capability.Grant{grant}, AllowTeamAdmin: grant == capability.TeamAdmin})
		if err != nil {
			t.Fatal(err)
		}
		w := credentialRequestHTTP(t, s.Handler(), session, string(body), false)
		if !slices.Contains(allowed, grant) {
			var count int
			if err := teststore.SQL(t, s.store).QueryRow(`SELECT COUNT(*) FROM capabilities`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if w.Code != 403 || count != 0 {
				t.Logf("role=%s grant=%s: %d %s count=%d", role, grant, w.Code, w.Body, count)
			}
			return w.Code == 403 && count == 0
		}
		var issued credentialResponse
		if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &issued) != nil || !strings.HasPrefix(issued.Capability, "swk_") || !slices.Equal(issued.Grants, []capability.Grant{grant}) {
			t.Logf("issuance: %d %s", w.Code, w.Body)
			return false
		}
		principal, err := s.store.AsOperator().LookupCapability(t.Context(), sha256.Sum256([]byte(issued.Capability)))
		return err == nil && principal.Capability.Parent != nil && principal.Capability.Parent.Kind == capability.AuthoritySession && principal.Capability.Subject == (capability.Subject{Kind: capability.SubjectAccount, ID: "member"}) && principal.Capability.Team == "a"
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 42}); err != nil {
		t.Fatal(err)
	}
}

func TestCredentialMemberHTTPMembershipAndSessionLifecycle(t *testing.T) {
	for _, change := range []string{"session revoked", "removed", "demoted"} {
		t.Run(change, func(t *testing.T) {
			s, session := credentialSessionServer(t)
			h := s.Handler()
			w := credentialRequestHTTP(t, h, session, `{"grants":["runs.read","runs.write"]}`, false)
			var issued credentialResponse
			if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &issued) != nil {
				t.Fatalf("issuance: %d %s", w.Code, w.Body)
			}
			if w := credentialRequestHTTP(t, h, issued.Capability, `{"grants":["runs.read"]}`, true); w.Code != 201 {
				t.Fatalf("delegation: %d %s", w.Code, w.Body)
			}
			engineClaimHTTP(t, h, issued.Capability, "PATCH", "/api/v1/team", `{"display_name":"forged"}`, 403)
			engineClaimHTTP(t, h, issued.Capability, "POST", "/api/v1/runners", `{"name":"forged","repos":["github.com/acme/*"]}`, 403)
			switch change {
			case "session revoked":
				digest := sha256.Sum256([]byte(session))
				engineSQL(t, s.store, `UPDATE sessions SET revoked_at=1 WHERE hash=?`, hex.EncodeToString(digest[:]))
				if w := credentialRequestHTTP(t, h, issued.Capability, `{"grants":["runs.read"]}`, true); w.Code != 401 {
					t.Fatalf("revoked session descendant: %d %s", w.Code, w.Body)
				}
			case "removed":
				engineSQL(t, s.store, `DELETE FROM memberships WHERE team='a' AND account_id='member'`)
				if w := credentialRequestHTTP(t, h, issued.Capability, `{"grants":["runs.read"]}`, true); w.Code != 401 {
					t.Fatalf("dead member: %d %s", w.Code, w.Body)
				}
			case "demoted":
				engineSQL(t, s.store, `UPDATE memberships SET role='reader' WHERE team='a' AND account_id='member'`)
				if w := credentialRequestHTTP(t, h, issued.Capability, `{"grants":["runs.read"]}`, true); w.Code != 201 {
					t.Fatalf("reader delegation: %d %s", w.Code, w.Body)
				}
				if w := credentialRequestHTTP(t, h, issued.Capability, `{"grants":["runs.write"]}`, true); w.Code != 403 {
					t.Fatalf("reader widened: %d %s", w.Code, w.Body)
				}
			}
		})
	}
}

func TestCredentialClaimHTTPAncestryAndFenceProperty(t *testing.T) {
	property := func(mutation uint8, header []byte, trigger bool) bool {
		e, h, root := engineClaimHTTPFixture(t, []capability.Grant{capability.NodesClaim})
		body := `{"runner_version":"v1","kinds":["node"]}`
		if trigger {
			e, _ = engineRuntimeStore(t)
			e.claimProfiles = engineProfileFake{}
			e.RunnerPolicy = runnerAdmissionFunc(func(context.Context, string) error { return nil })
			root = engineHTTPRoot(t, e, "a", "root", []capability.Grant{capability.TriggersClaim})
			h = New(e.Store, nil).WithEngineController(e.EngineController).Handler()
			body = `{"runner_version":"v1","kinds":["trigger"]}`
		}
		w := engineClaimHTTP(t, h, root, "POST", "/api/v1/runners/claim", body, 200)
		var response controllerwire.ClaimResponse
		if json.Unmarshal(w.Body.Bytes(), &response) != nil || response.Claim == nil {
			t.Fatal("missing awarded capability")
		}
		raw := response.Claim.Capability
		engineClaimHTTP(t, h, root, "POST", "/api/v1/runs/run/jobs/job/annotations", `{"message":"root"}`, 403)
		engineClaimHTTP(t, h, raw, "GET", "/api/v1/runners", "", 403)
		engineClaimHTTP(t, h, raw, "GET", "/api/v1/runs/run", "", 200)
		status := 401
		switch mutation % 5 {
		case 0:
			status = 200
		case 1:
			revokeEngineMachine(t, e.Store, "root")
		case 2:
			if trigger {
				engineSQL(t, e.Store, `UPDATE triggers SET claimed_by='replacement',generation=generation+1 WHERE team='a' AND id='run'`)
			} else {
				engineSQL(t, e.Store, `UPDATE nodes SET claimed_by='replacement',generation=generation+1 WHERE team='a' AND run_id='run' AND node_id='job'`)
			}
		case 3:
			if trigger {
				engineSQL(t, e.Store, `UPDATE triggers SET status='done',finished_at=1,claimed_by=NULL,claimed_at=NULL,lease_expires_at=NULL,capability_prefix='' WHERE team='a' AND id='run'`)
			} else {
				engineSQL(t, e.Store, `UPDATE nodes SET status='done',outcome='success',finished_at=1,claimed_by=NULL,lease_expires_at=NULL,capability_prefix='' WHERE team='a' AND run_id='run' AND node_id='job'`)
			}
		case 4:
			if trigger {
				engineSQL(t, e.Store, `UPDATE triggers SET lease_expires_at=1 WHERE team='a' AND id='run'`)
			} else {
				engineSQL(t, e.Store, `UPDATE nodes SET lease_expires_at=1 WHERE team='a' AND run_id='run' AND node_id='job'`)
			}
		}
		r := httptest.NewRequest(http.MethodGet, "/api/v1/runs/run", nil)
		r.Header.Set("Authorization", "Bearer "+raw)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Sparkwing-Claim-Generation", "99")
		r.Header.Set("X-Sparkwing-Trigger-Generation", "99")
		r.Header.Set("X-Sparkwing-Runner", hex.EncodeToString(header))
		w = httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != status {
			t.Logf("mutation %d: %d %s", mutation%5, w.Code, w.Body)
			return false
		}
		if status == 401 {
			var failure apierr.Envelope
			return json.Unmarshal(w.Body.Bytes(), &failure) == nil && failure.Error != nil && failure.Error.Code == apierr.Unauthenticated
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 48}); err != nil {
		t.Fatal(err)
	}
}

func TestCredentialMachineHTTPCannotBecomeAMemberProperty(t *testing.T) {
	property := func(grantIndex uint8, selector []byte) bool {
		e, _ := engineRuntimeStore(t)
		grant := []capability.Grant{capability.NodesClaim, capability.TriggersClaim, capability.RunsWrite, capability.RunsControl}[grantIndex%4]
		raw := engineHTTPRoot(t, e, "a", "machine", []capability.Grant{grant})
		h := New(e.Store, nil).WithEngineController(e.EngineController).Handler()
		for _, request := range []struct{ path, body string }{
			{"/api/v1/runs", `{"request":{"version":1,"team":"a","pipeline":"victim","git":{"repository":{"key":"gh:1"}}}}`},
			{"/api/v1/runs/run/retry", `{"idempotency_key":"retry-once"}`},
			{"/api/v1/runs/run/cancel", `{"reason":"forged"}`},
		} {
			r := httptest.NewRequest(http.MethodPost, request.path, strings.NewReader(request.body))
			r.Header.Set("Authorization", "Bearer "+raw)
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("X-Sparkwing-Team", hex.EncodeToString(selector))
			r.Header.Set("X-Sparkwing-Claim-Generation", "1")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			var failure apierr.Envelope
			if w.Code != http.StatusForbidden || json.Unmarshal(w.Body.Bytes(), &failure) != nil || failure.Error == nil || failure.Error.Code != apierr.Forbidden {
				t.Logf("grant=%s path=%s: %d %s", grant, request.path, w.Code, w.Body)
				return false
			}
		}
		var runs int
		var cancelled any
		if err := teststore.SQL(t, e.Store).QueryRow(`SELECT COUNT(*),MAX(cancel_requested_at) FROM runs`).Scan(&runs, &cancelled); err != nil {
			t.Fatal(err)
		}
		return runs == 1 && cancelled == nil
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 32}); err != nil {
		t.Fatal(err)
	}
}

func TestCredentialMemberHTTPEncodedPathsPreserveTenantProperty(t *testing.T) {
	cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	s, session := credentialSessionServer(t, store.WithPolicies(func(*store.Store) (store.Policies, error) {
		return store.Policies{SessionKeys: cipher}, nil
	}))
	session, _, _, err = s.store.AsOperator().CreateAccountSession(t.Context(), store.Account{ID: "member"}, "a", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	mint := func(raw string) string {
		w := credentialRequestHTTP(t, h, raw, `{"grants":["runs.read"]}`, false)
		var issued credentialResponse
		if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &issued) != nil || issued.Capability == "" {
			t.Fatalf("issuance: %d %s", w.Code, w.Body)
		}
		return issued.Capability
	}
	own := mint(session)
	engineSQL(t, s.store, `INSERT INTO teams(name,created_at,updated_at) VALUES ('b',1,1)`)
	engineSQL(t, s.store, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('b','member','reader',1)`)
	foreignSession, _, _, err := s.store.AsOperator().CreateAccountSession(t.Context(), store.Account{ID: "member"}, "b", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	foreign := mint(foreignSession)
	engineSQL(t, s.store, `INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json) VALUES ('a','run','private-job','pending',?,?)`, []byte(`{"id":"private-job"}`), []byte(`{}`))
	property := func(encoded uint16, hint []byte) bool {
		path := "/api/v1/runs/run/jobs"
		var spelling strings.Builder
		for i, value := range []byte(path) {
			if value != '/' && encoded&(1<<uint(i%16)) != 0 {
				spelling.WriteByte('%')
				spelling.WriteString(hex.EncodeToString([]byte{value}))
			} else {
				spelling.WriteByte(value)
			}
		}
		for _, auth := range []struct {
			raw    string
			status int
		}{{own, http.StatusOK}, {foreign, http.StatusNotFound}} {
			r := httptest.NewRequest(http.MethodGet, spelling.String()+"?team=a&holder="+hex.EncodeToString(hint), nil)
			r.Header.Set("Authorization", "Bearer "+auth.raw)
			r.Header.Set("X-Sparkwing-Team", "a")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != auth.status || (auth.status == http.StatusOK) != strings.Contains(w.Body.String(), "private-job") {
				t.Logf("%s: %d %s want %d", spelling.String(), w.Code, w.Body, auth.status)
				return false
			}
			if auth.status == http.StatusNotFound {
				var failure apierr.Envelope
				if json.Unmarshal(w.Body.Bytes(), &failure) != nil || failure.Error == nil || failure.Error.Code != apierr.NotFound {
					return false
				}
			}
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 64}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/api/v1/runs//run/jobs", "/api/v1/runs/./run/jobs", "/api/v1/runs/x/../run/jobs",
		"/api/v1//runs/run/jobs", "/api/v1/runs/run/jobs/", "/api/v1/runs/%2E%2E/run/jobs",
		"/api/v1/runs/run%2Fjobs", "/api/v1/runs/%2572un/jobs", "/api/v1/runs/%EF%BD%92un/jobs",
	} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("Authorization", "Bearer "+foreign)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if !slices.Contains([]int{http.StatusMovedPermanently, http.StatusTemporaryRedirect, http.StatusBadRequest, http.StatusNotFound, http.StatusMethodNotAllowed}, w.Code) || strings.Contains(w.Body.String(), "private-job") {
			t.Fatalf("noncanonical path %s: %d %s", path, w.Code, w.Body)
		}
	}
	var state string
	var cancelled any
	if err := teststore.SQL(t, s.store).QueryRow(`SELECT status,cancel_requested_at FROM runs WHERE team='a' AND id='run'`).Scan(&state, &cancelled); err != nil || state != "running" || cancelled != nil {
		t.Fatalf("run changed: %s %v %v", state, cancelled, err)
	}
}

func TestCredentialStartedClaimHTTPConsumesContractAttempt(t *testing.T) {
	e, h, root := engineClaimHTTPFixture(t, []capability.Grant{capability.NodesClaim})
	w := engineClaimHTTP(t, h, root, "POST", "/api/v1/runners/claim", `{"runner_version":"v1","kinds":["node"]}`, 200)
	var response controllerwire.ClaimResponse
	if json.Unmarshal(w.Body.Bytes(), &response) != nil || response.Claim == nil {
		t.Fatal("missing awarded capability")
	}
	raw := response.Claim.Capability
	engineAdmitBearer(t, e.Store, raw)
	w = engineClaimHTTP(t, h, raw, "POST", "/api/v1/runs/run/jobs/job/transition", `{"to":"running","cause":"start","source_attempt":0}`, 200)
	var started controllerwire.Applied
	if json.Unmarshal(w.Body.Bytes(), &started) != nil || started.Attempt != 1 {
		t.Fatal("missing committed attempt")
	}
	digest := sha256.Sum256([]byte(raw))
	snapshot, err := e.Store.AsOperator().ReadAuthentication(t.Context(), capability.CredentialLookup{Class: capability.CredentialBearer, Digest: digest, Audience: capability.Controller}, capability.RequestEvidence{})
	if err != nil || snapshot.Binding.Current.Attempt != started.Attempt {
		t.Fatalf("started snapshot: %+v %v", snapshot, err)
	}
	snapshot.Authorities[0].Principal.Capability.Binding.Attempt = started.Attempt
	facts := &reportingFacts{digest: digest, snapshot: snapshot}
	s := New(e.Store, nil).WithEngineController(e.EngineController)
	a := s.authorization
	a.Authenticator = capability.NewAuthenticator(facts)
	s.WithAuthorization(a)
	h = s.Handler()
	engineClaimHTTP(t, h, raw, "POST", "/api/v1/runs/run/jobs/job/annotations", `{"message":"accepted"}`, 204)
	engineClaimHTTP(t, h, raw, "POST", "/api/v1/runs/run/jobs/other/annotations", `{"message":"foreign"}`, 403)
	revokeEngineMachine(t, e.Store, "root")
	engineClaimHTTP(t, h, raw, "POST", "/api/v1/runs/run/jobs/job/annotations", `{"message":"after-revocation"}`, 401)
	var count int
	if err := teststore.SQL(t, e.Store).QueryRow(`SELECT annotation_count FROM runs WHERE team='a' AND id='run'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("revoked proof changed annotation count: %d %v", count, err)
	}
}
