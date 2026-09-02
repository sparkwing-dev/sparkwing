package controller_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func sessionLifetimeServer(t *testing.T, maxLifetime time.Duration) (*httptest.Server, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := controller.New(st, nil)
	if maxLifetime != 0 {
		srv = srv.WithSessionMaxLifetime(maxLifetime)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, st
}

func ageSession(t *testing.T, st *store.Store, createdAt, expiresAt time.Time) {
	t.Helper()
	if _, err := st.DB().Exec(
		`UPDATE sessions SET created_at = ?, expires_at = ?`,
		createdAt.UTC().Unix(), expiresAt.UTC().Unix(),
	); err != nil {
		t.Fatalf("age session: %v", err)
	}
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

func TestSessionRoute_RefusesRenewalPastTheAbsoluteCap(t *testing.T) {
	ts, st := sessionLifetimeServer(t, 0)
	bootstrapAdmin(t, ts)
	login := loginForSessionHash(t, ts)

	now := time.Now().UTC()
	ageSession(t, st, now.Add(-controller.DefaultSessionMaxLifetime-time.Minute), now.Add(6*time.Hour))

	status, body := resolveSession(t, ts, login.SessionID)
	if status != http.StatusUnauthorized {
		t.Fatalf("resolve capped session = %d, want 401: %s", status, body)
	}
	if !strings.Contains(string(body), "maximum lifetime") {
		t.Errorf("error body = %s, want the lifetime refusal", body)
	}
	var remaining int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Errorf("sessions after the cap = %d, want 0", remaining)
	}
}

func TestSessionRoute_ClampsRenewalToTheAbsoluteCap(t *testing.T) {
	const maxLifetime = 3 * time.Hour
	ts, st := sessionLifetimeServer(t, maxLifetime)
	bootstrapAdmin(t, ts)
	login := loginForSessionHash(t, ts)

	now := time.Now().UTC()
	created := now.Add(-2 * time.Hour)
	ageSession(t, st, created, now.Add(30*time.Minute))

	status, body := resolveSession(t, ts, login.SessionID)
	if status != http.StatusOK {
		t.Fatalf("resolve session = %d, want 200: %s", status, body)
	}
	var session struct {
		ExpiresAt int64 `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &session); err != nil {
		t.Fatalf("decode session %s: %v", body, err)
	}
	limit := created.Add(maxLifetime)
	if got := time.Unix(session.ExpiresAt, 0).UTC(); got.After(limit) {
		t.Errorf("renewed expiry = %s, want no later than the cap %s", got, limit)
	}
	var stored int64
	if err := st.DB().QueryRow(`SELECT expires_at FROM sessions`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if got := time.Unix(stored, 0).UTC(); got.After(limit) {
		t.Errorf("stored expiry = %s, want no later than the cap %s", got, limit)
	}
}
