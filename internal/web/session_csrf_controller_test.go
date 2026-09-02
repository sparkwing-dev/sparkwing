package web

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func liveControllerSession(t *testing.T) (string, *loginResp, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ts := httptest.NewServer(controller.New(st, nil).Handler())
	t.Cleanup(ts.Close)

	post := func(path string, body any) []byte {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.Post(ts.URL+path, "application/json", bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		if resp.StatusCode/100 != 2 {
			t.Fatalf("POST %s = %d: %s", path, resp.StatusCode, out)
		}
		return out
	}

	post("/api/v1/users", map[string]string{"name": "root", "password": "correct-horse"})
	var login loginResp
	if err := json.Unmarshal(
		post("/api/v1/auth/login", map[string]string{"username": "root", "password": "correct-horse"}),
		&login,
	); err != nil {
		t.Fatalf("decode login: %v", err)
	}
	return ts.URL, &login, st
}

func TestDashboardCSRFAcceptsTheLiveControllerToken(t *testing.T) {
	controllerURL, login, st := liveControllerSession(t)
	handler := HandlerFromOptionsWithBundle(HandlerOptions{
		ControllerURL: controllerURL,
		RequireLogin:  true,
	}, authTestBundle)

	logout := func(token string) *httptest.ResponseRecorder {
		form := url.Values{"csrf_token": {token}}
		req := httptest.NewRequest(http.MethodPost, "https://dashboard.example/logout", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "https://dashboard.example")
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: login.SessionID})
		req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: token})
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	if rec := logout("forged-token"); rec.Code != http.StatusForbidden {
		t.Fatalf("forged csrf token = %d, want 403", rec.Code)
	}
	var stillThere int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&stillThere); err != nil {
		t.Fatal(err)
	}
	if stillThere != 1 {
		t.Fatalf("sessions after rejected logout = %d, want 1", stillThere)
	}

	rec := logout(login.CSRFToken)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("logout with the controller token = %d %q, want 303 /login",
			rec.Code, rec.Header().Get("Location"))
	}
	var remaining int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Errorf("sessions after logout = %d, want 0", remaining)
	}
}
