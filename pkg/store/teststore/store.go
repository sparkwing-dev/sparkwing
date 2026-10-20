// Package teststore provides isolated current-schema SQLite fixtures for tests.
package teststore

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// ForTeam returns a registered tenant or fails the fixture.
func ForTeam(t T, s *store.Store, team store.Team) *store.Tenant {
	t.Helper()
	tenant, err := s.ForTeam(t.Context(), team)
	if err != nil {
		t.Fatal(err)
	}
	return tenant
}

var snapshot = sync.OnceValues(func() (seed []byte, err error) {
	dir, err := os.MkdirTemp("", "sparkwing-test-schema-")
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(dir)) }()
	st, err := store.Open(filepath.Join(dir, "seed.db"))
	if err != nil {
		return nil, err
	}
	if err := st.Close(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", sqliteDSN(filepath.Join(dir, "seed.db")))
	if err != nil {
		return nil, err
	}
	_, err = db.Exec(`VACUUM INTO ?`, filepath.Join(dir, "snapshot.db"))
	if err := errors.Join(err, db.Close()); err != nil {
		return nil, err
	}
	return os.ReadFile(filepath.Join(dir, "snapshot.db"))
})

// Open creates an empty current-schema fixture at a new path, then opens it
// with OpenExisting. Existing files are refused; reopening and migration
// tests use OpenExisting, so SQL can still reach the store.
func Open(path string, opts ...store.Option) (*store.Store, error) {
	seed, err := snapshot()
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	_, err = f.Write(seed)
	if err := errors.Join(err, f.Close()); err != nil {
		return nil, err
	}
	return OpenExisting(path, opts...)
}
