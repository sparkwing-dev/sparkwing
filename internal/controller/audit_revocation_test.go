package controller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"

	"github.com/sparkwing-dev/sparkwing/internal/deployment"
	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/license"
)

func TestRevocationPathsEmitIdentifyingAuditLines(t *testing.T) {
	for _, path := range []string{"api-token", "machine-credential", "runner-removal", "github-runner-removal", "member-removal", "member-demotion", "admin-revocation", "token-rotation", "api-logout", "browser-logout"} {
		t.Run(path, func(t *testing.T) {
			s, raw, csrf := reviewAccount(t, "owner")
			var logs bytes.Buffer
			s.logger = slog.New(slog.NewJSONHandler(&logs, nil))
			s.deployment = deploymenttest.Parse(t, deployment.Inputs{Kind: string(deployment.KindTeam), PublicOrigin: "https://controller.test"}, license.FeatureMultiTeam)
			secrets := []string{raw, csrf}
			issue := func(endpoint, body string) credentialResponse {
				t.Helper()
				response := reviewSessionRequest(t, s, raw, csrf, "POST", endpoint, body, http.StatusCreated)
				var token credentialResponse
				if err := json.Unmarshal(response.Body.Bytes(), &token); err != nil {
					t.Fatal(err)
				}
				secrets = append(secrets, token.Capability)
				return token
			}
			method, endpoint, body, key, id, status := "DELETE", "", "", "token_prefix", "", http.StatusNoContent
			switch path {
			case "api-token", "token-rotation":
				token := issue("/api/v1/tokens", `{"grants":["runs.read"]}`)
				endpoint, id = "/api/v1/tokens/"+token.Prefix, token.Prefix
			case "machine-credential", "runner-removal":
				token := issue("/api/v1/runners", `{"name":"restore-worker","repos":["github.com/acme/*"]}`)
				endpoint, id = "/api/v1/tokens/"+token.Prefix, token.Prefix
				if path == "runner-removal" {
					endpoint, key, id, status = "/api/v1/runners/restore-worker", "machine_id", "restore-worker", http.StatusOK
				}
			case "github-runner-removal":
				reviewSessionRequest(t, s, raw, csrf, "POST", "/api/v1/runners/github", `{"repository":"acme/repo","repository_id":42,"repository_owner_id":7}`, http.StatusCreated)
				endpoint, key, id = "/api/v1/runners/github/42", "repository_id", "42"
			case "member-removal", "member-demotion":
				id, key = "0123456789abcdef", "member_id"
				engineSQL(t, s.store, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES (?,'departing@example.test',1,1)`, id)
				engineSQL(t, s.store, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('a',?,'owner',1)`, id)
				endpoint = "/api/v1/team/members/" + id
				if path == "member-demotion" {
					method, body = "PATCH", `{"role":"reader"}`
				}
			case "admin-revocation":
				s.WithOperatorAccounts([]string{"member"})
				token := issue("/api/v1/operator/tokens", `{"name":"restore-admin","grants":["operator.tokens"]}`)
				endpoint, id = "/api/v1/operator/tokens/"+token.Prefix, token.Prefix
			case "api-logout", "browser-logout":
				method, endpoint, key, id = "POST", "/api/v1/auth/logout", "principal_id", "member"
			}
			if strings.HasSuffix(path, "rotation") {
				method, endpoint, body, status = "POST", endpoint+"/rotate", `{"grace_secs":0}`, http.StatusOK
			}
			logs.Reset()
			if path == "browser-logout" {
				status = http.StatusSeeOther
				s.WithDashboard(Dashboard{})
				req := httptest.NewRequest(http.MethodPost, "https://controller.test/logout", strings.NewReader(url.Values{"csrf_token": {csrf}}.Encode()))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				req.Header.Set("Origin", "https://controller.test")
				req.AddCookie(&http.Cookie{Name: cookieName(sessionCookieName, s.cookiesSecure()), Value: raw})
				req.AddCookie(&http.Cookie{Name: cookieName(csrfCookieName, s.cookiesSecure()), Value: csrf})
				rec := httptest.NewRecorder()
				s.Handler().ServeHTTP(rec, req)
				if rec.Code != status {
					t.Fatalf("browser logout: %d %s", rec.Code, rec.Body)
				}
			} else {
				reviewSessionRequest(t, s, raw, csrf, method, endpoint, body, status)
			}
			records := auditLines(t, &logs, "audit")
			if len(records) != 1 {
				t.Fatalf("want one revocation audit line: %s", &logs)
			}
			rec := records[0]
			team := "a"
			if path == "admin-revocation" {
				team = ""
			}
			if rec[key] != id || rec["team"] != team || rec["status"] != float64(status) || rec["method"] != method {
				t.Fatalf("unidentifiable revocation: %v (want %s=%s, team=%q)", rec, key, id, team)
			}
			if path == "member-demotion" && rec["role"] != "reader" {
				t.Fatalf("missing demotion role: %v", rec)
			}
			for _, secret := range append(secrets, "departing@example.test") {
				if secret != "" && strings.Contains(logs.String(), secret) {
					t.Fatalf("audit contains a secret or email: %s", &logs)
				}
			}
		})
	}
}

