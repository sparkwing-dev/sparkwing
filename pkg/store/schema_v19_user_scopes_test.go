package store_test

import (
	"slices"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestSchemaV19_ExistingUsersDefaultToAdmin(t *testing.T) {
	target := storetest.New(t)
	st, err := target.TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`ALTER TABLE users DROP COLUMN scopes`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(storetest.Rebind(st,
		`INSERT INTO users (name, pw_hash, created_at) VALUES (?, ?, ?)`),
		"legacy", "argon2id$00$00", time.Now().Unix(),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`DELETE FROM sparkwing_schema_version WHERE version >= 19`); err != nil {
		t.Fatal(err)
	}
	deleteFleetRequirements(t, st.DB())
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	up, err := target.TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()

	users, err := up.ListUsers()
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 {
		t.Fatalf("users = %+v, want the one legacy row", users)
	}
	if !slices.Equal(users[0].Scopes, []string{"admin"}) {
		t.Fatalf("legacy user scopes = %v, want [admin]", users[0].Scopes)
	}
}
