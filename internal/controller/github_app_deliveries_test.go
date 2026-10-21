package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFailedGitHubDeliveryRoutesRequireTeamAdmin(t *testing.T) {
	s, session := credentialSessionServer(t)
	handler := s.Handler()
	for _, role := range []string{"reader", "owner"} {
		engineSQL(t, s.store, `UPDATE memberships SET role=? WHERE team='a' AND account_id='member'`, role)
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			path := "/api/v1/team/github-app/deliveries/failed"
			if method == http.MethodPost {
				path = "/api/v1/team/github-app/deliveries/foreign/process-again"
			}
			r := httptest.NewRequest(method, "https://controller.test"+path, nil)
			r.Header.Set("Origin", "https://controller.test")
			r.Header.Set(csrfHeaderName, "session-csrf")
			r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
			r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "session-csrf"})
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			want := http.StatusForbidden
			if role == "owner" {
				want = http.StatusOK
				if method == http.MethodPost {
					want = http.StatusNotFound
				}
			}
			if w.Code != want {
				t.Fatalf("%s %s: got %d want %d: %s", role, method, w.Code, want, w.Body)
			}
		}
	}
}
