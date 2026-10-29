package acceptance_test

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestOAuthHTTPGitHubPrimaryEmailProperty(t *testing.T) {
	property := func(profile, email string, selection uint8) bool {
		f := newOAuthHTTP(t)
		user, err := json.Marshal(map[string]any{"id": 123, "login": "member", "email": profile})
		if err != nil {
			t.Fatal(err)
		}
		f.provider.userBody = string(user)
		emails := []map[string]any{}
		switch selection % 3 {
		case 0:
			emails = append(emails, map[string]any{"email": email, "primary": true, "verified": false})
		case 1:
			emails = append(emails, map[string]any{"email": email, "primary": false, "verified": true})
		}
		encoded, err := json.Marshal(emails)
		if err != nil {
			t.Fatal(err)
		}
		f.provider.emailsBody = string(encoded)
		before, _ := f.state(t)
		w := f.callback(url.Values{"state": {f.oauthState}, "code": {f.provider.code}}, f.flow)
		after, _ := f.state(t)
		return w.Code == 303 && w.Header().Get("Location") == "/login?next=%2Fruns%3Frun%3Dr1&refused=email_unverified" && oauthCookie(w, "__Host-sw_session") == nil && before == after
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 32}); err != nil {
		t.Fatal(err)
	}
}

func TestOAuthHTTPGitHubUsesNumericIdentityProperty(t *testing.T) {
	property := func(login string, other bool) bool {
		f := newOAuthHTTP(t)
		account, id := "member", 123
		if other {
			account, id = "other", 124
			for _, statement := range []string{
				`INSERT INTO accounts(id,email,active_team,created_at,updated_at) VALUES ('other','other@example.test','team',1,1)`,
				`INSERT INTO memberships(team,account_id,role,created_at) VALUES ('team','other','reader',1)`,
				`INSERT INTO identities(provider,subject,account_id,email,email_verified,created_at) VALUES ('github','124','other','other@example.test',1,1)`,
			} {
				if _, err := teststore.SQL(t, f.store).ExecContext(t.Context(), statement); err != nil {
					t.Fatal(err)
				}
			}
		}
		user, err := json.Marshal(map[string]any{"id": id, "login": login, "name": login})
		if err != nil {
			t.Fatal(err)
		}
		f.provider.userBody = string(user)
		f.provider.emailsBody = fmt.Sprintf(`[{"email":"%s@example.test","primary":true,"verified":true}]`, account)
		w := f.callback(url.Values{"state": {f.oauthState}, "code": {f.provider.code}}, f.flow)
		session := oauthCookie(w, "__Host-sw_session")
		if w.Code != 200 || session == nil {
			t.Logf("sign-in: %d %s", w.Code, w.Body)
			return false
		}
		r := httptest.NewRequest("GET", oauthOrigin+"/api/v1/auth/whoami", nil)
		r.AddCookie(session)
		w = httptest.NewRecorder()
		f.handler.ServeHTTP(w, r)
		return w.Code == 200 && strings.Contains(w.Body.String(), `"principal":"`+account+`"`)
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 24}); err != nil {
		t.Fatal(err)
	}
}
