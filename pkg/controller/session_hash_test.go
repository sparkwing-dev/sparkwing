package controller_test

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func sessionHashServer(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ts := httptest.NewServer(operatorSignInServer(t, st, controller.New(st, nil)).Handler())
	t.Cleanup(ts.Close)
	return ts, st
}

type sessionLoginJSON struct {
	SessionID string `json:"session_id"`
	CSRFToken string `json:"csrf_token"`
}

// safety: signing in needs a token to exist before auth is enabled, the way a provisioned deployment starts.
func operatorSignInServer(t *testing.T, st *store.Store, srv *controller.Server) *controller.Server {
	t.Helper()
	if _, err := controller.EnsureBootstrapAdminToken(st, bootstrapAdminRaw, time.Now().UTC()); err != nil {
		t.Fatalf("bootstrap token: %v", err)
	}
	return srv.EnableAuthFromStore().WithDashboard(controller.Dashboard{})
}

func loginForSessionHash(t *testing.T, ts *httptest.Server) sessionLoginJSON {
	t.Helper()
	page, err := http.Get(ts.URL + "/login")
	if err != nil {
		t.Fatalf("login page: %v", err)
	}
	_ = page.Body.Close()
	var formToken *http.Cookie
	for _, c := range page.Cookies() {
		if c.Name == "__Host-sw_csrf" {
			formToken = c
		}
	}
	if page.StatusCode != http.StatusOK || formToken == nil {
		t.Fatalf("login page = %d with no CSRF cookie", page.StatusCode)
	}
	form := url.Values{"token": {bootstrapAdminRaw}, "csrf_token": {formToken.Value}}
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/login", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", ts.URL)
	req.AddCookie(formToken)
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noFollow.Do(req)
	if err != nil {
		t.Fatalf("sign in: %v", err)
	}
	_ = resp.Body.Close()
	var login sessionLoginJSON
	for _, c := range resp.Cookies() {
		switch c.Name {
		case "__Host-sw_session":
			login.SessionID = c.Value
		case "__Host-sw_csrf":
			login.CSRFToken = c.Value
		}
	}
	if resp.StatusCode != http.StatusSeeOther || login.SessionID == "" || login.CSRFToken == "" {
		t.Fatalf("sign in = %d with session %t and csrf %t", resp.StatusCode, login.SessionID != "", login.CSRFToken != "")
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
