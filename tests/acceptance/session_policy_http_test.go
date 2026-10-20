package acceptance_test

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func TestSessionPolicyHTTPTransports(t *testing.T) {
	for _, dashboard := range []bool{false, true} {
		for _, header := range []bool{false, true} {
			f := newSessionFlow(t)
			if dashboard {
				f.server.WithDashboard(controller.Dashboard{})
			}
			h := f.server.Handler()
			for _, method := range []string{"GET", "POST"} {
				path, body, status := "/api/v1/auth/whoami", "", 200
				if method == "POST" {
					path, body, status = "/api/v1/tokens", `{"grants":["runs.read"]}`, 201
				}
				r := f.request(method, path, body)
				if header {
					r.Header.Set("Authorization", "sEsSiOn "+f.raw)
					r.Header.Set("Cookie", "__Host-sw_session=unrelated; __Host-sw_csrf="+f.csrf)
				}
				before, expiry := f.state(t)
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				after, renewed := f.state(t)
				if w.Code != status || w.Header().Get("Cache-Control") != "no-store" || before != after || expiry != renewed || strings.Contains(w.Body.String(), f.raw) {
					t.Fatalf("dashboard=%v header=%v %s: HTTP %d %s", dashboard, header, method, w.Code, w.Body.String())
				}
			}
		}
	}
}

