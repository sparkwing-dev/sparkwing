package store_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestSchemaV21_DropsPlaintextSessions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session-hash.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(
		`ALTER TABLE sessions ADD COLUMN csrf_token TEXT NOT NULL DEFAULT ''`,
	); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := st.DB().Exec(`
        INSERT INTO sessions (hash, principal, scopes, csrf_token, created_at, expires_at)
        VALUES (?, ?, ?, ?, ?, ?)`,
		"legacy-raw-session", "legacy", "admin", "legacy-raw-csrf",
		now.Unix(), now.Add(time.Hour).Unix(),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`DELETE FROM sparkwing_schema_version WHERE version >= 21`); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	up, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()

	var remaining int
	if err := up.DB().QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Errorf("sessions rows after migration = %d, want 0", remaining)
	}
	if _, err := up.LookupSession("legacy-raw-session", time.Now()); err == nil {
		t.Error("legacy raw session still resolves after migration")
	}
	if _, err := up.DB().Exec(`SELECT csrf_token FROM sessions`); err == nil {
		t.Error("sessions still carries a csrf_token column")
	}
}

func TestSessions_StoreDigestsAndDeriveCSRFTokens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	raw, csrf, sess, err := st.CreateSession("root", []string{"admin"}, time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}

	var stored string
	if err := st.DB().QueryRow(`SELECT hash FROM sessions`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(raw))
	if stored != hex.EncodeToString(sum[:]) {
		t.Errorf("stored hash = %q, want the sha256 digest of the raw id", stored)
	}
	if stored == raw {
		t.Error("sessions stores the raw session id")
	}
	if csrf == "" || csrf == raw {
		t.Errorf("csrf token = %q, want a derived value distinct from the session id", csrf)
	}
	if sess.CSRFToken != csrf {
		t.Errorf("returned session csrf = %q, want %q", sess.CSRFToken, csrf)
	}

	got, err := st.LookupSession(raw, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.CSRFToken != csrf {
		t.Errorf("looked-up csrf = %q, want %q", got.CSRFToken, csrf)
	}
	if got.Principal != "root" {
		t.Errorf("principal = %q, want root", got.Principal)
	}
	if _, err := st.LookupSession(stored, now); err == nil {
		t.Error("the stored digest authenticates as a session id")
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	survived, err := reopened.LookupSession(raw, now)
	if err != nil {
		t.Fatal(err)
	}
	if survived.CSRFToken != csrf {
		t.Errorf("csrf after reopen = %q, want the original %q", survived.CSRFToken, csrf)
	}
	if err := reopened.DeleteSession(raw); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.LookupSession(raw, now); err == nil {
		t.Error("session resolves after DeleteSession")
	}
}

func TestCSRFKey_ReadOnlyStoreResolvesSessions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "readonly.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	raw, csrf, _, err := st.CreateSession("root", []string{"admin"}, time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	ro, err := store.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	sess, err := ro.LookupSession(raw, now)
	if err != nil {
		t.Fatalf("read-only LookupSession: %v", err)
	}
	if sess.CSRFToken != csrf {
		t.Errorf("read-only csrf = %q, want %q", sess.CSRFToken, csrf)
	}
}

func TestCSRFKey_MintingRotatesLiveSessions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rotate.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	raw, _, _, err := st.CreateSession("root", []string{"admin"}, time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(
		`DELETE FROM sparkwing_meta WHERE key = 'session_csrf_key'`,
	); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.LookupSession(raw, now); err == nil {
		t.Error("session survives a new signing key, so its csrf token no longer verifies")
	}
	var remaining int
	if err := reopened.DB().QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Errorf("sessions after minting a new key = %d, want 0", remaining)
	}
}

func TestCSRFKey_ExistingKeyKeepsSessions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keep.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	raw, csrf, _, err := st.CreateSession("root", []string{"admin"}, time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	sess, err := reopened.LookupSession(raw, now)
	if err != nil {
		t.Fatalf("LookupSession after reopen: %v", err)
	}
	if sess.CSRFToken != csrf {
		t.Errorf("csrf after reopen = %q, want %q", sess.CSRFToken, csrf)
	}
}

func TestLookupSession_BackendFaultsAreDistinguishable(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name    string
		corrupt func(t *testing.T, st *store.Store)
		backend bool
	}{
		{
			name:    "unknown session",
			corrupt: func(*testing.T, *store.Store) {},
		},
		{
			name: "malformed signing key",
			corrupt: func(t *testing.T, st *store.Store) {
				if _, err := st.DB().Exec(
					`UPDATE sparkwing_meta SET value = 'zz' WHERE key = 'session_csrf_key'`,
				); err != nil {
					t.Fatal(err)
				}
			},
			backend: true,
		},
		{
			name: "missing sessions table",
			corrupt: func(t *testing.T, st *store.Store) {
				if _, err := st.DB().Exec(`DROP TABLE sessions`); err != nil {
					t.Fatal(err)
				}
			},
			backend: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "fault.db")
			st, err := store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			raw, _, _, err := st.CreateSession("root", []string{"admin"}, time.Hour, now)
			if err != nil {
				t.Fatal(err)
			}
			tc.corrupt(t, st)
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			if tc.name == "unknown session" {
				raw = "not-a-session"
			}
			_, err = reopened.LookupSession(raw, now)
			if err == nil {
				t.Fatal("LookupSession succeeded, want an error")
			}
			if got := errors.Is(err, store.ErrSessionBackend); got != tc.backend {
				t.Errorf("errors.Is(%v, ErrSessionBackend) = %v, want %v", err, got, tc.backend)
			}
		})
	}
}