func TestAuditRejectsCredentialRequestIDsAndUncommittedRunnerTargets(t *testing.T) {
	s, raw, csrf := reviewAccount(t, "owner")
	var logs bytes.Buffer
	s.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	s.deployment = deploymenttest.Parse(t, deployment.Inputs{Kind: string(deployment.KindTeam), PublicOrigin: "https://controller.test"}, license.FeatureMultiTeam)
	for _, prefix := range capability.CredentialPrefixes() {
		t.Run(prefix, func(t *testing.T) {
			logs.Reset()
			credential := prefix + strings.Repeat("x", 43)
			req := httptest.NewRequest(http.MethodDelete, "https://controller.test/api/v1/runners/"+credential, nil)
			req.Header.Set(RequestIDHeader, credential)
			req.Header.Set("Origin", "https://controller.test")
			req.Header.Set(csrfHeaderName, csrf)
			req.AddCookie(&http.Cookie{Name: cookieName(sessionCookieName, s.cookiesSecure()), Value: raw})
			req.AddCookie(&http.Cookie{Name: cookieName(csrfCookieName, s.cookiesSecure()), Value: csrf})
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("delete: %d %s", w.Code, w.Body)
			}
			if strings.Contains(logs.String(), credential) {
				t.Fatal("credential leaked in audit output")
			}
			records := auditLines(t, &logs, "audit")
			if len(records) != 1 || records[0]["machine_id"] != nil {
				t.Fatalf("invented revocation: %v", records)
			}
			id := w.Header().Get(RequestIDHeader)
			if id == credential || len(id) != 24 || records[0]["request_id"] != id {
				t.Fatalf("unsafe request ID: %q", id)
			}
		})
	}
}