func TestSessionPolicyHTTPRefusals(t *testing.T) {
	for _, state := range []string{"expired", "revoked", "membership", "license", "backend"} {
		t.Run(state, func(t *testing.T) {
			f := newSessionFlow(t)
			code := apierr.Unauthenticated
			var query string
			switch state {
			case "expired":
				query = `UPDATE sessions SET expires_at=1`
			case "revoked":
				query = `UPDATE sessions SET revoked_at=1`
			case "membership":
				query = `DELETE FROM memberships`
			case "license":
				f.server.WithLicense(nil)
			case "backend":
				query, code = `DROP TABLE memberships`, apierr.Unavailable
			}
			if query != "" {
				if _, err := teststore.SQL(t, f.store).ExecContext(t.Context(), query); err != nil {
					t.Fatal(err)
				}
			}
			before, expiry := f.state(t)
			w := httptest.NewRecorder()
			f.server.WithDashboard(controller.Dashboard{}).Handler().ServeHTTP(w, f.request("GET", "/api/v1/auth/whoami", ""))
			assertEngineError(t, w, apierr.Status(code), code)
			after, renewed := f.state(t)
			if before != after || expiry != renewed || strings.Contains(w.Body.String(), "memberships") {
				t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestSessionPolicyHTTPProofProperty(t *testing.T) {
	f := newSessionFlow(t)
	h := f.server.WithDashboard(controller.Dashboard{}).Handler()
	property := func(value []byte, mutation uint8) bool {
		r := f.request("POST", "/api/v1/tokens", `{"grants":["runs.read"]}`)
		forged := "forged-" + hex.EncodeToString(value)
		switch mutation % 10 {
		case 0:
			r.Header.Del("X-CSRF-Token")
		case 1:
			r.Header.Set("X-CSRF-Token", forged)
		case 2:
			r.Header.Set("Origin", "https://foreign.test")
		case 3:
			r.Header.Del("Origin")
		case 4:
			r.Header.Set("Cookie", "__Host-sw_session="+f.raw+"; __Host-sw_csrf="+forged)
			r.Header.Set("X-CSRF-Token", forged)
		case 5:
			r.Header.Set("Authorization", "Session "+forged)
		case 6:
			r.Header.Set("Authorization", "Bearer "+f.raw)
		case 7:
			r.Header.Add("Authorization", "Session "+f.raw)
			r.Header.Add("Authorization", "Session "+forged)
		case 8:
			r.AddCookie(&http.Cookie{Name: "__Host-sw_session", Value: forged})
		case 9:
			r.Header.Set("Authorization", "Session "+f.raw+" extra")
		}
		before, expiry := f.state(t)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		after, renewed := f.state(t)
		var result apierr.Envelope
		var minted int
		if err := teststore.SQL(t, f.store).QueryRowContext(t.Context(), `SELECT COUNT(*) FROM capabilities`).Scan(&minted); err != nil {
			t.Fatal(err)
		}
		return (w.Code == 401 || w.Code == 403) && json.Unmarshal(w.Body.Bytes(), &result) == nil && result.Error != nil && before == after && expiry == renewed && minted == 0 && w.Header().Get("Cache-Control") == "no-store" && !strings.Contains(w.Body.String(), f.raw) && !strings.Contains(w.Body.String(), f.csrf)
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 100}); err != nil {
		t.Fatal(err)
	}
}

func TestSessionPolicyHTTPLifetimeProperty(t *testing.T) {
	f := newSessionFlow(t)
	const maximum = 48 * time.Hour
	h := f.server.WithSessionMaxLifetime(maximum).Handler()
	property := func(age uint8, header bool) bool {
		elapsed := (time.Duration(age%96) - 24) * time.Hour
		created := time.Now().Truncate(time.Millisecond).Add(-elapsed)
		if _, err := teststore.SQL(t, f.store).ExecContext(t.Context(), `UPDATE sessions SET created_at=?`, created.UnixNano()); err != nil {
			t.Fatal(err)
		}
		r := f.request("GET", "/api/v1/auth/whoami", "")
		if header {
			r.Header.Set("Authorization", "Session "+f.raw)
			r.Header.Del("Cookie")
		}
		before, expiry := f.state(t)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		after, renewed := f.state(t)
		status := 200
		if elapsed < 0 || elapsed >= maximum {
			status = 401
		}
		return w.Code == status && before == after && expiry == renewed && !strings.Contains(w.Body.String(), f.raw)
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 96}); err != nil {
		t.Fatal(err)
	}
}

func TestSessionFlowHTTPLifetimeCases(t *testing.T) {
	for _, test := range []struct {
		name                 string
		maximum, age, expiry time.Duration
		refused              bool
	}{
		{"within default cap", 30 * 24 * time.Hour, 29 * 24 * time.Hour, time.Hour, false},
		{"past default cap", 30 * 24 * time.Hour, 30*24*time.Hour + time.Minute, time.Hour, true},
		{"configured cap", 3 * time.Hour, 2 * time.Hour, 30 * time.Minute, false},
		{"recently used", 30 * 24 * time.Hour, 24 * time.Hour, 61 * time.Minute, false},
		{"future creation", 30 * 24 * time.Hour, -365 * 24 * time.Hour, 6 * time.Hour, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newSessionFlow(t)
			now := time.Now().UTC()
			created := now.Add(-test.age)
			if _, err := teststore.SQL(t, f.store).ExecContext(t.Context(), `UPDATE sessions SET created_at=?,expires_at=?`, created.UnixNano(), now.Add(test.expiry).UnixNano()); err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			f.server.WithSessionMaxLifetime(test.maximum).Handler().ServeHTTP(w, f.request("GET", "/api/v1/auth/session", ""))
			count, stored := f.state(t)
			if test.refused {
				assertEngineError(t, w, 401, apierr.Unauthenticated)
				if count != 0 || !strings.Contains(w.Body.String(), "maximum lifetime") {
					t.Fatalf("refused session: count=%d body=%s", count, w.Body.String())
				}
				return
			}
			var result struct {
				Expires int64 `json:"expires_at"`
			}
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || count != 1 || stored/time.Second.Nanoseconds() != result.Expires || stored > created.Add(test.maximum).UnixNano() {
				t.Fatalf("HTTP %d %s stored=%d", w.Code, w.Body.String(), stored)
			}
			minimum := min(now.Add(7*24*time.Hour).Unix(), created.Add(test.maximum).Unix())
			if result.Expires < minimum {
				t.Fatalf("renewal expires at %d, want at least %d", result.Expires, minimum)
			}
		})
	}
}

func browserLogoutRequest(f sessionFlow, token string) *http.Request {
	r := f.request("POST", "/logout", url.Values{"csrf_token": {token}}.Encode())
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return r
}

func TestBrowserLogoutHTTPUsesNamedProof(t *testing.T) {
	f := newSessionFlow(t)
	other, _, _, err := f.store.CreateAccountSession(t.Context(), store.Account{ID: "member"}, "team", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	f.server.WithDashboard(controller.Dashboard{}).Handler().ServeHTTP(w, browserLogoutRequest(f, f.csrf))
	count, _ := f.state(t)
	if w.Code != 303 || w.Header().Get("Location") != "/login" || count != 1 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("HTTP %d %s count=%d", w.Code, w.Body.String(), count)
	}
	cleared := map[string]bool{}
	for _, cookie := range w.Result().Cookies() {
		if cookie.MaxAge < 0 {
			cleared[cookie.Name] = true
		}
	}
	if !cleared["__Host-sw_session"] || !cleared["__Host-sw_csrf"] {
		t.Fatalf("logout cookies: %v", w.Result().Cookies())
	}
	if _, err := f.store.LookupSession(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	if logs := f.logs.String(); !strings.Contains(logs, "route=/logout") || !strings.Contains(logs, "principal_id=member") || strings.Contains(logs, f.raw) || strings.Contains(logs, f.csrf) {
		t.Fatalf("audit log: %s", logs)
	}
}

func TestBrowserLogoutHTTPProofProperty(t *testing.T) {
	f := newSessionFlow(t)
	h := f.server.WithDashboard(controller.Dashboard{}).Handler()
	property := func(value []byte, mutation uint8) bool {
		forged := "forged-" + hex.EncodeToString(value)
		r := browserLogoutRequest(f, f.csrf)
		switch mutation % 7 {
		case 0:
			r = browserLogoutRequest(f, forged)
		case 1:
			r = browserLogoutRequest(f, forged)
			r.Header.Set("Cookie", "__Host-sw_session="+f.raw+"; __Host-sw_csrf="+forged)
		case 2:
			r.Header.Set("Origin", "https://foreign.test")
		case 3:
			r.Header.Del("Origin")
		case 4:
			r.AddCookie(&http.Cookie{Name: "__Host-sw_session", Value: forged})
		case 5:
			r.Header.Set("Authorization", "Session "+forged)
		case 6:
			r.Header.Set("Cookie", "__Host-sw_session="+f.raw)
		}
		before, expiry := f.state(t)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		after, renewed := f.state(t)
		var result apierr.Envelope
		return (w.Code == 401 || w.Code == 403) && json.Unmarshal(w.Body.Bytes(), &result) == nil && result.Error != nil && before == after && expiry == renewed && w.Header().Get("Cache-Control") == "no-store" && !strings.Contains(w.Body.String(), f.raw) && !strings.Contains(w.Body.String(), f.csrf)
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 100}); err != nil {
		t.Fatal(err)
	}
}

func TestBrowserLogoutHTTPRefusals(t *testing.T) {
	for _, state := range []string{"method", "expired", "revoked", "license", "key", "delete", "form"} {
		t.Run(state, func(t *testing.T) {
			f := newSessionFlow(t)
			r := browserLogoutRequest(f, f.csrf)
			code, query := apierr.Unauthenticated, ""
			switch state {
			case "method":
				r.Method, code = "GET", apierr.MethodNotAllowed
			case "expired":
				query = `UPDATE sessions SET expires_at=1`
			case "revoked":
				query = `UPDATE sessions SET revoked_at=1`
			case "license":
				f.server.WithLicense(nil)
			case "key":
				f.server.WithSecretsCipher(nil)
				code = apierr.Unavailable
			case "delete":
				query, code = `CREATE TRIGGER refuse_session_delete BEFORE DELETE ON sessions BEGIN SELECT RAISE(ABORT,'private deletion fault'); END`, apierr.Unavailable
			case "form":
				r.Body, code = http.NoBody, apierr.BadRequest
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded; invalid")
			}
			if query != "" {
				if _, err := teststore.SQL(t, f.store).ExecContext(t.Context(), query); err != nil {
					t.Fatal(err)
				}
			}
			before, expiry := f.state(t)
			w := httptest.NewRecorder()
			f.server.WithDashboard(controller.Dashboard{}).Handler().ServeHTTP(w, r)
			assertEngineError(t, w, apierr.Status(code), code)
			after, renewed := f.state(t)
			if before != after || expiry != renewed || len(w.Result().Cookies()) != 0 || strings.Contains(w.Body.String(), "private deletion fault") {
				t.Fatalf("HTTP %d %s cookies=%v", w.Code, w.Body.String(), w.Result().Cookies())
			}
		})
	}
}
