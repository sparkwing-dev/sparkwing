package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPoolLauncherEnrollmentRequiresExclusivePoolAuthority(t *testing.T) {
	s, session := credentialSessionServer(t)
	engineSQL(t, s.store, `UPDATE sessions SET scopes='runs.read,runs.write,team.admin'`)
	engineSQL(t, s.store, `UPDATE memberships SET role='owner' WHERE account_id='member'`)
	for i, tc := range []struct {
		grants, pools string
		status        int
	}{
		{`["claims.launch"]`, `["deploy"]`, 201},
		{`["claims.launch","nodes.claim"]`, `["deploy"]`, 403},
		{`["claims.launch"]`, `[]`, 403},
	} {
		body := fmt.Sprintf(`{"name":"deploy-%d","repos":["github.com/acme/*"],"reserved_pools":%s,"trust":"verified-default","grants":%s}`, i, tc.pools, tc.grants)
		r := httptest.NewRequest("POST", "https://controller.test/api/v1/runners", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "https://controller.test")
		r.Header.Set(csrfHeaderName, "session-csrf")
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
		r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "session-csrf"})
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("pool enrollment: %d %s; want %d", w.Code, w.Body, tc.status)
		}
	}
}
