// Package teststore provides isolated current-schema SQLite fixtures for tests.
package teststore

import (
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

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
	// safety: Each fixture must mint its own authority during the normal store.Open.
	_, err = st.DB().Exec(`DELETE FROM sparkwing_meta WHERE key = 'controller_authority_id'`)
	if err == nil {
		_, err = st.DB().Exec(`VACUUM INTO ?`, filepath.Join(dir, "snapshot.db"))
	}
	if err := errors.Join(err, st.Close()); err != nil {
		return nil, err
	}
	return os.ReadFile(filepath.Join(dir, "snapshot.db"))
})

// Open creates an empty current-schema fixture at a new path, then calls store.Open.
// Existing files are refused; reopening and migration tests must use store.Open.
func Open(path string) (*store.Store, error) {
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
	return store.Open(path)
}
