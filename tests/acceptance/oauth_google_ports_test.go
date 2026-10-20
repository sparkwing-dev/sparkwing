package acceptance_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/googleauth"
	"github.com/sparkwing-dev/sparkwing/internal/jwks/jwkstest"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func TestGoogleOAuthHTTPSubjectAndProofProperty(t *testing.T) {
	signer := jwkstest.NewSigner(t, "google")
	property := func(name string, mode uint8) bool {
		f := newSessionFlow(t)
		for _, statement := range []string{
			`UPDATE accounts SET email='shared@example.test',active_team='team' WHERE id='member'`,
			`INSERT INTO accounts(id,email,active_team,created_at,updated_at) VALUES ('other','shared@example.test','team',1,1)`,
			`INSERT INTO memberships(team,account_id,role,created_at) VALUES ('team','other','reader',1)`,
			`INSERT INTO identities(provider,subject,account_id,email,email_verified,created_at) VALUES ('google','google-1','member','shared@example.test',1,1)`,
			`INSERT INTO identities(provider,subject,account_id,email,email_verified,created_at) VALUES ('google','google-2','other','shared@example.test',1,1)`,
		} {
			if _, err := teststore.SQL(t, f.store).ExecContext(t.Context(), statement); err != nil {
				t.Fatal(err)
			}
		}
		id, subject := "member", "google-1"
		if mode%4 == 1 {
			id, subject = "other", "google-2"
		}
		now := time.Now()
		token, err := signer.Sign(map[string]any{
			"iss": "https://google.test", "aud": "client", "sub": subject,
			"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(), "email": "shared@example.test",
			"email_verified": mode%4 != 2, "name": name, "given_name": name,
		})
		if err != nil {
			t.Fatal(err)
		}
		redirect, challenge := oauthOrigin+"/auth/google/callback", ""
		transport := surfaceTransport(func(r *http.Request) (*http.Response, error) {
			w := httptest.NewRecorder()
			switch r.URL.Path {
			case "/certs":
				signer.ServeKeys(w, r)
			case "/token":
				if err := r.ParseForm(); err != nil {
					return nil, err
				}
				if r.Form.Get("code") != "provider-code" || r.Form.Get("redirect_uri") != redirect ||
					googleauth.CodeChallenge(r.Form.Get("code_verifier")) != challenge {
					w.WriteHeader(http.StatusBadRequest)
					if err := json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"}); err != nil {
						return nil, err
					}
				} else if err := json.NewEncoder(w).Encode(map[string]string{"id_token": token}); err != nil {
					return nil, err
				}
			default:
				return nil, fmt.Errorf("unexpected provider path %s", r.URL.Path)
			}
			return w.Result(), nil
		})
		h := f.server.WithDashboard(controller.Dashboard{}).WithGoogleSignIn(googleauth.New(googleauth.Config{
			ClientID: "client", ClientSecret: "private-client-secret", AuthURL: "https://google.test/auth",
			TokenURL: "https://google.test/token", JWKSURL: "https://google.test/certs", Issuers: []string{"https://google.test"},
			HTTP: &http.Client{Transport: transport},
		}), []string{redirect}).Handler()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", oauthOrigin+"/auth/google/start", nil))
		authorize, err := url.Parse(w.Header().Get("Location"))
		flow := oauthCookie(w, "__Host-sw_oauth")
		if err != nil || w.Code != 303 || flow == nil {
			t.Logf("start HTTP %d %s, %v", w.Code, w.Body, err)
			return false
		}
		challenge = authorize.Query().Get("code_challenge")
		if challenge == "" || authorize.Query().Get("scope") != "openid email profile" || authorize.Query().Get("redirect_uri") != redirect {
			return false
		}
		if mode%4 == 3 {
			data, err := base64.RawURLEncoding.DecodeString(flow.Value)
			var state map[string]string
			if err != nil || json.Unmarshal(data, &state) != nil {
				return false
			}
			state["verifier"] = "unrelated-verifier"
			data, err = json.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			flow.Value = base64.RawURLEncoding.EncodeToString(data)
		}
		before, expiry := f.state(t)
		r := httptest.NewRequest("GET", redirect+"?"+url.Values{"state": {authorize.Query().Get("state")}, "code": {"provider-code"}}.Encode(), nil)
		r.AddCookie(flow)
		w = httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if mode%4 >= 2 {
			after, expires := f.state(t)
			status := 403
			if mode%4 == 3 {
				status = 401
			}
			return w.Code == status && oauthCookie(w, "__Host-sw_session") == nil && before == after && expiry == expires
		}
		session := oauthCookie(w, "__Host-sw_session")
		if w.Code != 200 || session == nil || session.Value == f.raw {
			t.Logf("callback HTTP %d %s", w.Code, w.Body)
			return false
		}
		r = httptest.NewRequest("GET", oauthOrigin+"/api/v1/auth/whoami", nil)
		r.AddCookie(session)
		w = httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code == 200 && strings.Contains(w.Body.String(), `"principal":"`+id+`"`) &&
			!strings.Contains(f.logs.String(), session.Value) && !strings.Contains(f.logs.String(), token)
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 24}); err != nil {
		t.Fatal(err)
	}
}
