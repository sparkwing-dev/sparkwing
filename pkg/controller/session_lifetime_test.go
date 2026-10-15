package controller_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func sessionLifetimeServer(t *testing.T, maxLifetime time.Duration) (*httptest.Server, *store.Store) {
	t.Helper()
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := controller.New(st, nil)
	if maxLifetime != 0 {
		srv = srv.WithSessionMaxLifetime(maxLifetime)
	}
	ts := httptest.NewServer(operatorSignInServer(t, st, srv).Handler())
	t.Cleanup(ts.Close)
	return ts, st
}

func resolveSession(t *testing.T, ts *httptest.Server, id string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/auth/session", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Session "+id)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("resolve session: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read session body: %v", err)
	}
	return resp.StatusCode, body
}

func TestLoginSessionStartsWithSevenDaysOfValidity(t *testing.T) {
	ts, _ := sessionLifetimeServer(t, 0)
	started := time.Now().UTC()
	login := loginForSessionHash(t, ts)
	finished := time.Now().UTC()

	status, body := resolveSession(t, ts, login.SessionID)
	if status != http.StatusOK {
		t.Fatalf("resolve new session = %d, want 200: %s", status, body)
	}
	var session struct {
		ExpiresAt int64 `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &session); err != nil {
		t.Fatalf("decode session %s: %v", body, err)
	}
	got := time.Unix(session.ExpiresAt, 0).UTC()
	if got.Before(started.Add(7*24*time.Hour-2*time.Second)) || got.After(finished.Add(7*24*time.Hour+2*time.Second)) {
		t.Errorf("new session expires at %s, want seven days after login", got)
	}
}
