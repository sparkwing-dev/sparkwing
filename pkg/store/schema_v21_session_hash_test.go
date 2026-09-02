package store_test

import (
	"crypto/sha256"
	"encoding/hex"
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
