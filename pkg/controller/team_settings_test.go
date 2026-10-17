package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/quick"
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
			allowed := role == "owner" || enabled && personal
			status := 403
			if allowed {
				status = 201
			}
			body := fmt.Sprintf(`{"name":"r-%x-%t","repos":["github.com/acme/*"],"personal":%t}`, input, personal, personal)
			w := call("POST", "/api/v1/runners", body, status)
			if !allowed && personal && !strings.Contains(w.Body.String(), "members_can_add_machines") {
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