func TestSessionRevocationAuditIdentifiesOnlyTheEndedSession(t *testing.T) {
	for _, flow := range []string{"logout", "login", "oauth", "other-account-login"} {
		t.Run(flow, func(t *testing.T) {
			s, prior, csrf := reviewAccount(t, "owner")
			account, team := "member", store.Team("a")
			if flow == "other-account-login" {
				account, team = "other", "b"
				engineSQL(t, s.store, `INSERT INTO teams(name,created_at,updated_at) VALUES ('b',1,1)`)
				engineSQL(t, s.store, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('other','other@example.test',1,1)`)
				engineSQL(t, s.store, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('b','other','owner',1)`)
			}
			next, _, _, err := s.operator.CreateAccountSession(t.Context(), store.Account{ID: account}, team, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			session, err := s.operator.LookupSession(t.Context(), next)
			if err != nil {
				t.Fatal(err)
			}
			var logs *bytes.Buffer
			if flow == "logout" {
				logs = new(bytes.Buffer)
				s.logger = slog.New(slog.NewJSONHandler(logs, nil))
				s.deployment = deploymenttest.Parse(t, deployment.Inputs{Kind: string(deployment.KindTeam), PublicOrigin: "https://controller.test"}, license.FeatureMultiTeam)
				reviewSessionRequest(t, s, prior, csrf, "POST", "/api/v1/auth/logout", "", http.StatusNoContent)
			} else {
				handler, output := auditHarness(t, "/login", func(w http.ResponseWriter, r *http.Request) {
					notePolicyPrincipal(r.Context(), capability.Principal{Capability: capability.Capability{Team: string(team), Subject: capability.Subject{Kind: capability.SubjectAccount, ID: account}}})
					var err error
					if flow == "oauth" {
						r = r.WithContext(context.WithValue(r.Context(), oauthProofKey{}, oauthProof{raw: next, session: session}))
						err = s.handleOAuthCallback(w, r, capability.Authorized{})
					} else {
						r = r.WithContext(context.WithValue(r.Context(), loginProofKey{}, loginProof{raw: next, session: session}))
						err = s.handleLoginSubmit(w, r, capability.Authorized{})
					}
					if err != nil {
						t.Fatal(err)
					}
				})
				logs = output
				req := httptest.NewRequest(http.MethodPost, "https://controller.test/login", nil)
				req.AddCookie(&http.Cookie{Name: cookieName(sessionCookieName, s.cookiesSecure()), Value: prior})
				handler.ServeHTTP(httptest.NewRecorder(), req)
			}
			records := auditLines(t, logs, "audit")
			digest := fmt.Sprintf("%x", sha256.Sum256([]byte(prior)))
			if len(records) != 1 || records[0]["session_digest"] != digest || records[0]["revoked_account_id"] != "member" || records[0]["revoked_team"] != "a" {
				t.Fatalf("unrecoverable session revocation: %v", records)
			}
			if _, err := s.operator.LookupSession(t.Context(), prior); err == nil {
				t.Fatal("ended session still works")
			}
			if _, err := s.operator.LookupSession(t.Context(), next); err != nil {
				t.Fatalf("other session ended: %v", err)
			}
			if strings.Contains(logs.String(), prior) || strings.Contains(logs.String(), next) {
				t.Fatal("raw session logged")
			}
		})
	}
}

func TestRunnerRevocationAuditFingerprintsCredentialShapedMachineNames(t *testing.T) {
	s, raw, csrf := reviewAccount(t, "owner")
	s.deployment = deploymenttest.Parse(t, deployment.Inputs{Kind: string(deployment.KindTeam), PublicOrigin: "https://controller.test"}, license.FeatureMultiTeam)
	response := reviewSessionRequest(t, s, raw, csrf, "POST", "/api/v1/tokens", `{"grants":["runs.read"]}`, http.StatusCreated)
	var token credentialResponse
	if err := json.Unmarshal(response.Body.Bytes(), &token); err != nil {
		t.Fatal(err)
	}
	response = reviewSessionRequest(t, s, raw, csrf, "POST", "/api/v1/runners", fmt.Sprintf(`{"name":%q,"repos":["github.com/acme/*"]}`, token.Capability), http.StatusCreated)
	var runner credentialResponse
	if err := json.Unmarshal(response.Body.Bytes(), &runner); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	s.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	reviewSessionRequest(t, s, raw, csrf, "DELETE", "/api/v1/runners/"+token.Capability, "", http.StatusOK)
	records := auditLines(t, &logs, "audit")
	digest := sha256.Sum256([]byte(token.Capability))
	fingerprint := fmt.Sprintf("[credential:%x]", digest[:8])
	if len(records) != 1 || records[0]["machine_id"] != fingerprint || records[0]["token_prefixes"] != runner.Prefix || records[0]["team"] != "a" {
		t.Fatalf("unrecoverable runner revocation: %v", records)
	}
	if strings.Contains(logs.String(), token.Capability) || strings.Contains(logs.String(), runner.Capability) {
		t.Fatal("credential leaked in audit")
	}
}

func TestRejectedRevocationsDoNotLogCommittedTargets(t *testing.T) {
	s, raw, csrf := reviewAccount(t, "owner")
	var logs bytes.Buffer
	s.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	s.deployment = deploymenttest.Parse(t, deployment.Inputs{Kind: string(deployment.KindTeam), PublicOrigin: "https://controller.test"}, license.FeatureMultiTeam)
	for _, target := range []struct {
		method, path, body string
		status             int
	}{
		{"DELETE", "/api/v1/tokens/swk_abcdefghijkl", "", http.StatusUnauthorized},
		{"DELETE", "/api/v1/team/members/0123456789abcdef", "", http.StatusNotFound},
		{"PATCH", "/api/v1/team/members/member", `{"role":"reader"}`, http.StatusConflict},
	} {
		logs.Reset()
		reviewSessionRequest(t, s, raw, csrf, target.method, target.path, target.body, target.status)
		records := auditLines(t, &logs, "audit")
		if len(records) != 1 || records[0]["token_prefix"] != nil || records[0]["member_id"] != nil || records[0]["role"] != nil {
			t.Fatalf("uncommitted revocation target logged: %v", records)
		}
	}
}

func TestIdentityUnlinkAuditIdentifiesKeptAndEndedSessions(t *testing.T) {
	for _, count := range []int{0, 3} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			s, keep, csrf := reviewAccount(t, "owner")
			engineSQL(t, s.store, `INSERT INTO identities(provider,subject,account_id,email,email_verified,created_at,linked) VALUES ('github','linked','member','',0,1,1),('google','original','member','',0,1,0)`)
			ended := make(map[string]bool)
			for range count {
				raw, _, _, err := s.operator.CreateAccountSession(t.Context(), store.Account{ID: "member"}, "a", time.Hour)
				if err != nil {
					t.Fatal(err)
				}
				ended[fmt.Sprintf("%x", sha256.Sum256([]byte(raw)))] = true
			}
			var logs bytes.Buffer
			s.logger = slog.New(slog.NewJSONHandler(&logs, nil))
			s.deployment = deploymenttest.Parse(t, deployment.Inputs{Kind: string(deployment.KindTeam), PublicOrigin: "https://controller.test"}, license.FeatureMultiTeam)
			reviewSessionRequest(t, s, keep, csrf, "DELETE", "/api/v1/me/identities/github", "", http.StatusNoContent)
			records := auditLines(t, &logs, "audit")
			if len(records) != max(1, count) {
				t.Fatalf("missing session identities: %v", records)
			}
			for _, rec := range records {
				if rec["kept_session_digest"] != fmt.Sprintf("%x", sha256.Sum256([]byte(keep))) {
					t.Fatalf("missing kept session: %v", rec)
				}
				if count == 0 {
					if rec["session_digest"] != nil {
						t.Fatalf("invented revocation: %v", rec)
					}
					continue
				}
				digest, _ := rec["session_digest"].(string)
				if !ended[digest] || rec["revoked_account_id"] != "member" || rec["revoked_team"] != "a" {
					t.Fatalf("unrecoverable unlink: %v", rec)
				}
				delete(ended, digest)
			}
			if _, err := s.operator.LookupSession(t.Context(), keep); err != nil {
				t.Fatalf("kept session ended: %v", err)
			}
			logs.Reset()
			reviewSessionRequest(t, s, keep, csrf, "DELETE", "/api/v1/me/identities/google", "", http.StatusConflict)
			for _, rec := range auditLines(t, &logs, "audit") {
				if rec["session_digest"] != nil || rec["kept_session_digest"] != nil {
					t.Fatalf("failed unlink logged committed targets: %v", rec)
				}
			}
		})
	}
}
