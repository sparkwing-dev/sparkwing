package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestConsoleDefaultCompositionCommitsEveryDeclaredWrite(t *testing.T) {
	cases := []struct {
		method, path, body string
		status             int
		query              string
	}{
		{"POST", "/api/v1/operator/teams/b/grants", `{"amount_cents":10,"reason":"review","key":"once"}`, 201, `SELECT COUNT(*) FROM credit_grants WHERE team='b' AND created_by='operator'`},
		{"POST", "/api/v1/operator/teams/b/trust", `{"trust":"granted","reason":"review"}`, 200, `SELECT COUNT(*) FROM teams WHERE name='b' AND billing_trust='granted' AND billing_trust_by='operator'`},
		{"POST", "/api/v1/operator/teams/b/freeze", `{"reason":"review"}`, 200, `SELECT COUNT(*) FROM credit_freezes WHERE team='b' AND released_at IS NULL`},
		{"POST", "/api/v1/operator/teams/b/unfreeze", `{"reason":"resolved","hold":"held"}`, 200, `SELECT COUNT(*) FROM credit_freezes WHERE team='b' AND dispute_id='held' AND released_at IS NOT NULL`},
		{"DELETE", "/api/v1/operator/teams/b", "", 202, `SELECT COUNT(*) FROM team_deletions WHERE slug='b' AND requested_by='operator'`},
		{"DELETE", "/api/v1/operator/accounts/victim@example.test", "", 200, `SELECT COUNT(*) FROM accounts WHERE id='operator' AND NOT EXISTS(SELECT 1 FROM accounts WHERE id='victim')`},
		{"POST", "/api/v1/operator/waitlist/approve", `{"account_ids":["waiting"]}`, 200, `SELECT COUNT(*) FROM accounts WHERE id='waiting' AND waitlisted_at=0`},
		{"PUT", "/api/v1/operator/credits/settings", `{"warm_cpu_class_cores":1}`, 200, ""},
		{"DELETE", "/api/v1/operator/github-app/installations/17", "", 204, `SELECT COUNT(*) FROM teams WHERE name='b' AND NOT EXISTS(SELECT 1 FROM github_app_installations WHERE installation_id=17)`},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
			if err != nil {
				t.Fatal(err)
			}
			engine, _ := engineServerFixture(t, deploymenttest.Team(t), func(s *Server) { s.WithSecretsCipher(cipher) })
			st := engine.Store
			engineSQL(t, st, `INSERT INTO teams(name,created_at,updated_at) VALUES ('b',1,1)`)
			engineSQL(t, st, `INSERT INTO team_counters(team) VALUES ('b')`)
			engineSQL(t, st, `INSERT INTO accounts(id,email,created_at,updated_at,waitlisted_at) VALUES ('operator','operator@example.test',1,1,0),('victim','victim@example.test',1,1,0),('waiting','waiting@example.test',1,1,1)`)
			engineSQL(t, st, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('a','operator','reader',1)`)
			if strings.HasSuffix(tc.path, "/unfreeze") {
				engineSQL(t, st, `INSERT INTO credit_freezes(dispute_id,team,payment_id,reason,created_at,cause,placed_by) VALUES ('held','b','','review',1,'operator','operator')`)
			}
			engineSQL(t, st, `INSERT INTO github_app_installations(installation_id,team,account_id,account_login,account_type,created_at,updated_at) VALUES (17,'b',17,'owner','User',1,1)`)
			s := engine.Server.WithOperatorAccounts([]string{"operator"})
			s.githubApp = &githubAppState{}
			raw, csrf, _, err := st.AsOperator().CreateAccountSession(t.Context(), store.Account{ID: "operator"}, "a", time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest(tc.method, "https://controller.test"+tc.path+"?team=a&actor=forged", strings.NewReader(tc.body))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Origin", "https://controller.test")
			r.Header.Set("Idempotency-Key", "once")
			r.AddCookie(&http.Cookie{Name: cookieName(sessionCookieName, s.cookiesSecure()), Value: raw})
			r.AddCookie(&http.Cookie{Name: cookieName(csrfCookieName, s.cookiesSecure()), Value: csrf})
			r.Header.Set(csrfHeaderName, csrf)
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("write = %d %s", w.Code, w.Body)
			}
			if tc.query != "" {
				var count int
				if err := teststore.SQL(t, st).QueryRow(tc.query).Scan(&count); err != nil || count != 1 {
					t.Fatalf("commit: %d %v", count, err)
				}
			} else {
				settings, err := st.AsOperator().CreditSettings(t.Context())
				if err != nil || settings.WarmCPUClassCores != 1 {
					t.Fatalf("settings: %+v %v", settings, err)
				}
			}
			digest := sha256.Sum256([]byte(raw))
			engineSQL(t, st, `UPDATE sessions SET revoked_at=1 WHERE hash=?`, hex.EncodeToString(digest[:]))
			r = httptest.NewRequest(tc.method, "https://controller.test"+tc.path, strings.NewReader(tc.body))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Origin", "https://controller.test")
			r.AddCookie(&http.Cookie{Name: cookieName(sessionCookieName, s.cookiesSecure()), Value: raw})
			r.AddCookie(&http.Cookie{Name: cookieName(csrfCookieName, s.cookiesSecure()), Value: csrf})
			r.Header.Set(csrfHeaderName, csrf)
			w = httptest.NewRecorder()
			s.Handler().ServeHTTP(w, r)
			if w.Code != 401 {
				t.Fatalf("revoked write = %d %s", w.Code, w.Body)
			}
		})
	}
}
