package controller_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func sessionHashServer(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ts := httptest.NewServer(controller.New(st, nil).Handler())
	t.Cleanup(ts.Close)
	return ts, st
}

type sessionLoginJSON struct {
	SessionID string `json:"session_id"`
	CSRFToken string `json:"csrf_token"`
}

func loginForSessionHash(t *testing.T, ts *httptest.Server) sessionLoginJSON {
	t.Helper()
	status, body := postUserJSON(t, ts.URL+"/api/v1/auth/login", map[string]string{
		"username": "root", "password": "correct-horse",
	})
	if status != http.StatusOK {
		t.Fatalf("login = %d: %s", status, body)
	}
	var login sessionLoginJSON
	if err := json.Unmarshal(body, &login); err != nil {
		t.Fatalf("decode login %s: %v", body, err)
	}
	if login.SessionID == "" || login.CSRFToken == "" {
		t.Fatalf("login returned an incomplete session: %s", body)
	}
	return login
}

func sessionRowCount(t *testing.T, st *store.Store, column, value string) int {
	t.Helper()
	var n int
	if err := st.DB().QueryRow(
		`SELECT COUNT(*) FROM sessions WHERE `+column+` = ?`, value,
	).Scan(&n); err != nil {
		t.Fatalf("count sessions by %s: %v", column, err)
	}
	return n
}

func TestLogin_StoresOnlyTheSessionDigest(t *testing.T) {
	ts, st := sessionHashServer(t)
	bootstrapAdmin(t, ts)
	login := loginForSessionHash(t, ts)

	if n := sessionRowCount(t, st, "hash", login.SessionID); n != 0 {
		t.Errorf("raw session id matched %d rows, want 0", n)
	}
	sum := sha256.Sum256([]byte(login.SessionID))
	if n := sessionRowCount(t, st, "hash", hex.EncodeToString(sum[:])); n != 1 {
		t.Errorf("session digest matched %d rows, want 1", n)
	}

	cols, err := st.DB().Query(`SELECT * FROM sessions LIMIT 0`)
	if err != nil {
		t.Fatal(err)
	}
	defer cols.Close()
	names, err := cols.Columns()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name == "csrf_token" {
			t.Errorf("sessions still carries a csrf_token column: %v", names)
		}
	}
}

func TestSessionRoute_ResolvesRawIDAndRepeatsTheCSRFToken(t *testing.T) {
	ts, _ := sessionHashServer(t)
	bootstrapAdmin(t, ts)
	login := loginForSessionHash(t, ts)

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/auth/session", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Session "+login.SessionID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("resolve session: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resolve session = %d, want 200", resp.StatusCode)
	}
	var session struct {
		Principal string `json:"principal"`
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&session); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	if session.Principal != "root" {
		t.Errorf("principal = %q, want root", session.Principal)
	}
	if session.CSRFToken != login.CSRFToken {
		t.Errorf("resolved csrf token = %q, want the login token %q", session.CSRFToken, login.CSRFToken)
	}
}

func TestLogout_DeletesTheHashedRow(t *testing.T) {
	ts, st := sessionHashServer(t)
	bootstrapAdmin(t, ts)
	login := loginForSessionHash(t, ts)

	status, body := postUserJSON(t, ts.URL+"/api/v1/auth/logout", map[string]string{
		"session_id": login.SessionID,
	})
	if status != http.StatusNoContent {
		t.Fatalf("logout = %d: %s", status, body)
	}
	var remaining int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Errorf("sessions rows after logout = %d, want 0", remaining)
	}

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/auth/session", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Session "+login.SessionID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("resolve session: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("resolve after logout = %d, want 401", resp.StatusCode)
	}
}
