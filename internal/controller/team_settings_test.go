package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
)

func TestMachineTeamSettingsHTTP(t *testing.T) {
	s, session := credentialSessionServer(t)
	handler := s.Handler()
	call := func(method, path, body string, status int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "https://controller.test"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "https://controller.test")
		r.Header.Set(csrfHeaderName, "session-csrf")
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
		r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "session-csrf"})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body)
		}
		return w
	}
	read := call("GET", "/api/v1/team/settings", "", 200)
	if !strings.Contains(read.Body.String(), `"members_can_add_machines":true`) {
		t.Fatalf("default settings: %s", read.Body)
	}
	call("PUT", "/api/v1/team/settings", `{"members_can_add_machines":false}`, 403)
	engineSQL(t, s.store, `UPDATE sessions SET scopes='runs.read,runs.write,team.admin'`)
	property := func(enabled bool, roleIndex uint8, input [8]byte) bool {
		engineSQL(t, s.store, `UPDATE memberships SET role='owner' WHERE account_id='member'`)
		call("PUT", "/api/v1/team/settings", fmt.Sprintf(`{"members_can_add_machines":%t}`, enabled), 200)
		role := []string{"owner", "editor", "reader"}[roleIndex%3]
		engineSQL(t, s.store, `UPDATE memberships SET role=? WHERE account_id='member'`, role)
		for _, personal := range []bool{false, true} {
			allowed := role == "owner" || enabled && personal && role != "reader"
			status := 403
			if allowed {
				status = 201
			}
			body := fmt.Sprintf(`{"name":"r-%x-%t","repos":["github.com/acme/*"],"personal":%t}`, input, personal, personal)
			w := call("POST", "/api/v1/runners", body, status)
			if !allowed && personal && role != "reader" && !strings.Contains(w.Body.String(), "members_can_add_machines") {
				return false
			}
			if allowed {
				var credential credentialResponse
				if json.Unmarshal(w.Body.Bytes(), &credential) != nil || credential.Capability == "" {
					return false
				}
				if !personal {
					engineSQL(t, s.store, `UPDATE memberships SET role='editor' WHERE account_id='member'`)
					call("POST", "/api/v1/tokens/"+credential.Prefix+"/rotate", `{}`, 403)
					call("DELETE", "/api/v1/tokens/"+credential.Prefix, "", 403)
					engineSQL(t, s.store, `UPDATE memberships SET role='owner' WHERE account_id='member'`)
					call("DELETE", "/api/v1/tokens/"+credential.Prefix, "", 204)
				}
			}
		}
		if role != "owner" {
			call("PUT", "/api/v1/team/settings", `{"members_can_add_machines":true}`, 403)
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 24}); err != nil {
		t.Fatal(err)
	}
	engineSQL(t, s.store, `UPDATE memberships SET role='owner' WHERE account_id='member'`)
	for _, body := range []string{`{}`, `{"members_can_add_machines":null}`, `{"members_can_add_machines":false,"team":"b"}`} {
		call("PUT", "/api/v1/team/settings", body, 400)
	}
}

func TestSelfHostedControllerRefusesTheCloudTier(t *testing.T) {
	for _, cloud := range []bool{false, true} {
		s, session := credentialSessionServer(t)
		if cloud {
			s.deployment = deploymenttest.Cloud(t, "https://controller.test")
		}
		engineSQL(t, s.store, `UPDATE sessions SET scopes='runs.read,runs.write,team.admin'`)
		engineSQL(t, s.store, `UPDATE memberships SET role='owner' WHERE account_id='member'`)
		r := httptest.NewRequest("PUT", "https://controller.test/api/v1/team/settings", strings.NewReader(`{"claim_ranking":["self-hosted","cloud"]}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "https://controller.test")
		r.Header.Set(csrfHeaderName, "session-csrf")
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
		r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "session-csrf"})
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if cloud && w.Code != http.StatusOK || !cloud && (w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "no Cloud launcher")) {
			t.Fatalf("cloud deployment=%t ranking with cloud: %d %s", cloud, w.Code, w.Body)
		}
	}
}

func TestChildCeilingTeamSettingsHTTP(t *testing.T) {
	s, session := credentialSessionServer(t)
	engineSQL(t, s.store, `UPDATE sessions SET scopes='runs.read,runs.write,team.admin'`)
	for _, tc := range []struct {
		role, body string
		status     int
	}{
		{"editor", `{"max_children_per_root":200}`, 403},
		{"owner", `{"max_children_per_root":0}`, 400},
		{"owner", `{"max_children_per_root":-1}`, 400},
		{"owner", `{"max_children_per_root":200}`, 200},
	} {
		engineSQL(t, s.store, `UPDATE memberships SET role=? WHERE account_id='member'`, tc.role)
		r := httptest.NewRequest("PUT", "https://controller.test/api/v1/team/settings", strings.NewReader(tc.body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "https://controller.test")
		r.Header.Set(csrfHeaderName, "session-csrf")
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
		r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "session-csrf"})
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != tc.status || tc.status == 200 && !strings.Contains(w.Body.String(), `"max_children_per_root":200`) {
			t.Fatalf("%s %s: %d %s", tc.role, tc.body, w.Code, w.Body)
		}
	}
}
