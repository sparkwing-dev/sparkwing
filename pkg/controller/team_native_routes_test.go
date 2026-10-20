package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func TestTeamWritesUseNativeTenantTransactions(t *testing.T) {
	s, session := credentialSessionServer(t)
	engineSQL(t, s.store, `UPDATE memberships SET role='owner' WHERE team='a' AND account_id='member'`)
	handler := s.Handler()
	request := func(method, path, body string, status int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "https://controller.test"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "https://controller.test")
		r.Header.Set("Idempotency-Key", "stable")
		r.Header.Set(csrfHeaderName, "session-csrf")
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
		r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "session-csrf"})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s: HTTP %d %s", method, path, w.Code, w.Body)
		}
		return w
	}
	request("PATCH", "/api/v1/team", `{"display_name":"Team A"}`, 200)
	request("PUT", "/api/v1/team/build-trust", `{"trust_local_builds":true}`, 200)
	response := request("POST", "/api/v1/team/invitations", `{"email":"guest@example.test","role":"reader"}`, 201)
	var invitation struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &invitation); err != nil || invitation.ID == "" {
		t.Fatalf("invitation: %s %v", response.Body, err)
	}
	request("POST", "/api/v1/team/invitations", `{"email":"guest@example.test","role":"reader"}`, 201)
	request("POST", "/api/v1/team/invitations", `{"email":"changed@example.test","role":"reader"}`, 409)
	request("DELETE", "/api/v1/team/invitations/"+invitation.ID, "", 204)
	tenant, err := s.store.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	if info, err := tenant.Info(t.Context()); err != nil || info.DisplayName != "Team A" {
		t.Fatalf("team=%+v: %v", info, err)
	}
	if trust, err := tenant.TrustLocalBuilds(t.Context()); err != nil || !trust {
		t.Fatalf("trust=%v: %v", trust, err)
	}
	var actor string
	if err := teststore.SQL(t, s.store).QueryRow(`SELECT invited_by FROM invitations WHERE id=?`, invitation.ID).Scan(&actor); err != nil || actor != "member" {
		t.Fatalf("actor=%q: %v", actor, err)
	}
	request("DELETE", "/api/v1/runs/run", "", 409)
	engineSQL(t, s.store, `UPDATE runs SET status='success',finished_at=? WHERE team='a' AND id='run'`, time.Now().UnixNano())
	request("DELETE", "/api/v1/runs/run", "", 204)
	teststore.SeedTeam(t, s.store, "b")
	engineSQL(t, s.store, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('b','member','owner',1)`)
	request("DELETE", "/api/v1/team", `{"confirm_slug":"a"}`, 202)
	if _, err := s.store.ForTeam(t.Context(), "a"); err == nil {
		t.Fatal("deleted team stayed open")
	}
	if _, err := s.store.ForTeam(t.Context(), store.Team("b")); err != nil {
		t.Fatal(err)
	}
}
