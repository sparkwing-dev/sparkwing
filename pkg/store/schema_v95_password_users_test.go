package store_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestSchemaV95_DropsPasswordUsersAndTheirSessions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "password-users.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, stmt := range []string{
		`CREATE TABLE users (name TEXT PRIMARY KEY, pw_hash TEXT NOT NULL, created_at INTEGER NOT NULL,
			last_login_at INTEGER, scopes TEXT NOT NULL DEFAULT 'admin', team TEXT NOT NULL DEFAULT 'default')`,
		`INSERT INTO users (name, pw_hash, created_at) VALUES ('legacy', 'argon2id$00$00', 1)`,
	} {
		if _, err := st.DB().Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []struct{ hash, principal, account string }{
		{"password-session", "legacy", ""},
		{"account-session", "acct", "acct-1"},
	} {
		if _, err := st.DB().Exec(storetest.Rebind(st, `
			INSERT INTO sessions (hash, principal, scopes, account_id, created_at, expires_at)
			VALUES (?, ?, 'admin', ?, ?, ?)`),
			row.hash, row.principal, row.account, now.Unix(), now.Add(time.Hour).Unix(),
		); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.DB().Exec(`DELETE FROM sparkwing_schema_version WHERE version >= 95`); err != nil {
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
	var hashes []string
	rows, err := up.DB().Query(`SELECT hash FROM sessions ORDER BY hash`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			t.Fatal(err)
		}
		hashes = append(hashes, h)
	}
	if len(hashes) != 1 || hashes[0] != "account-session" {
		t.Fatalf("sessions after v95 = %v, want only the account session", hashes)
	}
	if _, err := up.DB().Exec(`SELECT 1 FROM users`); err == nil {
		t.Fatal("the users table survived v95")
	}
}
