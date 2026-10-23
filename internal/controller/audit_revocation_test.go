package controller

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

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
				endpoint, status = "/logout", http.StatusSeeOther
				s.WithDashboard(Dashboard{})
				req := httptest.NewRequest("POST", "https://controller.test/logout", strings.NewReader(url.Values{"csrf_token": {csrf}}.Encode()))
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
